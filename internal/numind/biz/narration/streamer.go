package narration

import (
	"sync"

	"numind-server/internal/pkg/log"
)

// Streamer is the per-runID event channel registry.
// v1 has a single in-memory impl; #11 student-ux may wrap.
type Streamer interface {
	Send(ev Event)
	Subscribe(runID uint64) (<-chan Event, func())
	CloseRun(runID uint64)
}

const defaultBufferSize = 256

// memStreamer is the v1 in-memory impl.
type memStreamer struct {
	mu       sync.RWMutex
	runs     map[uint64]*runChannel
	bufferSz int
}

func newMemStreamer(bufferSz int) *memStreamer {
	if bufferSz <= 0 {
		bufferSz = defaultBufferSize
	}
	return &memStreamer{
		runs:     make(map[uint64]*runChannel),
		bufferSz: bufferSz,
	}
}

// runChannel pairs the buffered event channel with an RWMutex that serialises
// close against send. The naive atomic.Bool + defer-recover pattern is
// race-safe under panic, but Go's race detector flags concurrent send-on-chan
// and close-on-chan because they touch the same channel header word. The
// RWMutex pattern (RLock for send, Lock for close) introduces a happens-before
// edge that satisfies the race detector with no functional behavior change.
type runChannel struct {
	ch     chan Event
	mu     sync.RWMutex
	closed bool
}

// Send routes event to the per-runID channel. Lazy-creates the channel if absent.
// Fire-and-forget: if the channel is full, drops oldest event and warns.
func (s *memStreamer) Send(ev Event) {
	rc := s.getOrCreate(ev.RunID)
	dropped := rc.send(ev)
	if dropped {
		log.Warnw("narration stream buffer full or closed; dropping",
			"run_id", ev.RunID,
			"tool_call_id", ev.ToolCallID,
			"state", string(ev.State))
	}
}

// Subscribe lazy-creates the channel if absent (S2-D2: Subscribe after CloseRun
// returns a new open channel that will never receive events).
// Cleanup callback is a no-op signal of subscriber abandonment in v1; only
// CloseRun owns channel close (S1-D19).
func (s *memStreamer) Subscribe(runID uint64) (<-chan Event, func()) {
	rc := s.getOrCreate(runID)
	cleanup := func() {
		// v1: cleanup is a no-op signal. Multi-subscriber fan-out is #11's
		// concern (S1-D13).
	}
	return rc.ch, cleanup
}

// CloseRun is idempotent across multiple invocations (rc.close holds the
// write lock and short-circuits if already closed). Deletes the entry from
// the runs map BEFORE closing the channel; any racing getOrCreate after
// delete-but-before-close creates a benign new orphan channel that GC reclaims.
func (s *memStreamer) CloseRun(runID uint64) {
	s.mu.Lock()
	rc, ok := s.runs[runID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.runs, runID)
	s.mu.Unlock()
	rc.close()
}

// getOrCreate uses RWLock + double-check on write lock — standard idiom for
// lazy-init under contention.
func (s *memStreamer) getOrCreate(runID uint64) *runChannel {
	s.mu.RLock()
	if rc, ok := s.runs[runID]; ok {
		s.mu.RUnlock()
		return rc
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if rc, ok := s.runs[runID]; ok {
		return rc
	}
	rc := &runChannel{ch: make(chan Event, s.bufferSz)}
	s.runs[runID] = rc
	return rc
}

// send pushes ev to rc.ch under RLock so close() (which takes Lock) is
// serialised against in-flight sends. Returns true if the event was dropped
// (channel closed OR full).
func (rc *runChannel) send(ev Event) bool {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	if rc.closed {
		return true
	}
	select {
	case rc.ch <- ev:
		return false
	default:
		// Buffer full — drain one (drop oldest) then retry once.
		select {
		case <-rc.ch:
		default:
		}
		select {
		case rc.ch <- ev:
			return false
		default:
			return true
		}
	}
}

// close is idempotent under Lock; safe against concurrent send (which holds RLock).
func (rc *runChannel) close() {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.closed {
		return
	}
	rc.closed = true
	close(rc.ch)
}
