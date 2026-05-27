package stream

import "sync"

// SubscriptionLock ensures at most one SSE subscriber per agent run.
// It is process-local (no Redis); sufficient while agent runs stick to one pod.
//
// Usage:
//
//	lock := NewSubscriptionLock()
//	if !lock.Acquire(runID) {
//	    // another subscriber already connected — return 409
//	}
//	defer lock.Release(runID)
type SubscriptionLock struct {
	mu     sync.Mutex
	locked map[uint64]struct{}
}

// NewSubscriptionLock creates a ready-to-use SubscriptionLock.
func NewSubscriptionLock() *SubscriptionLock {
	return &SubscriptionLock{
		locked: make(map[uint64]struct{}),
	}
}

// Acquire tries to lock runID. It returns true if the lock was acquired,
// false if runID is already held by another subscriber.
func (l *SubscriptionLock) Acquire(runID uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.locked[runID]; ok {
		return false
	}
	l.locked[runID] = struct{}{}
	return true
}

// Release unlocks runID. It is idempotent — calling Release on an unlocked
// runID is a no-op and does not panic.
func (l *SubscriptionLock) Release(runID uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.locked, runID)
}
