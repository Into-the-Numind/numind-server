package stream

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestLock_AcquireRelease verifies basic acquire→release→acquire sequence.
func TestLock_AcquireRelease(t *testing.T) {
	l := NewSubscriptionLock()

	if !l.Acquire(1) {
		t.Fatal("first Acquire should succeed")
	}
	// Second Acquire on same runID should fail.
	if l.Acquire(1) {
		t.Fatal("second Acquire before Release should fail")
	}
	// After Release, Acquire should succeed again.
	l.Release(1)
	if !l.Acquire(1) {
		t.Fatal("Acquire after Release should succeed")
	}
	l.Release(1)
}

// TestLock_Release_Idempotent verifies that releasing an unlocked runID is safe.
func TestLock_Release_Idempotent(t *testing.T) {
	l := NewSubscriptionLock()
	// Should not panic.
	l.Release(999)
	l.Release(999)
}

// TestLock_DifferentRunIDs verifies that different runIDs do not interfere.
func TestLock_DifferentRunIDs(t *testing.T) {
	l := NewSubscriptionLock()

	if !l.Acquire(10) {
		t.Fatal("Acquire(10) should succeed")
	}
	if !l.Acquire(20) {
		t.Fatal("Acquire(20) should succeed independently of runID 10")
	}
	if !l.Acquire(30) {
		t.Fatal("Acquire(30) should succeed")
	}

	// Attempting to acquire already-held IDs should fail.
	if l.Acquire(10) {
		t.Fatal("re-Acquire(10) should fail")
	}
	if l.Acquire(20) {
		t.Fatal("re-Acquire(20) should fail")
	}

	l.Release(10)
	l.Release(20)
	l.Release(30)

	// All released — re-acquire should work.
	if !l.Acquire(10) {
		t.Fatal("Acquire(10) after Release should succeed")
	}
	l.Release(10)
}

// TestLock_Concurrent verifies that exactly one goroutine wins Acquire
// when 1000 goroutines contend on the same runID.
func TestLock_Concurrent(t *testing.T) {
	l := NewSubscriptionLock()
	const n = 1000
	var winners int64

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if l.Acquire(42) {
				atomic.AddInt64(&winners, 1)
			}
		}()
	}
	wg.Wait()

	if winners != 1 {
		t.Errorf("exactly 1 goroutine should win Acquire, got %d", winners)
	}

	// The winner should have left the lock held.
	if l.Acquire(42) {
		t.Error("lock should still be held after concurrent test")
		l.Release(42)
	}

	// Release and verify it's available again.
	l.Release(42)
	if !l.Acquire(42) {
		t.Error("Acquire after final Release should succeed")
	}
	l.Release(42)
}

// TestLock_Concurrent_MultipleRunIDs verifies isolation under concurrency for
// independent runIDs.
func TestLock_Concurrent_MultipleRunIDs(t *testing.T) {
	l := NewSubscriptionLock()
	const n = 100

	var wg sync.WaitGroup
	for id := uint64(1); id <= n; id++ {
		wg.Add(1)
		go func(runID uint64) {
			defer wg.Done()
			if !l.Acquire(runID) {
				t.Errorf("Acquire(%d) should succeed (no contention)", runID)
				return
			}
			l.Release(runID)
		}(id)
	}
	wg.Wait()
}
