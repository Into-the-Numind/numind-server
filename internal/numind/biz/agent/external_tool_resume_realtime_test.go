package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/model"
)

// recordingPostCardBroker is the browser-facing transport seam required by the
// customer regression: detached continuation events must be published instead
// of drained into the void after an external-action card.
type recordingPostCardBroker struct {
	mu     sync.Mutex
	events []stream.Event
	subs   int
}

func (b *recordingPostCardBroker) Publish(_ context.Context, _ uint64, event stream.Event) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	return "1-0", nil
}

func (b *recordingPostCardBroker) Subscribe(_ context.Context, _ uint64, _ string) (<-chan stream.PublishedEvent, error) {
	b.mu.Lock()
	b.subs++
	b.mu.Unlock()
	ch := make(chan stream.PublishedEvent)
	close(ch)
	return ch, nil
}

func TestSubscribeRunEvents_EnforcesOwnerBeforeBroker(t *testing.T) {
	broker := &recordingPostCardBroker{}
	runs := newLifecycleRunStore()
	run := &model.AgentRun{UserID: 7, SessionID: "owned-session"}
	require.NoError(t, runs.Create(context.Background(), run))
	service := NewStudentRunService(nil, runs, nil, nil, nil, nil).WithRunEventBroker(broker)

	_, err := service.SubscribeRunEvents(context.Background(), 8, run.ID, "")
	require.Error(t, err)
	broker.mu.Lock()
	assert.Zero(t, broker.subs, "cross-user request must be rejected before Redis")
	broker.mu.Unlock()

	_, err = service.SubscribeRunEvents(context.Background(), 7, run.ID, "")
	require.NoError(t, err)
	broker.mu.Lock()
	assert.Equal(t, 1, broker.subs)
	broker.mu.Unlock()
}

func TestSubscribeRunEvents_ReturnsPersistedTerminalWhenBrokerMissedCloseFrame(t *testing.T) {
	broker := &recordingPostCardBroker{}
	runs := newLifecycleRunStore()
	run := &model.AgentRun{
		UserID:      7,
		SessionID:   "persisted-terminal-session",
		Status:      "terminated",
		StateReason: string(TerminalCompleted),
	}
	require.NoError(t, runs.Create(context.Background(), run))
	service := NewStudentRunService(nil, runs, nil, nil, nil, nil).WithRunEventBroker(broker)

	events, err := service.SubscribeRunEvents(context.Background(), 7, run.ID, "9999-0")
	require.NoError(t, err)
	published, open := <-events
	require.True(t, open)
	assert.Empty(t, published.Cursor)
	assert.Equal(t, stream.EventTerminal, published.Event.Type)
	var payload stream.TerminalPayload
	require.NoError(t, json.Unmarshal(published.Event.Data, &payload))
	assert.Equal(t, string(TerminalCompleted), payload.Reason)
	broker.mu.Lock()
	assert.Zero(t, broker.subs, "persisted terminal must not wait on Redis")
	broker.mu.Unlock()
}

func TestSubscribeRunEvents_DurableExternalContinuationStatesStayLive(t *testing.T) {
	for _, tc := range []struct {
		name, status, reason string
	}{
		{name: "ready", status: "terminated", reason: "external_resume_ready"},
		{name: "leased", status: "running", reason: "ext_resume:lease-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broker := &recordingPostCardBroker{}
			runs := newLifecycleRunStore()
			run := &model.AgentRun{
				UserID:      7,
				SessionID:   "durable-continuation-" + tc.name,
				Status:      tc.status,
				StateReason: tc.reason,
			}
			require.NoError(t, runs.Create(context.Background(), run))
			service := NewStudentRunService(nil, runs, nil, nil, nil, nil).WithRunEventBroker(broker)

			_, err := service.SubscribeRunEvents(context.Background(), 7, run.ID, "")
			require.NoError(t, err)
			broker.mu.Lock()
			assert.Equal(t, 1, broker.subs, "durable continuation must attach to live broker")
			broker.mu.Unlock()
		})
	}
}

type postCardRealtimeRunner struct{}

func (*postCardRealtimeRunner) Run(context.Context, RunRequest) (*RunResult, error) {
	return nil, assert.AnError
}

func (*postCardRealtimeRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, assert.AnError
}

func (*postCardRealtimeRunner) RunExternalContinuationStream(
	_ context.Context,
	req RunRequest,
	ch chan<- stream.Event,
) (*RunResult, error) {
	types := []stream.EventType{
		stream.EventReasoningDelta,
		stream.EventTokenDelta,
		stream.EventToolCallStart,
		stream.EventTerminal,
	}
	for i, eventType := range types {
		event, err := stream.Encode(eventType, map[string]any{"index": i}, uint64(i+1), req.ExistingRunID, i)
		if err != nil {
			return nil, err
		}
		ch <- event
	}
	return &RunResult{AgentRunID: req.ExistingRunID, TerminalReason: TerminalCompleted}, nil
}

func (*postCardRealtimeRunner) Cancel(uint64) bool { return false }

func TestExternalToolResume_PublishesEveryDetachedStreamEvent(t *testing.T) {
	broker := &recordingPostCardBroker{}
	studentRuns := NewStudentRunService(&postCardRealtimeRunner{}, nil, nil, nil, nil, nil).
		WithRunEventBroker(broker)
	resumer := &AgentRunResumer{studentRuns: studentRuns}

	err := resumer.callRunner(context.Background(), RunRequest{UserID: 7, ExistingRunID: 283})
	require.NoError(t, err)

	broker.mu.Lock()
	defer broker.mu.Unlock()
	require.Len(t, broker.events, 4)
	assert.Equal(t, []stream.EventType{
		stream.EventReasoningDelta,
		stream.EventTokenDelta,
		stream.EventToolCallStart,
		stream.EventTerminal,
	}, []stream.EventType{
		broker.events[0].Type,
		broker.events[1].Type,
		broker.events[2].Type,
		broker.events[3].Type,
	})
}
