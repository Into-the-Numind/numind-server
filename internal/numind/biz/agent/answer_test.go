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

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Answer-specific mocks (extend lifecycleRunStore concept with call tracking)
// ---------------------------------------------------------------------------

type answerRunStore struct {
	runs                map[uint64]*model.AgentRun
	nextID              uint64
	appendMessageCalls  []uint64
	clearPendingCalls   []uint64
	updatePendingCalls  []uint64
	answerAndClearCalls []uint64
	appendMessageErr    error
	clearPendingErr     error
	answerAndClearErr   error
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
func (s *answerRunStore) AnswerAndClear(_ context.Context, id uint64, _ string) error {
	s.answerAndClearCalls = append(s.answerAndClearCalls, id)
	return s.answerAndClearErr
}
func (s *answerRunStore) UpdateSessionPinned(_ context.Context, _ string, _ bool) error {
	return nil
}
func (s *answerRunStore) UpdateSessionName(_ context.Context, _ string, _ string) error {
	return nil
}
func (s *answerRunStore) UpdateSessionDeleted(_ context.Context, _ string, _ bool) error {
	return nil
}

// answerRunner is a no-op runner for Answer tests. It captures the RunRequest
// from the detached resume goroutine so tests can assert History injection.
type answerRunner struct {
	runCalled   bool
	capturedReq RunRequest
	runDone     chan struct{}
}

func (r *answerRunner) Run(_ context.Context, req RunRequest) (*RunResult, error) {
	r.runCalled = true
	r.capturedReq = req
	if r.runDone != nil {
		close(r.runDone)
	}
	return &RunResult{}, nil
}
func (r *answerRunner) RunStream(_ context.Context, _ RunRequest, _ uint64, _ chan<- stream.Event) (*RunResult, error) {
	return &RunResult{}, nil
}
func (r *answerRunner) Cancel(_ uint64) bool { return false }

// newAnswerService creates a minimal StudentRunService wired for Answer tests.
func newAnswerService(rs *answerRunStore) *StudentRunService {
	return newAnswerServiceWithRunner(rs, &answerRunner{})
}

func newAnswerServiceWithRunner(rs *answerRunStore, runner *answerRunner) *StudentRunService {
	return &StudentRunService{
		runner:     runner,
		runStore:   rs,
		skillStore: nil, // not needed for Answer tests
	}
}

// seedAnswerRunWithTranscript seeds a waiting run whose Messages carry a
// pre-yield ReAct transcript (the work the agent did before pausing).
func seedAnswerRunWithTranscript(rs *answerRunStore, userID uint, transcript string) uint64 {
	run := &model.AgentRun{
		UserID:              userID,
		SessionID:           "sess-resume",
		Status:              "terminated",
		StateReason:         string(TerminalWaitingForUserChoice),
		Messages:            datatypes.JSON(transcript),
		PendingQuestionJSON: datatypes.JSON(`{"question":"公司全称?","options":[{"key":"a","label":"我口述"},{"key":"b","label":"上传"}],"multi_select":false}`),
		StartedAt:           time.Now(),
	}
	_ = rs.Create(context.Background(), run)
	return run.ID
}

// seedAnswerRun seeds a run with the given stateReason owned by userID.
// For runs in waiting_for_user_choice state, PendingQuestionJSON is populated
// to satisfy the P2-1 consistency guard.
func seedAnswerRun(rs *answerRunStore, userID uint, stateReason string) uint64 {
	run := &model.AgentRun{
		UserID:      userID,
		SessionID:   "sess-abc",
		Status:      "terminated",
		StateReason: stateReason,
		Messages:    datatypes.JSON(`[]`),
		StartedAt:   time.Now(),
	}
	if stateReason == string(TerminalWaitingForUserChoice) {
		run.PendingQuestionJSON = datatypes.JSON(`{"question":"Which region?","options":[{"key":"a","label":"北"},{"key":"b","label":"南"}],"multi_select":false}`)
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

	// AnswerAndClear must have been called (atomic replace for AppendUserMessage + ClearPendingQuestion).
	assert.Contains(t, rs.answerAndClearCalls, runID)
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
	// Seed a run in "completed" state (no pending JSON).
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

func TestAnswer_NoPendingJSON(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	// Manually seed a run in waiting state but WITHOUT pending_question_json
	// to trigger the P2-1 consistency guard.
	run := &model.AgentRun{
		UserID:              userID,
		SessionID:           "sess-abc",
		Status:              "terminated",
		StateReason:         string(TerminalWaitingForUserChoice),
		Messages:            datatypes.JSON(`[]`),
		PendingQuestionJSON: nil, // deliberately empty — inconsistent state
		StartedAt:           time.Now(),
	}
	_ = rs.Create(context.Background(), run)

	_, err := svc.Answer(context.Background(), userID, run.ID, AnswerRequest{Selected: []string{"a"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no pending question json")
}

func TestAnswer_AnswerAndClearError(t *testing.T) {
	rs := newAnswerRunStore()
	rs.answerAndClearErr = errors.New("db error")
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRun(rs, userID, string(TerminalWaitingForUserChoice))

	_, err := svc.Answer(context.Background(), userID, runID, AnswerRequest{Selected: []string{"a"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "atomic answer+clear")
}

func TestBuildAnswerMessage(t *testing.T) {
	pendingJSON := []byte(`{"question":"Which region?","options":[{"key":"a","label":"北"}],"multi_select":false}`)
	msg := buildAnswerMessage(pendingJSON, []string{"a", "b"}, "hello")
	assert.Contains(t, msg, "[user answered]")
	assert.Contains(t, msg, "Which region?")
	assert.Contains(t, msg, `["a","b"]`)
	assert.Contains(t, msg, "hello")
}

func TestBuildAnswerMessage_NoFreeText(t *testing.T) {
	pendingJSON := []byte(`{"question":"Pick one","options":[{"key":"x","label":"X"}],"multi_select":false}`)
	msg := buildAnswerMessage(pendingJSON, []string{"x"}, "")
	assert.Contains(t, msg, "[user answered]")
	assert.Contains(t, msg, "Pick one")
	assert.NotContains(t, msg, "Free text")
}

func TestBuildAnswerMessage_UnparsableJSON(t *testing.T) {
	// Malformed JSON should not panic; fallback question text used.
	msg := buildAnswerMessage([]byte(`not-json`), []string{"a"}, "")
	assert.Contains(t, msg, "[user answered]")
	assert.Contains(t, msg, "<unparseable question>")
}

// test(qa): reproduce dev run #119 — after answering an ask_user_question pause,
// the resumed agent had ZERO memory of the research it did before pausing
// (loadSessionHistory excludes the current run, and the waiting run's transcript
// was never reloaded). The agent re-asked for the company name it had already
// been researching. Expected: the resume RunRequest carries the pre-yield
// transcript as History so the agent retains its prior work.
func TestAnswer_Resume_InjectsPriorTranscriptAsHistory(t *testing.T) {
	rs := newAnswerRunStore()
	runner := &answerRunner{runDone: make(chan struct{})}
	svc := newAnswerServiceWithRunner(rs, runner)
	userID := uint(7)
	transcript := `[{"role":"user","content":"为莫小派做小红书定位调研"},{"role":"assistant","content":"我先联网检索莫小派的公开信息"},{"role":"tool_group","tool_calls":[{"tool_name":"web_search"}]},{"role":"assistant","content":"已找到部分信息，需要确认创办初心"}]`
	runID := seedAnswerRunWithTranscript(rs, userID, transcript)

	_, err := svc.Answer(context.Background(), userID, runID, AnswerRequest{Selected: []string{"我口述"}, FreeText: "2020年创办，创始人是前MCN操盘手"})
	require.NoError(t, err)

	select {
	case <-runner.runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("resume runner goroutine did not run")
	}

	require.True(t, runner.runCalled)
	assert.Contains(t, runner.capturedReq.Input, "[user answered]", "the answer must become the resume Input")
	assert.Equal(t, runID, runner.capturedReq.ExistingRunID, "resume must target the same run")
	hist := runner.capturedReq.History
	require.NotEmpty(t, hist, "resumed agent must receive its pre-yield transcript as History (else it forgets all prior research)")

	// History must contain the original task + the agent's prior research steps.
	var joined string
	for _, m := range hist {
		joined += m.Content + "\n"
	}
	assert.Contains(t, joined, "为莫小派做小红书定位调研", "original task must survive into resume context")
	assert.Contains(t, joined, "已找到部分信息", "agent's prior research must survive into resume context")
}

// ask-question-freetext: free-text-only answers (no option selected) are valid.
func TestAnswer_FreeTextOnly_Succeeds(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRun(rs, userID, string(TerminalWaitingForUserChoice))

	resp, err := svc.Answer(context.Background(), userID, runID, AnswerRequest{Selected: nil, FreeText: "我们主要服务留学生，客单价3000"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "resumed", resp.Status)
}

// ask-question-freetext: empty selection AND empty free text is rejected.
func TestAnswer_EmptySelectionAndFreeText_Rejected(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRun(rs, userID, string(TerminalWaitingForUserChoice))

	_, err := svc.Answer(context.Background(), userID, runID, AnswerRequest{Selected: nil, FreeText: "   "})
	require.Error(t, err, "an answer with neither an option nor free text must be rejected")
}
