package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	runEventAfterPause    = "pause"
	// Redis is an optional realtime transport, never part of Agent execution's
	// critical path. One unhealthy command gets a small budget; a short circuit
	// then makes subsequent token events fail immediately instead of applying
	// network backpressure to the model/runtime.
	runEventCommandTimeout = 150 * time.Millisecond
	runEventCircuitOpenFor = 2 * time.Second
)

var publishRunEventScript = goredis.NewScript(`
local id = redis.call('XADD', KEYS[1], 'MAXLEN', '~', ARGV[2], '*', 'event', ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
redis.call('PUBLISH', KEYS[2], id .. '\n' .. ARGV[1])
return id
`)

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
	client        *goredis.Client // shared client; owns the single Pub/Sub connection
	commandClient *goredis.Client // isolated fail-fast pool for short Stream commands
	ctx           context.Context
	cancel        context.CancelFunc

	mu          sync.RWMutex
	subscribers map[uint64]map[chan PublishedEvent]struct{}
	// Per-run deadlines prevent one customer's Redis timeout from slowing every
	// other customer. Values are *atomic.Int64 so delayed cleanup cannot remove a
	// newer circuit for the same run.
	publishCircuits sync.Map // map[uint64]*atomic.Int64
	criticalRetries sync.Map // map[uint64]*criticalRetryState
}

type criticalRetryState struct {
	mu         sync.RWMutex
	event      Event
	generation uint64
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
		// Do not mutate the application's shared Redis options. A dedicated pool
		// gives this optional transport strict fail-fast network bounds without
		// changing billing, auth, locks, or any other Redis-backed subsystem.
		options := *client.Options()
		options.DialTimeout = runEventCommandTimeout
		options.ReadTimeout = runEventCommandTimeout
		options.WriteTimeout = runEventCommandTimeout
		options.ContextTimeoutEnabled = true
		options.MaxRetries = -1
		b.commandClient = goredis.NewClient(&options)
		go b.consumeLiveEvents()
	}
	return b
}

// Close releases the broker-owned command pool and Pub/Sub loop. Production
// process shutdown also closes these resources, while tests call Close directly.
func (b *RedisRunEventBroker) Close() error {
	if b == nil {
		return nil
	}
	b.cancel()
	if b.commandClient != nil {
		return b.commandClient.Close()
	}
	return nil
}

func runEventStreamKey(runID uint64) string {
	return fmt.Sprintf("%s%d%s", runEventStreamPrefix, runID, runEventStreamSuffix)
}

func runEventChannel(runID uint64) string {
	return fmt.Sprintf("%s%d%s", runEventStreamPrefix, runID, runEventChannelSuffix)
}

func (b *RedisRunEventBroker) Publish(ctx context.Context, runID uint64, event Event) (string, error) {
	return b.publish(ctx, runID, event, true)
}

func (b *RedisRunEventBroker) publish(ctx context.Context, runID uint64, event Event, scheduleCritical bool) (string, error) {
	if b == nil || b.commandClient == nil {
		return "", ErrRunEventBrokerUnavailable
	}
	if runID == 0 || event.RunID != runID {
		return "", fmt.Errorf("publish run event identity mismatch: run=%d event_run=%d", runID, event.RunID)
	}
	if b.publishCircuitOpen(runID) && event.Type != EventTerminal && event.Type != EventError {
		return "", ErrRunEventBrokerUnavailable
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal run event: %w", err)
	}
	commandCtx, cancel := withRunEventCommandTimeout(ctx)
	defer cancel()
	// XADD + TTL refresh + wake notification execute as one Redis script. This
	// prevents a successful XADD followed by a failed EXPIRE from leaking one
	// permanent stream key per historical run.
	cursor, err := publishRunEventScript.Run(
		commandCtx,
		b.commandClient,
		[]string{runEventStreamKey(runID), runEventChannel(runID)},
		raw,
		runEventMaxLen,
		runEventTTL.Milliseconds(),
	).Text()
	if err != nil {
		// A cancelled caller must not degrade unrelated or later work. Genuine
		// transport failures open only this run's circuit and close its local SSE
		// subscriptions, causing the browser to reconnect/fall back immediately
		// instead of waiting forever for a terminal that could not be published.
		if ctx == nil || ctx.Err() == nil {
			b.openPublishCircuit(runID)
			b.disconnectSubscribers(runID)
		}
		if scheduleCritical && event.Type == EventTerminal && !isWaitingRunEventTerminal(event) {
			b.scheduleCriticalRetry(runID, event)
		}
		return "", fmt.Errorf("%w: atomic publish: %v", ErrRunEventBrokerUnavailable, err)
	}
	b.publishCircuits.Delete(runID)
	if event.Type == EventTerminal && !isWaitingRunEventTerminal(event) {
		b.cancelCriticalRetry(runID)
	}

	published := PublishedEvent{Cursor: cursor, Event: event}
	// Same-process subscribers receive the event without a Redis round trip.
	// The subsequent Pub/Sub notification carries it to every other instance;
	// cursor de-duplication makes the local echo harmless.
	b.dispatch(published)
	return cursor, nil
}

func (b *RedisRunEventBroker) Subscribe(ctx context.Context, runID uint64, after string) (<-chan PublishedEvent, error) {
	if b == nil || b.commandClient == nil {
		return nil, ErrRunEventBrokerUnavailable
	}
	if runID == 0 {
		return nil, fmt.Errorf("run id is required")
	}
	if after != "" && after != runEventAfterPause && !validRunEventCursor(after) {
		return nil, ErrInvalidRunEventCursor
	}

	if after == runEventAfterPause {
		baseline, err := b.latestWaitingTerminalCursor(ctx, runID)
		if err != nil {
			return nil, fmt.Errorf("%w: pause baseline: %v", ErrRunEventBrokerUnavailable, err)
		}
		if baseline == "" {
			return nil, fmt.Errorf("%w: pause baseline not found", ErrRunEventBrokerUnavailable)
		}
		after = baseline
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
	// A successful replay proves Redis recovered; let this run publish again
	// immediately instead of discarding deltas for the rest of the circuit window.
	b.publishCircuits.Delete(runID)

	out := make(chan PublishedEvent, runEventSubscriberCap)
	go b.serveSubscription(ctx, runID, after, history, live, out)
	return out, nil
}

func (b *RedisRunEventBroker) latestWaitingTerminalCursor(ctx context.Context, runID uint64) (string, error) {
	commandCtx, cancel := withRunEventCommandTimeout(ctx)
	defer cancel()
	messages, err := b.commandClient.XRevRangeN(
		commandCtx,
		runEventStreamKey(runID),
		"+",
		"-",
		runEventMaxLen,
	).Result()
	if err != nil {
		return "", err
	}
	for _, message := range messages {
		raw, ok := message.Values["event"]
		if !ok {
			continue
		}
		var encoded []byte
		switch value := raw.(type) {
		case string:
			encoded = []byte(value)
		case []byte:
			encoded = value
		default:
			continue
		}
		var event Event
		if json.Unmarshal(encoded, &event) == nil && event.RunID == runID && isWaitingRunEventTerminal(event) {
			return message.ID, nil
		}
	}
	return "", nil
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
		case item, open := <-live:
			if !open {
				return
			}
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
	commandCtx, cancel := withRunEventCommandTimeout(ctx)
	defer cancel()
	messages, err := b.commandClient.XRangeN(commandCtx, runEventStreamKey(runID), start, "+", runEventMaxLen).Result()
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
		cursor, raw, ok := strings.Cut(message.Payload, "\n")
		if !ok || !validRunEventCursor(cursor) {
			continue
		}
		var streamEvent Event
		if err := json.Unmarshal([]byte(raw), &streamEvent); err != nil || streamEvent.RunID == 0 {
			continue
		}
		b.dispatch(PublishedEvent{Cursor: cursor, Event: streamEvent})
	}
}

func withRunEventCommandTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, runEventCommandTimeout)
}

func (b *RedisRunEventBroker) publishCircuitOpen(runID uint64) bool {
	value, ok := b.publishCircuits.Load(runID)
	if !ok {
		return false
	}
	deadline, ok := value.(*atomic.Int64)
	if !ok || deadline.Load() <= time.Now().UnixNano() {
		b.publishCircuits.CompareAndDelete(runID, value)
		return false
	}
	return true
}

func (b *RedisRunEventBroker) openPublishCircuit(runID uint64) {
	deadline := &atomic.Int64{}
	deadline.Store(time.Now().Add(runEventCircuitOpenFor).UnixNano())
	b.publishCircuits.Store(runID, deadline)
	time.AfterFunc(runEventCircuitOpenFor, func() {
		b.publishCircuits.CompareAndDelete(runID, deadline)
	})
}

func (b *RedisRunEventBroker) disconnectSubscribers(runID uint64) {
	b.mu.Lock()
	subscribers := b.subscribers[runID]
	delete(b.subscribers, runID)
	for ch := range subscribers {
		close(ch)
	}
	b.mu.Unlock()
}

// A terminal frame is the transport's close signal. Retrying it asynchronously
// preserves runtime/finalize latency while ensuring a short Redis outage cannot
// leave subscribers on other application instances open forever. Persisted DB
// terminal state remains the restart-safe fallback at subscribe time.
func (b *RedisRunEventBroker) scheduleCriticalRetry(runID uint64, event Event) {
	state := &criticalRetryState{event: event, generation: 1}
	actual, loaded := b.criticalRetries.LoadOrStore(runID, state)
	if loaded {
		state = actual.(*criticalRetryState)
		state.mu.Lock()
		// Never let a later retry of an old pause overwrite a final terminal.
		if !isWaitingRunEventTerminal(state.event) && isWaitingRunEventTerminal(event) {
			state.mu.Unlock()
			return
		}
		state.event = event
		state.generation++
		generation := state.generation
		state.mu.Unlock()
		go b.runCriticalRetry(runID, state, generation)
		return
	}
	go b.runCriticalRetry(runID, state, state.generation)
}

func (b *RedisRunEventBroker) cancelCriticalRetry(runID uint64) {
	value, ok := b.criticalRetries.LoadAndDelete(runID)
	if !ok {
		return
	}
	state := value.(*criticalRetryState)
	state.mu.Lock()
	state.generation++
	state.mu.Unlock()
}

func (b *RedisRunEventBroker) runCriticalRetry(runID uint64, state *criticalRetryState, generation uint64) {
	defer func() {
		state.mu.RLock()
		stillLatest := state.generation == generation
		state.mu.RUnlock()
		if stillLatest {
			b.criticalRetries.CompareAndDelete(runID, state)
		}
	}()
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	delay := 250 * time.Millisecond
	for {
		timer := time.NewTimer(delay)
		select {
		case <-b.ctx.Done():
			timer.Stop()
			return
		case <-deadline.C:
			timer.Stop()
			return
		case <-timer.C:
		}
		state.mu.RLock()
		if state.generation != generation {
			state.mu.RUnlock()
			return
		}
		event := state.event
		state.mu.RUnlock()
		if _, err := b.publish(b.ctx, runID, event, false); err == nil {
			// A newer final terminal replaced the event during this attempt.
			// Its own generation goroutine is responsible for delivery.
			return
		}
		if delay < 5*time.Second {
			delay *= 2
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
		}
	}
}

func isWaitingRunEventTerminal(event Event) bool {
	if event.Type != EventTerminal {
		return false
	}
	var payload TerminalPayload
	return json.Unmarshal(event.Data, &payload) == nil && payload.Reason == "waiting_for_user_choice"
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
