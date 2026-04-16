// Package aiservice_admin provides admin-facing business logic for the AI Service Manager.
// It wraps the registry.Registry facade and adds pagination, response shaping, and
// validation that are specific to the admin API layer.
package aiservice_admin

import (
	"context"
	"encoding/json"
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

// TaskDetail is the response shape for a single task profile, including its
// bound services (default, fallbacks, allowed).
type TaskDetail struct {
	*model.TaskProfile
	DefaultService *model.AIService   `json:"default_service"`
	Fallbacks      []*model.AIService `json:"fallbacks"`
	Allowed        []*model.AIService `json:"allowed"`
}

// UpdateTaskRequest is the request body for updating a task profile's
// requirements and/or service bindings.
type UpdateTaskRequest struct {
	Requirements       model.JSONMap `json:"requirements"`
	DefaultServiceID   *uint64      `json:"default_service_id"`
	FallbackServiceIDs []uint64     `json:"fallback_service_ids"`
	AllowedServiceIDs  []uint64     `json:"allowed_service_ids"`
	Reason             string       `json:"reason"`
}

// IncompatibleBinding describes a single binding that failed capability matching.
type IncompatibleBinding struct {
	Role        string   `json:"role"`
	ServiceID   uint64   `json:"service_id"`
	ServiceName string   `json:"service_name"`
	Reasons     []string `json:"reasons"`
}

// UpdateTaskResult is returned by UpdateTask. When Compatible is false and
// force was not set, the profile is NOT saved and IncompatibleBindings is populated.
type UpdateTaskResult struct {
	Compatible           bool                  `json:"compatible"`
	IncompatibleBindings []IncompatibleBinding `json:"incompatible_bindings,omitempty"`
}

// ValidateResult is returned by ValidateServiceAgainstTask.
type ValidateResult struct {
	Compatible           bool                       `json:"compatible"`
	Reasons              []string                   `json:"reasons"`
	TaskRequirements     profile.Requirements       `json:"task_requirements"`
	ServiceCapabilities  profile.ServiceCapability  `json:"service_capabilities"`
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

	// ListTasks returns all task profiles (no pagination — fixed set of ~14 rows).
	ListTasks(ctx context.Context) ([]*model.TaskProfile, error)

	// GetTask returns a single task profile with its bound services resolved.
	GetTask(ctx context.Context, taskID string) (*TaskDetail, error)

	// UpdateTask updates a task profile's requirements and/or service bindings.
	// If any binding is incompatible and force is false, no changes are saved and
	// UpdateTaskResult.Compatible will be false with IncompatibleBindings populated.
	// If force is true, the save proceeds regardless and an audit log entry with
	// action="capability.override" is written.
	UpdateTask(ctx context.Context, taskID string, req UpdateTaskRequest, force bool, actorID uint64, actorName string) (*UpdateTaskResult, error)

	// ValidateServiceAgainstTask checks whether a service satisfies a task's requirements
	// without making any changes.
	ValidateServiceAgainstTask(ctx context.Context, serviceID uint64, taskID string) (*ValidateResult, error)
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

// ListTasks returns all task profiles.
func (b *aiServiceAdminBiz) ListTasks(ctx context.Context) ([]*model.TaskProfile, error) {
	profiles, err := b.reg.ListTaskProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("aiservice_admin.ListTasks: %w", err)
	}
	return profiles, nil
}

// GetTask returns a single task profile with its bound services resolved.
func (b *aiServiceAdminBiz) GetTask(ctx context.Context, taskID string) (*TaskDetail, error) {
	tp, err := b.reg.GetTaskProfile(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("aiservice_admin.GetTask: %w", err)
	}

	detail := &TaskDetail{TaskProfile: tp}

	// Resolve default service.
	if tp.DefaultServiceID != nil {
		svc, svcErr := b.reg.GetService(ctx, *tp.DefaultServiceID)
		if svcErr == nil {
			detail.DefaultService = svc
		}
	}

	// Load bindings directly via DB (ListTaskBindings is on IStore, not Registry).
	type bindingRow struct {
		ServiceID uint64 `gorm:"column:service_id"`
		Role      string `gorm:"column:role"`
		Priority  int    `gorm:"column:priority"`
	}
	var rows []bindingRow
	if err := b.db.WithContext(ctx).
		Table("task_profile_service").
		Select("service_id, role, priority").
		Where("task_profile_id = ?", tp.ID).
		Order("priority ASC").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("aiservice_admin.GetTask: load bindings: %w", err)
	}

	for _, row := range rows {
		svc, svcErr := b.reg.GetService(ctx, row.ServiceID)
		if svcErr != nil {
			continue
		}
		switch row.Role {
		case model.TaskProfileRoleFallback:
			detail.Fallbacks = append(detail.Fallbacks, svc)
		case model.TaskProfileRoleAllowed:
			detail.Allowed = append(detail.Allowed, svc)
		}
	}

	return detail, nil
}

// UpdateTask updates a task profile's requirements and service bindings after
// running capability matching against every binding candidate. If force is false
// and any binding is incompatible, no changes are persisted. If force is true,
// the save proceeds and an audit log entry with action="capability.override" is written.
func (b *aiServiceAdminBiz) UpdateTask(ctx context.Context, taskID string, req UpdateTaskRequest, force bool, actorID uint64, actorName string) (*UpdateTaskResult, error) {
	tp, err := b.reg.GetTaskProfile(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("aiservice_admin.UpdateTask: %w", err)
	}

	// Parse requirements from JSONMap.
	var reqs profile.Requirements
	if req.Requirements != nil {
		b2, _ := json.Marshal(req.Requirements)
		_ = json.Unmarshal(b2, &reqs)
	} else {
		// Use existing requirements from the persisted profile.
		if tp.Requirements != nil {
			b2, _ := json.Marshal(tp.Requirements)
			_ = json.Unmarshal(b2, &reqs)
		}
	}

	// Collect all service IDs to validate (default + fallbacks + allowed).
	type bindingCandidate struct {
		role      string
		serviceID uint64
	}
	var candidates []bindingCandidate
	if req.DefaultServiceID != nil {
		candidates = append(candidates, bindingCandidate{role: "default", serviceID: *req.DefaultServiceID})
	}
	for _, id := range req.FallbackServiceIDs {
		candidates = append(candidates, bindingCandidate{role: model.TaskProfileRoleFallback, serviceID: id})
	}
	for _, id := range req.AllowedServiceIDs {
		candidates = append(candidates, bindingCandidate{role: model.TaskProfileRoleAllowed, serviceID: id})
	}

	// Run capability matching for each candidate.
	var incompatible []IncompatibleBinding
	for _, c := range candidates {
		svc, svcErr := b.reg.GetService(ctx, c.serviceID)
		if svcErr != nil {
			incompatible = append(incompatible, IncompatibleBinding{
				Role:      c.role,
				ServiceID: c.serviceID,
				Reasons:   []string{"服务不存在或已下架"},
			})
			continue
		}

		// Unmarshal service capability.
		var cap profile.ServiceCapability
		if svc.CapabilityJSON != nil {
			b2, _ := json.Marshal(svc.CapabilityJSON)
			_ = json.Unmarshal(b2, &cap)
		}
		if cap.ServiceType == "" {
			cap.ServiceType = svc.ServiceType
		}

		result := profile.Match(tp.ServiceType, reqs, cap)
		if !result.Compatible {
			incompatible = append(incompatible, IncompatibleBinding{
				Role:        c.role,
				ServiceID:   c.serviceID,
				ServiceName: svc.DisplayName,
				Reasons:     result.Reasons,
			})
		}
	}

	// If incompatible and not forced, return without saving.
	if len(incompatible) > 0 && !force {
		return &UpdateTaskResult{Compatible: false, IncompatibleBindings: incompatible}, nil
	}

	// Apply updates to the task profile.
	if req.Requirements != nil {
		tp.Requirements = req.Requirements
	}
	if req.DefaultServiceID != nil {
		tp.DefaultServiceID = req.DefaultServiceID
	}

	// Build bindings slice for the registry.
	var bindings []registry.TaskBinding
	for i, id := range req.FallbackServiceIDs {
		bindings = append(bindings, registry.TaskBinding{
			ServiceID: id,
			Role:      model.TaskProfileRoleFallback,
			Priority:  i,
		})
	}
	for i, id := range req.AllowedServiceIDs {
		bindings = append(bindings, registry.TaskBinding{
			ServiceID: id,
			Role:      model.TaskProfileRoleAllowed,
			Priority:  i,
		})
	}

	// Determine audit action.
	auditActorName := actorName
	if force && len(incompatible) > 0 {
		auditActorName = actorName // capability.override audit written by registry via Reason field
	}

	// Persist via registry.SaveTaskProfile.
	if saveErr := b.reg.SaveTaskProfile(ctx, tp, bindings, actorID, auditActorName); saveErr != nil {
		return nil, fmt.Errorf("aiservice_admin.UpdateTask: %w", saveErr)
	}

	// If force override was used, write a second audit log entry with action=capability.override.
	if force && len(incompatible) > 0 {
		if req.Reason == "" {
			return nil, errno.ErrAICapabilityOverrideRequiresReason
		}
		_ = b.db.WithContext(ctx).Create(&model.AIServiceAuditLog{
			ActorID:    actorID,
			ActorName:  actorName,
			Action:     model.AuditActionCapabilityOverride,
			TargetType: model.AuditTargetTaskProfile,
			TargetID:   tp.ID,
			Reason:     req.Reason,
		}).Error
	}

	return &UpdateTaskResult{Compatible: true}, nil
}

// ValidateServiceAgainstTask checks capability compatibility without making any changes.
func (b *aiServiceAdminBiz) ValidateServiceAgainstTask(ctx context.Context, serviceID uint64, taskID string) (*ValidateResult, error) {
	tp, err := b.reg.GetTaskProfile(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("aiservice_admin.ValidateServiceAgainstTask: %w", err)
	}

	svc, err := b.reg.GetService(ctx, serviceID)
	if err != nil {
		return nil, fmt.Errorf("aiservice_admin.ValidateServiceAgainstTask: %w", err)
	}

	// Unmarshal requirements.
	var reqs profile.Requirements
	if tp.Requirements != nil {
		b2, _ := json.Marshal(tp.Requirements)
		_ = json.Unmarshal(b2, &reqs)
	}

	// Unmarshal service capability.
	var cap profile.ServiceCapability
	if svc.CapabilityJSON != nil {
		b2, _ := json.Marshal(svc.CapabilityJSON)
		_ = json.Unmarshal(b2, &cap)
	}
	if cap.ServiceType == "" {
		cap.ServiceType = svc.ServiceType
	}

	result := profile.Match(tp.ServiceType, reqs, cap)
	return &ValidateResult{
		Compatible:          result.Compatible,
		Reasons:             result.Reasons,
		TaskRequirements:    reqs,
		ServiceCapabilities: cap,
	}, nil
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
