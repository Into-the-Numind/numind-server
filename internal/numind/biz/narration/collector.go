package narration

import (
	"context"
	"sync"
)

// EventCollector accumulates every narration Event produced by Provider.Emit
// during a single run, so the run finalizer can PERSIST the tool-call timeline
// into agent_run.messages and replay it on session reload.
//
// Why a separate collector and not NarrationBuffer:
//   - NarrationBuffer is the live-polling ring buffer: capped small and GC'd
//     30min–1h after the run, and populated ASYNCHRONOUSLY (forwardNarration
//     goroutine). It is unsuitable as a durable source at finalize time.
//   - This collector is attached to the run context, captured SYNCHRONOUSLY at
//     emit time (inside Provider.Emit, where the full Event is already built),
//     and lives exactly as long as the run context. The finalizer reads it back
//     deterministically.
//
// It mirrors the agent package's imageCollector ctx pattern. It lives in the
// narration package (not agent) so Provider.Emit can write to it without an
// agent→narration→agent import cycle.
type EventCollector struct {
	mu      sync.Mutex
	events  []Event
	dropped int
}

// maxCollect bounds memory / persisted-JSON size for pathological runs. Typical
// runs emit a handful of events; this only guards against runaway loops. Events
// beyond the cap are dropped (earliest kept, so the start of the timeline and
// the dropped count survive).
const maxCollect = 2000

type collectorCtxKey struct{}

// WithCollector attaches a fresh EventCollector to ctx. Attach it on the
// run-level context (ancestor of the query/attempt contexts AND the context
// passed to finalize) so emit-side writes and finalize-side reads share one
// instance.
func WithCollector(ctx context.Context) context.Context {
	return context.WithValue(ctx, collectorCtxKey{}, &EventCollector{})
}

// CollectorFrom returns the collector attached to ctx, or nil. All methods are
// nil-safe so callers need not check.
func CollectorFrom(ctx context.Context) *EventCollector {
	c, _ := ctx.Value(collectorCtxKey{}).(*EventCollector)
	return c
}

// add records one built Event. Safe for concurrent calls and on a nil receiver.
func (c *EventCollector) add(ev Event) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) >= maxCollect {
		c.dropped++
		return
	}
	c.events = append(c.events, ev)
}

// Events returns a snapshot copy of the collected events in emit order. Safe on
// a nil receiver (returns nil).
func (c *EventCollector) Events() []Event {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return nil
	}
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}
