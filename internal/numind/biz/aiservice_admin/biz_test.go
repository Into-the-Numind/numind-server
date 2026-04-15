package aiservice_admin_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/aiservice_admin"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// mockRegistry is a minimal Registry implementation for unit tests.
// Only the methods exercised by aiservice_admin are wired up; others panic.
type mockRegistry struct {
	services       map[uint64]*model.AIService
	nextID         uint64
	deprecateCalls []uint64
	restoreCalls   []uint64
	auditLog       []string
}

func newMockRegistry() *mockRegistry {
	return &mockRegistry{
		services: make(map[uint64]*model.AIService),
		nextID:   1,
	}
}

func (m *mockRegistry) GetService(_ context.Context, id uint64) (*model.AIService, error) {
	svc, ok := m.services[id]
	if !ok {
		return nil, errno.ErrAIServiceNotFound
	}
	return svc, nil
}

func (m *mockRegistry) ListServices(_ context.Context, filter registry.ServiceFilter) ([]*model.AIService, error) {
	var result []*model.AIService
	for _, svc := range m.services {
		if filter.ServiceType != "" && svc.ServiceType != filter.ServiceType {
			continue
		}
		if !filter.IncludeDeprecated && svc.DeprecatedAt != nil {
			continue
		}
		result = append(result, svc)
	}
	return result, nil
}

func (m *mockRegistry) SaveService(_ context.Context, svc *model.AIService, _ uint64, actorName string) error {
	if svc.ID == 0 {
		svc.ID = m.nextID
		m.nextID++
	}
	cp := *svc
	m.services[svc.ID] = &cp
	m.auditLog = append(m.auditLog, "save:"+actorName)
	return nil
}

func (m *mockRegistry) DeprecateService(_ context.Context, id uint64, _ uint64, actorName string, _ string) error {
	svc, ok := m.services[id]
	if !ok {
		return errno.ErrAIServiceNotFound
	}
	now := time.Now()
	svc.DeprecatedAt = &now
	m.deprecateCalls = append(m.deprecateCalls, id)
	m.auditLog = append(m.auditLog, "deprecate:"+actorName)
	return nil
}

func (m *mockRegistry) RestoreService(_ context.Context, id uint64, _ uint64, actorName string, _ string) error {
	svc, ok := m.services[id]
	if !ok {
		return errno.ErrAIServiceNotFound
	}
	svc.DeprecatedAt = nil
	m.restoreCalls = append(m.restoreCalls, id)
	m.auditLog = append(m.auditLog, "restore:"+actorName)
	return nil
}

func (m *mockRegistry) GetTaskProfile(_ context.Context, _ string) (*model.TaskProfile, error) {
	panic("not implemented in mock")
}

func (m *mockRegistry) ListTaskProfiles(_ context.Context) ([]*model.TaskProfile, error) {
	panic("not implemented in mock")
}

func (m *mockRegistry) SaveTaskProfile(_ context.Context, _ *model.TaskProfile, _ []registry.TaskBinding, _ uint64, _ string) error {
	panic("not implemented in mock")
}

func (m *mockRegistry) ResolveTask(_ context.Context, _ string) (*registry.ResolvedRoute, []registry.ResolvedRoute, error) {
	panic("not implemented in mock")
}

// ----------------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------------

func TestListServices_Pagination(t *testing.T) {
	reg := newMockRegistry()
	// Seed 5 services.
	for i := 0; i < 5; i++ {
		svc := &model.AIService{ModelKey: "svc-" + string(rune('a'+i)), ServiceType: "llm", IsActive: true}
		_ = reg.SaveService(context.Background(), svc, 1, "admin")
	}

	b := aiservice_admin.New(reg, nil) // nil DB — no route queries in this test

	// Page 1, size 3 → 3 items.
	res, err := b.ListServices(context.Background(), registry.ServiceFilter{}, 1, 3)
	require.NoError(t, err)
	assert.EqualValues(t, 5, res.Total)
	assert.Len(t, res.List, 3)

	// Page 2, size 3 → remaining 2 items.
	res2, err := b.ListServices(context.Background(), registry.ServiceFilter{}, 2, 3)
	require.NoError(t, err)
	assert.EqualValues(t, 5, res2.Total)
	assert.Len(t, res2.List, 2)
}

func TestListServices_FilterByServiceType(t *testing.T) {
	reg := newMockRegistry()
	svcLLM := &model.AIService{ModelKey: "llm-svc", ServiceType: "llm", IsActive: true}
	svcOCR := &model.AIService{ModelKey: "ocr-svc", ServiceType: "ocr", IsActive: true}
	_ = reg.SaveService(context.Background(), svcLLM, 1, "admin")
	_ = reg.SaveService(context.Background(), svcOCR, 1, "admin")

	b := aiservice_admin.New(reg, nil)
	res, err := b.ListServices(context.Background(), registry.ServiceFilter{ServiceType: "ocr"}, 1, 20)
	require.NoError(t, err)
	assert.EqualValues(t, 1, res.Total)
	assert.Equal(t, "ocr", res.List[0].ServiceType)
}

func TestListServices_IncludeDeprecated(t *testing.T) {
	reg := newMockRegistry()
	svc := &model.AIService{ModelKey: "old-svc", ServiceType: "llm", IsActive: true}
	_ = reg.SaveService(context.Background(), svc, 1, "admin")
	_ = reg.DeprecateService(context.Background(), svc.ID, 1, "admin", "sunset")

	b := aiservice_admin.New(reg, nil)

	// Default: deprecated excluded.
	res, err := b.ListServices(context.Background(), registry.ServiceFilter{}, 1, 20)
	require.NoError(t, err)
	assert.EqualValues(t, 0, res.Total)

	// With include_deprecated: appears.
	res2, err := b.ListServices(context.Background(), registry.ServiceFilter{IncludeDeprecated: true}, 1, 20)
	require.NoError(t, err)
	assert.EqualValues(t, 1, res2.Total)
}

func TestCreateService_InvalidServiceType(t *testing.T) {
	reg := newMockRegistry()
	b := aiservice_admin.New(reg, nil)
	svc := &model.AIService{ModelKey: "bad-svc", ServiceType: "video"}
	_, err := b.CreateService(context.Background(), svc, 1, "admin")
	assert.Error(t, err)
	assert.IsType(t, &errno.Errno{}, err)
}

func TestDeprecateService_WritesAuditLog(t *testing.T) {
	reg := newMockRegistry()
	svc := &model.AIService{ModelKey: "audit-svc", ServiceType: "llm", IsActive: true}
	_ = reg.SaveService(context.Background(), svc, 1, "admin")

	b := aiservice_admin.New(reg, nil)
	err := b.DeprecateService(context.Background(), svc.ID, 1, "admin", "")
	require.NoError(t, err)
	assert.Contains(t, reg.auditLog, "deprecate:admin")
}

func TestRestoreService_RequiresReason(t *testing.T) {
	reg := newMockRegistry()
	svc := &model.AIService{ModelKey: "restore-svc", ServiceType: "llm", IsActive: true}
	_ = reg.SaveService(context.Background(), svc, 1, "admin")
	_ = reg.DeprecateService(context.Background(), svc.ID, 1, "admin", "sunset")

	b := aiservice_admin.New(reg, nil)

	// Missing reason → 400.
	err := b.RestoreService(context.Background(), svc.ID, 1, "admin", "")
	require.Error(t, err)
	e, ok := err.(*errno.Errno)
	require.True(t, ok)
	assert.Equal(t, 400, e.HTTP)

	// With reason → success.
	err = b.RestoreService(context.Background(), svc.ID, 1, "admin", "decision reversed")
	require.NoError(t, err)
}

func TestGetCapabilitySchemas_ReturnsThreeTypes(t *testing.T) {
	b := aiservice_admin.New(newMockRegistry(), nil)
	schemas, err := b.GetCapabilitySchemas(context.Background())
	require.NoError(t, err)
	require.Len(t, schemas, 3)
	for _, st := range []string{"llm", "ocr", "asr"} {
		s, ok := schemas[st]
		require.True(t, ok, "missing schema for %s", st)
		assert.IsType(t, &profile.CapabilitySchema{}, s)
		assert.Equal(t, st, s.ServiceType)
	}
}
