package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/errno"
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

// PricingInfo summarises the billing parameters for a specific route.
type PricingInfo struct {
	Unit               string   // "per_1m_tokens" | "per_call" | "per_second"
	InputPricePerMTok  float64
	OutputPricePerMTok float64
	PricePerCall       *float64
	PricePerSecond     *float64
}

// ResolvedRoute is a call-ready description of a single AI service route.
// It merges task_profile + ai_service + ai_service_route + llm_provider into
// a single flat struct so callers can invoke the service without extra DB queries.
type ResolvedRoute struct {
	TaskID          string
	ServiceID       uint64
	ServiceKey      string // ai_service.model_key
	ServiceType     string // llm | ocr | asr
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

	// SaveService creates or updates a service and invalidates related caches.
	SaveService(ctx context.Context, svc *model.AIService, actorID uint64) error

	// DeprecateService marks a service as deprecated (soft-delete) and writes an audit log.
	DeprecateService(ctx context.Context, id uint64, actorID uint64, reason string) error

	// RestoreService clears the deprecated_at flag and writes an audit log.
	RestoreService(ctx context.Context, id uint64, actorID uint64, reason string) error

	// GetTaskProfile returns a single TaskProfile by task_id.
	GetTaskProfile(ctx context.Context, taskID string) (*model.TaskProfile, error)

	// ListTaskProfiles returns all task profiles.
	ListTaskProfiles(ctx context.Context) ([]*model.TaskProfile, error)

	// SaveTaskProfile creates or updates a task profile and replaces its service bindings.
	SaveTaskProfile(ctx context.Context, tp *model.TaskProfile, bindings []TaskBinding) error

	// ResolveTask looks up the task profile for taskID and resolves the primary +
	// fallback service routes. The primary route is returned as the first value;
	// fallback routes (ordered by priority DESC) are the second value.
	//
	// Errors:
	//   - errno.ErrAITaskNotFound      — no TaskProfile row for taskID
	//   - errno.ErrAIServiceNotFound   — default_service_id is NULL or the service
	//                                    is deprecated / inactive
	ResolveTask(ctx context.Context, taskID string) (*ResolvedRoute, []ResolvedRoute, error)
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

// SaveService creates or updates a service, then invalidates the cache for that service
// and all task entries that reference it.
func (r *registryImpl) SaveService(ctx context.Context, svc *model.AIService, actorID uint64) error {
	isCreate := svc.ID == 0
	if err := r.store.SaveService(ctx, svc); err != nil {
		return fmt.Errorf("registry.SaveService: %w", err)
	}
	// Invalidate cache for this service ID.
	r.cache.InvalidateService(svc.ID)

	// Write audit log.
	action := model.AuditActionServiceCreate
	if !isCreate {
		action = model.AuditActionServiceUpdate
	}
	_ = r.store.InsertAuditLog(ctx, &model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  "",
		Action:     action,
		TargetType: model.AuditTargetService,
		TargetID:   svc.ID,
	})
	return nil
}

// DeprecateService sets deprecated_at to now and writes an audit log entry.
func (r *registryImpl) DeprecateService(ctx context.Context, id uint64, actorID uint64, reason string) error {
	now := time.Now()
	if err := r.store.SetServiceDeprecated(ctx, id, &now); err != nil {
		return fmt.Errorf("registry.DeprecateService: %w", err)
	}
	r.cache.InvalidateService(id)

	_ = r.store.InsertAuditLog(ctx, &model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  "",
		Action:     model.AuditActionServiceDeprecate,
		TargetType: model.AuditTargetService,
		TargetID:   id,
		Reason:     reason,
	})
	return nil
}

// RestoreService clears deprecated_at and writes an audit log entry.
func (r *registryImpl) RestoreService(ctx context.Context, id uint64, actorID uint64, reason string) error {
	if err := r.store.SetServiceDeprecated(ctx, id, nil); err != nil {
		return fmt.Errorf("registry.RestoreService: %w", err)
	}
	r.cache.InvalidateService(id)

	_ = r.store.InsertAuditLog(ctx, &model.AIServiceAuditLog{
		ActorID:    actorID,
		ActorName:  "",
		Action:     model.AuditActionServiceRestore,
		TargetType: model.AuditTargetService,
		TargetID:   id,
		Reason:     reason,
	})
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

// SaveTaskProfile creates or updates a task profile and atomically replaces its
// service bindings, then invalidates the task cache entry.
func (r *registryImpl) SaveTaskProfile(ctx context.Context, tp *model.TaskProfile, bindings []TaskBinding) error {
	if err := r.store.UpsertTaskProfile(ctx, tp); err != nil {
		return fmt.Errorf("registry.SaveTaskProfile: %w", err)
	}
	if err := r.store.ReplaceTaskBindings(ctx, tp.ID, bindings); err != nil {
		return fmt.Errorf("registry.SaveTaskProfile (bindings): %w", err)
	}
	r.cache.InvalidateTask(tp.TaskID)

	_ = r.store.InsertAuditLog(ctx, &model.AIServiceAuditLog{
		ActorID:    0,
		ActorName:  "",
		Action:     model.AuditActionTaskBind,
		TargetType: model.AuditTargetTaskProfile,
		TargetID:   tp.ID,
	})
	return nil
}

// ----------------------------------------------------------------------------
// Resolver
// ----------------------------------------------------------------------------

// ResolveTask resolves the primary and fallback routes for a given task ID.
//
// Resolution algorithm:
//  1. Fetch TaskProfile by taskID (errno.ErrAITaskNotFound if missing).
//  2. If default_service_id is NULL → return errno.ErrAIServiceNotFound.
//  3. Resolve primary service: call GetResolvedRoute(default_service_id).
//     If the service is missing, deprecated, or inactive, return errno.ErrAIServiceNotFound.
//  4. Fetch TaskProfileService rows with role="fallback", ordered by priority DESC.
//     Attempt to resolve each; silently skip services that are deprecated/inactive.
//  5. Return (primary, fallbacks, nil).
//
// Results are cached for the configured TTL. Cache miss triggers a full DB load.
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
		return nil, nil, errno.ErrAIServiceNotFound
	}

	// Resolve primary route.
	primaryRow, err := r.store.GetResolvedRoute(ctx, *tp.DefaultServiceID)
	if err != nil {
		return nil, nil, fmt.Errorf("registry.ResolveTask (primary): %w", err)
	}
	primaryRoute := buildResolvedRoute(taskID, primaryRow)

	// Fetch fallback bindings from DB.
	fallbackBindings, err := r.loadFallbackBindings(ctx, tp.ID)
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

// loadFallbackBindings fetches TaskProfileService rows for the given profile ID
// where role = "fallback", ordered by priority DESC.
func (r *registryImpl) loadFallbackBindings(ctx context.Context, taskProfileID uint64) ([]TaskBinding, error) {
	// We access the store's underlying DB indirectly. Because IStore does not
	// expose a generic query method, we query task_profile_service through a
	// type-assertion to the concrete store for the list query.
	//
	// Design note: we keep the list query here in the registry (not in IStore)
	// to avoid bloating the interface with rarely-needed methods. In the production
	// path the store is always a *gormStore; in tests the mock implements the
	// full IStore which includes this data via GetTaskProfile's side-effects.
	//
	// The concrete path uses the gormStore's db field via a type assertion.
	gs, ok := r.store.(*gormStore)
	if !ok {
		// Allow test mocks that embed fake binding data via a different mechanism.
		return nil, nil
	}

	type tpsRow struct {
		ServiceID uint64 `gorm:"column:service_id"`
		Priority  int    `gorm:"column:priority"`
	}
	var rows []tpsRow
	err := gs.db.Table("task_profile_service").
		Select("service_id, priority").
		Where("task_profile_id = ? AND role = ?", taskProfileID, model.TaskProfileRoleFallback).
		Order("priority DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("loadFallbackBindings: %w", err)
	}

	bindings := make([]TaskBinding, len(rows))
	for i, row := range rows {
		bindings[i] = TaskBinding{ServiceID: row.ServiceID, Role: model.TaskProfileRoleFallback, Priority: row.Priority}
	}
	return bindings, nil
}

// buildResolvedRoute converts a resolvedRouteRow into the exported ResolvedRoute type,
// unmarshaling the capability JSON into a profile.ServiceCapability struct.
func buildResolvedRoute(taskID string, row *resolvedRouteRow) ResolvedRoute {
	cap := unmarshalCapability(row.CapabilityJSON, row.ServiceType)
	return ResolvedRoute{
		TaskID:      taskID,
		ServiceID:   row.ServiceID,
		ServiceKey:  row.ModelKey,
		ServiceType: row.ServiceType,
		Provider: ProviderInfo{
			ID:      row.ProviderID,
			Name:    row.ProviderName,
			BaseURL: row.ProviderBaseURL,
			APIKey:  row.ProviderAPIKey,
		},
		ProviderModelID: row.ProviderModelID,
		Capability:      cap,
		Pricing: PricingInfo{
			Unit:               row.PricingUnit,
			InputPricePerMTok:  row.InputPricePerMTok,
			OutputPricePerMTok: row.OutputPricePerMTok,
			PricePerCall:       row.PricePerCall,
			PricePerSecond:     row.PricePerSecond,
		},
	}
}

// unmarshalCapability converts a model.JSONMap (read from capability_json) into a
// profile.ServiceCapability struct. Errors are silently swallowed — a partially
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
		cap.ServiceType = serviceType
		return cap
	}
	if err := json.Unmarshal(b, &cap); err != nil {
		cap.ServiceType = serviceType
		return cap
	}
	// Ensure ServiceType is set (may be absent in older capability docs).
	if cap.ServiceType == "" {
		cap.ServiceType = serviceType
	}
	return cap
}
