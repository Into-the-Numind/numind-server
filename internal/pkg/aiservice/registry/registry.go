// Package registry provides DB access, in-memory caching, and task-profile
// resolution for the AI Service Manager.
//
// Audit log policy: InsertAuditLog is best-effort. Write failures are logged
// at WARN level but do NOT block the primary business operation. This ensures
// that transient DB hiccups on the audit table never prevent service
// configuration changes.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// Public types returned by the Registry
// ----------------------------------------------------------------------------

// ProviderInfo contains the provider identity and credentials needed to call
// the upstream AI provider API.
type ProviderInfo struct {
	ID      uint64
	Name    string
	BaseURL string
	APIKey  string
}

// PricingInfo summarises the billing unit for a specific route.
// Pricing amounts are no longer stored on ai_service_route; they are resolved
// at call time from pricing_rule by the billing middleware.
// Unit is carried here as informational metadata; the billing middleware derives
// the actual unit directly from the pricing_rule it fetches at call time
// (per_1m_tokens → PricingInputSnapshot/PricingOutputSnapshot,
// per_call → PricingCallSnapshot).
type PricingInfo struct {
	Unit string // "per_1m_tokens" | "per_call"
}

// ResolvedRoute is a call-ready description of a single AI service route.
// It merges task_profile + ai_service + ai_service_route + llm_provider into
// a single flat struct so callers can invoke the service without extra DB queries.
type ResolvedRoute struct {
	TaskID          string
	ServiceID       uint64
	ServiceKey      string // ai_service.model_key
	DisplayName     string // ai_service.display_name — human-readable label for tracing/logging
	ServiceType     string // llm | ocr | asr
	LatencyTier     string
	QualityTier     string
	Provider        ProviderInfo
	ProviderModelID string // the model identifier sent to the provider API
	Capability      profile.ServiceCapability
	Pricing         PricingInfo
}

// ----------------------------------------------------------------------------
// Registry interface
// ----------------------------------------------------------------------------

// Registry is the top-level facade for AI service management and task routing.
// All business-layer code should reference this interface, not the concrete type.
type Registry interface {
	// GetService returns a single AIService by ID.
	GetService(ctx context.Context, id uint64) (*model.AIService, error)

	// ListServices returns all services that match the filter.
	ListServices(ctx context.Context, filter ServiceFilter) ([]*model.AIService, error)

	// ListServicesPaginated returns a page of services matching the filter along
	// with the total match count. offset is 0-based; limit must be > 0.
	ListServicesPaginated(ctx context.Context, filter ServiceFilter, offset, limit int) ([]*model.AIService, int64, error)

	// SaveService creates or updates a service and invalidates related caches.
	// actorID and actorName identify the admin user performing the action and are
	// recorded in the audit log.
	SaveService(ctx context.Context, svc *model.AIService, actorID uint64, actorName string) error

	// DeprecateService marks a service as deprecated (soft-delete) and writes an audit log.
	// actorID and actorName identify the admin user performing the action.
	DeprecateService(ctx context.Context, id uint64, actorID uint64, actorName string, reason string) error

	// RestoreService clears the deprecated_at flag and writes an audit log.
	// actorID and actorName identify the admin user performing the action.
	RestoreService(ctx context.Context, id uint64, actorID uint64, actorName string, reason string) error

	// GetTaskProfile returns a single TaskProfile by task_id.
	GetTaskProfile(ctx context.Context, taskID string) (*model.TaskProfile, error)

	// ListTaskProfiles returns all task profiles.
	ListTaskProfiles(ctx context.Context) ([]*model.TaskProfile, error)

	// SaveTaskProfile creates or updates a task profile and atomically replaces its
	// service bindings. actorID and actorName are recorded in the audit log.
	SaveTaskProfile(ctx context.Context, tp *model.TaskProfile, bindings []TaskBinding, actorID uint64, actorName string) error

	// ResolveTask looks up the task profile for taskID and resolves the primary +
	// fallback service routes. The primary route is returned as the first value;
	// fallback routes (ordered by priority DESC, higher number = tried first) are the second value.
	//
	// Errors:
	//   - errno.ErrAITaskNotFound      — no TaskProfile row for taskID
	//   - errno.ErrAIServiceUnbound    — default_service_id is NULL (not yet bound)
	//   - errno.ErrAIServiceNotFound   — the bound service is deprecated / inactive
	//
	// Note: Capability Matching is intentionally NOT performed at resolve time
	// (per spec §6.5: trust persisted state, validate only at admin save time).
	ResolveTask(ctx context.Context, taskID string) (*ResolvedRoute, []ResolvedRoute, error)

	// ResolveByModelKey looks up a service by model_key and builds a ResolvedRoute
	// with the given taskID. Used to honour ChatRequest.ModelOverride — when a user
	// selects a specific model, the gateway calls this instead of using the task
	// profile's default_service_id.
	//
	// Errors:
	//   - errno.ErrAIServiceNotFound — no active service with the given model_key
	ResolveByModelKey(ctx context.Context, taskID string, modelKey string) (*ResolvedRoute, error)
}

// ----------------------------------------------------------------------------
// registryImpl — concrete implementation
// ----------------------------------------------------------------------------

// registryImpl wires together a store and a cache.
type registryImpl struct {
	store IStore
	cache *cache
}

// New creates a Registry backed by the given *gorm.DB with a 30-second cache TTL.
// Use NewWithStore when you need to inject a custom IStore (e.g. in tests).
func New(db *gorm.DB) Registry {
	return NewWithStore(NewStore(db), 0)
}

// NewWithStore creates a Registry with an injected IStore and a custom TTL.
// ttl <= 0 uses the default 30-second TTL.
func NewWithStore(store IStore, ttl time.Duration) Registry {
	return &registryImpl{
		store: store,
		cache: newCache(ttl),
	}
}

// ----------------------------------------------------------------------------
// Service operations
// ----------------------------------------------------------------------------

// GetService returns a single AIService by ID, using the cache when possible.
func (r *registryImpl) GetService(ctx context.Context, id uint64) (*model.AIService, error) {
	if svc, ok := r.cache.GetService(id); ok {
		return svc, nil
	}
	svc, err := r.store.GetService(ctx, id)
	if err != nil {
		return nil, err
	}
	r.cache.SetService(svc)
	return svc, nil
}

// ListServices returns all services matching the filter. Results are NOT cached
// (list queries are admin-facing and relatively infrequent).
func (r *registryImpl) ListServices(ctx context.Context, filter ServiceFilter) ([]*model.AIService, error) {
	return r.store.ListServices(ctx, filter)
}

// ListServicesPaginated delegates to the store so admin list queries pull
// only one page at a time instead of the full service set.
func (r *registryImpl) ListServicesPaginated(ctx context.Context, filter ServiceFilter, offset, limit int) ([]*model.AIService, int64, error) {
	return r.store.ListServicesPaginated(ctx, filter, offset, limit)
}

// SaveService creates or updates a service, then invalidates the cache for that service
// and all task entries that reference it. actorID and actorName are recorded in the audit log.
func (r *registryImpl) SaveService(ctx context.Context, svc *model.AIService, actorID uint64, actorName string) error {
	isCreate := svc.ID == 0

	// For updates, load the old state to build a diff.
	var diffJSON model.JSONMap
	if !isCreate {
		if old, err := r.store.GetService(ctx, svc.ID); err == nil {
			diffJSON = buildServiceDiff(old, svc)
		}
	}

	if err := r.store.SaveService(ctx, svc); err != nil {
		return fmt.Errorf("registry.SaveService: %w", err)
	}
	// Invalidate cache for this service ID.
	r.cache.InvalidateService(svc.ID)

	// Write audit log (best-effort — failure is logged but does not block).
	action := model.AuditActionServiceCreate
	if !isCreate {
		action = model.AuditActionServiceUpdate
	}
	if err := r.store.InsertAuditLog(ctx, &model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     action,
		TargetType: model.AuditTargetService,
		TargetID:   svc.ID,
		DiffJSON:   diffJSON,
	}); err != nil {
		log.Warnw("failed to write audit log",
			"action", action,
			"target_id", svc.ID,
			"error", err,
		)
	}
	return nil
}

// DeprecateService sets deprecated_at to now and writes an audit log entry.
// actorID and actorName identify the admin user performing the action.
func (r *registryImpl) DeprecateService(ctx context.Context, id uint64, actorID uint64, actorName string, reason string) error {
	// Load old state for diff.
	var diffJSON model.JSONMap
	if old, err := r.store.GetService(ctx, id); err == nil {
		diffJSON = model.JSONMap{"before": map[string]interface{}{"deprecated_at": nil}, "after": map[string]interface{}{"deprecated_at": "now"}}
		_ = old // used to confirm existence; diff content is fixed for deprecation
	}

	now := time.Now()
	if err := r.store.SetServiceDeprecated(ctx, id, &now); err != nil {
		return fmt.Errorf("registry.DeprecateService: %w", err)
	}
	r.cache.InvalidateService(id)

	if err := r.store.InsertAuditLog(ctx, &model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     model.AuditActionServiceDeprecate,
		TargetType: model.AuditTargetService,
		TargetID:   id,
		Reason:     reason,
		DiffJSON:   diffJSON,
	}); err != nil {
		log.Warnw("failed to write audit log",
			"action", model.AuditActionServiceDeprecate,
			"target_id", id,
			"error", err,
		)
	}
	return nil
}

// RestoreService clears deprecated_at and writes an audit log entry.
// actorID and actorName identify the admin user performing the action.
func (r *registryImpl) RestoreService(ctx context.Context, id uint64, actorID uint64, actorName string, reason string) error {
	// Load old state for diff.
	var diffJSON model.JSONMap
	if old, err := r.store.GetService(ctx, id); err == nil {
		diffJSON = model.JSONMap{"before": map[string]interface{}{"deprecated_at": old.DeprecatedAt}, "after": map[string]interface{}{"deprecated_at": nil}}
	}

	if err := r.store.SetServiceDeprecated(ctx, id, nil); err != nil {
		return fmt.Errorf("registry.RestoreService: %w", err)
	}
	r.cache.InvalidateService(id)

	if err := r.store.InsertAuditLog(ctx, &model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     model.AuditActionServiceRestore,
		TargetType: model.AuditTargetService,
		TargetID:   id,
		Reason:     reason,
		DiffJSON:   diffJSON,
	}); err != nil {
		log.Warnw("failed to write audit log",
			"action", model.AuditActionServiceRestore,
			"target_id", id,
			"error", err,
		)
	}
	return nil
}

// ----------------------------------------------------------------------------
// Task Profile operations
// ----------------------------------------------------------------------------

// GetTaskProfile returns a single TaskProfile by task_id.
func (r *registryImpl) GetTaskProfile(ctx context.Context, taskID string) (*model.TaskProfile, error) {
	return r.store.GetTaskProfile(ctx, taskID)
}

// ListTaskProfiles returns all task profiles.
func (r *registryImpl) ListTaskProfiles(ctx context.Context) ([]*model.TaskProfile, error) {
	return r.store.ListTaskProfiles(ctx)
}

// SaveTaskProfile atomically upserts a task profile and replaces its service
// bindings within a single DB transaction, then invalidates the task cache entry.
// actorID and actorName are recorded in the audit log.
func (r *registryImpl) SaveTaskProfile(ctx context.Context, tp *model.TaskProfile, bindings []TaskBinding, actorID uint64, actorName string) error {
	if err := r.store.SaveTaskProfileWithBindings(ctx, tp, bindings); err != nil {
		return fmt.Errorf("registry.SaveTaskProfile: %w", err)
	}
	r.cache.InvalidateTask(tp.TaskID)

	if err := r.store.InsertAuditLog(ctx, &model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  actorName,
		Action:     model.AuditActionTaskBind,
		TargetType: model.AuditTargetTaskProfile,
		TargetID:   tp.ID,
	}); err != nil {
		log.Warnw("failed to write audit log",
			"action", model.AuditActionTaskBind,
			"target_id", tp.ID,
			"error", err,
		)
	}
	return nil
}

// ----------------------------------------------------------------------------
// Resolver
// ----------------------------------------------------------------------------

// ResolveTask resolves the primary and fallback routes for a given task ID.
//
// Resolution algorithm:
//  1. Fetch TaskProfile by taskID (errno.ErrAITaskNotFound if missing).
//  2. If default_service_id is NULL → return errno.ErrAIServiceUnbound.
//  3. Resolve primary service: call GetResolvedRoute(default_service_id).
//     If the service is missing, deprecated, or inactive, return errno.ErrAIServiceNotFound.
//  4. Fetch TaskProfileService rows with role="fallback", ordered by priority DESC (higher number = tried first).
//     Attempt to resolve each; silently skip services that are deprecated/inactive.
//  5. Return (primary, fallbacks, nil).
//
// Results are cached for the configured TTL. Cache miss triggers a full DB load.
//
// Note: Capability Matching is intentionally NOT performed at resolve time
// (per spec §6.5: trust persisted state, validate only at admin save time).
func (r *registryImpl) ResolveTask(ctx context.Context, taskID string) (*ResolvedRoute, []ResolvedRoute, error) {
	// Cache lookup.
	if primary, fallbacks, ok := r.cache.GetTask(taskID); ok {
		return primary, fallbacks, nil
	}

	// Load task profile.
	tp, err := r.store.GetTaskProfile(ctx, taskID)
	if err != nil {
		return nil, nil, fmt.Errorf("registry.ResolveTask: %w", err)
	}

	// Require a non-NULL default service.
	if tp.DefaultServiceID == nil {
		return nil, nil, errno.ErrAIServiceUnbound
	}

	// Resolve primary route.
	primaryRow, err := r.store.GetResolvedRoute(ctx, *tp.DefaultServiceID)
	if err != nil {
		return nil, nil, fmt.Errorf("registry.ResolveTask (primary): %w", err)
	}
	primaryRoute := buildResolvedRoute(taskID, primaryRow)

	// Fetch fallback bindings via IStore (ordered by priority DESC, higher number = tried first).
	fallbackBindings, err := r.store.ListTaskBindings(ctx, tp.ID, model.TaskProfileRoleFallback)
	if err != nil {
		return nil, nil, fmt.Errorf("registry.ResolveTask (fallbacks): %w", err)
	}

	// Resolve each fallback (skip deprecated/inactive silently).
	var fallbacks []ResolvedRoute
	var allServiceIDs []uint64
	allServiceIDs = append(allServiceIDs, *tp.DefaultServiceID)

	for _, b := range fallbackBindings {
		row, err := r.store.GetResolvedRoute(ctx, b.ServiceID)
		if err != nil {
			// Skip deprecated or missing services — fallback is best-effort.
			continue
		}
		fallbacks = append(fallbacks, buildResolvedRoute(taskID, row))
		allServiceIDs = append(allServiceIDs, b.ServiceID)
	}

	// Populate cache.
	r.cache.SetTask(taskID, &primaryRoute, fallbacks, allServiceIDs)

	return &primaryRoute, fallbacks, nil
}

// ResolveByModelKey resolves a route by model_key and associates it with the provided taskID.
// This is used by the gateway when a ChatRequest.ModelOverride is set — the user explicitly
// selected a model, so we bypass the task profile's default_service_id.
//
// The result is NOT cached (model-key overrides are per-call user selections that should
// always reflect the latest DB state).
func (r *registryImpl) ResolveByModelKey(ctx context.Context, taskID string, modelKey string) (*ResolvedRoute, error) {
	row, err := r.store.GetResolvedRouteByModelKey(ctx, modelKey)
	if err != nil {
		return nil, fmt.Errorf("registry.ResolveByModelKey(%s): %w", modelKey, err)
	}
	route := buildResolvedRoute(taskID, row)
	return &route, nil
}

// ----------------------------------------------------------------------------
// Internal helpers
// ----------------------------------------------------------------------------

// buildResolvedRoute converts a resolvedRouteRow into the exported ResolvedRoute type,
// unmarshaling the capability JSON into a profile.ServiceCapability struct.
func buildResolvedRoute(taskID string, row *resolvedRouteRow) ResolvedRoute {
	cap := unmarshalCapability(row.CapabilityJSON, row.ServiceType)
	return ResolvedRoute{
		TaskID:      taskID,
		ServiceID:   row.ServiceID,
		ServiceKey:  row.ModelKey,
		DisplayName: row.DisplayName,
		ServiceType: row.ServiceType,
		LatencyTier: row.LatencyTier,
		QualityTier: row.QualityTier,
		Provider: ProviderInfo{
			ID:      row.ProviderID,
			Name:    row.ProviderName,
			BaseURL: row.ProviderBaseURL,
			APIKey:  row.ProviderAPIKey,
		},
		ProviderModelID: row.ProviderModelID,
		Capability:      cap,
		// Pricing.Unit is resolved from pricing_rule at call time by the billing
		// middleware (buildBaseRecord). The column was removed from ai_service_route
		// in T-arch; the billing middleware performs the lookup and writes Unit into
		// the UsageRecord.
		Pricing: PricingInfo{},
	}
}

// unmarshalCapability converts a model.JSONMap (read from capability_json) into a
// profile.ServiceCapability struct. Errors are logged at WARN level — a partially
// populated struct is better than an opaque failure at resolve-time. The JSONMap
// is re-serialised to JSON and then decoded into the struct, reusing existing
// JSON marshaling tags on ServiceCapability.
func unmarshalCapability(m model.JSONMap, serviceType string) profile.ServiceCapability {
	var cap profile.ServiceCapability
	if len(m) == 0 {
		cap.ServiceType = serviceType
		return cap
	}
	// Re-marshal to JSON then unmarshal into the typed struct.
	b, err := json.Marshal(m)
	if err != nil {
		log.Warnw("failed to unmarshal capability_json",
			"service_type", serviceType,
			"error", err,
		)
		cap.ServiceType = serviceType
		return cap
	}
	if err := json.Unmarshal(b, &cap); err != nil {
		log.Warnw("failed to unmarshal capability_json",
			"service_type", serviceType,
			"error", err,
		)
		cap.ServiceType = serviceType
		return cap
	}
	// Ensure ServiceType is set (may be absent in older capability docs).
	if cap.ServiceType == "" {
		cap.ServiceType = serviceType
	}
	return cap
}

// buildServiceDiff constructs a before/after diff map for audit logging of service updates.
// Only key identifying fields are included to keep the diff compact.
func buildServiceDiff(old, newSvc *model.AIService) model.JSONMap {
	before := map[string]interface{}{
		"model_key":    old.ModelKey,
		"service_type": old.ServiceType,
		"is_active":    old.IsActive,
	}
	after := map[string]interface{}{
		"model_key":    newSvc.ModelKey,
		"service_type": newSvc.ServiceType,
		"is_active":    newSvc.IsActive,
	}
	return model.JSONMap{"before": before, "after": after}
}
