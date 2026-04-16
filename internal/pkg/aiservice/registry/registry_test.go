package registry

import (
	"context"
	"errors"
	"sort"
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
	services       map[uint64]*model.AIService
	taskProfiles   map[string]*model.TaskProfile // keyed by task_id
	taskBindings   map[uint64][]TaskBinding      // keyed by task_profile_id
	resolvedRoutes map[uint64]*resolvedRouteRow  // keyed by service_id
	auditLogs      []*model.AIServiceAuditLog
	dbCallCount    map[string]int // tracks how many times each method was called
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

// ListTaskBindings returns bindings for a task profile filtered by role, ordered by priority ASC.
func (m *mockStore) ListTaskBindings(_ context.Context, taskProfileID uint64, role string) ([]TaskBinding, error) {
	m.dbCallCount["ListTaskBindings"]++
	var out []TaskBinding
	for _, b := range m.taskBindings[taskProfileID] {
		if role == "" || b.Role == role {
			out = append(out, b)
		}
	}
	// Sort by priority ASC to match production behaviour.
	sort.Slice(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out, nil
}

// SaveTaskProfileWithBindings atomically upserts tp and replaces bindings.
func (m *mockStore) SaveTaskProfileWithBindings(_ context.Context, tp *model.TaskProfile, bindings []TaskBinding) error {
	m.dbCallCount["SaveTaskProfileWithBindings"]++
	if tp.ID == 0 {
		tp.ID = uint64(len(m.taskProfiles) + 1)
	}
	m.taskProfiles[tp.TaskID] = tp
	m.taskBindings[tp.ID] = bindings
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

func (m *mockStore) GetResolvedRouteByModelKey(_ context.Context, modelKey string) (*resolvedRouteRow, error) {
	m.dbCallCount["GetResolvedRouteByModelKey"]++
	for _, row := range m.resolvedRoutes {
		if row.ModelKey == modelKey {
			return row, nil
		}
	}
	return nil, errno.ErrAIServiceNotFound
}

func (m *mockStore) InsertAuditLog(_ context.Context, entry *model.AIServiceAuditLog) error {
	m.dbCallCount["InsertAuditLog"]++
	m.auditLogs = append(m.auditLogs, entry)
	return nil
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

// newTestRegistry creates a Registry backed by ms with the given TTL.
func newTestRegistry(ms *mockStore, ttl time.Duration) Registry {
	return NewWithStore(ms, ttl)
}

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
	ms.taskBindings[100] = []TaskBinding{
		{ServiceID: svcID2, Role: model.TaskProfileRoleFallback, Priority: 10},
	}

	reg := newTestRegistry(ms, 0)

	primary, fbs, err := reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	require.NotNil(t, primary)
	assert.Equal(t, svcID1, primary.ServiceID)
	assert.Equal(t, "glm-4", primary.ServiceKey)
	assert.Equal(t, "sop.text", primary.TaskID)
	require.Len(t, fbs, 1)
	assert.Equal(t, svcID2, fbs[0].ServiceID)
}

func TestResolveTask_NullDefaultService_ReturnsUnbound(t *testing.T) {
	ms := newMockStore()
	ms.taskProfiles["sop.text"] = &model.TaskProfile{
		ID:               101,
		TaskID:           "sop.text",
		ServiceType:      "llm",
		DefaultServiceID: nil, // NULL — not yet bound
	}
	reg := newTestRegistry(ms, 0)

	primary, fbs, err := reg.ResolveTask(context.Background(), "sop.text")
	assert.Nil(t, primary)
	assert.Nil(t, fbs)
	assert.Equal(t, errno.ErrAIServiceUnbound, err)
}

func TestResolveTask_UnknownTaskID_ReturnsNotFound(t *testing.T) {
	ms := newMockStore()
	reg := newTestRegistry(ms, 0)

	_, _, err := reg.ResolveTask(context.Background(), "does.not.exist")
	assert.True(t, errors.Is(err, errno.ErrAITaskNotFound), "expected ErrAITaskNotFound, got: %v", err)
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
	reg := newTestRegistry(ms, 0)

	_, _, err := reg.ResolveTask(context.Background(), "sop.text")
	assert.True(t, errors.Is(err, errno.ErrAIServiceNotFound), "expected ErrAIServiceNotFound, got: %v", err)
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
	ms.taskBindings[103] = []TaskBinding{
		{ServiceID: svcID3, Role: model.TaskProfileRoleFallback, Priority: 5}, // deprecated/missing
		{ServiceID: svcID2, Role: model.TaskProfileRoleFallback, Priority: 10},
	}
	reg := newTestRegistry(ms, 0)

	primary, fbs, err := reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	require.NotNil(t, primary)
	// Only the healthy fallback should appear.
	require.Len(t, fbs, 1)
	assert.Equal(t, svcID2, fbs[0].ServiceID)
}

// ----------------------------------------------------------------------------
// Tests: Priority ordering — spec §2.2.2 (0 = highest priority)
// ----------------------------------------------------------------------------

// TestResolveTask_FallbackPriorityOrder verifies that fallback[0] is the service
// with the lowest priority value (i.e. priority=0 comes before priority=10).
func TestResolveTask_FallbackPriorityOrder(t *testing.T) {
	ms := newMockStore()

	var svcHigh uint64 = 10 // priority = 0  → should be first
	var svcLow uint64 = 11  // priority = 10 → should be second

	ms.taskProfiles["sop.vision"] = &model.TaskProfile{
		ID:               200,
		TaskID:           "sop.vision",
		ServiceType:      "llm",
		DefaultServiceID: &svcID1,
	}
	ms.resolvedRoutes[svcID1] = makeResolvedRow(svcID1, "primary-model")
	ms.resolvedRoutes[svcHigh] = makeResolvedRow(svcHigh, "high-priority-fallback")
	ms.resolvedRoutes[svcLow] = makeResolvedRow(svcLow, "low-priority-fallback")

	// Store bindings: svcLow registered first but with higher priority number.
	ms.taskBindings[200] = []TaskBinding{
		{ServiceID: svcLow, Role: model.TaskProfileRoleFallback, Priority: 10},
		{ServiceID: svcHigh, Role: model.TaskProfileRoleFallback, Priority: 0},
	}
	reg := newTestRegistry(ms, 0)

	_, fbs, err := reg.ResolveTask(context.Background(), "sop.vision")
	require.NoError(t, err)
	require.Len(t, fbs, 2)
	// The fallback with priority=0 must come first (highest priority = lowest value).
	assert.Equal(t, svcHigh, fbs[0].ServiceID, "priority=0 service should be first fallback")
	assert.Equal(t, svcLow, fbs[1].ServiceID, "priority=10 service should be second fallback")
}

// ----------------------------------------------------------------------------
// Tests: ResolvedRoute fields — LatencyTier + QualityTier populated
// ----------------------------------------------------------------------------

func TestResolveTask_ResolvedRouteHasTierFields(t *testing.T) {
	ms := newMockStore()
	row := makeResolvedRow(svcID1, "glm-4")
	row.LatencyTier = "fast"
	row.QualityTier = "premium"
	ms.taskProfiles["sop.text"] = &model.TaskProfile{
		ID:               300,
		TaskID:           "sop.text",
		ServiceType:      "llm",
		DefaultServiceID: &svcID1,
	}
	ms.resolvedRoutes[svcID1] = row

	reg := newTestRegistry(ms, 0)
	primary, _, err := reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	assert.Equal(t, "fast", primary.LatencyTier)
	assert.Equal(t, "premium", primary.QualityTier)
}

// ----------------------------------------------------------------------------
// Tests: Cache TTL and invalidation
// ----------------------------------------------------------------------------

func TestCache_Miss_ThenHit(t *testing.T) {
	ms := newMockStore()
	ms.taskProfiles["sop.text"] = &model.TaskProfile{
		ID:               400,
		TaskID:           "sop.text",
		DefaultServiceID: &svcID1,
	}
	ms.resolvedRoutes[svcID1] = makeResolvedRow(svcID1, "glm-4")

	reg := newTestRegistry(ms, 10*time.Second)

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
		ID:               401,
		TaskID:           "sop.text",
		DefaultServiceID: &svcID1,
	}
	ms.resolvedRoutes[svcID1] = makeResolvedRow(svcID1, "glm-4")

	// Use a very short TTL (1 ms) so it expires immediately.
	reg := newTestRegistry(ms, 1*time.Millisecond)

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
		ID:               402,
		TaskID:           "sop.text",
		DefaultServiceID: &svcID1,
	}
	ms.resolvedRoutes[svcID1] = makeResolvedRow(svcID1, "glm-4")
	ms.services[svcID1] = &model.AIService{ID: svcID1, ModelKey: "glm-4"}

	reg := newTestRegistry(ms, 10*time.Second)

	// Warm the cache.
	_, _, err := reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	assert.Equal(t, 1, ms.dbCallCount["GetResolvedRoute"])

	// Write to the service (simulates an admin update).
	svc := &model.AIService{ID: svcID1, ModelKey: "glm-4-updated", ServiceType: "llm"}
	err = reg.SaveService(context.Background(), svc, 1, "admin")
	require.NoError(t, err)

	// ResolveTask must now bypass cache and hit DB again.
	_, _, err = reg.ResolveTask(context.Background(), "sop.text")
	require.NoError(t, err)
	assert.Equal(t, 2, ms.dbCallCount["GetResolvedRoute"], "expected cache miss after SaveService invalidation")
}

// ----------------------------------------------------------------------------
// Tests: AuditLog — writes correct fields
// ----------------------------------------------------------------------------

func TestDeprecateService_WritesAuditLog(t *testing.T) {
	ms := newMockStore()
	ms.services[svcID1] = &model.AIService{ID: svcID1, ModelKey: "glm-4"}

	reg := newTestRegistry(ms, 0)

	err := reg.DeprecateService(context.Background(), svcID1, 42, "alice", "switching to new model")
	require.NoError(t, err)

	require.Len(t, ms.auditLogs, 1)
	entry := ms.auditLogs[0]
	assert.Equal(t, uint64(42), entry.ActorID)
	assert.Equal(t, "alice", entry.ActorName)
	assert.Equal(t, model.AuditActionServiceDeprecate, entry.Action)
	assert.Equal(t, model.AuditTargetService, entry.TargetType)
	assert.Equal(t, svcID1, entry.TargetID)
	assert.Equal(t, "switching to new model", entry.Reason)
	assert.NotNil(t, entry.DiffJSON)
}

func TestRestoreService_WritesAuditLog(t *testing.T) {
	now := time.Now()
	ms := newMockStore()
	ms.services[svcID1] = &model.AIService{ID: svcID1, ModelKey: "glm-4", DeprecatedAt: &now}

	reg := newTestRegistry(ms, 0)

	err := reg.RestoreService(context.Background(), svcID1, 99, "bob", "re-activating")
	require.NoError(t, err)

	require.Len(t, ms.auditLogs, 1)
	entry := ms.auditLogs[0]
	assert.Equal(t, model.AuditActionServiceRestore, entry.Action)
	assert.Equal(t, "bob", entry.ActorName)
	assert.NotNil(t, entry.DiffJSON)
	// Service should no longer be deprecated.
	assert.Nil(t, ms.services[svcID1].DeprecatedAt)
}

func TestSaveService_WritesAuditLogWithActorName(t *testing.T) {
	ms := newMockStore()
	ms.services[svcID1] = &model.AIService{ID: svcID1, ModelKey: "glm-4", ServiceType: "llm"}

	reg := newTestRegistry(ms, 0)

	// Update an existing service.
	svc := &model.AIService{ID: svcID1, ModelKey: "glm-4-v2", ServiceType: "llm"}
	err := reg.SaveService(context.Background(), svc, 7, "carol")
	require.NoError(t, err)

	require.Len(t, ms.auditLogs, 1)
	entry := ms.auditLogs[0]
	assert.Equal(t, uint64(7), entry.ActorID)
	assert.Equal(t, "carol", entry.ActorName)
	assert.Equal(t, model.AuditActionServiceUpdate, entry.Action)
	assert.NotNil(t, entry.DiffJSON)
}

func TestSaveTaskProfile_WritesAuditLogWithActorID(t *testing.T) {
	ms := newMockStore()

	reg := newTestRegistry(ms, 0)

	tp := &model.TaskProfile{TaskID: "sop.new", ServiceType: "llm"}
	err := reg.SaveTaskProfile(context.Background(), tp, nil, 5, "dave")
	require.NoError(t, err)

	require.Len(t, ms.auditLogs, 1)
	entry := ms.auditLogs[0]
	assert.Equal(t, uint64(5), entry.ActorID)
	assert.Equal(t, "dave", entry.ActorName)
	assert.Equal(t, model.AuditActionTaskBind, entry.Action)
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
