// Package aiservice_admin provides admin-facing business logic for the AI Service Manager.
// It wraps the registry.Registry facade and adds pagination, response shaping, and
// validation that are specific to the admin API layer.
package aiservice_admin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// RouteItem is the wire-format representation of a single ai_service_route row
// returned inside the service detail response (spec §4.4).
// Pricing fields were removed in T-arch (pricing_architecture_decision "搞法 2"):
// they are now read from pricing_rule, not ai_service_route.
type RouteItem struct {
	ID              uint64 `json:"id"`
	ProviderID      uint64 `json:"provider_id"`
	ProviderName    string `json:"provider_name"`
	ProviderModelID string `json:"provider_model_id"`
	Priority        int    `json:"priority"`
	IsActive        bool   `json:"is_active"`
}

// RouteDTO is a slim DTO for route CRUD responses that intentionally omits
// pricing fields. T-arch is dropping those columns from ai_service_route in
// parallel; this struct will remain valid after that migration.
type RouteDTO struct {
	ID              uint64 `json:"id"`
	ServiceID       uint64 `json:"service_id"`
	ProviderID      uint64 `json:"provider_id"`
	ProviderName    string `json:"provider_name"`
	ProviderModelID string `json:"provider_model_id"`
	Priority        int    `json:"priority"`
	IsActive        bool   `json:"is_active"`
}

// CreateRouteRequest is the request body for creating a new ai_service_route.
type CreateRouteRequest struct {
	ProviderID      uint64 `json:"provider_id" binding:"required"`
	ProviderModelID string `json:"provider_model_id" binding:"required,min=1"`
	Priority        int    `json:"priority"`  // default 0
	IsActive        *bool  `json:"is_active"` // default true; pointer to distinguish unset
}

// UpdateRouteRequest is the request body for partially updating an ai_service_route.
// provider_id is immutable after create (requires delete + recreate).
type UpdateRouteRequest struct {
	ProviderModelID *string `json:"provider_model_id,omitempty"`
	Priority        *int    `json:"priority,omitempty"`
	IsActive        *bool   `json:"is_active,omitempty"`
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

// TaskProfileListItem is the list-row shape returned by ListTasks. It embeds
// the base TaskProfile and adds aggregated counts of bound services by role so
// the admin table can render "fallback / allowed service count" columns
// without a separate detail fetch per row.
type TaskProfileListItem struct {
	*model.TaskProfile
	FallbackCount int `json:"fallback_count"`
	AllowedCount  int `json:"allowed_count"`
}

// UpdateTaskRequest is the request body for updating a task profile's
// requirements and/or service bindings.
type UpdateTaskRequest struct {
	Requirements       model.JSONMap `json:"requirements"`
	DefaultServiceID   *uint64       `json:"default_service_id"`
	FallbackServiceIDs []uint64      `json:"fallback_service_ids"`
	AllowedServiceIDs  []uint64      `json:"allowed_service_ids"`
	Reason             string        `json:"reason"`
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
	Compatible          bool                      `json:"compatible"`
	Reasons             []string                  `json:"reasons"`
	TaskRequirements    profile.Requirements      `json:"task_requirements"`
	ServiceCapabilities profile.ServiceCapability `json:"service_capabilities"`
}

// AuditLogFilter holds optional filters for listing audit log entries.
type AuditLogFilter struct {
	Actor      string
	TargetType string
	DateFrom   *time.Time
	DateTo     *time.Time
}

// AuditLogItem is the wire-format representation of a single audit log row
// returned by ListAuditLogs (spec §4.x GET /v1/admin/ai/audit-logs).
type AuditLogItem struct {
	ID         uint64    `json:"id"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	TargetName string    `json:"target_name,omitempty"`
	Diff       any       `json:"diff,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// ListAuditLogsResult is the paginated result returned by ListAuditLogs.
type ListAuditLogsResult struct {
	Items []AuditLogItem
	Total int64
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

	// ListTasks returns all task profiles (no pagination — fixed set of ~14 rows),
	// each annotated with aggregated binding counts (fallback / allowed).
	ListTasks(ctx context.Context) ([]*TaskProfileListItem, error)

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

	// ListAuditLogs returns a paginated list of audit log entries with optional
	// filters (actor LIKE, target_type exact, date range). Results are sorted
	// by created_at DESC. TargetName is resolved via a batched IN query per target_type.
	ListAuditLogs(ctx context.Context, filter AuditLogFilter, page, pageSize int) (*ListAuditLogsResult, error)

	// CreateRoute creates a new ai_service_route for the given service.
	// Returns the created RouteDTO, optional priority-conflict warnings, and any error.
	CreateRoute(ctx context.Context, serviceID uint64, req CreateRouteRequest, actorID uint64, actorName string) (*RouteDTO, []string, error)

	// UpdateRoute partially updates an existing ai_service_route.
	// Returns the updated RouteDTO, optional priority-conflict warnings, and any error.
	UpdateRoute(ctx context.Context, routeID uint64, req UpdateRouteRequest, actorID uint64, actorName string) (*RouteDTO, []string, error)

	// DeleteRoute removes a route after verifying the last-active guard.
	DeleteRoute(ctx context.Context, routeID uint64, actorID uint64, actorName string) error

	// ToggleRoute flips the is_active flag on a route after verifying the last-active guard.
	ToggleRoute(ctx context.Context, routeID uint64, actorID uint64, actorName string) (*RouteDTO, error)
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
// page is 1-based; pageSize is capped to 100. Pagination happens at the DB
// layer so memory use stays bounded as service count grows.
func (b *aiServiceAdminBiz) ListServices(ctx context.Context, filter registry.ServiceFilter, page, pageSize int) (*ListServicesResult, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	offset := (page - 1) * pageSize
	list, total, err := b.reg.ListServicesPaginated(ctx, filter, offset, pageSize)
	if err != nil {
		return nil, fmt.Errorf("aiservice_admin.ListServices: %w", err)
	}
	// GORM Find always initialises the slice; this guard protects JSON consumers
	// against any mock that returns nil instead of an empty slice.
	if list == nil {
		list = []*model.AIService{}
	}
	return &ListServicesResult{List: list, Total: total}, nil
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
			ID:              r.ID,
			ProviderID:      r.ProviderID,
			ProviderModelID: r.ProviderModelID,
			Priority:        r.Priority,
			IsActive:        r.IsActive,
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

// ListTasks returns all task profiles with aggregated binding counts
// (fallback / allowed) resolved via a single GROUP BY on task_profile_service.
func (b *aiServiceAdminBiz) ListTasks(ctx context.Context) ([]*TaskProfileListItem, error) {
	profiles, err := b.reg.ListTaskProfiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("aiservice_admin.ListTasks: %w", err)
	}

	type countRow struct {
		TaskProfileID uint64 `gorm:"column:task_profile_id"`
		Role          string `gorm:"column:role"`
		Cnt           int    `gorm:"column:cnt"`
	}
	var rows []countRow
	if err := b.db.WithContext(ctx).
		Table("task_profile_service").
		Select("task_profile_id, role, COUNT(*) AS cnt").
		Group("task_profile_id, role").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("aiservice_admin.ListTasks: aggregate bindings: %w", err)
	}
	counts := make(map[uint64]map[string]int, len(rows))
	for _, r := range rows {
		if counts[r.TaskProfileID] == nil {
			counts[r.TaskProfileID] = make(map[string]int, 2)
		}
		counts[r.TaskProfileID][r.Role] = r.Cnt
	}

	result := make([]*TaskProfileListItem, 0, len(profiles))
	for _, p := range profiles {
		c := counts[p.ID]
		result = append(result, &TaskProfileListItem{
			TaskProfile:   p,
			FallbackCount: c[model.TaskProfileRoleFallback],
			AllowedCount:  c[model.TaskProfileRoleAllowed],
		})
	}
	return result, nil
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
			log.C(ctx).Warnw("GetTask: failed to load service binding", "task_id", taskID, "service_id", row.ServiceID, "err", svcErr)
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

	// Guard: force override requires a reason before any DB write.
	if force && len(incompatible) > 0 && req.Reason == "" {
		return nil, errno.ErrAICapabilityOverrideRequiresReason.SetMessage("强制覆盖不兼容绑定必须填写原因")
	}

	// Persist via registry.SaveTaskProfile.
	if saveErr := b.reg.SaveTaskProfile(ctx, tp, bindings, actorID, actorName); saveErr != nil {
		return nil, fmt.Errorf("aiservice_admin.UpdateTask: %w", saveErr)
	}

	// If force override was used, write a second audit log entry with action=capability.override.
	if force && len(incompatible) > 0 {
		if err := b.db.WithContext(ctx).Create(&model.AIServiceAuditLog{
			ActorID:    actorID,
			ActorName:  actorName,
			Action:     model.AuditActionCapabilityOverride,
			TargetType: model.AuditTargetTaskProfile,
			TargetID:   tp.ID,
			Reason:     req.Reason,
		}).Error; err != nil {
			log.C(ctx).Warnw("UpdateTask: failed to write capability.override audit log", "task_id", taskID, "actor_id", actorID, "err", err)
		}
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

// ListAuditLogs returns a paginated, filtered list of audit log entries sorted by
// created_at DESC. TargetName is resolved by batching one IN query per target_type
// against the appropriate name table (ai_service, task_profile, llm_provider).
//
// Note on pagination: biz enforces canonical defaults (page ≥ 1, 1 ≤ pageSize ≤ 100).
// The controller pre-clamps inputs to the same bounds to reduce unnecessary DB round-trips,
// but biz is the authoritative enforcement point.
func (b *aiServiceAdminBiz) ListAuditLogs(ctx context.Context, filter AuditLogFilter, page, pageSize int) (*ListAuditLogsResult, error) {
	if filter.DateFrom != nil && filter.DateTo != nil && filter.DateFrom.After(*filter.DateTo) {
		return nil, errno.ErrInvalidParameter.SetMessage("date_from 不能晚于 date_to")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize

	q := b.db.WithContext(ctx).Model(&model.AIServiceAuditLog{})
	if filter.Actor != "" {
		q = q.Where("actor_name LIKE ?", "%"+filter.Actor+"%")
	}
	if filter.TargetType != "" {
		q = q.Where("target_type = ?", filter.TargetType)
	}
	if filter.DateFrom != nil {
		q = q.Where("created_at >= ?", *filter.DateFrom)
	}
	if filter.DateTo != nil {
		// Include the entire day by adding 24 hours when only a date is provided.
		end := filter.DateTo.Add(24 * time.Hour)
		q = q.Where("created_at < ?", end)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("aiservice_admin.ListAuditLogs: count: %w", err)
	}

	var rows []model.AIServiceAuditLog
	if err := q.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("aiservice_admin.ListAuditLogs: find: %w", err)
	}

	// Batch-resolve target names per target_type.
	nameByTypeID := b.resolveTargetNames(ctx, rows)

	items := make([]AuditLogItem, 0, len(rows))
	for _, r := range rows {
		compositeKey := fmt.Sprintf("%s:%d", r.TargetType, r.TargetID)
		item := AuditLogItem{
			ID:         r.ID,
			Actor:      r.ActorName,
			Action:     r.Action,
			TargetType: r.TargetType,
			TargetID:   strconv.FormatUint(r.TargetID, 10),
			TargetName: nameByTypeID[compositeKey],
			Reason:     r.Reason,
			CreatedAt:  r.CreatedAt,
		}
		// Pass DiffJSON as-is; frontend expects {before, after} shape.
		if len(r.DiffJSON) > 0 {
			item.Diff = r.DiffJSON
		}
		items = append(items, item)
	}

	return &ListAuditLogsResult{Items: items, Total: total}, nil
}

// resolveTargetNames collects target IDs grouped by target_type from the given audit rows,
// then performs one IN query per type to fetch display names. Returns a composite-key map
// keyed by "target_type:id" to prevent aliasing when two different target types share the
// same numeric ID (e.g. a service with id=5 and a task_profile with id=5).
func (b *aiServiceAdminBiz) resolveTargetNames(ctx context.Context, rows []model.AIServiceAuditLog) map[string]string {
	nameByTypeID := make(map[string]string, len(rows))
	if len(rows) == 0 {
		return nameByTypeID
	}

	// Group IDs by target_type.
	byType := make(map[string][]uint64)
	for _, r := range rows {
		byType[r.TargetType] = append(byType[r.TargetType], r.TargetID)
	}

	type nameRow struct {
		ID          uint64 `gorm:"column:id"`
		DisplayName string `gorm:"column:display_name"`
	}

	for targetType, ids := range byType {
		var nameRows []nameRow
		var err error
		switch targetType {
		case model.AuditTargetService:
			err = b.db.WithContext(ctx).
				Table("ai_service").
				Select("id, display_name").
				Where("id IN ?", ids).
				Scan(&nameRows).Error
		case model.AuditTargetTaskProfile:
			err = b.db.WithContext(ctx).
				Table("task_profile").
				Select("id, display_name").
				Where("id IN ?", ids).
				Scan(&nameRows).Error
		default:
			// Unknown target_type (e.g. "provider" added by T2/T4) — skip gracefully.
			log.C(ctx).Debugw("resolveTargetNames: unknown target_type, skipping", "target_type", targetType)
			continue
		}
		if err != nil {
			log.C(ctx).Warnw("resolveTargetNames: failed to resolve names", "target_type", targetType, "err", err)
			continue
		}
		for _, nr := range nameRows {
			nameByTypeID[fmt.Sprintf("%s:%d", targetType, nr.ID)] = nr.DisplayName
		}
	}
	return nameByTypeID
}

// routeDTOFromModel converts an AIServiceRoute model (with optional Provider preload)
// to the slim RouteDTO used in CRUD responses.
func routeDTOFromModel(r *model.AIServiceRoute) *RouteDTO {
	dto := &RouteDTO{
		ID:              r.ID,
		ServiceID:       r.ModelID,
		ProviderID:      r.ProviderID,
		ProviderModelID: r.ProviderModelID,
		Priority:        r.Priority,
		IsActive:        r.IsActive,
	}
	if r.Provider != nil {
		dto.ProviderName = r.Provider.Name
	}
	return dto
}

// checkPriorityConflicts returns warning strings when another active route on
// the same service shares the same priority as the given route.
func (b *aiServiceAdminBiz) checkPriorityConflicts(ctx context.Context, serviceID uint64, excludeRouteID uint64, priority int) ([]string, error) {
	type conflictRow struct {
		ID              uint64 `gorm:"column:id"`
		ProviderModelID string `gorm:"column:provider_model_id"`
	}
	var conflicts []conflictRow
	q := b.db.WithContext(ctx).
		Table("ai_service_route").
		Select("id, provider_model_id").
		Where("model_id = ? AND is_active = true AND priority = ?", serviceID, priority)
	if excludeRouteID > 0 {
		q = q.Where("id != ?", excludeRouteID)
	}
	if err := q.Scan(&conflicts).Error; err != nil {
		return nil, fmt.Errorf("checkPriorityConflicts: %w", err)
	}
	var warnings []string
	for _, c := range conflicts {
		warnings = append(warnings, fmt.Sprintf("priority %d conflicts with route %d (%s)", priority, c.ID, c.ProviderModelID))
	}
	return warnings, nil
}

// countOtherActiveRoutes returns the number of active routes for serviceID
// excluding the given routeID. Used by the last-active guard.
//
// countOtherActiveRoutes is a best-effort check; it does NOT protect
// against concurrent Delete/Toggle-off requests racing past the guard
// and leaving the service with 0 active routes. For a low-traffic
// admin panel this risk is accepted. Hard enforcement would require
// a DB-level trigger or SELECT FOR UPDATE transaction.
func (b *aiServiceAdminBiz) countOtherActiveRoutes(ctx context.Context, serviceID uint64, excludeRouteID uint64) (int64, error) {
	var count int64
	err := b.db.WithContext(ctx).
		Table("ai_service_route").
		Where("model_id = ? AND is_active = true AND id != ?", serviceID, excludeRouteID).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("countOtherActiveRoutes: %w", err)
	}
	return count, nil
}

// writeRouteAudit writes an audit log entry best-effort. If the audit
// INSERT fails (disk pressure, table lock), it logs a Warn and the
// mutation remains committed without an audit trail. Do NOT add retry
// logic here — that could cause double-auditing under transient failures.
func (b *aiServiceAdminBiz) writeRouteAudit(ctx context.Context, action string, routeID uint64, before, after interface{}, actorID uint64, actorName string) {
	diff := model.JSONMap{}
	if before != nil {
		diff["before"] = before
	}
	if after != nil {
		diff["after"] = after
	}
	entry := &model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     action,
		TargetType: model.AuditTargetRoute,
		TargetID:   routeID,
		DiffJSON:   diff,
	}
	if err := b.db.WithContext(ctx).Create(entry).Error; err != nil {
		log.C(ctx).Warnw("writeRouteAudit: failed to write audit log", "action", action, "route_id", routeID, "err", err)
	}
}

// CreateRoute creates a new ai_service_route for the given service.
func (b *aiServiceAdminBiz) CreateRoute(ctx context.Context, serviceID uint64, req CreateRouteRequest, actorID uint64, actorName string) (*RouteDTO, []string, error) {
	// Validate service exists.
	if _, err := b.reg.GetService(ctx, serviceID); err != nil {
		return nil, nil, fmt.Errorf("aiservice_admin.CreateRoute: %w", err)
	}

	// Validate provider exists.
	var provider model.LLMProvider
	if err := b.db.WithContext(ctx).First(&provider, req.ProviderID).Error; err != nil {
		return nil, nil, errno.ErrInvalidParameter.SetMessage("provider_id %d 不存在", req.ProviderID)
	}

	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	route := &model.AIServiceRoute{
		ModelID:         serviceID,
		ProviderID:      req.ProviderID,
		ProviderModelID: req.ProviderModelID,
		Priority:        req.Priority,
		IsActive:        isActive,
	}
	if err := b.db.WithContext(ctx).Create(route).Error; err != nil {
		return nil, nil, fmt.Errorf("aiservice_admin.CreateRoute: create: %w", err)
	}

	// Attach provider name for DTO.
	route.Provider = &provider

	// Write audit log.
	b.writeRouteAudit(ctx, model.AuditActionRouteCreate, route.ID, nil, routeDTOFromModel(route), actorID, actorName)

	// Non-blocking priority conflict check.
	var warnings []string
	if isActive {
		w, wErr := b.checkPriorityConflicts(ctx, serviceID, route.ID, route.Priority)
		if wErr == nil {
			warnings = w
		}
	}

	return routeDTOFromModel(route), warnings, nil
}

// UpdateRoute partially updates an existing ai_service_route.
func (b *aiServiceAdminBiz) UpdateRoute(ctx context.Context, routeID uint64, req UpdateRouteRequest, actorID uint64, actorName string) (*RouteDTO, []string, error) {
	// Reject explicit empty string for provider_model_id even though it is a pointer field
	// (binding:"required" only guards Create; here we guard the update path explicitly).
	if req.ProviderModelID != nil && *req.ProviderModelID == "" {
		return nil, nil, errno.ErrInvalidParameter.SetMessage("provider_model_id 不能为空字符串")
	}

	var route model.AIServiceRoute
	if err := b.db.WithContext(ctx).Preload("Provider").First(&route, routeID).Error; err != nil {
		return nil, nil, errno.ErrInvalidParameter.SetMessage("路由 %d 不存在", routeID)
	}
	before := routeDTOFromModel(&route)

	// Last-active guard: reject if setting is_active=false would leave 0 active routes.
	if req.IsActive != nil && !*req.IsActive && route.IsActive {
		count, err := b.countOtherActiveRoutes(ctx, route.ModelID, routeID)
		if err != nil {
			return nil, nil, fmt.Errorf("aiservice_admin.UpdateRoute: %w", err)
		}
		if count == 0 {
			return nil, nil, errno.ErrInvalidParameter.SetMessage("至少保留一条激活路由")
		}
	}

	// Apply partial updates.
	updates := map[string]interface{}{}
	if req.ProviderModelID != nil {
		updates["provider_model_id"] = *req.ProviderModelID
		route.ProviderModelID = *req.ProviderModelID
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
		route.Priority = *req.Priority
	}
	if req.IsActive != nil {
		updates["is_active"] = *req.IsActive
		route.IsActive = *req.IsActive
	}

	if len(updates) > 0 {
		if err := b.db.WithContext(ctx).Model(&route).Updates(updates).Error; err != nil {
			return nil, nil, fmt.Errorf("aiservice_admin.UpdateRoute: update: %w", err)
		}
	}

	after := routeDTOFromModel(&route)
	b.writeRouteAudit(ctx, model.AuditActionRouteUpdate, routeID, before, after, actorID, actorName)

	// Non-blocking priority conflict check (only when resulting route is active).
	var warnings []string
	if route.IsActive {
		w, wErr := b.checkPriorityConflicts(ctx, route.ModelID, routeID, route.Priority)
		if wErr == nil {
			warnings = w
		}
	}

	return after, warnings, nil
}

// DeleteRoute removes a route after verifying the last-active guard.
func (b *aiServiceAdminBiz) DeleteRoute(ctx context.Context, routeID uint64, actorID uint64, actorName string) error {
	var route model.AIServiceRoute
	if err := b.db.WithContext(ctx).First(&route, routeID).Error; err != nil {
		return errno.ErrInvalidParameter.SetMessage("路由 %d 不存在", routeID)
	}

	// Last-active guard: only applies when the route is currently active.
	if route.IsActive {
		count, err := b.countOtherActiveRoutes(ctx, route.ModelID, routeID)
		if err != nil {
			return fmt.Errorf("aiservice_admin.DeleteRoute: %w", err)
		}
		if count == 0 {
			return errno.ErrInvalidParameter.SetMessage("至少保留一条激活路由")
		}
	}

	before := routeDTOFromModel(&route)
	if err := b.db.WithContext(ctx).Delete(&route).Error; err != nil {
		return fmt.Errorf("aiservice_admin.DeleteRoute: delete: %w", err)
	}

	b.writeRouteAudit(ctx, model.AuditActionRouteDelete, routeID, before, nil, actorID, actorName)
	return nil
}

// ToggleRoute flips the is_active flag on a route after verifying the last-active guard.
func (b *aiServiceAdminBiz) ToggleRoute(ctx context.Context, routeID uint64, actorID uint64, actorName string) (*RouteDTO, error) {
	var route model.AIServiceRoute
	if err := b.db.WithContext(ctx).Preload("Provider").First(&route, routeID).Error; err != nil {
		return nil, errno.ErrInvalidParameter.SetMessage("路由 %d 不存在", routeID)
	}
	before := routeDTOFromModel(&route)

	newActive := !route.IsActive

	// Last-active guard: reject toggling active→inactive when it would leave 0 active routes.
	if route.IsActive && !newActive {
		count, err := b.countOtherActiveRoutes(ctx, route.ModelID, routeID)
		if err != nil {
			return nil, fmt.Errorf("aiservice_admin.ToggleRoute: %w", err)
		}
		if count == 0 {
			return nil, errno.ErrInvalidParameter.SetMessage("至少保留一条激活路由")
		}
	}

	if err := b.db.WithContext(ctx).Model(&route).Update("is_active", newActive).Error; err != nil {
		return nil, fmt.Errorf("aiservice_admin.ToggleRoute: update: %w", err)
	}
	route.IsActive = newActive

	after := routeDTOFromModel(&route)
	b.writeRouteAudit(ctx, model.AuditActionRouteToggle, routeID, before, after, actorID, actorName)

	return after, nil
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
