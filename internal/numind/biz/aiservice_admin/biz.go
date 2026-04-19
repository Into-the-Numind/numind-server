// Package aiservice_admin provides admin-facing business logic for the AI Service Manager.
// It wraps the registry.Registry facade and adds pagination, response shaping, and
// validation that are specific to the admin API layer.
package aiservice_admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/httpclient"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ProviderDTO is the wire-format representation of an llm_provider row.
// api_key is always masked (MaskedAPIKey output) — raw key never leaves the server.
type ProviderDTO struct {
	ID          uint64    `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	BaseURL     string    `json:"base_url"`
	APIKey      string    `json:"api_key"` // MaskedAPIKey() result, e.g. "****abcd"
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CreateProviderRequest is the request body for creating a new provider.
//
// APIKey enforces min=8 to align with the admin-web ProviderEdit form's
// client-side guard. The minimum is a heuristic for "user accidentally
// pasted truncated key"; if a future provider legitimately uses shorter
// credentials, relax this here and on the frontend together.
type CreateProviderRequest struct {
	Name        string `json:"name"         binding:"required"`
	DisplayName string `json:"display_name" binding:"required"`
	BaseURL     string `json:"base_url"     binding:"required"`
	APIKey      string `json:"api_key"      binding:"required,min=8"`
	IsActive    *bool  `json:"is_active"`
}

// UpdateProviderRequest is the request body for updating a provider.
// Pointer fields: nil = preserve existing; non-nil empty string for APIKey = preserve existing;
// non-nil non-empty APIKey = update.
type UpdateProviderRequest struct {
	DisplayName *string `json:"display_name,omitempty"`
	BaseURL     *string `json:"base_url,omitempty"`
	APIKey      *string `json:"api_key,omitempty"`
	IsActive    *bool   `json:"is_active,omitempty"`
}

// TestConnectionResult is the result of probing an OpenAI-compatible provider endpoint.
type TestConnectionResult struct {
	Success    bool   `json:"success"`
	LatencyMs  int64  `json:"latency_ms,omitempty"`
	Error      string `json:"error,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

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

// ServiceListItem embeds an AIService plus derived metadata for the admin list view.
// RouteCount is the number of *active* ai_service_route rows pointing at this service;
// the admin UI uses it to flag orphan services (route_count == 0), which caused the
// 2026-04-19 SalesRAG outage.
type ServiceListItem struct {
	*model.AIService
	RouteCount int `json:"route_count"`
}

// ListServicesResult is the paginated result returned by ListServices.
type ListServicesResult struct {
	List  []*ServiceListItem `json:"list"`
	Total int64              `json:"total"`
}

// CreateServiceWithRouteRequest is the request body for POST /v1/admin/ai/services-with-route.
// Both nested payloads are validated, then persisted atomically within a single transaction
// so admin UI can never produce an ai_service row without a matching ai_service_route.
type CreateServiceWithRouteRequest struct {
	Service CreateServiceInner `json:"service" binding:"required"`
	Route   CreateRouteInner   `json:"route"   binding:"required"`
}

// CreateServiceInner mirrors controller.createServiceReq but lives in the biz package so
// the atomic endpoint can reuse it without a controller→biz→controller detour.
type CreateServiceInner struct {
	ModelKey         string                `json:"model_key"        binding:"required"`
	DisplayName      string                `json:"display_name"     binding:"required"`
	ServiceType      string                `json:"service_type"     binding:"required"`
	CapabilityJSON   model.JSONMap         `json:"capability_json"`
	LatencyTier      string                `json:"latency_tier"`
	QualityTier      string                `json:"quality_tier"`
	Tags             model.JSONStringSlice `json:"tags"`
	IsThinking       bool                  `json:"is_thinking"`
	SupportsThinking bool                  `json:"supports_thinking"`
	ThinkingOnly     bool                  `json:"thinking_only"`
	Icon             string                `json:"icon"`
	SortOrder        int                   `json:"sort_order"`
	IsActive         *bool                 `json:"is_active"`
	BaseModelID      *uint64               `json:"base_model_id"`
}

// CreateRouteInner is the route-half of CreateServiceWithRouteRequest.
// Same semantics as CreateRouteRequest but omitted binding:"required" on nested struct
// fields (Gin validates nested struct tags when the outer struct itself is non-empty).
type CreateRouteInner struct {
	ProviderID      uint64 `json:"provider_id"       binding:"required"`
	ProviderModelID string `json:"provider_model_id" binding:"required,min=1"`
	Priority        int    `json:"priority"`
	IsActive        *bool  `json:"is_active"`
}

// CreateServiceWithRouteResult is the wire-format result of the atomic create.
type CreateServiceWithRouteResult struct {
	Service *model.AIService `json:"service"`
	Route   *RouteDTO        `json:"route"`
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

	// CreateServiceWithRoute atomically creates an ai_service row together with a
	// paired ai_service_route row in a single DB transaction. Both INSERTs succeed
	// or both roll back, guaranteeing admin UI cannot produce an orphan service.
	// Audit log entries are written for BOTH service.create and route.create.
	CreateServiceWithRoute(ctx context.Context, req CreateServiceWithRouteRequest, actorID uint64, actorName string) (*CreateServiceWithRouteResult, error)

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

	// ListProviders returns all llm_provider rows with api_key masked.
	ListProviders(ctx context.Context) ([]ProviderDTO, error)

	// GetProvider returns a single llm_provider by ID with api_key masked.
	GetProvider(ctx context.Context, id uint64) (*ProviderDTO, error)

	// CreateProvider creates a new llm_provider and returns the masked DTO.
	CreateProvider(ctx context.Context, req CreateProviderRequest, actorID uint64, actorName string) (*ProviderDTO, error)

	// UpdateProvider applies a partial update to a provider. An empty or nil APIKey
	// field preserves the existing key; a non-empty value replaces it.
	UpdateProvider(ctx context.Context, id uint64, req UpdateProviderRequest, actorID uint64, actorName string) (*ProviderDTO, error)

	// DeleteProvider hard-deletes a provider. Returns an error if any ai_service_route
	// rows reference the provider (delete guard).
	DeleteProvider(ctx context.Context, id uint64, actorID uint64, actorName string) error

	// TestProviderConnection probes an OpenAI-compatible endpoint with a 1-token request.
	// Returns success=false with a descriptive error for non-OpenAI-compatible providers.
	TestProviderConnection(ctx context.Context, id uint64) (TestConnectionResult, error)
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

	// One batch query for all route counts — never N+1.
	// `b.db == nil` is tolerated so that existing unit tests using aiservice_admin.New(reg, nil)
	// keep working; in that case every item gets route_count=0.
	counts := map[uint64]int{}
	if b.db != nil && len(list) > 0 {
		ids := make([]uint64, 0, len(list))
		for _, svc := range list {
			ids = append(ids, svc.ID)
		}
		type routeCountRow struct {
			ModelID uint64 `gorm:"column:model_id"`
			Cnt     int    `gorm:"column:cnt"`
		}
		var rows []routeCountRow
		if err := b.db.WithContext(ctx).
			Table("ai_service_route").
			Select("model_id, COUNT(*) AS cnt").
			Where("is_active = ? AND model_id IN ?", true, ids).
			Group("model_id").
			Scan(&rows).Error; err != nil {
			return nil, fmt.Errorf("aiservice_admin.ListServices: route counts: %w", err)
		}
		for _, r := range rows {
			counts[r.ModelID] = r.Cnt
		}
	}

	items := make([]*ServiceListItem, 0, len(list))
	for _, svc := range list {
		items = append(items, &ServiceListItem{
			AIService:  svc,
			RouteCount: counts[svc.ID],
		})
	}
	return &ListServicesResult{List: items, Total: total}, nil
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
		if isUniqueKeyViolation(err) {
			return nil, errno.ErrAIServiceModelKeyExists.SetMessage("model_key %s 已存在", svc.ModelKey)
		}
		return nil, fmt.Errorf("aiservice_admin.CreateService: %w", err)
	}
	return svc, nil
}

// CreateServiceWithRoute atomically creates an ai_service row together with an
// ai_service_route pointing at it. Runs in a single GORM transaction so both
// INSERTs succeed or both roll back — plugs the systemic hole that caused the
// 2026-04-19 SalesRAG outage (service row existed, no route → resolver hit
// "no active route" and user requests 500'd).
//
// Audit logs: writes BOTH service.create and route.create entries (best-effort,
// outside the transaction — matches existing CreateRoute behaviour).
func (b *aiServiceAdminBiz) CreateServiceWithRoute(ctx context.Context, req CreateServiceWithRouteRequest, actorID uint64, actorName string) (*CreateServiceWithRouteResult, error) {
	// Up-front validation: service_type ∈ {llm, ocr, asr}.
	if err := validateServiceType(req.Service.ServiceType); err != nil {
		return nil, err
	}
	// provider_model_id must be non-empty (gin binding already enforces; guard also here for direct biz callers).
	if req.Route.ProviderModelID == "" {
		return nil, errno.ErrInvalidParameter.SetMessage("provider_model_id 不能为空")
	}

	svcActive := true
	if req.Service.IsActive != nil {
		svcActive = *req.Service.IsActive
	}
	routeActive := true
	if req.Route.IsActive != nil {
		routeActive = *req.Route.IsActive
	}

	svc := &model.AIService{
		ModelKey:         req.Service.ModelKey,
		DisplayName:      req.Service.DisplayName,
		ServiceType:      req.Service.ServiceType,
		CapabilityJSON:   req.Service.CapabilityJSON,
		LatencyTier:      req.Service.LatencyTier,
		QualityTier:      req.Service.QualityTier,
		Tags:             req.Service.Tags,
		IsThinking:       req.Service.IsThinking,
		SupportsThinking: req.Service.SupportsThinking,
		ThinkingOnly:     req.Service.ThinkingOnly,
		Icon:             req.Service.Icon,
		SortOrder:        req.Service.SortOrder,
		IsActive:         svcActive,
		BaseModelID:      req.Service.BaseModelID,
	}

	// Provider FK existence check outside the transaction; cheap SELECT and
	// lets us surface a 400 with a human-friendly message instead of letting
	// the INSERT blow up mid-transaction with a FK error that we'd have to parse.
	var provider model.LLMProvider
	if err := b.db.WithContext(ctx).First(&provider, req.Route.ProviderID).Error; err != nil {
		return nil, errno.ErrInvalidParameter.SetMessage("provider_id %d 不存在", req.Route.ProviderID)
	}

	route := &model.AIServiceRoute{
		ProviderID:      req.Route.ProviderID,
		ProviderModelID: req.Route.ProviderModelID,
		Priority:        req.Route.Priority,
		IsActive:        routeActive,
	}

	// Transaction: service INSERT → route INSERT. Both or neither.
	txErr := b.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Capture intended values before Create; GORM may overwrite bool fields
		// marked `default:true` when the Go value is false — same gotcha as
		// SaveService / CreateProvider / CreateRoute.
		wantSvcActive := svc.IsActive
		if err := tx.Create(svc).Error; err != nil {
			return fmt.Errorf("create service: %w", err)
		}
		if !wantSvcActive && svc.IsActive {
			if err := tx.Model(svc).UpdateColumn("is_active", false).Error; err != nil {
				return fmt.Errorf("fixup service is_active: %w", err)
			}
			svc.IsActive = false
		}

		route.ModelID = svc.ID
		wantRouteActive := route.IsActive
		if err := tx.Create(route).Error; err != nil {
			return fmt.Errorf("create route: %w", err)
		}
		if !wantRouteActive && route.IsActive {
			if err := tx.Model(route).UpdateColumn("is_active", false).Error; err != nil {
				return fmt.Errorf("fixup route is_active: %w", err)
			}
			route.IsActive = false
		}
		return nil
	})
	if txErr != nil {
		if isUniqueKeyViolation(txErr) {
			return nil, errno.ErrAIServiceModelKeyExists.SetMessage("model_key %s 已存在", req.Service.ModelKey)
		}
		return nil, fmt.Errorf("aiservice_admin.CreateServiceWithRoute: %w", txErr)
	}

	// Best-effort audit logs (same policy as CreateRoute / CreateProvider).
	// Writes happen outside the transaction: audit failure must not roll back
	// the actual change, but a commit guarantees we're recording a real row.
	if err := b.db.WithContext(ctx).Create(&model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     model.AuditActionServiceCreate,
		TargetType: model.AuditTargetService,
		TargetID:   svc.ID,
		DiffJSON: model.JSONMap{
			"model_key":    svc.ModelKey,
			"display_name": svc.DisplayName,
			"service_type": svc.ServiceType,
			"is_active":    svc.IsActive,
		},
	}).Error; err != nil {
		log.C(ctx).Warnw("CreateServiceWithRoute: service audit write failed", "service_id", svc.ID, "err", err)
	}

	route.Provider = &provider
	b.writeRouteAudit(ctx, model.AuditActionRouteCreate, route.ID, nil, routeDTOFromModel(route), actorID, actorName)

	return &CreateServiceWithRouteResult{
		Service: svc,
		Route:   routeDTOFromModel(route),
	}, nil
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
	// GORM v2 skips bool zero value (false) when the field has a `default:true`
	// tag (see model.AIServiceRoute.IsActive), silently falling back to the DB
	// default of true. Forcing a follow-up UpdateColumn restores the requested
	// value. Cheap (single UPDATE) and only triggers when the request explicitly
	// asked for is_active=false.
	if !isActive && route.IsActive {
		if err := b.db.WithContext(ctx).Model(route).UpdateColumn("is_active", false).Error; err != nil {
			return nil, nil, fmt.Errorf("aiservice_admin.CreateRoute: fixup is_active: %w", err)
		}
		route.IsActive = false
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

// ----------------------------------------------------------------------------
// Provider CRUD — implementations
// ----------------------------------------------------------------------------

// providerToDTO converts a model.LLMProvider to a ProviderDTO with masked API key.
func providerToDTO(p *model.LLMProvider) ProviderDTO {
	return ProviderDTO{
		ID:          p.ID,
		Name:        p.Name,
		DisplayName: p.DisplayName,
		BaseURL:     p.BaseURL,
		APIKey:      p.MaskedAPIKey(),
		IsActive:    p.IsActive,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
}

// ListProviders returns all llm_provider rows with api_key masked.
func (b *aiServiceAdminBiz) ListProviders(ctx context.Context) ([]ProviderDTO, error) {
	var providers []model.LLMProvider
	if err := b.db.WithContext(ctx).Order("id ASC").Find(&providers).Error; err != nil {
		return nil, fmt.Errorf("aiservice_admin.ListProviders: %w", err)
	}
	result := make([]ProviderDTO, 0, len(providers))
	for i := range providers {
		result = append(result, providerToDTO(&providers[i]))
	}
	return result, nil
}

// GetProvider returns a single llm_provider by ID with api_key masked.
func (b *aiServiceAdminBiz) GetProvider(ctx context.Context, id uint64) (*ProviderDTO, error) {
	var p model.LLMProvider
	if err := b.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if isNotFound(err) {
			return nil, errno.ErrAIProviderNotFound
		}
		return nil, fmt.Errorf("aiservice_admin.GetProvider: %w", err)
	}
	dto := providerToDTO(&p)
	return &dto, nil
}

// CreateProvider creates a new llm_provider and returns the masked DTO.
func (b *aiServiceAdminBiz) CreateProvider(ctx context.Context, req CreateProviderRequest, actorID uint64, actorName string) (*ProviderDTO, error) {
	isActive := true
	if req.IsActive != nil {
		isActive = *req.IsActive
	}

	p := model.LLMProvider{
		Name:        req.Name,
		DisplayName: req.DisplayName,
		BaseURL:     req.BaseURL,
		APIKey:      req.APIKey,
		IsActive:    isActive,
	}

	if err := b.db.WithContext(ctx).Create(&p).Error; err != nil {
		return nil, fmt.Errorf("aiservice_admin.CreateProvider: %w", err)
	}
	// Same GORM default:true gotcha as AIServiceRoute.IsActive: if the caller
	// explicitly asked for is_active=false, GORM's default handling persisted
	// true. Apply a follow-up UpdateColumn to honour the request.
	if !isActive && p.IsActive {
		if err := b.db.WithContext(ctx).Model(&p).UpdateColumn("is_active", false).Error; err != nil {
			return nil, fmt.Errorf("aiservice_admin.CreateProvider: fixup is_active: %w", err)
		}
		p.IsActive = false
	}

	// Write audit log entry.
	_ = b.db.WithContext(ctx).Create(&model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     model.AuditActionProviderCreate,
		TargetType: model.AuditTargetProvider,
		TargetID:   p.ID,
		DiffJSON: model.JSONMap{
			"name":         req.Name,
			"display_name": req.DisplayName,
			"base_url":     req.BaseURL,
			"api_key":      p.MaskedAPIKey(),
		},
	}).Error

	dto := providerToDTO(&p)
	return &dto, nil
}

// UpdateProvider applies a partial update. Empty or nil APIKey preserves existing key.
func (b *aiServiceAdminBiz) UpdateProvider(ctx context.Context, id uint64, req UpdateProviderRequest, actorID uint64, actorName string) (*ProviderDTO, error) {
	var p model.LLMProvider
	if err := b.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if isNotFound(err) {
			return nil, errno.ErrAIProviderNotFound
		}
		return nil, fmt.Errorf("aiservice_admin.UpdateProvider: fetch: %w", err)
	}

	// Early return if no fields are actually changing — avoids a spurious Save()
	// (which bumps updated_at) and an empty audit log entry.
	if req.DisplayName == nil && req.BaseURL == nil &&
		(req.APIKey == nil || *req.APIKey == "") && req.IsActive == nil {
		currentDTO := providerToDTO(&p)
		return &currentDTO, nil
	}

	beforeMasked := p.MaskedAPIKey()
	diff := model.JSONMap{}

	if req.DisplayName != nil {
		diff["display_name"] = map[string]interface{}{"before": p.DisplayName, "after": *req.DisplayName}
		p.DisplayName = *req.DisplayName
	}
	if req.BaseURL != nil {
		diff["base_url"] = map[string]interface{}{"before": p.BaseURL, "after": *req.BaseURL}
		p.BaseURL = *req.BaseURL
	}
	if req.APIKey != nil && *req.APIKey != "" {
		// Show masked before/after in audit; never expose raw key.
		newMasked := maskedKey(*req.APIKey)
		diff["api_key"] = map[string]interface{}{"before": beforeMasked, "after": newMasked}
		p.APIKey = *req.APIKey
	}
	if req.IsActive != nil {
		diff["is_active"] = map[string]interface{}{"before": p.IsActive, "after": *req.IsActive}
		p.IsActive = *req.IsActive
	}

	if err := b.db.WithContext(ctx).Save(&p).Error; err != nil {
		return nil, fmt.Errorf("aiservice_admin.UpdateProvider: save: %w", err)
	}

	// Write audit log entry.
	_ = b.db.WithContext(ctx).Create(&model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     model.AuditActionProviderUpdate,
		TargetType: model.AuditTargetProvider,
		TargetID:   p.ID,
		DiffJSON:   diff,
	}).Error

	dto := providerToDTO(&p)
	return &dto, nil
}

// DeleteProvider hard-deletes a provider. Rejects if any ai_service_route references it.
func (b *aiServiceAdminBiz) DeleteProvider(ctx context.Context, id uint64, actorID uint64, actorName string) error {
	// Fetch the provider first (needed for audit log; also validates existence).
	var p model.LLMProvider
	if err := b.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if isNotFound(err) {
			return errno.ErrAIProviderNotFound
		}
		return fmt.Errorf("aiservice_admin.DeleteProvider: fetch: %w", err)
	}

	// Guard: reject if any route references this provider.
	var routeCount int64
	if err := b.db.WithContext(ctx).
		Model(&model.AIServiceRoute{}).
		Where("provider_id = ?", id).
		Count(&routeCount).Error; err != nil {
		return fmt.Errorf("aiservice_admin.DeleteProvider: route count: %w", err)
	}
	if routeCount > 0 {
		return errno.ErrAIProviderInUse.SetMessage("该供应商被 %d 条路由引用，无法删除，请先删除相关路由", routeCount)
	}

	if err := b.db.WithContext(ctx).Delete(&p).Error; err != nil {
		return fmt.Errorf("aiservice_admin.DeleteProvider: delete: %w", err)
	}

	// Write audit log entry. api_key is intentionally omitted (never log raw/masked keys on delete).
	_ = b.db.WithContext(ctx).Create(&model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     model.AuditActionProviderDelete,
		TargetType: model.AuditTargetProvider,
		TargetID:   id,
		DiffJSON: model.JSONMap{
			"before": map[string]any{
				"name":         p.Name,
				"display_name": p.DisplayName,
				"base_url":     p.BaseURL,
				"is_active":    p.IsActive,
				// api_key intentionally omitted (masked elsewhere)
			},
		},
	}).Error

	return nil
}

// TestProviderConnection probes an OpenAI-compatible provider with a 1-token request.
// Non-OpenAI-compatible providers (baidu, bailian) return success=false with a helpful message.
func (b *aiServiceAdminBiz) TestProviderConnection(ctx context.Context, id uint64) (TestConnectionResult, error) {
	// Fetch the provider (with raw API key for the probe).
	var p model.LLMProvider
	if err := b.db.WithContext(ctx).First(&p, id).Error; err != nil {
		if isNotFound(err) {
			return TestConnectionResult{}, errno.ErrAIProviderNotFound
		}
		return TestConnectionResult{}, fmt.Errorf("aiservice_admin.TestProviderConnection: %w", err)
	}

	// Check if the provider is OpenAI-compatible.
	if !isOpenAICompatible(&p) {
		return TestConnectionResult{
			Success: false,
			Error:   "provider type not testable (only OpenAI-compatible providers supported)",
		}, nil
	}

	// Find the first active route for this provider to get a model ID.
	type routeRow struct {
		ProviderModelID string `gorm:"column:provider_model_id"`
	}
	var row routeRow
	if err := b.db.WithContext(ctx).
		Table("ai_service_route").
		Select("provider_model_id").
		Where("provider_id = ? AND is_active = true", id).
		Order("priority DESC").
		Limit(1).
		Scan(&row).Error; err != nil {
		return TestConnectionResult{}, fmt.Errorf("aiservice_admin.TestProviderConnection: route query: %w", err)
	}
	if row.ProviderModelID == "" {
		return TestConnectionResult{
			Success: false,
			Error:   "no active route references this provider — cannot probe",
		}, nil
	}

	// Build 5-second-timeout probe context.
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Build the request body.
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":       row.ProviderModelID,
		"messages":    []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens":  1,
		"temperature": 0,
	})

	// Normalise base URL: strip trailing slash.
	baseURL := strings.TrimRight(p.BaseURL, "/")
	probeURL := baseURL + "/chat/completions"

	hc := httpclient.NewClient(&httpclient.Config{
		Timeout:        5 * time.Second,
		ConnectTimeout: 5 * time.Second,
		MaxRetries:     0, // probe should not retry — measure raw latency
	})

	start := time.Now()
	resp, err := hc.Do(&httpclient.Request{
		Method:  http.MethodPost,
		URL:     probeURL,
		Headers: map[string]string{"Authorization": "Bearer " + p.APIKey},
		Body:    bytes.NewReader(reqBody),
		Context: probeCtx,
		RetryPolicy: &httpclient.RetryPolicy{
			MaxRetries: 0,
		},
	})
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		log.C(ctx).Warnw("Provider test-connection failed", "provider_id", id, "error", err)
		return TestConnectionResult{
			Success:   false,
			LatencyMs: latencyMs,
			Error:     truncate(err.Error(), 200),
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return TestConnectionResult{
			Success:    true,
			LatencyMs:  latencyMs,
			HTTPStatus: resp.StatusCode,
		}, nil
	}

	// Non-2xx: read body for error detail.
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return TestConnectionResult{
		Success:    false,
		LatencyMs:  latencyMs,
		HTTPStatus: resp.StatusCode,
		Error:      truncate(string(bodyBytes), 200),
	}, nil
}

// isOpenAICompatible returns true for providers using OpenAI-compatible chat completions API.
// Providers whose names start with "baidu" or "bailian" (e.g. "baidu-ocr", "bailian-file")
// are known non-compatible providers and are excluded from OpenAI-style probing.
func isOpenAICompatible(p *model.LLMProvider) bool {
	lower := strings.ToLower(p.Name)
	if strings.HasPrefix(lower, "baidu") || strings.HasPrefix(lower, "bailian") {
		return false
	}
	return true
}

// maskedKey returns a masked representation of the given raw API key.
func maskedKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}

// truncate limits a string to at most n bytes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// isNotFound reports whether a GORM error is a "record not found" error.
func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// isUniqueKeyViolation detects MySQL/SQLite unique-index violations without
// importing driver-specific error types. Both drivers surface a string
// containing "UNIQUE constraint failed" (SQLite) or "Duplicate entry" /
// "1062" (MySQL). Keeping the detection string-based also keeps the admin biz
// free of a direct go-sql-driver dependency.
func isUniqueKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, needle := range []string{
		"UNIQUE constraint failed",
		"Duplicate entry",
		"duplicate key value",
		"1062",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
