// Package aiservice_admin provides admin-facing business logic for the AI Service Manager.
// It wraps the registry.Registry facade and adds pagination, response shaping, and
// validation that are specific to the admin API layer.
package aiservice_admin

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// RouteItem is the wire-format representation of a single ai_service_route row
// returned inside the service detail response (spec §4.4).
type RouteItem struct {
	ID                 uint64   `json:"id"`
	ProviderID         uint64   `json:"provider_id"`
	ProviderName       string   `json:"provider_name"`
	ProviderModelID    string   `json:"provider_model_id"`
	Priority           int      `json:"priority"`
	PricingUnit        string   `json:"pricing_unit"`
	InputPricePerMTok  float64  `json:"input_price_per_mtok"`
	OutputPricePerMTok float64  `json:"output_price_per_mtok"`
	PricePerCall       *float64 `json:"price_per_call,omitempty"`
	PricePerSecond     *float64 `json:"price_per_second,omitempty"`
	IsActive           bool     `json:"is_active"`
}

// ServiceDetail is the wire-format representation of a single ai_service row with
// its associated routes (spec §4.4 GET /v1/admin/ai/services/:id).
type ServiceDetail struct {
	model.AIService
	Routes []RouteItem `json:"routes"`
}

// ListServicesResult is the paginated result returned by ListServices.
type ListServicesResult struct {
	List  []*model.AIService `json:"list"`
	Total int64              `json:"total"`
}

// IAIServiceAdminBiz is the biz interface for admin AI service CRUD.
// All methods take actorID + actorName (extracted from admin JWT) so the
// registry can write accurate audit log entries.
type IAIServiceAdminBiz interface {
	// ListServices returns a paginated, optionally filtered list of AI services.
	ListServices(ctx context.Context, filter registry.ServiceFilter, page, pageSize int) (*ListServicesResult, error)

	// GetService returns a single AI service with its routes.
	GetService(ctx context.Context, id uint64) (*ServiceDetail, error)

	// CreateService creates a new AI service and returns the created record.
	CreateService(ctx context.Context, svc *model.AIService, actorID uint64, actorName string) (*model.AIService, error)

	// UpdateService updates an existing AI service.
	UpdateService(ctx context.Context, svc *model.AIService, actorID uint64, actorName string) error

	// DeprecateService soft-deletes an AI service.
	DeprecateService(ctx context.Context, id uint64, actorID uint64, actorName string, reason string) error

	// RestoreService un-deprecates an AI service.
	RestoreService(ctx context.Context, id uint64, actorID uint64, actorName string, reason string) error

	// GetCapabilitySchemas returns the capability schema for each service type.
	GetCapabilitySchemas(ctx context.Context) (map[string]*profile.CapabilitySchema, error)
}

// aiServiceAdminBiz is the concrete implementation of IAIServiceAdminBiz.
type aiServiceAdminBiz struct {
	reg registry.Registry
	db  *gorm.DB
}

// New creates a new IAIServiceAdminBiz backed by the given registry and DB.
// The DB reference is used for route queries (not exposed via registry.Registry).
func New(reg registry.Registry, db *gorm.DB) IAIServiceAdminBiz {
	return &aiServiceAdminBiz{reg: reg, db: db}
}

// ListServices returns a paginated slice of AI services matching the filter.
// page is 1-based; pageSize is capped to 100.
func (b *aiServiceAdminBiz) ListServices(ctx context.Context, filter registry.ServiceFilter, page, pageSize int) (*ListServicesResult, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// Fetch all matching services from the registry (no pagination at the registry layer).
	all, err := b.reg.ListServices(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("aiservice_admin.ListServices: %w", err)
	}

	total := int64(len(all))
	offset := (page - 1) * pageSize
	if offset >= len(all) {
		return &ListServicesResult{List: []*model.AIService{}, Total: total}, nil
	}
	end := offset + pageSize
	if end > len(all) {
		end = len(all)
	}

	return &ListServicesResult{List: all[offset:end], Total: total}, nil
}

// GetService returns a single AI service with its associated routes.
func (b *aiServiceAdminBiz) GetService(ctx context.Context, id uint64) (*ServiceDetail, error) {
	svc, err := b.reg.GetService(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("aiservice_admin.GetService: %w", err)
	}

	// Load routes with provider preloaded.
	var routes []model.AIServiceRoute
	if err := b.db.WithContext(ctx).
		Preload("Provider").
		Where("model_id = ?", id).
		Order("priority ASC, id ASC").
		Find(&routes).Error; err != nil {
		return nil, fmt.Errorf("aiservice_admin.GetService: load routes: %w", err)
	}

	routeItems := make([]RouteItem, 0, len(routes))
	for _, r := range routes {
		item := RouteItem{
			ID:                 r.ID,
			ProviderID:         r.ProviderID,
			ProviderModelID:    r.ProviderModelID,
			Priority:           r.Priority,
			PricingUnit:        r.PricingUnit,
			InputPricePerMTok:  r.InputPricePerMTok,
			OutputPricePerMTok: r.OutputPricePerMTok,
			PricePerCall:       r.PricePerCall,
			PricePerSecond:     r.PricePerSecond,
			IsActive:           r.IsActive,
		}
		if r.Provider != nil {
			item.ProviderName = r.Provider.Name
		}
		routeItems = append(routeItems, item)
	}

	return &ServiceDetail{AIService: *svc, Routes: routeItems}, nil
}

// CreateService validates service_type and delegates to the registry SaveService.
func (b *aiServiceAdminBiz) CreateService(ctx context.Context, svc *model.AIService, actorID uint64, actorName string) (*model.AIService, error) {
	if err := validateServiceType(svc.ServiceType); err != nil {
		return nil, err
	}
	if err := b.reg.SaveService(ctx, svc, actorID, actorName); err != nil {
		return nil, fmt.Errorf("aiservice_admin.CreateService: %w", err)
	}
	return svc, nil
}

// UpdateService validates service_type and delegates to the registry SaveService.
// The caller is responsible for loading the existing record and merging fields.
func (b *aiServiceAdminBiz) UpdateService(ctx context.Context, svc *model.AIService, actorID uint64, actorName string) error {
	if err := validateServiceType(svc.ServiceType); err != nil {
		return err
	}
	if err := b.reg.SaveService(ctx, svc, actorID, actorName); err != nil {
		return fmt.Errorf("aiservice_admin.UpdateService: %w", err)
	}
	return nil
}

// DeprecateService soft-deletes the service by setting deprecated_at.
func (b *aiServiceAdminBiz) DeprecateService(ctx context.Context, id uint64, actorID uint64, actorName string, reason string) error {
	if err := b.reg.DeprecateService(ctx, id, actorID, actorName, reason); err != nil {
		return fmt.Errorf("aiservice_admin.DeprecateService: %w", err)
	}
	return nil
}

// RestoreService clears deprecated_at.
func (b *aiServiceAdminBiz) RestoreService(ctx context.Context, id uint64, actorID uint64, actorName string, reason string) error {
	if reason == "" {
		return errno.ErrAIRestoreRequiresReason.SetMessage("恢复服务必须填写原因")
	}
	if err := b.reg.RestoreService(ctx, id, actorID, actorName, reason); err != nil {
		return fmt.Errorf("aiservice_admin.RestoreService: %w", err)
	}
	return nil
}

// GetCapabilitySchemas returns the schemas for all three service types.
func (b *aiServiceAdminBiz) GetCapabilitySchemas(_ context.Context) (map[string]*profile.CapabilitySchema, error) {
	schemas := make(map[string]*profile.CapabilitySchema, 3)
	for _, st := range []string{"llm", "ocr", "asr"} {
		s, err := profile.SchemaFor(st)
		if err != nil {
			return nil, fmt.Errorf("aiservice_admin.GetCapabilitySchemas: %w", err)
		}
		schemas[st] = s
	}
	return schemas, nil
}

// validateServiceType returns an error when serviceType is not one of llm | ocr | asr.
func validateServiceType(serviceType string) error {
	switch serviceType {
	case "llm", "ocr", "asr":
		return nil
	default:
		return errno.ErrInvalidParameter.SetMessage("service_type 必须为 llm、ocr 或 asr")
	}
}
