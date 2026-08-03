package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

type supervisedStreamRunner struct {
	events            []stream.Event
	runErr            error
	blockBeforeEvents <-chan struct{}
	panicBeforeEvents bool

	capturedReq      RunRequest
	capturedRunID    uint64
	ctxErrAfterBlock error
	ctxUserID        uint

	started chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newSupervisedStreamRunner(events []stream.Event, runErr error) *supervisedStreamRunner {
	return &supervisedStreamRunner{
		events:  events,
		runErr:  runErr,
		started: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (r *supervisedStreamRunner) Run(context.Context, RunRequest) (*RunResult, error) {
	return nil, errors.New("unexpected non-streaming run")
}

func (r *supervisedStreamRunner) RunStream(ctx context.Context, req RunRequest, runID uint64, ch chan<- stream.Event) (*RunResult, error) {
	r.capturedReq = req
	r.capturedRunID = runID
	if uid, ok := middleware.UserIDFromCtx(ctx); ok {
		r.ctxUserID = uid
	}
	r.once.Do(func() { close(r.started) })
	defer close(r.done)

	if r.blockBeforeEvents != nil {
		select {
		case <-r.blockBeforeEvents:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r.ctxErrAfterBlock = ctx.Err()
	if r.panicBeforeEvents {
		panic("supervised runner panic")
	}

	for _, event := range r.events {
		ch <- event
	}
	return &RunResult{
		AgentRunID:     runID,
		TerminalReason: TerminalCompleted,
	}, r.runErr
}

func (r *supervisedStreamRunner) Cancel(uint64) bool { return false }

type recordingSupervisorBroker struct {
	mu       sync.Mutex
	events   []stream.Event
	ctxErrs  []error
	failures []error
}

func newRecordingSupervisorBroker(failures ...error) *recordingSupervisorBroker {
	return &recordingSupervisorBroker{failures: failures}
}

func (b *recordingSupervisorBroker) Publish(ctx context.Context, _ uint64, event stream.Event) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.events = append(b.events, event)
	b.ctxErrs = append(b.ctxErrs, ctx.Err())
	callIndex := len(b.events) - 1
	if callIndex < len(b.failures) && b.failures[callIndex] != nil {
		return "", b.failures[callIndex]
	}
	return "1-0", nil
}

func (b *recordingSupervisorBroker) Subscribe(context.Context, uint64, string) (<-chan stream.PublishedEvent, error) {
	ch := make(chan stream.PublishedEvent)
	close(ch)
	return ch, nil
}

func (b *recordingSupervisorBroker) eventTypes() []stream.EventType {
	b.mu.Lock()
	defer b.mu.Unlock()
	types := make([]stream.EventType, 0, len(b.events))
	for _, event := range b.events {
		types = append(types, event.Type)
	}
	return types
}

func (b *recordingSupervisorBroker) contextErrs() []error {
	b.mu.Lock()
	defer b.mu.Unlock()
	errs := make([]error, len(b.ctxErrs))
	copy(errs, b.ctxErrs)
	return errs
}

func (b *recordingSupervisorBroker) eventsCopy() []stream.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	events := make([]stream.Event, len(b.events))
	copy(events, b.events)
	return events
}

func TestStartPreparedStreamRun_DrainsEventsWithoutSubscriber(t *testing.T) {
	const userID = uint(7)
	releaseRunner := make(chan struct{})
	runner := newSupervisedStreamRunner(nil, nil)
	broker := newRecordingSupervisorBroker()
	svc, prepared := newSupervisorServiceForTest(t, userID, runner, broker)
	runner.events = []stream.Event{
		mustSupervisorEvent(t, stream.EventTokenDelta, prepared.RunID, 1),
		mustSupervisorEvent(t, stream.EventAssistantMessage, prepared.RunID, 2),
		mustSupervisorEvent(t, stream.EventTerminal, prepared.RunID, 3),
	}
	runner.blockBeforeEvents = releaseRunner

	require.True(t, startPreparedRunWithin(t, svc, prepared))
	require.False(t, startPreparedRunWithin(t, svc, prepared), "duplicate starts for one run must be rejected")

	close(releaseRunner)
	waitSupervisorRunnerDone(t, runner)
	require.Eventually(t, func() bool {
		return len(broker.eventTypes()) == 3
	}, time.Second, 10*time.Millisecond)

	assert.Equal(t, []stream.EventType{
		stream.EventTokenDelta,
		stream.EventAssistantMessage,
		stream.EventTerminal,
	}, broker.eventTypes())
	require.Eventually(t, func() bool {
		return !svc.streamExecutions.IsActive(prepared.RunID)
	}, time.Second, 10*time.Millisecond)
}

func TestStartPreparedStreamRun_PublishUsesDetachedContext(t *testing.T) {
	const userID = uint(123)
	runner := newSupervisedStreamRunner(nil, nil)
	broker := newRecordingSupervisorBroker()
	runStore := newLifecycleRunStore()
	skillStore := newLifecycleSkillStore()
	skillStore.defs[1] = &model.AgentDefinition{ID: 1, ParentUserID: userID, IsActive: true}
	svc := NewStudentRunService(runner, runStore, skillStore, nil, nil, nil).
		WithRunEventBroker(broker)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	prepared, err := svc.PrepareStreamRun(requestCtx, userID, CreateRunRequest{
		AgentDefinitionID: 1,
		Message:           "hello",
	})
	require.NoError(t, err)
	cancelRequest()
	runner.events = []stream.Event{
		mustSupervisorEvent(t, stream.EventTerminal, prepared.RunID, 1),
	}

	require.True(t, startPreparedRunWithin(t, svc, prepared))
	waitSupervisorRunnerDone(t, runner)
	require.Eventually(t, func() bool {
		return len(broker.contextErrs()) == 1
	}, time.Second, 10*time.Millisecond)

	assert.NoError(t, broker.contextErrs()[0], "publish context must be detached from the canceled request context")
}

func TestStartPreparedAnswerStream_StartsSupervisedResumeAfterPersistingAnswer(t *testing.T) {
	const userID = uint(42)
	rs := newAnswerRunStore()
	runID := seedAnswerRun(rs, userID, string(TerminalWaitingForUserChoice))
	releaseRunner := make(chan struct{})
	runner := newSupervisedStreamRunner([]stream.Event{
		mustSupervisorEvent(t, stream.EventTokenDelta, runID, 1),
		mustSupervisorEvent(t, stream.EventTerminal, runID, 2),
	}, nil)
	runner.blockBeforeEvents = releaseRunner
	broker := newRecordingSupervisorBroker()
	svc := NewStudentRunService(runner, rs, nil, nil, nil, nil).
		WithRunEventBroker(broker)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	ok, err := svc.StartPreparedAnswerStream(requestCtx, userID, runID, AnswerRequest{
		Answers: map[string]AnswerItem{"Which region?": {Selected: []string{"北"}}},
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Contains(t, rs.answerAndClearCalls, runID, "answer must be persisted before returning success")
	require.True(t, svc.streamExecutions.IsActive(runID), "valid resume must enter the supervisor registry")

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("supervised answer resume runner did not start")
	}

	cancelRequest()
	close(releaseRunner)
	waitSupervisorRunnerDone(t, runner)
	require.Eventually(t, func() bool {
		return len(broker.eventTypes()) == 2 && !svc.streamExecutions.IsActive(runID)
	}, time.Second, 10*time.Millisecond)

	assert.Equal(t, runID, runner.capturedRunID)
	assert.Equal(t, runID, runner.capturedReq.ExistingRunID)
	assert.Contains(t, runner.capturedReq.Input, "用户已回答")
	assert.Equal(t, userID, runner.ctxUserID)
	assert.NoError(t, runner.ctxErrAfterBlock, "runner context must be detached from the canceled request context")
	assert.Equal(t, []stream.EventType{stream.EventTokenDelta, stream.EventTerminal}, broker.eventTypes())
	for _, ctxErr := range broker.contextErrs() {
		assert.NoError(t, ctxErr, "publisher context must survive caller cancellation")
	}
}

func TestStartPreparedAnswerStream_ActiveExecutionSkipsPersistenceAndRunner(t *testing.T) {
	const userID = uint(42)
	rs := newAnswerRunStore()
	runID := seedAnswerRun(rs, userID, string(TerminalWaitingForUserChoice))
	runner := newSupervisedStreamRunner(nil, nil)
	svc := NewStudentRunService(runner, rs, nil, nil, nil, nil)

	activeCtx, cancelActive := context.WithCancel(context.Background())
	activeDone := make(chan struct{})
	require.True(t, svc.streamExecutions.Start(runID, cancelActive, activeDone))
	t.Cleanup(func() {
		cancelActive()
		close(activeDone)
		svc.streamExecutions.Finish(runID)
	})

	ok, err := svc.StartPreparedAnswerStream(context.Background(), userID, runID, AnswerRequest{
		Answers: map[string]AnswerItem{"Which region?": {Selected: []string{"北"}}},
	})

	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, rs.answerAndClearCalls, "active duplicate answer stream must not persist the answer")
	assertStreamAnswerRunnerNotStarted(t, runner.started)
	assert.NoError(t, activeCtx.Err(), "preflight must not cancel the existing active execution")
}

func TestStartPreparedAnswerStream_ConcurrentDuplicateDoesNotPersistLoser(t *testing.T) {
	const userID = uint(42)
	rs := newAnswerRunStore()
	runID := seedAnswerRun(rs, userID, string(TerminalWaitingForUserChoice))
	releaseRunner := make(chan struct{})
	runner := newSupervisedStreamRunner([]stream.Event{
		mustSupervisorEvent(t, stream.EventTerminal, runID, 1),
	}, nil)
	runner.blockBeforeEvents = releaseRunner
	svc := NewStudentRunService(runner, rs, nil, nil, nil, nil)
	req := AnswerRequest{Answers: map[string]AnswerItem{
		"Which region?": {Selected: []string{"北"}},
	}}

	type answerStreamResult struct {
		ok  bool
		err error
	}
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	results := make(chan answerStreamResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			ready <- struct{}{}
			<-start
			ok, err := svc.StartPreparedAnswerStream(context.Background(), userID, runID, req)
			results <- answerStreamResult{ok: ok, err: err}
		}()
	}

	<-ready
	<-ready
	close(start)
	got := []answerStreamResult{<-results, <-results}

	okCount := 0
	falseNilCount := 0
	for _, res := range got {
		require.NoError(t, res.err)
		if res.ok {
			okCount++
			continue
		}
		falseNilCount++
	}
	assert.Equal(t, 1, okCount, "exactly one concurrent caller should admit the supervised resume")
	assert.Equal(t, 1, falseNilCount, "the duplicate caller should get ok=false, err=nil")
	assert.Len(t, rs.answerAndClearCalls, 1, "duplicate caller must not persist a second answer")

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("winning supervised answer resume runner did not start")
	}
	close(releaseRunner)
	waitSupervisorRunnerDone(t, runner)
}

func TestStartPreparedStreamRun_PublishFailureDoesNotStopRunner(t *testing.T) {
	const userID = uint(7)
	runner := newSupervisedStreamRunner(nil, nil)
	broker := newRecordingSupervisorBroker(errors.New("redis unavailable"))
	svc, prepared := newSupervisorServiceForTest(t, userID, runner, broker)
	runner.events = []stream.Event{
		mustSupervisorEvent(t, stream.EventTokenDelta, prepared.RunID, 1),
		mustSupervisorEvent(t, stream.EventTerminal, prepared.RunID, 2),
	}

	require.True(t, startPreparedRunWithin(t, svc, prepared))
	waitSupervisorRunnerDone(t, runner)
	require.Eventually(t, func() bool {
		return len(broker.eventTypes()) == 2
	}, time.Second, 10*time.Millisecond)

	assert.Equal(t, []stream.EventType{stream.EventTokenDelta, stream.EventTerminal}, broker.eventTypes())
}

func TestStartPreparedStreamRun_PublishesErrorWhenRunnerFailsBeforeTerminal(t *testing.T) {
	const userID = uint(7)
	runner := newSupervisedStreamRunner(nil, errors.New("runner exploded before terminal"))
	broker := newRecordingSupervisorBroker()
	svc, prepared := newSupervisorServiceForTest(t, userID, runner, broker)
	runner.events = []stream.Event{
		mustSupervisorEvent(t, stream.EventTokenDelta, prepared.RunID, 1),
	}

	require.True(t, startPreparedRunWithin(t, svc, prepared))
	waitSupervisorRunnerDone(t, runner)
	require.Eventually(t, func() bool {
		return len(broker.eventTypes()) == 3
	}, time.Second, 10*time.Millisecond)

	events := broker.eventsCopy()
	assert.Equal(t, []stream.EventType{stream.EventTokenDelta, stream.EventError, stream.EventTerminal}, supervisorEventTypes(events))
	assert.Equal(t, []uint64{1, 2, 3}, supervisorEventSeqs(events))
	assert.Equal(t, string(TerminalModelError), terminalReason(t, events[2]))
}

func TestStartPreparedStreamRun_CancelBeforeRunnerEmitsPersistsAbortedTerminal(t *testing.T) {
	const userID = uint(7)
	runStore := newLifecycleRunStore()
	run := &model.AgentRun{
		UserID:            userID,
		SessionID:         "cancel-before-runner",
		AgentDefinitionID: 1,
		Status:            "running",
	}
	require.NoError(t, runStore.Create(context.Background(), run))

	broker := newRecordingSupervisorBroker()
	svc := NewStudentRunService(nil, runStore, nil, nil, nil, nil).
		WithRunEventBroker(broker)
	runEntered := make(chan struct{})

	require.True(t, svc.startSupervisedRun(run.ID, userID, func(ctx context.Context, _ chan<- stream.Event) (*RunResult, error) {
		close(runEntered)
		<-ctx.Done()
		return nil, fmt.Errorf("pre-run load canceled: %w", ctx.Err())
	}))

	select {
	case <-runEntered:
	case <-time.After(time.Second):
		t.Fatal("supervised run closure did not start")
	}
	require.NoError(t, svc.Cancel(context.Background(), userID, run.ID))
	require.Eventually(t, func() bool {
		return len(broker.eventTypes()) == 1 && !svc.streamExecutions.IsActive(run.ID)
	}, time.Second, 10*time.Millisecond)

	updated, err := runStore.Get(context.Background(), run.ID)
	require.NoError(t, err)
	assert.Equal(t, "terminated", updated.Status)
	assert.Equal(t, string(TerminalAbortedStreaming), updated.StateReason)
	assert.NotNil(t, updated.EndedAt)

	events := broker.eventsCopy()
	assert.Equal(t, []stream.EventType{stream.EventTerminal}, supervisorEventTypes(events))
	payload := terminalPayload(t, events[0])
	assert.Equal(t, string(TerminalAbortedStreaming), payload.Reason)
	assert.Equal(t, UserFacingTerminalMessage(TerminalAbortedStreaming), payload.UserMessage)
}

func TestStartPreparedStreamRun_TerminalizesErrorOnlyRunnerFailure(t *testing.T) {
	const userID = uint(7)
	runner := newSupervisedStreamRunner(nil, errors.New("runner returned after error event"))
	broker := newRecordingSupervisorBroker()
	svc, prepared := newSupervisorServiceForTest(t, userID, runner, broker)
	runner.events = []stream.Event{
		mustSupervisorEvent(t, stream.EventError, prepared.RunID, 7),
	}

	require.True(t, startPreparedRunWithin(t, svc, prepared))
	waitSupervisorRunnerDone(t, runner)
	require.Eventually(t, func() bool {
		return len(broker.eventTypes()) == 2
	}, time.Second, 10*time.Millisecond)

	events := broker.eventsCopy()
	assert.Equal(t, []stream.EventType{stream.EventError, stream.EventTerminal}, supervisorEventTypes(events))
	assert.Equal(t, []uint64{7, 8}, supervisorEventSeqs(events))
	assert.Equal(t, string(TerminalModelError), terminalReason(t, events[1]))
}

func TestStartPreparedStreamRun_DoesNotPublishFallbackAfterTerminal(t *testing.T) {
	const userID = uint(7)
	runner := newSupervisedStreamRunner(nil, errors.New("finalize failed after terminal"))
	broker := newRecordingSupervisorBroker()
	svc, prepared := newSupervisorServiceForTest(t, userID, runner, broker)
	runner.events = []stream.Event{
		mustSupervisorEvent(t, stream.EventTerminal, prepared.RunID, 4),
	}

	require.True(t, startPreparedRunWithin(t, svc, prepared))
	waitSupervisorRunnerDone(t, runner)
	require.Eventually(t, func() bool {
		return !svc.streamExecutions.IsActive(prepared.RunID)
	}, time.Second, 10*time.Millisecond)

	events := broker.eventsCopy()
	assert.Equal(t, []stream.EventType{stream.EventTerminal}, supervisorEventTypes(events))
	assert.Equal(t, []uint64{4}, supervisorEventSeqs(events))
}

func TestStartPreparedStreamRun_PanicPublishesFallbackAndReleasesRegistry(t *testing.T) {
	const userID = uint(7)
	panicRunner := newSupervisedStreamRunner(nil, nil)
	panicRunner.panicBeforeEvents = true
	broker := newRecordingSupervisorBroker()
	svc, prepared := newSupervisorServiceForTest(t, userID, panicRunner, broker)

	require.True(t, startPreparedRunWithin(t, svc, prepared))
	waitSupervisorRunnerDone(t, panicRunner)
	require.Eventually(t, func() bool {
		return len(broker.eventTypes()) == 2 && !svc.streamExecutions.IsActive(prepared.RunID)
	}, time.Second, 10*time.Millisecond)

	events := broker.eventsCopy()
	assert.Equal(t, []stream.EventType{stream.EventError, stream.EventTerminal}, supervisorEventTypes(events))
	assert.Equal(t, []uint64{1, 2}, supervisorEventSeqs(events))
	assert.Equal(t, string(TerminalModelError), terminalReason(t, events[1]))

	restartRunner := newSupervisedStreamRunner([]stream.Event{
		mustSupervisorEvent(t, stream.EventTerminal, prepared.RunID, 3),
	}, nil)
	svc.runner = restartRunner
	require.True(t, startPreparedRunWithin(t, svc, prepared), "finished runs must be removed from the registry")
	waitSupervisorRunnerDone(t, restartRunner)
	require.Eventually(t, func() bool {
		return len(broker.eventTypes()) == 3 && !svc.streamExecutions.IsActive(prepared.RunID)
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, stream.EventTerminal, broker.eventsCopy()[2].Type)
}

func newSupervisorServiceForTest(
	t *testing.T,
	userID uint,
	runner AgentRunner,
	broker stream.RunEventBroker,
) (*StudentRunService, *PreparedStreamRun) {
	t.Helper()
	runStore := newLifecycleRunStore()
	run := &model.AgentRun{
		UserID:            userID,
		SessionID:         "supervised-session",
		AgentDefinitionID: 1,
		Status:            "running",
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	svc := NewStudentRunService(runner, runStore, nil, nil, nil, nil).
		WithRunEventBroker(broker)
	return svc, &PreparedStreamRun{
		RunID:     run.ID,
		SessionID: run.SessionID,
		UserID:    userID,
		Request: CreateRunRequest{
			AgentDefinitionID: run.AgentDefinitionID,
			SessionID:         run.SessionID,
			Message:           "hello",
		},
	}
}

func mustSupervisorEvent(t *testing.T, eventType stream.EventType, runID uint64, seq uint64) stream.Event {
	t.Helper()
	event, err := stream.Encode(eventType, map[string]any{"seq": seq}, seq, runID, 0)
	require.NoError(t, err)
	return event
}

func supervisorEventTypes(events []stream.Event) []stream.EventType {
	types := make([]stream.EventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func supervisorEventSeqs(events []stream.Event) []uint64 {
	seqs := make([]uint64, 0, len(events))
	for _, event := range events {
		seqs = append(seqs, event.Seq)
	}
	return seqs
}

func terminalReason(t *testing.T, event stream.Event) string {
	t.Helper()
	return terminalPayload(t, event).Reason
}

func terminalPayload(t *testing.T, event stream.Event) stream.TerminalPayload {
	t.Helper()
	var payload stream.TerminalPayload
	require.NoError(t, json.Unmarshal(event.Data, &payload))
	return payload
}

func startPreparedRunWithin(t *testing.T, svc *StudentRunService, prepared *PreparedStreamRun) bool {
	t.Helper()
	started := make(chan bool, 1)
	go func() {
		started <- svc.StartPreparedStreamRun(prepared)
	}()

	select {
	case ok := <-started:
		return ok
	case <-time.After(time.Second):
		t.Fatal("StartPreparedStreamRun did not return promptly")
		return false
	}
}

func waitSupervisorRunnerDone(t *testing.T, runner *supervisedStreamRunner) {
	t.Helper()
	select {
	case <-runner.done:
	case <-time.After(time.Second):
		t.Fatal("supervised runner did not finish")
	}
}
