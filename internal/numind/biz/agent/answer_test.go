package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"gorm.io/datatypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Answer-specific mocks (extend lifecycleRunStore concept with call tracking)
// ---------------------------------------------------------------------------

type answerRunStore struct {
	runs               map[uint64]*model.AgentRun
	nextID             uint64
	appendMessageCalls []uint64
	clearPendingCalls  []uint64
	updatePendingCalls []uint64
	appendMessageErr   error
	clearPendingErr    error
}

func newAnswerRunStore() *answerRunStore {
	return &answerRunStore{runs: make(map[uint64]*model.AgentRun)}
}

func (s *answerRunStore) Create(_ context.Context, run *model.AgentRun) error {
	s.nextID++
	run.ID = s.nextID
	cp := *run
	s.runs[run.ID] = &cp
	return nil
}
func (s *answerRunStore) Get(_ context.Context, id uint64) (*model.AgentRun, error) {
	r, ok := s.runs[id]
	if !ok {
		return nil, errors.New("record not found")
	}
	return r, nil
}
func (s *answerRunStore) UpdateState(_ context.Context, id uint64, status, reason string, _ *time.Time) error {
	r, ok := s.runs[id]
	if !ok {
		return errors.New("not found")
	}
	r.Status = status
	r.StateReason = reason
	return nil
}
func (s *answerRunStore) WriteTurn(_ context.Context, _ uint64, _ json.RawMessage) error {
	return nil
}
func (s *answerRunStore) ListBySession(_ context.Context, _ string, _, _ int) ([]model.AgentRun, int64, error) {
	return nil, 0, nil
}
func (s *answerRunStore) UpdateTerminalMetadata(_ context.Context, _ uint64, _ datatypes.JSON) error {
	return nil
}
func (s *answerRunStore) SetCancellationRequested(_ context.Context, _ uint64, _ datatypes.JSON) error {
	return nil
}
func (s *answerRunStore) ListByParentUserIDAndStatus(_ context.Context, _ uint, _ string, _, _ int) ([]model.AgentRun, int64, error) {
	return nil, 0, nil
}
func (s *answerRunStore) ListByUser(_ context.Context, _ uint, _ *time.Time, _ int) ([]model.AgentRun, error) {
	return nil, nil
}
func (s *answerRunStore) MergeTerminalMetadata(_ context.Context, _ uint64, _ map[string]interface{}) error {
	return nil
}
func (s *answerRunStore) UpdatePendingQuestion(_ context.Context, id uint64, _ []byte) error {
	s.updatePendingCalls = append(s.updatePendingCalls, id)
	return nil
}
func (s *answerRunStore) ClearPendingQuestion(_ context.Context, id uint64) error {
	s.clearPendingCalls = append(s.clearPendingCalls, id)
	return s.clearPendingErr
}
func (s *answerRunStore) AppendUserMessage(_ context.Context, id uint64, _ string) error {
	s.appendMessageCalls = append(s.appendMessageCalls, id)
	return s.appendMessageErr
}

// answerRunner is a no-op runner for Answer tests.
type answerRunner struct{ runCalled bool }

func (r *answerRunner) Run(_ context.Context, _ RunRequest) (*RunResult, error) {
	r.runCalled = true
	return &RunResult{}, nil
}
func (r *answerRunner) Cancel(_ uint64) bool { return false }

// newAnswerService creates a minimal StudentRunService wired for Answer tests.
func newAnswerService(rs *answerRunStore) *StudentRunService {
	return &StudentRunService{
		runner:     &answerRunner{},
		runStore:   rs,
		skillStore: nil, // not needed for Answer tests
	}
}

// seedAnswerRun seeds a run in waiting_for_user_choice state owned by userID.
func seedAnswerRun(rs *answerRunStore, userID uint, stateReason string) uint64 {
	run := &model.AgentRun{
		UserID:      userID,
		SessionID:   "sess-abc",
		Status:      "terminated",
		StateReason: stateReason,
		Messages:    datatypes.JSON(`[]`),
		StartedAt:   time.Now(),
	}
	_ = rs.Create(context.Background(), run)
	return run.ID
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestAnswer_HappyPath(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRun(rs, userID, string(TerminalWaitingForUserChoice))

	req := AnswerRequest{Selected: []string{"a"}, FreeText: "extra note"}
	resp, err := svc.Answer(context.Background(), userID, runID, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, runID, resp.RunID)
	assert.Equal(t, "resumed", resp.Status)

	// AppendUserMessage must have been called.
	assert.Contains(t, rs.appendMessageCalls, runID)
	// ClearPendingQuestion must have been called.
	assert.Contains(t, rs.clearPendingCalls, runID)
}

func TestAnswer_CrossUserRunNotFound(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	ownerID := uint(10)
	attackerID := uint(99)
	runID := seedAnswerRun(rs, ownerID, string(TerminalWaitingForUserChoice))

	req := AnswerRequest{Selected: []string{"a"}}
	_, err := svc.Answer(context.Background(), attackerID, runID, req)

	require.Error(t, err)
	// Should be 404 (run not found for attacker).
	var e *errno.Errno
	if errors.As(err, &e) {
		assert.Equal(t, 404, e.HTTP)
	}
}

func TestAnswer_RunNotWaiting(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	// Seed a run in "completed" state.
	runID := seedAnswerRun(rs, userID, "completed")

	req := AnswerRequest{Selected: []string{"a"}}
	_, err := svc.Answer(context.Background(), userID, runID, req)

	require.Error(t, err)
	var e *errno.Errno
	if errors.As(err, &e) {
		assert.Equal(t, 400, e.HTTP)
	}
}

func TestAnswer_RunNotFound(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)

	_, err := svc.Answer(context.Background(), 42, 9999, AnswerRequest{Selected: []string{"a"}})
	require.Error(t, err)
}

func TestAnswer_AppendMessageError(t *testing.T) {
	rs := newAnswerRunStore()
	rs.appendMessageErr = errors.New("db error")
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRun(rs, userID, string(TerminalWaitingForUserChoice))

	_, err := svc.Answer(context.Background(), userID, runID, AnswerRequest{Selected: []string{"a"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append message")
}

func TestBuildAnswerMessage(t *testing.T) {
	msg := buildAnswerMessage(AnswerRequest{Selected: []string{"a", "b"}, FreeText: "hello"})
	assert.Contains(t, msg, "[user answered]")
	assert.Contains(t, msg, `["a","b"]`)
	assert.Contains(t, msg, "hello")
}

func TestBuildAnswerMessage_NoFreeText(t *testing.T) {
	msg := buildAnswerMessage(AnswerRequest{Selected: []string{"x"}})
	assert.Contains(t, msg, "[user answered]")
	assert.NotContains(t, msg, "Free text")
}
