package stream

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunEventCursorValidationAndOrdering(t *testing.T) {
	valid := []string{"0-0", "1-0", "18446744073709551615-99"}
	for _, cursor := range valid {
		if !validRunEventCursor(cursor) {
			t.Fatalf("expected valid cursor %q", cursor)
		}
	}
	invalid := []string{"", "1", "1-", "-1", "1-2-3", "a-1", "1.0-2"}
	for _, cursor := range invalid {
		if validRunEventCursor(cursor) {
			t.Fatalf("expected invalid cursor %q", cursor)
		}
	}

	cases := []struct {
		a, b string
		want int
	}{
		{"1-0", "1-1", -1},
		{"9-99", "10-0", -1},
		{"18446744073709551615-1", "18446744073709551615-0", 1},
		{"42-7", "42-7", 0},
	}
	for _, tc := range cases {
		got := compareRunEventCursor(tc.a, tc.b)
		if got != tc.want {
			t.Fatalf("compare(%q,%q)=%d want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func newTestRunEventBroker(t *testing.T) (*RedisRunEventBroker, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	broker := NewRedisRunEventBroker(client)
	t.Cleanup(func() {
		_ = broker.Close()
		_ = client.Close()
	})
	return broker, server
}

func testRunEvent(t *testing.T, runID, seq uint64, eventType EventType) Event {
	t.Helper()
	event, err := Encode(eventType, map[string]any{"seq": seq}, seq, runID, 0)
	require.NoError(t, err)
	return event
}

func receivePublishedEvent(t *testing.T, events <-chan PublishedEvent) PublishedEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
		return PublishedEvent{}
	}
}

func TestRedisRunEventBroker_AtomicPublishReplayAndTTL(t *testing.T) {
	broker, server := newTestRunEventBroker(t)
	const runID = uint64(283)

	first := testRunEvent(t, runID, 1, EventReasoningDelta)
	cursor, err := broker.Publish(context.Background(), runID, first)
	require.NoError(t, err)
	require.True(t, validRunEventCursor(cursor))
	assert.Greater(t, server.TTL(runEventStreamKey(runID)), 23*time.Hour)

	events, err := broker.Subscribe(context.Background(), runID, "0-0")
	require.NoError(t, err)
	replayed := receivePublishedEvent(t, events)
	assert.Equal(t, cursor, replayed.Cursor)
	assert.Equal(t, EventReasoningDelta, replayed.Event.Type)
}

func TestRedisRunEventBroker_FansOutWithoutConsumerCompetition(t *testing.T) {
	broker, _ := newTestRunEventBroker(t)
	const runID = uint64(284)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	left, err := broker.Subscribe(ctx, runID, "")
	require.NoError(t, err)
	right, err := broker.Subscribe(ctx, runID, "")
	require.NoError(t, err)

	cursor, err := broker.Publish(ctx, runID, testRunEvent(t, runID, 1, EventTokenDelta))
	require.NoError(t, err)
	assert.Equal(t, cursor, receivePublishedEvent(t, left).Cursor)
	assert.Equal(t, cursor, receivePublishedEvent(t, right).Cursor)
}

func TestRedisRunEventBroker_PauseBaselineReplaysOnlyContinuationEvents(t *testing.T) {
	broker, _ := newTestRunEventBroker(t)
	const runID = uint64(291)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := broker.Publish(ctx, runID, testRunEvent(t, runID, 1, EventTokenDelta))
	require.NoError(t, err)
	waiting, err := Encode(EventTerminal, TerminalPayload{Reason: "waiting_for_user_choice"}, 2, runID, 0)
	require.NoError(t, err)
	_, err = broker.Publish(ctx, runID, waiting)
	require.NoError(t, err)
	continuationCursor, err := broker.Publish(ctx, runID, testRunEvent(t, runID, 3, EventReasoningDelta))
	require.NoError(t, err)

	events, err := broker.Subscribe(ctx, runID, runEventAfterPause)
	require.NoError(t, err)
	received := receivePublishedEvent(t, events)
	assert.Equal(t, continuationCursor, received.Cursor)
	assert.Equal(t, EventReasoningDelta, received.Event.Type)
	assert.Equal(t, uint64(3), received.Event.Seq)
}

func TestRedisRunEventBroker_CancelRemovesLocalSubscriber(t *testing.T) {
	broker, _ := newTestRunEventBroker(t)
	const runID = uint64(285)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := broker.Subscribe(ctx, runID, "")
	require.NoError(t, err)
	cancel()

	require.Eventually(t, func() bool {
		broker.mu.RLock()
		defer broker.mu.RUnlock()
		return len(broker.subscribers[runID]) == 0
	}, time.Second, 10*time.Millisecond)
}

func TestRedisRunEventBroker_UnavailablePublishFailsFastAndTripsCircuit(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{
		Addr:       "127.0.0.1:1",
		MaxRetries: -1,
	})
	t.Cleanup(func() { _ = client.Close() })
	broker := NewRedisRunEventBroker(client)
	t.Cleanup(func() { _ = broker.Close() })
	assert.True(t, broker.commandClient.Options().ContextTimeoutEnabled)
	assert.Equal(t, runEventCommandTimeout, broker.commandClient.Options().DialTimeout)
	assert.Equal(t, runEventCommandTimeout, broker.commandClient.Options().ReadTimeout)
	assert.Equal(t, runEventCommandTimeout, broker.commandClient.Options().WriteTimeout)
	const runID = uint64(286)
	event := testRunEvent(t, runID, 1, EventTokenDelta)

	started := time.Now()
	_, err := broker.Publish(context.Background(), runID, event)
	require.ErrorIs(t, err, ErrRunEventBrokerUnavailable)
	assert.Less(t, time.Since(started), 500*time.Millisecond)

	started = time.Now()
	for i := 0; i < 100; i++ {
		_, err = broker.Publish(context.Background(), runID, event)
		require.ErrorIs(t, err, ErrRunEventBrokerUnavailable)
	}
	assert.Less(t, time.Since(started), 100*time.Millisecond)
}

func TestRedisRunEventBroker_FailureDisconnectsSubscriberAndRetriesTerminal(t *testing.T) {
	broker, server := newTestRunEventBroker(t)
	const runID = uint64(287)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := broker.Subscribe(ctx, runID, "")
	require.NoError(t, err)

	addr := server.Addr()
	server.Close()
	_, err = broker.Publish(ctx, runID, testRunEvent(t, runID, 1, EventTokenDelta))
	require.ErrorIs(t, err, ErrRunEventBrokerUnavailable)
	select {
	case _, open := <-events:
		assert.False(t, open, "transport failure must close SSE so the browser can fall back")
	case <-time.After(time.Second):
		t.Fatal("subscriber remained open after transport failure")
	}

	terminal := testRunEvent(t, runID, 2, EventTerminal)
	_, err = broker.Publish(ctx, runID, terminal)
	require.ErrorIs(t, err, ErrRunEventBrokerUnavailable, "terminal must try even while the circuit is open")

	require.NoError(t, server.StartAddr(addr))
	replayed, err := broker.Subscribe(ctx, runID, "")
	require.NoError(t, err)
	received := receivePublishedEvent(t, replayed)
	assert.Equal(t, EventTerminal, received.Event.Type)
	require.True(t, validRunEventCursor(received.Cursor))
}

func TestRedisRunEventBroker_WaitingTerminalIsNotRetriedAheadOfFinal(t *testing.T) {
	broker, server := newTestRunEventBroker(t)
	const runID = uint64(290)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := server.Addr()
	server.Close()

	waiting, err := Encode(EventTerminal, TerminalPayload{Reason: "waiting_for_user_choice"}, 1, runID, 0)
	require.NoError(t, err)
	_, err = broker.Publish(ctx, runID, waiting)
	require.ErrorIs(t, err, ErrRunEventBrokerUnavailable)
	_, waitingRetryExists := broker.criticalRetries.Load(runID)
	assert.False(t, waitingRetryExists, "pause markers must preserve their original Stream order")
	completed, err := Encode(EventTerminal, TerminalPayload{Reason: "completed"}, 2, runID, 0)
	require.NoError(t, err)
	_, err = broker.Publish(ctx, runID, completed)
	require.ErrorIs(t, err, ErrRunEventBrokerUnavailable)

	require.NoError(t, server.StartAddr(addr))
	events, err := broker.Subscribe(ctx, runID, "")
	require.NoError(t, err)
	received := receivePublishedEvent(t, events)
	var payload TerminalPayload
	require.NoError(t, json.Unmarshal(received.Event.Data, &payload))
	assert.Equal(t, "completed", payload.Reason)
	assert.Equal(t, uint64(2), received.Event.Seq)
}
