package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"

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

	svc := NewStudentRunService(nil, nil, skillStore, nil, nil)
	resp, err := svc.Estimate(context.Background(), userID, EstimateRunRequest{
		AgentDefinitionID: adID,
		Message:           "Hello agent",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.EstimatedCredits <= 0 {
		t.Error("expected positive estimated_credits")
	}
	if resp.Currency != "credits" {
		t.Errorf("expected currency='credits', got '%s'", resp.Currency)
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

	svc := NewStudentRunService(nil, nil, skillStore, nil, nil)
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

	svc := NewStudentRunService(runner, runStore, nil, nil, nil)
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

	svc := NewStudentRunService(runner, runStore, nil, nil, nil)
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

	svc := NewStudentRunService(runner, runStore, nil, nil, nil)
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

	svc := NewStudentRunService(nil, runStore, nil, nil, nil)
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

	svc := NewStudentRunService(nil, runStore, nil, nil, nil)
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

	svc := NewStudentRunService(nil, runStore, nil, nil, nil)
	_, err := svc.ExtendBudget(context.Background(), uint(1), run.ID, ExtendBudgetRequest{AddCredits: 100})
	if err == nil {
		t.Fatal("expected ownership error")
	}
	if !errors.Is(err, errno.ErrAgentRunNotFound) {
		t.Errorf("expected ErrAgentRunNotFound, got %v", err)
	}
}
