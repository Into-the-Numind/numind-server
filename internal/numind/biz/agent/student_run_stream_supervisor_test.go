package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/model"
)

type supervisedStreamRunner struct {
	events            []stream.Event
	runErr            error
	blockBeforeEvents <-chan struct{}
	panicBeforeEvents bool

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
	r.once.Do(func() { close(r.started) })
	defer close(r.done)

	if r.blockBeforeEvents != nil {
		select {
		case <-r.blockBeforeEvents:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
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
	var payload stream.TerminalPayload
	require.NoError(t, json.Unmarshal(event.Data, &payload))
	return payload.Reason
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
