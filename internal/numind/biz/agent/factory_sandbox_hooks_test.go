package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"numind-server/internal/numind/biz/sandbox"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// ===========================================================================
// mock sandbox.Pool implementation (testable Pool with controlled errors)
// ===========================================================================

type mockSandboxPool struct {
	borrowErr   error
	returnErr   error
	dc          sandbox.DockerClient
	borrowCount atomic.Int64
	returnCount atomic.Int64
}

var _ sandbox.Pool = (*mockSandboxPool)(nil)

func (m *mockSandboxPool) Borrow(_ context.Context) (*sandbox.Session, error) {
	m.borrowCount.Add(1)
	if m.borrowErr != nil {
		return nil, m.borrowErr
	}
	return &sandbox.Session{
		ContainerID: "mock-container-abc",
		ImageTag:    "python:3.11-slim",
		Config:      sandbox.DefaultSandboxConfig,
		BorrowedAt:  time.Now(),
	}, nil
}
func (m *mockSandboxPool) Return(_ *sandbox.Session, _ int, _ string) error {
	m.returnCount.Add(1)
	return m.returnErr
}
func (m *mockSandboxPool) Close() error                       { return nil }
func (m *mockSandboxPool) Size() int                          { return 0 }
func (m *mockSandboxPool) DockerClient() sandbox.DockerClient { return m.dc }

// ===========================================================================
// mock IAgentSandboxSessionStore
// ===========================================================================

type mockSandboxStore struct {
	nextID         atomic.Uint64
	createErr      error
	updateErr      error
	createdRecords []*model.AgentSandboxSession
	updateCalls    atomic.Int64
}

func (m *mockSandboxStore) Create(_ context.Context, sess *model.AgentSandboxSession) error {
	if m.createErr != nil {
		return m.createErr
	}
	sess.ID = m.nextID.Add(1)
	m.createdRecords = append(m.createdRecords, sess)
	return nil
}
func (m *mockSandboxStore) UpdateState(_ context.Context, _ uint64, _ string, _ *int, _ string, _ *time.Time) error {
	m.updateCalls.Add(1)
	return m.updateErr
}
func (m *mockSandboxStore) GetByContainerID(_ context.Context, _ string) (*model.AgentSandboxSession, error) {
	return nil, errors.New("not used in tests")
}
func (m *mockSandboxStore) ListByUser(_ context.Context, _ uint, _ int) ([]model.AgentSandboxSession, error) {
	return nil, errors.New("not used in tests")
}

// ===========================================================================
// mock einotool.BaseTool — minimal Info() impl returning configurable Name
// ===========================================================================

type mockEinoTool struct {
	name string
}

var _ einotool.BaseTool = (*mockEinoTool)(nil)

func (m *mockEinoTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: m.name, Desc: "mock"}, nil
}

type errInfoTool struct{}

var _ einotool.BaseTool = (*errInfoTool)(nil)

func (errInfoTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return nil, errors.New("mock Info failure")
}

// ===========================================================================
// PreToolCall tests
// ===========================================================================

func TestSandboxHookManager_PreToolCall_HappyPath(t *testing.T) {
	pool := &mockSandboxPool{}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)

	ctx := middleware.NewContextWithUserID(context.Background(), 30)
	ctx = WithRunID(ctx, 100)

	action, err := m.preToolCall(ctx, &mockEinoTool{name: "bash_exec"}, "")
	if err != nil {
		t.Fatalf("preToolCall err = %v", err)
	}
	if action != HookActionContinue {
		t.Errorf("action = %d; want HookActionContinue", action)
	}
	if pool.borrowCount.Load() != 1 {
		t.Errorf("Borrow count = %d; want 1", pool.borrowCount.Load())
	}
	if len(storeM.createdRecords) != 1 {
		t.Fatalf("created records = %d; want 1", len(storeM.createdRecords))
	}
	rec := storeM.createdRecords[0]
	if rec.UserID != 30 {
		t.Errorf("rec.UserID = %d; want 30", rec.UserID)
	}
	if rec.AgentRunID == nil || *rec.AgentRunID != 100 {
		t.Errorf("rec.AgentRunID = %v; want 100", rec.AgentRunID)
	}
	if rec.ContainerID != "mock-container-abc" {
		t.Errorf("rec.ContainerID = %q; want mock-container-abc", rec.ContainerID)
	}
	if rec.Status != "running" {
		t.Errorf("rec.Status = %q; want running", rec.Status)
	}

	// Session should be findable via SandboxSessionFor
	sess := m.SandboxSessionFor(100, "bash_exec")
	if sess == nil {
		t.Fatal("SandboxSessionFor returned nil after Pre")
	}
	if sess.ContainerID != "mock-container-abc" {
		t.Errorf("stashed session ContainerID = %q; want mock-container-abc", sess.ContainerID)
	}
}

func TestSandboxHookManager_PreToolCall_NonBashExecSkipped(t *testing.T) {
	pool := &mockSandboxPool{}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)
	ctx := WithRunID(context.Background(), 100)

	_, err := m.preToolCall(ctx, &mockEinoTool{name: "kb_search"}, "")
	if err != nil {
		t.Fatalf("preToolCall err = %v", err)
	}
	if pool.borrowCount.Load() != 0 {
		t.Errorf("non-bash_exec triggered Borrow")
	}
	if len(storeM.createdRecords) != 0 {
		t.Errorf("non-bash_exec wrote audit row")
	}
}

func TestSandboxHookManager_PreToolCall_NoRunIDSkipped(t *testing.T) {
	pool := &mockSandboxPool{}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)
	// No WithRunID
	_, _ = m.preToolCall(context.Background(), &mockEinoTool{name: "bash_exec"}, "")
	if pool.borrowCount.Load() != 0 {
		t.Errorf("no runID still triggered Borrow")
	}
}

func TestSandboxHookManager_PreToolCall_PoolBorrowError(t *testing.T) {
	pool := &mockSandboxPool{borrowErr: errors.New("simulated borrow fail")}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)
	ctx := WithRunID(context.Background(), 100)

	action, err := m.preToolCall(ctx, &mockEinoTool{name: "bash_exec"}, "")
	if err != nil {
		t.Fatalf("preToolCall err = %v", err)
	}
	if action != HookActionContinue {
		t.Errorf("expected Continue on Borrow fail; got %d", action)
	}
	if len(storeM.createdRecords) != 0 {
		t.Errorf("audit row written despite Borrow fail")
	}
	if m.SandboxSessionFor(100, "bash_exec") != nil {
		t.Errorf("session stashed despite Borrow fail")
	}
}

func TestSandboxHookManager_PreToolCall_StoreCreateError(t *testing.T) {
	pool := &mockSandboxPool{}
	storeM := &mockSandboxStore{createErr: errors.New("simulated create fail")}
	m := NewSandboxHookManager(pool, storeM)
	ctx := WithRunID(context.Background(), 100)

	_, _ = m.preToolCall(ctx, &mockEinoTool{name: "bash_exec"}, "")
	// Pool should have been Return'd to avoid leak
	if pool.returnCount.Load() != 1 {
		t.Errorf("Return count = %d; want 1 (cleanup on Create fail)", pool.returnCount.Load())
	}
	// No session stashed
	if m.SandboxSessionFor(100, "bash_exec") != nil {
		t.Errorf("session stashed despite Create fail")
	}
}

func TestSandboxHookManager_PreToolCall_InfoError(t *testing.T) {
	pool := &mockSandboxPool{}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)
	ctx := WithRunID(context.Background(), 100)

	action, err := m.preToolCall(ctx, errInfoTool{}, "")
	if err != nil {
		t.Errorf("preToolCall err = %v; want nil", err)
	}
	if action != HookActionContinue {
		t.Errorf("action = %d; want Continue on Info error", action)
	}
}

// ===========================================================================
// PostToolCall tests
// ===========================================================================

func TestSandboxHookManager_PostToolCall_HappyPath(t *testing.T) {
	pool := &mockSandboxPool{}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)
	ctx := middleware.NewContextWithUserID(context.Background(), 30)
	ctx = WithRunID(ctx, 100)

	// Pre first to stash a borrow
	_, _ = m.preToolCall(ctx, &mockEinoTool{name: "bash_exec"}, "")
	if m.SandboxSessionFor(100, "bash_exec") == nil {
		t.Fatal("preconditions: session should be stashed")
	}

	action, err := m.postToolCall(ctx, &mockEinoTool{name: "bash_exec"}, `{"stdout":"hi"}`, nil)
	if err != nil {
		t.Fatalf("postToolCall err = %v", err)
	}
	if action != HookActionContinue {
		t.Errorf("action = %d; want Continue", action)
	}
	if pool.returnCount.Load() != 1 {
		t.Errorf("Return count = %d; want 1", pool.returnCount.Load())
	}
	if storeM.updateCalls.Load() != 1 {
		t.Errorf("UpdateState calls = %d; want 1", storeM.updateCalls.Load())
	}
	// Borrow entry should be removed
	if m.SandboxSessionFor(100, "bash_exec") != nil {
		t.Errorf("session still stashed after Post")
	}
}

func TestSandboxHookManager_PostToolCall_NoEntrySkipped(t *testing.T) {
	pool := &mockSandboxPool{}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)
	ctx := WithRunID(context.Background(), 100)

	// Post without prior Pre
	action, _ := m.postToolCall(ctx, &mockEinoTool{name: "bash_exec"}, "", nil)
	if action != HookActionContinue {
		t.Errorf("action = %d; want Continue", action)
	}
	if pool.returnCount.Load() != 0 {
		t.Errorf("Return called despite no prior Pre")
	}
	if storeM.updateCalls.Load() != 0 {
		t.Errorf("UpdateState called despite no prior Pre")
	}
}

func TestSandboxHookManager_PostToolCall_OnExecError(t *testing.T) {
	pool := &mockSandboxPool{}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)
	ctx := middleware.NewContextWithUserID(context.Background(), 30)
	ctx = WithRunID(ctx, 100)

	_, _ = m.preToolCall(ctx, &mockEinoTool{name: "bash_exec"}, "")

	execErr := errors.New("simulated exec failure")
	_, _ = m.postToolCall(ctx, &mockEinoTool{name: "bash_exec"}, "", execErr)
	if pool.returnCount.Load() != 1 {
		t.Errorf("Return not called on Exec error")
	}
	if storeM.updateCalls.Load() != 1 {
		t.Errorf("UpdateState not called on Exec error")
	}
}

func TestSandboxHookManager_PostToolCall_NonBashExecSkipped(t *testing.T) {
	pool := &mockSandboxPool{}
	storeM := &mockSandboxStore{}
	m := NewSandboxHookManager(pool, storeM)
	ctx := WithRunID(context.Background(), 100)

	_, _ = m.postToolCall(ctx, &mockEinoTool{name: "kb_search"}, "", nil)
	if pool.returnCount.Load() != 0 {
		t.Errorf("non-bash_exec triggered Return")
	}
}

// ===========================================================================
// AsRunHooks integration
// ===========================================================================

func TestSandboxHookManager_AsRunHooks_ReturnsBoundRunHooks(t *testing.T) {
	m := NewSandboxHookManager(&mockSandboxPool{}, &mockSandboxStore{})
	hooks := m.AsRunHooks()
	if hooks == nil {
		t.Fatal("AsRunHooks returned nil")
	}
	if hooks.PreToolCall == nil || hooks.PostToolCall == nil {
		t.Errorf("AsRunHooks fields nil")
	}
}

func TestSandboxHookManager_DockerClient_ReturnsPoolDC(t *testing.T) {
	dc := sandbox.NewMockDockerClient()
	pool := &mockSandboxPool{dc: dc}
	m := NewSandboxHookManager(pool, &mockSandboxStore{})
	if got := m.DockerClient(); got != dc {
		t.Errorf("DockerClient returned different dc")
	}
}

func TestSandboxHookManager_DockerClient_NilPool(t *testing.T) {
	var m *SandboxHookManager // nil receiver
	if got := m.DockerClient(); got != nil {
		t.Errorf("nil manager DockerClient should be nil")
	}
}
