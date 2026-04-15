package registry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ----------------------------------------------------------------------------
// mockStore — in-memory IStore for unit tests
// ----------------------------------------------------------------------------

// mockStore implements IStore with simple in-memory maps.
// Tests populate the maps before calling Registry methods.
type mockStore struct {
	services        map[uint64]*model.AIService
	taskProfiles    map[string]*model.TaskProfile // keyed by task_id
	taskBindings    map[uint64][]TaskBinding       // keyed by task_profile_id
	resolvedRoutes  map[uint64]*resolvedRouteRow   // keyed by service_id
	auditLogs       []*model.AIServiceAuditLog
	dbCallCount     map[string]int // tracks how many times each method was called
}

func newMockStore() *mockStore {
	return &mockStore{
		services:       make(map[uint64]*model.AIService),
		taskProfiles:   make(map[string]*model.TaskProfile),
		taskBindings:   make(map[uint64][]TaskBinding),
		resolvedRoutes: make(map[uint64]*resolvedRouteRow),
		dbCallCount:    make(map[string]int),
	}
}

func (m *mockStore) GetService(_ context.Context, id uint64) (*model.AIService, error) {
	m.dbCallCount["GetService"]++
	svc, ok := m.services[id]
	if !ok {
		return nil, errno.ErrAIServiceNotFound
	}
	return svc, nil
}

func (m *mockStore) ListServices(_ context.Context, filter ServiceFilter) ([]*model.AIService, error) {
	m.dbCallCount["ListServices"]++
	var out []*model.AIService
	for _, svc := range m.services {
		if filter.ServiceType != "" && svc.ServiceType != filter.ServiceType {
			continue
		}
		if !filter.IncludeDeprecated && svc.DeprecatedAt != nil {
			continue
		}
		out = append(out, svc)
	}
	return out, nil
}

func (m *mockStore) SaveService(_ context.Context, svc *model.AIService) error {
	m.dbCallCount["SaveService"]++
	if svc.ID == 0 {
		svc.ID = uint64(len(m.services) + 1)
	}
	m.services[svc.ID] = svc
	return nil
}

func (m *mockStore) SetServiceDeprecated(_ context.Context, id uint64, deprecatedAt *time.Time) error {
	m.dbCallCount["SetServiceDeprecated"]++
	svc, ok := m.services[id]
	if !ok {
		return errno.ErrAIServiceNotFound
	}
	svc.DeprecatedAt = deprecatedAt
	return nil
}

func (m *mockStore) GetTaskProfile(_ context.Context, taskID string) (*model.TaskProfile, error) {
	m.dbCallCount["GetTaskProfile"]++
	tp, ok := m.taskProfiles[taskID]
	if !ok {
		return nil, errno.ErrAITaskNotFound
	}
	return tp, nil
}

func (m *mockStore) ListTaskProfiles(_ context.Context) ([]*model.TaskProfile, error) {
	m.dbCallCount["ListTaskProfiles"]++
	var out []*model.TaskProfile
	for _, tp := range m.taskProfiles {
		out = append(out, tp)
	}
	return out, nil
}

func (m *mockStore) UpsertTaskProfile(_ context.Context, tp *model.TaskProfile) error {
	m.dbCallCount["UpsertTaskProfile"]++
	if tp.ID == 0 {
		tp.ID = uint64(len(m.taskProfiles) + 1)
	}
	m.taskProfiles[tp.TaskID] = tp
	return nil
}

func (m *mockStore) ReplaceTaskBindings(_ context.Context, taskProfileID uint64, bindings []TaskBinding) error {
	m.dbCallCount["ReplaceTaskBindings"]++
	m.taskBindings[taskProfileID] = bindings
	return nil
}

func (m *mockStore) GetResolvedRoute(_ context.Context, serviceID uint64) (*resolvedRouteRow, error) {
	m.dbCallCount["GetResolvedRoute"]++
	row, ok := m.resolvedRoutes[serviceID]
	if !ok {
		return nil, errno.ErrAIServiceNotFound
	}
	return row, nil
}

func (m *mockStore) InsertAuditLog(_ context.Context, entry *model.AIServiceAuditLog) error {
	m.dbCallCount["InsertAuditLog"]++
	m.auditLogs = append(m.auditLogs, entry)
	return nil
}

// mockStoreWithFallbacks extends mockStore to expose fallback bindings through
// the registry's internal loadFallbackBindings path. Because loadFallbackBindings
// type-asserts to *gormStore to access the raw DB, we override ResolveTask at the
// registry level by using a custom registryImplWithFallbacks that directly calls
// GetResolvedRoute for pre-configured fallback IDs.
//
// Design: tests use the fakeFallbackRegistry helper below instead of injecting
// fallback bindings through a gormStore.

// ----------------------------------------------------------------------------
// fakeFallbackRegistry wraps registryImpl but overrides loadFallbackBindings
// so tests can supply fallback bindings without needing a real *gorm.DB.
// ----------------------------------------------------------------------------

type fakeFallbackRegistry struct {
	*registryImpl
	fallbackBindings map[uint64][]TaskBinding // taskProfileID → bindings
}

func (f *fakeFallbackRegistry) loadFallbackBindings(_ context.Context, taskProfileID uint64) ([]TaskBinding, error) {
	return f.fallbackBindings[taskProfileID], nil
}

// ResolveTask re-implements the resolution to use our override.
func (f *fakeFallbackRegistry) ResolveTask(ctx context.Context, taskID string) (*ResolvedRoute, []ResolvedRoute, error) {
	// Cache lookup.
	if primary, fallbacks, ok := f.cache.GetTask(taskID); ok {
		return primary, fallbacks, nil
	}

	tp, err := f.store.GetTaskProfile(ctx, taskID)
	if err != nil {
		return nil, nil, err
	}
	if tp.DefaultServiceID == nil {
		return nil, nil, errno.ErrAIServiceNotFound
	}

	primaryRow, err := f.store.GetResolvedRoute(ctx, *tp.DefaultServiceID)
	if err != nil {
		return nil, nil, err
	}
	primaryRoute := buildResolvedRoute(taskID, primaryRow)

	fallbackBindings, err := f.loadFallbackBindings(ctx, tp.ID)
	if err != nil {
		return nil, nil, err
	}

	var fallbacks []ResolvedRoute
	var allSvcIDs []uint64
	allSvcIDs = append(allSvcIDs, *tp.DefaultServiceID)
	for _, b := range fallbackBindings {
		row, err := f.store.GetResolvedRoute(ctx, b.ServiceID)
		if err != nil {
			continue
		}
		fallbacks = append(fallbacks, buildResolvedRoute(taskID, row))
		allSvcIDs = append(allSvcIDs, b.ServiceID)
	}

	f.cache.SetTask(taskID, &primaryRoute, fallbacks, allSvcIDs)
	return &primaryRoute, fallbacks, nil
}

// newFakeRegistry builds a fakeFallbackRegistry backed by the given mockStore.
func newFakeRegistry(ms *mockStore, fallbacks map[uint64][]TaskBinding, ttl time.Duration) *fakeFallbackRegistry {
	impl := &registryImpl{
		store: ms,
		cache: newCache(ttl),
	}
	return &fakeFallbackRegistry{
		registryImpl:     impl,
		fallbackBindings: fallbacks,
	}
}

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

var svcID1 uint64 = 1
var svcID2 uint64 = 2
var svcID3 uint64 = 3 // deprecated service

func makeResolvedRow(id uint64, modelKey string) *resolvedRouteRow {
	return &resolvedRouteRow{
		ServiceID:          id,
		ModelKey:           modelKey,
		ServiceType:        "llm",
		CapabilityJSON:     model.JSONMap{"context_window": 65536},
		LatencyTier:        "standard",
		QualityTier:        "standard",
		ProviderID:         10,
		ProviderName:       "volc",
		ProviderBaseURL:    "https://ark.volces.com/api/v3",
		ProviderAPIKey:     "test-key",
		ProviderModelID:    "deepseek-v3-" + modelKey,
		RoutePriority:      0,
		RouteIsActive:      true,
		PricingUnit:        "per_1m_tokens",
		InputPricePerMTok:  1.0,
		OutputPricePerMTok: 2.0,
	}
}

func ptr[T any](v T) *T { return &v }

// ----------------------------------------------------------------------------
// Tests: ResolveTask — basic paths
// ----------------------------------------------------------------------------

func TestResolveTask_PrimaryAndFallbacks(t *testing.T) {
	ms := newMockStore()

	// Set up task profile with default service.
	ms.taskProfiles["sop.text"] = &model.TaskProfile{
		ID:               100,
		TaskID:           "sop.text",
		ServiceType:      "llm",
		DefaultServiceID: &svcID1,
	}
	ms.resolvedRoutes[svcID1] = makeResolvedRow(svcID1, "glm-4")
	ms.resolvedRoutes[svcID2] = makeResolvedRow(svcID2, "deepseek-v3")

	fallbacks := map[uint64][]TaskBinding{
		100: {{ServiceID: svcID2, Role: model.TaskProfileRoleFallback, Priority: 10}},
	}
	reg := newFakeRegistry(ms, fallbacks, 0)

	primary, fbs, err := reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	require.NotNil(t, primary)
	assert.Equal(t, svcID1, primary.ServiceID)
	assert.Equal(t, "glm-4", primary.ServiceKey)
	assert.Equal(t, "sop.text", primary.TaskID)
	require.Len(t, fbs, 1)
	assert.Equal(t, svcID2, fbs[0].ServiceID)
}

func TestResolveTask_NullDefaultService_ReturnsError(t *testing.T) {
	ms := newMockStore()
	ms.taskProfiles["sop.text"] = &model.TaskProfile{
		ID:               101,
		TaskID:           "sop.text",
		ServiceType:      "llm",
		DefaultServiceID: nil, // NULL — not yet bound
	}
	reg := newFakeRegistry(ms, nil, 0)

	primary, fbs, err := reg.ResolveTask(context.Background(), "sop.text")
	assert.Nil(t, primary)
	assert.Nil(t, fbs)
	assert.Equal(t, errno.ErrAIServiceNotFound, err)
}

func TestResolveTask_UnknownTaskID_ReturnsNotFound(t *testing.T) {
	ms := newMockStore()
	reg := newFakeRegistry(ms, nil, 0)

	_, _, err := reg.ResolveTask(context.Background(), "does.not.exist")
	assert.Equal(t, errno.ErrAITaskNotFound, err)
}

func TestResolveTask_PrimaryDeprecated_ReturnsError(t *testing.T) {
	ms := newMockStore()
	ms.taskProfiles["sop.text"] = &model.TaskProfile{
		ID:               102,
		TaskID:           "sop.text",
		ServiceType:      "llm",
		DefaultServiceID: &svcID3,
	}
	// svcID3 has no resolvedRoute entry — simulates deprecated/missing service.
	reg := newFakeRegistry(ms, nil, 0)

	_, _, err := reg.ResolveTask(context.Background(), "sop.text")
	assert.Equal(t, errno.ErrAIServiceNotFound, err)
}

func TestResolveTask_FallbackDeprecatedIsSkipped(t *testing.T) {
	ms := newMockStore()
	ms.taskProfiles["sop.text"] = &model.TaskProfile{
		ID:               103,
		TaskID:           "sop.text",
		ServiceType:      "llm",
		DefaultServiceID: &svcID1,
	}
	ms.resolvedRoutes[svcID1] = makeResolvedRow(svcID1, "glm-4")
	// svcID3 is the fallback but has NO resolvedRoute → should be silently skipped.
	// svcID2 is a healthy second fallback.
	ms.resolvedRoutes[svcID2] = makeResolvedRow(svcID2, "deepseek-v3")

	fallbacks := map[uint64][]TaskBinding{
		103: {
			{ServiceID: svcID3, Role: model.TaskProfileRoleFallback, Priority: 20}, // deprecated/missing
			{ServiceID: svcID2, Role: model.TaskProfileRoleFallback, Priority: 10},
		},
	}
	reg := newFakeRegistry(ms, fallbacks, 0)

	primary, fbs, err := reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	require.NotNil(t, primary)
	// Only the healthy fallback should appear.
	require.Len(t, fbs, 1)
	assert.Equal(t, svcID2, fbs[0].ServiceID)
}

// ----------------------------------------------------------------------------
// Tests: Cache TTL and invalidation
// ----------------------------------------------------------------------------

func TestCache_Miss_ThenHit(t *testing.T) {
	ms := newMockStore()
	ms.taskProfiles["sop.text"] = &model.TaskProfile{
		ID:               200,
		TaskID:           "sop.text",
		DefaultServiceID: &svcID1,
	}
	ms.resolvedRoutes[svcID1] = makeResolvedRow(svcID1, "glm-4")

	reg := newFakeRegistry(ms, nil, 10*time.Second)

	// First call — must hit DB.
	_, _, err := reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	assert.Equal(t, 1, ms.dbCallCount["GetTaskProfile"])
	assert.Equal(t, 1, ms.dbCallCount["GetResolvedRoute"])

	// Second call — must be served from cache (no additional DB calls).
	_, _, err = reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	assert.Equal(t, 1, ms.dbCallCount["GetTaskProfile"], "expected no additional DB call for task profile")
	assert.Equal(t, 1, ms.dbCallCount["GetResolvedRoute"], "expected no additional DB call for route")
}

func TestCache_TTLExpiry_RefetchesFromDB(t *testing.T) {
	ms := newMockStore()
	ms.taskProfiles["sop.text"] = &model.TaskProfile{
		ID:               201,
		TaskID:           "sop.text",
		DefaultServiceID: &svcID1,
	}
	ms.resolvedRoutes[svcID1] = makeResolvedRow(svcID1, "glm-4")

	// Use a very short TTL (1 ms) so it expires immediately.
	reg := newFakeRegistry(ms, nil, 1*time.Millisecond)

	// First call — populates cache.
	_, _, err := reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	assert.Equal(t, 1, ms.dbCallCount["GetTaskProfile"])

	// Let the TTL expire.
	time.Sleep(5 * time.Millisecond)

	// Second call — cache is expired, must re-hit DB.
	_, _, err = reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	assert.Equal(t, 2, ms.dbCallCount["GetTaskProfile"], "expected second DB call after TTL expiry")
}

func TestCache_SaveService_InvalidatesTask(t *testing.T) {
	ms := newMockStore()
	ms.taskProfiles["sop.text"] = &model.TaskProfile{
		ID:               202,
		TaskID:           "sop.text",
		DefaultServiceID: &svcID1,
	}
	ms.resolvedRoutes[svcID1] = makeResolvedRow(svcID1, "glm-4")
	ms.services[svcID1] = &model.AIService{ID: svcID1, ModelKey: "glm-4"}

	reg := newFakeRegistry(ms, nil, 10*time.Second)

	// Warm the cache.
	_, _, err := reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	assert.Equal(t, 1, ms.dbCallCount["GetResolvedRoute"])

	// Write to the service (simulates an admin update).
	svc := &model.AIService{ID: svcID1, ModelKey: "glm-4-updated", ServiceType: "llm"}
	err = reg.SaveService(context.Background(), svc, 1)
	require.NoError(t, err)

	// ResolveTask must now bypass cache and hit DB again.
	_, _, err = reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	assert.Equal(t, 2, ms.dbCallCount["GetResolvedRoute"], "expected cache miss after SaveService invalidation")
}

// ----------------------------------------------------------------------------
// Tests: AuditLog — DeprecateService writes audit entry
// ----------------------------------------------------------------------------

func TestDeprecateService_WritesAuditLog(t *testing.T) {
	ms := newMockStore()
	ms.services[svcID1] = &model.AIService{ID: svcID1, ModelKey: "glm-4"}

	reg := newFakeRegistry(ms, nil, 0)

	err := reg.DeprecateService(context.Background(), svcID1, 42, "switching to new model")
	require.NoError(t, err)

	require.Len(t, ms.auditLogs, 1)
	log := ms.auditLogs[0]
	assert.Equal(t, uint64(42), log.ActorID)
	assert.Equal(t, model.AuditActionServiceDeprecate, log.Action)
	assert.Equal(t, model.AuditTargetService, log.TargetType)
	assert.Equal(t, svcID1, log.TargetID)
	assert.Equal(t, "switching to new model", log.Reason)
}

func TestRestoreService_WritesAuditLog(t *testing.T) {
	now := time.Now()
	ms := newMockStore()
	ms.services[svcID1] = &model.AIService{ID: svcID1, ModelKey: "glm-4", DeprecatedAt: &now}

	reg := newFakeRegistry(ms, nil, 0)

	err := reg.RestoreService(context.Background(), svcID1, 99, "re-activating")
	require.NoError(t, err)

	require.Len(t, ms.auditLogs, 1)
	assert.Equal(t, model.AuditActionServiceRestore, ms.auditLogs[0].Action)
	// Service should no longer be deprecated.
	assert.Nil(t, ms.services[svcID1].DeprecatedAt)
}

// ----------------------------------------------------------------------------
// Tests: CapabilityJSON unmarshal
// ----------------------------------------------------------------------------

func TestUnmarshalCapability_ValidJSON(t *testing.T) {
	m := model.JSONMap{
		"service_type":     "llm",
		"context_window":   float64(65536),
		"input_modalities": []interface{}{"text", "image"},
		"features": map[string]interface{}{
			"tool_use":  true,
			"streaming": true,
		},
	}
	cap := unmarshalCapability(m, "llm")
	assert.Equal(t, "llm", cap.ServiceType)
	assert.Equal(t, 65536, cap.ContextWindow)
	assert.Contains(t, cap.InputModalities, "text")
	assert.Contains(t, cap.InputModalities, "image")
	assert.True(t, cap.Features["tool_use"])
	assert.True(t, cap.Features["streaming"])
}

func TestUnmarshalCapability_EmptyMap_UsesServiceType(t *testing.T) {
	cap := unmarshalCapability(model.JSONMap{}, "ocr")
	assert.Equal(t, "ocr", cap.ServiceType)
}

func TestUnmarshalCapability_NilMap_UsesServiceType(t *testing.T) {
	cap := unmarshalCapability(nil, "asr")
	assert.Equal(t, "asr", cap.ServiceType)
}

func TestUnmarshalCapability_MissingServiceTypeField_FilledFromParam(t *testing.T) {
	m := model.JSONMap{"context_window": float64(32768)}
	cap := unmarshalCapability(m, "llm")
	assert.Equal(t, "llm", cap.ServiceType)
	assert.Equal(t, 32768, cap.ContextWindow)
}

// ----------------------------------------------------------------------------
// Tests: Cache unit tests (direct cache access)
// ----------------------------------------------------------------------------

func TestCacheService_HitAndMiss(t *testing.T) {
	c := newCache(10 * time.Second)

	svc := &model.AIService{ID: 1, ModelKey: "test-model"}
	c.SetService(svc)

	got, ok := c.GetService(1)
	assert.True(t, ok)
	assert.Equal(t, svc.ModelKey, got.ModelKey)

	_, ok2 := c.GetService(999)
	assert.False(t, ok2)
}

func TestCacheService_TTLExpiry(t *testing.T) {
	c := newCache(1 * time.Millisecond)
	svc := &model.AIService{ID: 1, ModelKey: "test-model"}
	c.SetService(svc)

	time.Sleep(5 * time.Millisecond)

	_, ok := c.GetService(1)
	assert.False(t, ok, "cache entry should have expired")
}

func TestCacheTask_InvalidateOnSave(t *testing.T) {
	c := newCache(10 * time.Second)
	primary := &ResolvedRoute{TaskID: "sop.text", ServiceID: 1}
	c.SetTask("sop.text", primary, nil, []uint64{1})

	// Cache should have the entry.
	p, _, ok := c.GetTask("sop.text")
	require.True(t, ok)
	assert.Equal(t, uint64(1), p.ServiceID)

	// Invalidate service 1 — should also evict "sop.text".
	c.InvalidateService(1)

	_, _, ok2 := c.GetTask("sop.text")
	assert.False(t, ok2, "task cache should be evicted when referenced service is invalidated")
}
