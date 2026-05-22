package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Mock implementations (prefixed "lifecycle" to avoid collision with runner_test.go)
// ---------------------------------------------------------------------------

// lifecycleRunner implements AgentRunner for lifecycle tests.
type lifecycleRunner struct {
	cancelCalled map[uint64]bool
	runResult    *RunResult
	runErr       error
}

func (m *lifecycleRunner) Run(_ context.Context, _ RunRequest) (*RunResult, error) {
	return m.runResult, m.runErr
}
func (m *lifecycleRunner) Cancel(runID uint64) bool {
	if m.cancelCalled == nil {
		m.cancelCalled = make(map[uint64]bool)
	}
	m.cancelCalled[runID] = true
	return true
}

// lifecycleRunStore implements store.IAgentRunStore for lifecycle tests.
type lifecycleRunStore struct {
	runs   map[uint64]*model.AgentRun
	nextID uint64
}

func newLifecycleRunStore() *lifecycleRunStore {
	return &lifecycleRunStore{runs: make(map[uint64]*model.AgentRun)}
}

func (s *lifecycleRunStore) Create(_ context.Context, run *model.AgentRun) error {
	s.nextID++
	run.ID = s.nextID
	s.runs[run.ID] = run
	return nil
}
func (s *lifecycleRunStore) Get(_ context.Context, id uint64) (*model.AgentRun, error) {
	r, ok := s.runs[id]
	if !ok {
		return nil, errors.New("record not found")
	}
	return r, nil
}
func (s *lifecycleRunStore) UpdateState(_ context.Context, id uint64, status, _ string, _ *time.Time) error {
	r, ok := s.runs[id]
	if !ok {
		return errors.New("not found")
	}
	r.Status = status
	return nil
}
func (s *lifecycleRunStore) WriteTurn(_ context.Context, _ uint64, _ json.RawMessage) error {
	return nil
}
func (s *lifecycleRunStore) ListBySession(_ context.Context, _ string, _, _ int) ([]model.AgentRun, int64, error) {
	return nil, 0, nil
}
func (s *lifecycleRunStore) UpdateTerminalMetadata(_ context.Context, id uint64, meta datatypes.JSON) error {
	r, ok := s.runs[id]
	if !ok {
		return errors.New("not found")
	}
	r.TerminalMetadata = meta
	return nil
}
func (s *lifecycleRunStore) SetCancellationRequested(_ context.Context, id uint64, _ datatypes.JSON) error {
	r, ok := s.runs[id]
	if !ok {
		return errors.New("not found")
	}
	now := time.Now()
	r.CancellationRequestedAt = &now
	return nil
}
func (s *lifecycleRunStore) ListByParentUserIDAndStatus(_ context.Context, _ uint, _ string, _, _ int) ([]model.AgentRun, int64, error) {
	return nil, 0, nil
}
func (s *lifecycleRunStore) ListByUser(_ context.Context, _ uint, _ *time.Time, _ int) ([]model.AgentRun, error) {
	return nil, nil
}
func (s *lifecycleRunStore) MergeTerminalMetadata(_ context.Context, _ uint64, _ map[string]interface{}) error {
	return nil
}
func (s *lifecycleRunStore) UpdatePendingQuestion(_ context.Context, id uint64, payloadJSON []byte) error {
	r, ok := s.runs[id]
	if !ok {
		return errors.New("not found")
	}
	r.StateReason = "waiting_for_user_choice"
	_ = payloadJSON
	return nil
}
func (s *lifecycleRunStore) ClearPendingQuestion(_ context.Context, id uint64) error {
	r, ok := s.runs[id]
	if !ok {
		return errors.New("not found")
	}
	r.StateReason = "running"
	r.PendingQuestionJSON = nil
	r.PendingQuestionAt = nil
	return nil
}
func (s *lifecycleRunStore) AppendUserMessage(_ context.Context, _ uint64, _ string) error {
	return nil
}

// AnswerAndClear — T4 reviewer-fix atomic answer flow mock impl.
func (s *lifecycleRunStore) AnswerAndClear(_ context.Context, _ uint64, _ string) error {
	return nil
}

// lifecycleSkillStore implements store.IAgentDefinitionStore for lifecycle tests.
type lifecycleSkillStore struct {
	defs map[uint64]*model.AgentDefinition
}

func newLifecycleSkillStore() *lifecycleSkillStore {
	return &lifecycleSkillStore{defs: make(map[uint64]*model.AgentDefinition)}
}

func (s *lifecycleSkillStore) GetByIDIncludeInactive(_ context.Context, id uint64) (*model.AgentDefinition, error) {
	d, ok := s.defs[id]
	if !ok {
		return nil, errors.New("record not found")
	}
	return d, nil
}
func (s *lifecycleSkillStore) Create(_ context.Context, _ *model.AgentDefinition) error { return nil }
func (s *lifecycleSkillStore) CreateTx(_ context.Context, _ *gorm.DB, _ *model.AgentDefinition) error {
	return nil
}
func (s *lifecycleSkillStore) GetByID(_ context.Context, id uint64) (*model.AgentDefinition, error) {
	return s.GetByIDIncludeInactive(context.Background(), id)
}
func (s *lifecycleSkillStore) ListByParent(_ context.Context, _ uint, _ bool, _, _ int) ([]model.AgentDefinition, int64, error) {
	return nil, 0, nil
}
func (s *lifecycleSkillStore) Update(_ context.Context, _ *model.AgentDefinition) error { return nil }
func (s *lifecycleSkillStore) UpdateTx(_ context.Context, _ *gorm.DB, _ *model.AgentDefinition) error {
	return nil
}
func (s *lifecycleSkillStore) SoftDelete(_ context.Context, _ uint64) error { return nil }
func (s *lifecycleSkillStore) SoftDeleteTx(_ context.Context, _ *gorm.DB, _ uint64) error {
	return nil
}
func (s *lifecycleSkillStore) WriteHistory(_ context.Context, _ *model.AgentDefinitionHistory) error {
	return nil
}
func (s *lifecycleSkillStore) WriteHistoryTx(_ context.Context, _ *gorm.DB, _ *model.AgentDefinitionHistory) error {
	return nil
}
func (s *lifecycleSkillStore) ListHistory(_ context.Context, _ uint64) ([]model.AgentDefinitionHistory, error) {
	return nil, nil
}
func (s *lifecycleSkillStore) GetHistoryByVersion(_ context.Context, _ uint64, _ uint) (*model.AgentDefinitionHistory, error) {
	return nil, nil
}
func (s *lifecycleSkillStore) MaxVersion(_ context.Context, _ uint64) (uint, error) { return 0, nil }

// ---------------------------------------------------------------------------
// Estimate tests
// ---------------------------------------------------------------------------

func TestStudentRunService_Estimate_HappyPath(t *testing.T) {
	skillStore := newLifecycleSkillStore()
	userID := uint(10)
	adID := uint64(1)
	skillStore.defs[adID] = &model.AgentDefinition{
		ID:           adID,
		ParentUserID: userID,
		IsActive:     true,
	}

	svc := NewStudentRunService(nil, nil, skillStore, nil, nil, nil)
	resp, err := svc.Estimate(context.Background(), userID, EstimateRunRequest{
		AgentDefinitionID: adID,
		Message:           "Hello agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Min <= 0 || resp.Max < resp.Min {
		t.Errorf("expected positive min/max (max>=min), got min=%d max=%d", resp.Min, resp.Max)
	}
}

func TestStudentRunService_Estimate_WrongOwner(t *testing.T) {
	skillStore := newLifecycleSkillStore()
	adID := uint64(2)
	skillStore.defs[adID] = &model.AgentDefinition{
		ID:           adID,
		ParentUserID: 999, // different user
		IsActive:     true,
	}

	svc := NewStudentRunService(nil, nil, skillStore, nil, nil, nil)
	_, err := svc.Estimate(context.Background(), uint(10), EstimateRunRequest{
		AgentDefinitionID: adID,
		Message:           "test",
	})
	if err == nil {
		t.Fatal("expected error for wrong owner")
	}
	if !errors.Is(err, errno.ErrSkillNotFound) {
		t.Errorf("expected ErrSkillNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cancel tests
// ---------------------------------------------------------------------------

func TestStudentRunService_Cancel_HappyPath(t *testing.T) {
	runner := &lifecycleRunner{}
	runStore := newLifecycleRunStore()

	userID := uint(5)
	run := &model.AgentRun{UserID: userID, Status: "running"}
	if err := runStore.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	svc := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	if err := svc.Cancel(context.Background(), userID, run.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !runner.cancelCalled[run.ID] {
		t.Error("Cancel was not forwarded to runner")
	}
}

func TestStudentRunService_Cancel_WrongOwner(t *testing.T) {
	runner := &lifecycleRunner{}
	runStore := newLifecycleRunStore()

	run := &model.AgentRun{UserID: 999, Status: "running"}
	if err := runStore.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	svc := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	err := svc.Cancel(context.Background(), uint(1), run.ID)
	if err == nil {
		t.Fatal("expected error for wrong owner")
	}
	if !errors.Is(err, errno.ErrAgentRunNotFound) {
		t.Errorf("expected ErrAgentRunNotFound, got %v", err)
	}
}

func TestStudentRunService_Cancel_AlreadyTerminal(t *testing.T) {
	runner := &lifecycleRunner{}
	runStore := newLifecycleRunStore()

	userID := uint(5)
	run := &model.AgentRun{UserID: userID, Status: "terminated"}
	if err := runStore.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	svc := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	err := svc.Cancel(context.Background(), userID, run.ID)
	if err == nil {
		t.Fatal("expected error for terminal run")
	}
	if !errors.Is(err, errno.ErrAgentRunNotCancellable) {
		t.Errorf("expected ErrAgentRunNotCancellable, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// ExtendBudget tests
// ---------------------------------------------------------------------------

func TestStudentRunService_ExtendBudget_HappyPath(t *testing.T) {
	runStore := newLifecycleRunStore()
	userID := uint(7)
	run := &model.AgentRun{UserID: userID, Status: "terminated", StateReason: "budget_exceeded"}
	if err := runStore.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	svc := NewStudentRunService(nil, runStore, nil, nil, nil, nil)
	updated, err := svc.ExtendBudget(context.Background(), userID, run.ID, ExtendBudgetRequest{AddCredits: 100})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.TerminalMetadata == nil {
		t.Error("expected terminal_metadata to be written")
	}
}

func TestStudentRunService_ExtendBudget_NotTerminal(t *testing.T) {
	runStore := newLifecycleRunStore()
	userID := uint(7)
	run := &model.AgentRun{UserID: userID, Status: "running"}
	if err := runStore.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	svc := NewStudentRunService(nil, runStore, nil, nil, nil, nil)
	_, err := svc.ExtendBudget(context.Background(), userID, run.ID, ExtendBudgetRequest{AddCredits: 100})
	if err == nil {
		t.Fatal("expected error for non-terminal run")
	}
}

func TestStudentRunService_ExtendBudget_WrongOwner(t *testing.T) {
	runStore := newLifecycleRunStore()
	run := &model.AgentRun{UserID: 999, Status: "terminated"}
	if err := runStore.Create(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	svc := NewStudentRunService(nil, runStore, nil, nil, nil, nil)
	_, err := svc.ExtendBudget(context.Background(), uint(1), run.ID, ExtendBudgetRequest{AddCredits: 100})
	if err == nil {
		t.Fatal("expected ownership error")
	}
	if !errors.Is(err, errno.ErrAgentRunNotFound) {
		t.Errorf("expected ErrAgentRunNotFound, got %v", err)
	}
}

// TestStudentRunService_forwardNarration_BridgeProviderToBuffer is the
// regression for the hotfix narration-buffer-bridge. Before the fix,
// Provider.Emit pushed events to an in-memory channel that nobody read,
// the parallel NarrationBuffer (which PollNarration queries) stayed empty
// forever, and the learner UI showed no tool-call narration despite tools
// actually firing. This test seeds a real Provider, spawns the forwarder
// goroutine the way Create does, emits a few events on the provider's
// behalf, then verifies the buffer surfaces them via QuerySince.
func TestStudentRunService_forwardNarration_BridgeProviderToBuffer(t *testing.T) {
	// Minimal YAML covering the two tools this test emits for. A real provider
	// is needed end-to-end (the goroutine subscribes on a real channel).
	yaml := []byte(`tools:
  web_search:
    verb: "正在搜索"
    detail_template: "网络"
    use_template: "{{ .verb }} {{ .detail }}"
    result_template: "搜索完成"
    error_template: "搜索失败"
    rejected_template: "搜索被拦截"
defaults:
  verb: "正在执行"
  detail_template: "{{ .ToolName }}"
  use_template: "{{ .verb }}"
  result_template: "执行完成"
  error_template: "执行失败"
  rejected_template: "执行被拦截"
`)
	prov, err := narration.NewProvider(narration.Config{
		YAMLBytes:  yaml,
		BufferSize: 8,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	buf := NewNarrationBuffer(16, 5*time.Minute)
	svc := NewStudentRunService(nil, nil, nil, nil, prov, buf)

	runID := uint64(4242)
	done := make(chan struct{})
	go func() {
		svc.forwardNarration(runID)
		close(done)
	}()

	// Emit two events on the provider; the forwarder should AppendEvent both
	// into the buffer.
	prov.Emit(context.Background(), runID, "web_search", narration.StateUse, narration.EmitPayload{})
	prov.Emit(context.Background(), runID, "web_search", narration.StateResult, narration.EmitPayload{})

	// Give the forwarder a moment to drain the channel. The events are
	// already in the channel buffer (sized 8 in this test), so the goroutine
	// scheduler is the only thing we are racing against.
	deadline := time.Now().Add(500 * time.Millisecond)
	var got []*narration.Event
	for time.Now().Before(deadline) {
		got = buf.QuerySince(runID, time.Time{})
		if len(got) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if len(got) != 2 {
		t.Fatalf("buffer.QuerySince: got %d events, want 2 (forwarder did not drain channel)", len(got))
	}
	if got[0].State != narration.StateUse {
		t.Errorf("first event State: got %v, want StateUse", got[0].State)
	}
	if got[1].State != narration.StateResult {
		t.Errorf("second event State: got %v, want StateResult", got[1].State)
	}

	// Closing the run unblocks the forwarder so the goroutine exits cleanly.
	prov.CloseRun(runID)
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("forwarder did not exit after CloseRun (channel close did not propagate)")
	}
}

// TestStudentRunService_forwardNarration_NilProviderIsNoop guards the
// graceful-degrade path: if Provider init failed earlier (yaml missing),
// the service still functions, the bridge just becomes a no-op.
func TestStudentRunService_forwardNarration_NilProviderIsNoop(t *testing.T) {
	buf := NewNarrationBuffer(16, time.Minute)
	svc := NewStudentRunService(nil, nil, nil, nil, nil, buf)
	// Must return immediately — no goroutine to block on. If forwardNarration
	// blocked, this test would hang.
	svc.forwardNarration(1)
	if got := buf.QuerySince(1, time.Time{}); len(got) != 0 {
		t.Errorf("buffer should be empty when provider is nil; got %d events", len(got))
	}
}
