package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	runEventStreamPrefix  = "numind:agent:run:"
	runEventStreamSuffix  = ":events"
	runEventChannelSuffix = ":live"
	runEventMaxLen        = int64(4096)
	runEventTTL           = 24 * time.Hour
	runEventSubscriberCap = 512
	runEventRecoveryEvery = 10 * time.Second
)

var (
	// ErrRunEventBrokerUnavailable tells the HTTP layer to use its persisted
	// snapshot fallback. Publishing failures never fail the Agent run itself.
	ErrRunEventBrokerUnavailable = errors.New("agent run event broker unavailable")
	ErrInvalidRunEventCursor     = errors.New("invalid agent run event cursor")
)

// PublishedEvent is the transport envelope layered around the stable Agent
// event protocol. Cursor is a Redis Stream ID and therefore remains ordered
// even when a detached continuation starts its local Event.Seq again at one.
type PublishedEvent struct {
	Cursor string `json:"cursor"`
	Event  Event  `json:"event"`
}

// RunEventBroker is a short-lived replayable transport, not the durable Agent
// SOT. Database run/messages persistence remains authoritative.
type RunEventBroker interface {
	Publish(ctx context.Context, runID uint64, event Event) (cursor string, err error)
	Subscribe(ctx context.Context, runID uint64, after string) (<-chan PublishedEvent, error)
}

// RedisRunEventBroker combines Redis Streams (bounded replay) with one
// process-wide pattern subscription (live fan-out). Browser SSE connections do
// not own blocking Redis commands, so concurrent customers do not exhaust the
// normal command pool.
type RedisRunEventBroker struct {
	client *goredis.Client
	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.RWMutex
	subscribers map[uint64]map[chan PublishedEvent]struct{}
}

var _ RunEventBroker = (*RedisRunEventBroker)(nil)

func NewRedisRunEventBroker(client *goredis.Client) *RedisRunEventBroker {
	ctx, cancel := context.WithCancel(context.Background())
	b := &RedisRunEventBroker{
		client:      client,
		ctx:         ctx,
		cancel:      cancel,
		subscribers: make(map[uint64]map[chan PublishedEvent]struct{}),
	}
	if client != nil {
		go b.consumeLiveEvents()
	}
	return b
}

func runEventStreamKey(runID uint64) string {
	return fmt.Sprintf("%s%d%s", runEventStreamPrefix, runID, runEventStreamSuffix)
}

func runEventChannel(runID uint64) string {
	return fmt.Sprintf("%s%d%s", runEventStreamPrefix, runID, runEventChannelSuffix)
}

func (b *RedisRunEventBroker) Publish(ctx context.Context, runID uint64, event Event) (string, error) {
	if b == nil || b.client == nil {
		return "", ErrRunEventBrokerUnavailable
	}
	if runID == 0 || event.RunID != runID {
		return "", fmt.Errorf("publish run event identity mismatch: run=%d event_run=%d", runID, event.RunID)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal run event: %w", err)
	}
	cursor, err := b.client.XAdd(ctx, &goredis.XAddArgs{
		Stream: runEventStreamKey(runID),
		MaxLen: runEventMaxLen,
		Approx: true,
		Values: map[string]any{"event": raw},
	}).Result()
	if err != nil {
		return "", fmt.Errorf("%w: xadd: %v", ErrRunEventBrokerUnavailable, err)
	}

	published := PublishedEvent{Cursor: cursor, Event: event}
	// Same-process subscribers receive the event without a Redis round trip.
	// The subsequent Pub/Sub notification carries it to every other instance;
	// cursor de-duplication makes the local echo harmless.
	b.dispatch(published)
	liveRaw, marshalErr := json.Marshal(published)
	if marshalErr != nil {
		return cursor, fmt.Errorf("marshal published run event: %w", marshalErr)
	}
	pipe := b.client.Pipeline()
	pipe.Expire(ctx, runEventStreamKey(runID), runEventTTL)
	pipe.Publish(ctx, runEventChannel(runID), liveRaw)
	if _, pipeErr := pipe.Exec(ctx); pipeErr != nil {
		// XADD already succeeded, so retain the cursor. A reconnect/recovery read
		// can still replay the event even when the live notification failed.
		return cursor, fmt.Errorf("%w: publish notification: %v", ErrRunEventBrokerUnavailable, pipeErr)
	}
	return cursor, nil
}

func (b *RedisRunEventBroker) Subscribe(ctx context.Context, runID uint64, after string) (<-chan PublishedEvent, error) {
	if b == nil || b.client == nil {
		return nil, ErrRunEventBrokerUnavailable
	}
	if runID == 0 {
		return nil, fmt.Errorf("run id is required")
	}
	if after != "" && !validRunEventCursor(after) {
		return nil, ErrInvalidRunEventCursor
	}

	live := make(chan PublishedEvent, runEventSubscriberCap)
	b.addSubscriber(runID, live)
	// Register before replay so events published during XRANGE are queued in
	// live. Cursor de-duplication merges the two sources without a gap.
	history, err := b.readAfter(ctx, runID, after)
	if err != nil {
		b.removeSubscriber(runID, live)
		return nil, fmt.Errorf("%w: replay: %v", ErrRunEventBrokerUnavailable, err)
	}

	out := make(chan PublishedEvent, runEventSubscriberCap)
	go b.serveSubscription(ctx, runID, after, history, live, out)
	return out, nil
}

func (b *RedisRunEventBroker) serveSubscription(
	ctx context.Context,
	runID uint64,
	after string,
	history []PublishedEvent,
	live chan PublishedEvent,
	out chan<- PublishedEvent,
) {
	defer close(out)
	defer b.removeSubscriber(runID, live)
	last := after
	deliver := func(item PublishedEvent) bool {
		if last != "" && compareRunEventCursor(item.Cursor, last) <= 0 {
			return true
		}
		select {
		case out <- item:
			last = item.Cursor
			return true
		case <-ctx.Done():
			return false
		}
	}
	for _, item := range history {
		if !deliver(item) {
			return
		}
	}

	recovery := time.NewTicker(runEventRecoveryEvery)
	defer recovery.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-live:
			if !deliver(item) {
				return
			}
		case <-recovery.C:
			items, err := b.readAfter(ctx, runID, last)
			if err != nil {
				return
			}
			for _, item := range items {
				if !deliver(item) {
					return
				}
			}
		}
	}
}

func (b *RedisRunEventBroker) readAfter(ctx context.Context, runID uint64, after string) ([]PublishedEvent, error) {
	start := "-"
	if after != "" {
		start = "(" + after
	}
	messages, err := b.client.XRangeN(ctx, runEventStreamKey(runID), start, "+", runEventMaxLen).Result()
	if err != nil {
		return nil, err
	}
	result := make([]PublishedEvent, 0, len(messages))
	for _, message := range messages {
		raw, ok := message.Values["event"]
		if !ok {
			continue
		}
		var bytes []byte
		switch value := raw.(type) {
		case string:
			bytes = []byte(value)
		case []byte:
			bytes = value
		default:
			continue
		}
		var event Event
		if err := json.Unmarshal(bytes, &event); err != nil || event.RunID != runID {
			continue
		}
		result = append(result, PublishedEvent{Cursor: message.ID, Event: event})
	}
	return result, nil
}

func (b *RedisRunEventBroker) consumeLiveEvents() {
	pubsub := b.client.PSubscribe(b.ctx, runEventStreamPrefix+"*"+runEventChannelSuffix)
	defer pubsub.Close()
	for message := range pubsub.Channel() {
		var event PublishedEvent
		if err := json.Unmarshal([]byte(message.Payload), &event); err != nil || event.Event.RunID == 0 || !validRunEventCursor(event.Cursor) {
			continue
		}
		b.dispatch(event)
	}
}

func (b *RedisRunEventBroker) addSubscriber(runID uint64, ch chan PublishedEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscribers[runID] == nil {
		b.subscribers[runID] = make(map[chan PublishedEvent]struct{})
	}
	b.subscribers[runID][ch] = struct{}{}
}

func (b *RedisRunEventBroker) removeSubscriber(runID uint64, ch chan PublishedEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers[runID], ch)
	if len(b.subscribers[runID]) == 0 {
		delete(b.subscribers, runID)
	}
}

func (b *RedisRunEventBroker) dispatch(event PublishedEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers[event.Event.RunID] {
		select {
		case ch <- event:
		default:
			// The durable Stream remains the recovery source. Never let a slow
			// browser apply backpressure to the Agent runtime.
		}
	}
}

func validRunEventCursor(cursor string) bool {
	left, right, ok := strings.Cut(cursor, "-")
	if !ok || left == "" || right == "" || strings.Contains(right, "-") {
		return false
	}
	_, leftErr := strconv.ParseUint(left, 10, 64)
	_, rightErr := strconv.ParseUint(right, 10, 64)
	return leftErr == nil && rightErr == nil
}

func compareRunEventCursor(a, b string) int {
	if a == b {
		return 0
	}
	aLeft, aRight, aOK := splitRunEventCursor(a)
	bLeft, bRight, bOK := splitRunEventCursor(b)
	if !aOK || !bOK {
		return strings.Compare(a, b)
	}
	if aLeft < bLeft {
		return -1
	}
	if aLeft > bLeft {
		return 1
	}
	if aRight < bRight {
		return -1
	}
	return 1
}

func splitRunEventCursor(cursor string) (uint64, uint64, bool) {
	left, right, ok := strings.Cut(cursor, "-")
	if !ok {
		return 0, 0, false
	}
	first, firstErr := strconv.ParseUint(left, 10, 64)
	second, secondErr := strconv.ParseUint(right, 10, 64)
	return first, second, firstErr == nil && secondErr == nil
}
