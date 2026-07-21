package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/agent/stream"
)

// recordingPostCardBroker is the browser-facing transport seam required by the
// customer regression: detached continuation events must be published instead
// of drained into the void after an external-action card.
type recordingPostCardBroker struct {
	mu     sync.Mutex
	events []stream.Event
}

func (b *recordingPostCardBroker) Publish(_ context.Context, _ uint64, event stream.Event) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	return "1-0", nil
}

func (b *recordingPostCardBroker) Subscribe(_ context.Context, _ uint64, _ string) (<-chan stream.PublishedEvent, error) {
	return nil, nil
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
