package agent

import (
	"container/list"
	"sync"
	"time"

	"numind-server/internal/numind/biz/narration"
)

// NarrationBuffer holds the most recent N narration events per runID in an
// in-memory ring buffer. Web-v3 polls /agent-runs/:id/narration?since=ts every
// 500ms; this buffer supports the cursor query.
//
// Design decisions:
//   - list.List capped at maxPerRun; excess entries are evicted from the front.
//   - QuerySince returns a snapshot copy so callers don't hold the lock.
//   - GC removes per-runID entries whose lastWrite was > retainFor ago.
//   - All operations are safe for concurrent access via sync.RWMutex.
type NarrationBuffer struct {
	mu        sync.RWMutex
	perRun    map[uint64]*list.List // runID → list of *narration.Event (capped at maxPerRun)
	maxPerRun int                   // default 200
	retainFor time.Duration         // default 1 hour after last write
	lastWrite map[uint64]time.Time  // runID → last write ts (for GC)
}

const (
	defaultMaxPerRun = 200
	defaultRetainFor = time.Hour
)

// NewNarrationBuffer constructs a NarrationBuffer.
// maxPerRun <= 0 defaults to 200; retainFor <= 0 defaults to 1 hour.
func NewNarrationBuffer(maxPerRun int, retainFor time.Duration) *NarrationBuffer {
	if maxPerRun <= 0 {
		maxPerRun = defaultMaxPerRun
	}
	if retainFor <= 0 {
		retainFor = defaultRetainFor
	}
	return &NarrationBuffer{
		perRun:    make(map[uint64]*list.List),
		maxPerRun: maxPerRun,
		retainFor: retainFor,
		lastWrite: make(map[uint64]time.Time),
	}
}

// AppendEvent adds ev to the buffer for runID. If the list has reached
// maxPerRun, the oldest event is evicted to maintain the cap.
// Safe for concurrent use.
func (b *NarrationBuffer) AppendEvent(runID uint64, ev *narration.Event) {
	if ev == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	l, ok := b.perRun[runID]
	if !ok {
		l = list.New()
		b.perRun[runID] = l
	}

	// Evict oldest if at cap.
	if l.Len() >= b.maxPerRun {
		front := l.Front()
		if front != nil {
			l.Remove(front)
		}
	}

	l.PushBack(ev)
	b.lastWrite[runID] = time.Now()
}

// QuerySince returns a snapshot of all events for runID where
// ev.Timestamp > since. If since is zero, all events are returned.
// Returns an empty slice if runID is not in the buffer.
func (b *NarrationBuffer) QuerySince(runID uint64, since time.Time) []*narration.Event {
	b.mu.RLock()
	defer b.mu.RUnlock()

	l, ok := b.perRun[runID]
	if !ok || l.Len() == 0 {
		return []*narration.Event{}
	}

	var result []*narration.Event
	for e := l.Front(); e != nil; e = e.Next() {
		ev, ok := e.Value.(*narration.Event)
		if !ok {
			continue
		}
		if since.IsZero() || ev.Timestamp.After(since) {
			result = append(result, ev)
		}
	}
	if result == nil {
		return []*narration.Event{}
	}
	return result
}

// GC removes entries for runs whose last write was more than retainFor ago.
// Call from a periodic goroutine ticker (e.g., every 5 minutes).
func (b *NarrationBuffer) GC() {
	cutoff := time.Now().Add(-b.retainFor)

	b.mu.Lock()
	defer b.mu.Unlock()

	for runID, ts := range b.lastWrite {
		if ts.Before(cutoff) {
			delete(b.perRun, runID)
			delete(b.lastWrite, runID)
		}
	}
}
