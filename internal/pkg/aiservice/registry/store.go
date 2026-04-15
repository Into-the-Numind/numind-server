// Package registry provides DB access, in-memory caching, and task-profile
// resolution for the AI Service Manager.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// Store interfaces (allow mocking in tests)
// ----------------------------------------------------------------------------

// IStore defines all DB operations used by the registry.
// Implementations are expected to be stateless — they hold only a *gorm.DB.
type IStore interface {
	// Service CRUD
	GetService(ctx context.Context, id uint64) (*model.AIService, error)
	ListServices(ctx context.Context, filter ServiceFilter) ([]*model.AIService, error)
	SaveService(ctx context.Context, svc *model.AIService) error
	SetServiceDeprecated(ctx context.Context, id uint64, deprecatedAt *time.Time) error

	// Task Profile CRUD
	GetTaskProfile(ctx context.Context, taskID string) (*model.TaskProfile, error)
	ListTaskProfiles(ctx context.Context) ([]*model.TaskProfile, error)
	UpsertTaskProfile(ctx context.Context, tp *model.TaskProfile) error

	// Task Profile ↔ Service bindings
	ReplaceTaskBindings(ctx context.Context, taskProfileID uint64, bindings []TaskBinding) error

	// Route resolution helper: loads a fully-joined AIService + AIServiceRoute + LLMProvider.
	GetResolvedRoute(ctx context.Context, serviceID uint64) (*resolvedRouteRow, error)

	// Audit log
	InsertAuditLog(ctx context.Context, entry *model.AIServiceAuditLog) error
}

// ----------------------------------------------------------------------------
// Supporting types
// ----------------------------------------------------------------------------

// ServiceFilter is used for listing AI services with optional filters.
type ServiceFilter struct {
	// ServiceType, when non-empty, filters to a specific type (llm | ocr | asr).
	ServiceType string
	// IncludeDeprecated, when true, also returns deprecated services.
	IncludeDeprecated bool
}

// TaskBinding captures a single service binding for a task profile.
type TaskBinding struct {
	ServiceID uint64
	Role      string // model.TaskProfileRoleFallback | model.TaskProfileRoleAllowed
	Priority  int
}

// resolvedRouteRow is the internal flat struct populated by the JOIN query in GetResolvedRoute.
// It is not exported; callers receive ResolvedRoute (see registry.go).
type resolvedRouteRow struct {
	ServiceID          uint64
	ModelKey           string
	ServiceType        string
	CapabilityJSON     model.JSONMap
	LatencyTier        string
	QualityTier        string
	DeprecatedAt       *time.Time
	IsActive           bool
	ProviderID         uint64
	ProviderName       string
	ProviderBaseURL    string
	ProviderAPIKey     string
	ProviderModelID    string
	RoutePriority      int
	RouteIsActive      bool
	PricingUnit        string
	InputPricePerMTok  float64
	OutputPricePerMTok float64
	PricePerCall       *float64
	PricePerSecond     *float64
}

// ----------------------------------------------------------------------------
// gormStore — GORM-backed implementation of IStore
// ----------------------------------------------------------------------------

// gormStore implements IStore using a *gorm.DB.
type gormStore struct {
	db *gorm.DB
}

// NewStore creates a new GORM-backed IStore.
func NewStore(db *gorm.DB) IStore {
	return &gormStore{db: db}
}

// GetService fetches a single AIService by ID.
// Returns errno.ErrAIServiceNotFound when the record does not exist.
func (s *gormStore) GetService(ctx context.Context, id uint64) (*model.AIService, error) {
	var svc model.AIService
	if err := s.db.WithContext(ctx).First(&svc, id).Error; err != nil {
		if isNotFound(err) {
			return nil, errno.ErrAIServiceNotFound
		}
		return nil, fmt.Errorf("gormStore.GetService: %w", err)
	}
	return &svc, nil
}

// ListServices returns all AIService records matching the filter.
func (s *gormStore) ListServices(ctx context.Context, filter ServiceFilter) ([]*model.AIService, error) {
	q := s.db.WithContext(ctx).Model(&model.AIService{})
	if filter.ServiceType != "" {
		q = q.Where("service_type = ?", filter.ServiceType)
	}
	if !filter.IncludeDeprecated {
		q = q.Where("deprecated_at IS NULL")
	}
	var services []*model.AIService
	if err := q.Order("sort_order ASC, id ASC").Find(&services).Error; err != nil {
		return nil, fmt.Errorf("gormStore.ListServices: %w", err)
	}
	return services, nil
}

// SaveService creates or updates an AIService (upsert by primary key).
// If svc.ID == 0, a new record is created; otherwise the existing record is updated.
func (s *gormStore) SaveService(ctx context.Context, svc *model.AIService) error {
	if svc.ID == 0 {
		if err := s.db.WithContext(ctx).Create(svc).Error; err != nil {
			return fmt.Errorf("gormStore.SaveService (create): %w", err)
		}
	} else {
		if err := s.db.WithContext(ctx).Save(svc).Error; err != nil {
			return fmt.Errorf("gormStore.SaveService (update): %w", err)
		}
	}
	return nil
}

// SetServiceDeprecated updates the deprecated_at column of a service.
// Pass a non-nil *time.Time to deprecate, nil to restore (undeprecate).
func (s *gormStore) SetServiceDeprecated(ctx context.Context, id uint64, deprecatedAt *time.Time) error {
	result := s.db.WithContext(ctx).Model(&model.AIService{}).
		Where("id = ?", id).
		Update("deprecated_at", deprecatedAt)
	if result.Error != nil {
		return fmt.Errorf("gormStore.SetServiceDeprecated: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return errno.ErrAIServiceNotFound
	}
	return nil
}

// GetTaskProfile fetches a single TaskProfile by task_id string.
// Returns errno.ErrAITaskNotFound when no matching record exists.
func (s *gormStore) GetTaskProfile(ctx context.Context, taskID string) (*model.TaskProfile, error) {
	var tp model.TaskProfile
	err := s.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		First(&tp).Error
	if err != nil {
		if isNotFound(err) {
			return nil, errno.ErrAITaskNotFound
		}
		return nil, fmt.Errorf("gormStore.GetTaskProfile: %w", err)
	}
	return &tp, nil
}

// ListTaskProfiles returns all task profiles ordered by id.
func (s *gormStore) ListTaskProfiles(ctx context.Context) ([]*model.TaskProfile, error) {
	var profiles []*model.TaskProfile
	if err := s.db.WithContext(ctx).Order("id ASC").Find(&profiles).Error; err != nil {
		return nil, fmt.Errorf("gormStore.ListTaskProfiles: %w", err)
	}
	return profiles, nil
}

// UpsertTaskProfile creates or updates a TaskProfile.
func (s *gormStore) UpsertTaskProfile(ctx context.Context, tp *model.TaskProfile) error {
	if tp.ID == 0 {
		if err := s.db.WithContext(ctx).Create(tp).Error; err != nil {
			return fmt.Errorf("gormStore.UpsertTaskProfile (create): %w", err)
		}
	} else {
		if err := s.db.WithContext(ctx).Save(tp).Error; err != nil {
			return fmt.Errorf("gormStore.UpsertTaskProfile (update): %w", err)
		}
	}
	return nil
}

// ReplaceTaskBindings replaces all TaskProfileService rows for a given task profile
// with the provided bindings. The replacement is done in a single transaction:
//  1. DELETE existing rows for taskProfileID
//  2. INSERT new rows from bindings
func (s *gormStore) ReplaceTaskBindings(ctx context.Context, taskProfileID uint64, bindings []TaskBinding) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete existing bindings for this profile.
		if err := tx.Where("task_profile_id = ?", taskProfileID).
			Delete(&model.TaskProfileService{}).Error; err != nil {
			return fmt.Errorf("gormStore.ReplaceTaskBindings (delete): %w", err)
		}
		// Insert new bindings.
		for _, b := range bindings {
			row := model.TaskProfileService{
				TaskProfileID: taskProfileID,
				ServiceID:     b.ServiceID,
				Role:          b.Role,
				Priority:      b.Priority,
			}
			if err := tx.Create(&row).Error; err != nil {
				return fmt.Errorf("gormStore.ReplaceTaskBindings (insert serviceID=%d): %w", b.ServiceID, err)
			}
		}
		return nil
	})
}

// GetResolvedRoute loads the route resolution data for a single service ID by
// joining ai_service → ai_service_route → llm_provider. It picks the route
// with the highest priority (ORDER BY priority DESC, route.id ASC) and only
// returns active, non-deprecated services.
//
// Returns errno.ErrAIServiceNotFound when no active route can be found.
func (s *gormStore) GetResolvedRoute(ctx context.Context, serviceID uint64) (*resolvedRouteRow, error) {
	// Raw query so we can do a multi-table JOIN and alias columns without
	// fighting GORM's auto-naming.
	const q = `
SELECT
  s.id                     AS service_id,
  s.model_key              AS model_key,
  s.service_type           AS service_type,
  s.capability_json        AS capability_json,
  s.latency_tier           AS latency_tier,
  s.quality_tier           AS quality_tier,
  s.deprecated_at          AS deprecated_at,
  s.is_active              AS is_active,
  p.id                     AS provider_id,
  p.name                   AS provider_name,
  p.base_url               AS provider_base_url,
  p.api_key                AS provider_api_key,
  r.provider_model_id      AS provider_model_id,
  r.priority               AS route_priority,
  r.is_active              AS route_is_active,
  r.pricing_unit           AS pricing_unit,
  r.input_price_per_mtok   AS input_price_per_mtok,
  r.output_price_per_mtok  AS output_price_per_mtok,
  r.price_per_call         AS price_per_call,
  r.price_per_second       AS price_per_second
FROM ai_service s
JOIN ai_service_route r ON r.model_id = s.id AND r.is_active = true
JOIN llm_provider p ON p.id = r.provider_id AND p.is_active = true
WHERE s.id = ?
  AND s.deprecated_at IS NULL
  AND s.is_active = true
ORDER BY r.priority DESC, r.id ASC
LIMIT 1
`
	type rawRow struct {
		ServiceID          uint64         `gorm:"column:service_id"`
		ModelKey           string         `gorm:"column:model_key"`
		ServiceType        string         `gorm:"column:service_type"`
		CapabilityJSONStr  *string        `gorm:"column:capability_json"`
		LatencyTier        string         `gorm:"column:latency_tier"`
		QualityTier        string         `gorm:"column:quality_tier"`
		DeprecatedAt       *time.Time     `gorm:"column:deprecated_at"`
		IsActive           bool           `gorm:"column:is_active"`
		ProviderID         uint64         `gorm:"column:provider_id"`
		ProviderName       string         `gorm:"column:provider_name"`
		ProviderBaseURL    string         `gorm:"column:provider_base_url"`
		ProviderAPIKey     string         `gorm:"column:provider_api_key"`
		ProviderModelID    string         `gorm:"column:provider_model_id"`
		RoutePriority      int            `gorm:"column:route_priority"`
		RouteIsActive      bool           `gorm:"column:route_is_active"`
		PricingUnit        string         `gorm:"column:pricing_unit"`
		InputPricePerMTok  float64        `gorm:"column:input_price_per_mtok"`
		OutputPricePerMTok float64        `gorm:"column:output_price_per_mtok"`
		PricePerCall       *float64       `gorm:"column:price_per_call"`
		PricePerSecond     *float64       `gorm:"column:price_per_second"`
	}

	var row rawRow
	if err := s.db.WithContext(ctx).Raw(q, serviceID).Scan(&row).Error; err != nil {
		return nil, fmt.Errorf("gormStore.GetResolvedRoute: %w", err)
	}
	if row.ServiceID == 0 {
		// GORM Scan does not return ErrRecordNotFound for raw queries — check zero ServiceID.
		return nil, errno.ErrAIServiceNotFound
	}

	// Parse capability_json from the nullable string column.
	var capJSON model.JSONMap
	if row.CapabilityJSONStr != nil && *row.CapabilityJSONStr != "" {
		if err := json.Unmarshal([]byte(*row.CapabilityJSONStr), &capJSON); err != nil {
			return nil, fmt.Errorf("gormStore.GetResolvedRoute: unmarshal capability_json: %w", err)
		}
	}

	return &resolvedRouteRow{
		ServiceID:          row.ServiceID,
		ModelKey:           row.ModelKey,
		ServiceType:        row.ServiceType,
		CapabilityJSON:     capJSON,
		LatencyTier:        row.LatencyTier,
		QualityTier:        row.QualityTier,
		DeprecatedAt:       row.DeprecatedAt,
		IsActive:           row.IsActive,
		ProviderID:         row.ProviderID,
		ProviderName:       row.ProviderName,
		ProviderBaseURL:    row.ProviderBaseURL,
		ProviderAPIKey:     row.ProviderAPIKey,
		ProviderModelID:    row.ProviderModelID,
		RoutePriority:      row.RoutePriority,
		RouteIsActive:      row.RouteIsActive,
		PricingUnit:        row.PricingUnit,
		InputPricePerMTok:  row.InputPricePerMTok,
		OutputPricePerMTok: row.OutputPricePerMTok,
		PricePerCall:       row.PricePerCall,
		PricePerSecond:     row.PricePerSecond,
	}, nil
}

// InsertAuditLog inserts an immutable audit log entry.
func (s *gormStore) InsertAuditLog(ctx context.Context, entry *model.AIServiceAuditLog) error {
	if err := s.db.WithContext(ctx).Create(entry).Error; err != nil {
		return fmt.Errorf("gormStore.InsertAuditLog: %w", err)
	}
	return nil
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// isNotFound reports whether a GORM error indicates a missing record.
func isNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}
