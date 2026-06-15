package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// streamAnswerRunner captures the RunStream call (request + channel) so tests
// can assert AnswerStream threads the resume RunRequest through to runner.RunStream
// and that streamed events reach the caller's channel. Run() is a no-op fallback.
type streamAnswerRunner struct {
	streamCalled bool
	capturedReq  RunRequest
	emit         []stream.Event
	runStreamErr error
}

func (r *streamAnswerRunner) Run(_ context.Context, _ RunRequest) (*RunResult, error) {
	return &RunResult{}, nil
}
func (r *streamAnswerRunner) RunStream(_ context.Context, req RunRequest, _ uint64, ch chan<- stream.Event) (*RunResult, error) {
	r.streamCalled = true
	r.capturedReq = req
	for _, ev := range r.emit {
		ch <- ev
	}
	return &RunResult{}, r.runStreamErr
}
func (r *streamAnswerRunner) Cancel(_ uint64) bool { return false }

func newStreamAnswerService(rs *answerRunStore, runner *streamAnswerRunner) *StudentRunService {
	return &StudentRunService{
		runner:     runner,
		runStore:   rs,
		skillStore: nil,
		streamLock: stream.NewSubscriptionLock(),
	}
}

// TestAnswerStream_HappyPath_DrivesRunStream verifies AnswerStream validates +
// persists the answer (shared helper) and then drives runner.RunStream with the
// resume RunRequest (ExistingRunID + the answer as Input), streaming events onto
// the caller's channel — the issue4 fix (streamed assistant prose on resume).
func TestAnswerStream_HappyPath_DrivesRunStream(t *testing.T) {
	rs := newAnswerRunStore()
	runner := &streamAnswerRunner{
		emit: []stream.Event{
			{Type: stream.EventTokenDelta, Seq: 1, RunID: 1},
			{Type: stream.EventTerminal, Seq: 2, RunID: 1},
		},
	}
	svc := newStreamAnswerService(rs, runner)
	userID := uint(42)
	runID := seedAnswerRun(rs, userID, string(TerminalWaitingForUserChoice))

	ch := make(chan stream.Event, 8)
	res, err := svc.AnswerStream(context.Background(), userID, runID,
		AnswerRequest{Answers: map[string]AnswerItem{"Which region?": {Selected: []string{"北"}}}}, ch)
	require.NoError(t, err)
	require.NotNil(t, res)

	require.True(t, runner.streamCalled, "AnswerStream must drive runner.RunStream")
	assert.Equal(t, runID, runner.capturedReq.ExistingRunID, "resume must target the same run")
	assert.Contains(t, runner.capturedReq.Input, "用户已回答", "the answer becomes the resume Input")
	assert.True(t, runner.capturedReq.EnableMemory, "resume must keep EnableMemory")

	// AnswerAndClear must have been called (atomic answer turn + clear pending).
	assert.Contains(t, rs.answerAndClearCalls, runID)

	// Streamed events reached the caller's channel.
	collected := drainStreamEvents(ch, 2)
	require.Len(t, collected, 2)
	assert.Equal(t, stream.EventTerminal, collected[len(collected)-1].Type)
}

// TestAnswerStream_NotWaiting_Rejected confirms AnswerStream reuses the same
// validation as Answer: a non-waiting run is rejected (400), and runner.RunStream
// is never driven.
func TestAnswerStream_NotWaiting_Rejected(t *testing.T) {
	rs := newAnswerRunStore()
	runner := &streamAnswerRunner{}
	svc := newStreamAnswerService(rs, runner)
	userID := uint(42)
	runID := seedAnswerRun(rs, userID, "completed")

	ch := make(chan stream.Event, 1)
	_, err := svc.AnswerStream(context.Background(), userID, runID,
		AnswerRequest{Answers: map[string]AnswerItem{"Which region?": {Selected: []string{"北"}}}}, ch)
	require.Error(t, err)
	var e *errno.Errno
	if errors.As(err, &e) {
		assert.Equal(t, 400, e.HTTP)
	}
	assert.False(t, runner.streamCalled, "validation failure must not drive RunStream")
}

// TestAnswerStream_CrossUser_NotFound confirms a cross-user caller gets 404 and
// RunStream is never driven (ownership check shared with Answer).
func TestAnswerStream_CrossUser_NotFound(t *testing.T) {
	rs := newAnswerRunStore()
	runner := &streamAnswerRunner{}
	svc := newStreamAnswerService(rs, runner)
	runID := seedAnswerRun(rs, 10, string(TerminalWaitingForUserChoice))

	ch := make(chan stream.Event, 1)
	_, err := svc.AnswerStream(context.Background(), 99, runID,
		AnswerRequest{Answers: map[string]AnswerItem{"Which region?": {Selected: []string{"北"}}}}, ch)
	require.Error(t, err)
	var e *errno.Errno
	if errors.As(err, &e) {
		assert.Equal(t, 404, e.HTTP)
	}
	assert.False(t, runner.streamCalled)
}

// TestAcquireResumeStreamLock_SingleSubscriber confirms the resume lock admits
// one subscriber per run and a second attempt is denied until Release.
func TestAcquireResumeStreamLock_SingleSubscriber(t *testing.T) {
	svc := newStreamAnswerService(newAnswerRunStore(), &streamAnswerRunner{})
	const runID uint64 = 555
	assert.True(t, svc.AcquireResumeStreamLock(runID), "first acquire succeeds")
	assert.False(t, svc.AcquireResumeStreamLock(runID), "second acquire denied while held")
	svc.ReleaseStreamLock(runID)
	assert.True(t, svc.AcquireResumeStreamLock(runID), "acquire succeeds again after release")
	svc.ReleaseStreamLock(runID)
}

// TestAnswerStream_PriorMessagesPreserved is a guard for constraint (A): the
// streaming resume must not clobber leg 1. A waiting run whose Messages already
// carry [leg1 turns…] resumes, and finalize merges the resumed leg onto the
// prior transcript. Here we assert AnswerAndClear (which appends the answer turn,
// leaving run.Messages non-empty for RunStream to capture as priorMessages) ran
// and the resume RunRequest carried the prior transcript as History.
func TestAnswerStream_PriorTranscriptInHistory(t *testing.T) {
	rs := newAnswerRunStore()
	runner := &streamAnswerRunner{}
	svc := newStreamAnswerService(rs, runner)
	userID := uint(7)
	transcript := `[{"role":"user","content":"为莫小派做调研"},{"role":"assistant","content":"我先联网检索"}]`
	run := &model.AgentRun{
		UserID:              userID,
		SessionID:           "sess-stream-resume",
		Status:              "terminated",
		StateReason:         string(TerminalWaitingForUserChoice),
		Messages:            datatypes.JSON(transcript),
		PendingQuestionJSON: datatypes.JSON(`{"question":"公司全称?","options":[{"key":"a","label":"我口述"}],"multi_select":false}`),
		StartedAt:           time.Now(),
	}
	_ = rs.Create(context.Background(), run)

	ch := make(chan stream.Event, 1)
	_, err := svc.AnswerStream(context.Background(), userID, run.ID,
		AnswerRequest{Answers: map[string]AnswerItem{"公司全称?": {Selected: []string{"我口述"}}}}, ch)
	require.NoError(t, err)
	require.True(t, runner.streamCalled)

	var joined string
	for _, m := range runner.capturedReq.History {
		joined += m.Content + "\n"
	}
	assert.Contains(t, joined, "为莫小派做调研", "prior task must survive into streamed resume context")
	assert.Contains(t, joined, "我先联网检索", "prior research must survive into streamed resume context")
}

// drainEvents reads up to n events from ch (non-blocking after the producer
// closes / pauses) for assertions.
func drainStreamEvents(ch <-chan stream.Event, n int) []stream.Event {
	out := make([]stream.Event, 0, n)
	for i := 0; i < n; i++ {
		select {
		case ev := <-ch:
			out = append(out, ev)
		case <-time.After(time.Second):
			return out
		}
	}
	return out
}
