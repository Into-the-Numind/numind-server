package agent

import (
	"sync"
	"testing"
	"time"

	"numind-server/internal/numind/biz/narration"
)

// makeEvent is a helper that creates a *narration.Event at the given time.
func makeEvent(runID uint64, ts time.Time) *narration.Event {
	return &narration.Event{
		RunID:     runID,
		ToolName:  "test_tool",
		State:     narration.StateUse,
		Message:   "test message",
		Timestamp: ts,
	}
}

// TestNarrationBuffer_AppendAndQuerySince verifies that QuerySince(t2) returns
// only events with Timestamp > t2.
func TestNarrationBuffer_AppendAndQuerySince(t *testing.T) {
	buf := NewNarrationBuffer(200, time.Hour)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t1 := base
	t2 := base.Add(time.Second)
	t3 := base.Add(2 * time.Second)
	t4 := base.Add(3 * time.Second)
	t5 := base.Add(4 * time.Second)

	runID := uint64(42)
	buf.AppendEvent(runID, makeEvent(runID, t1))
	buf.AppendEvent(runID, makeEvent(runID, t2))
	buf.AppendEvent(runID, makeEvent(runID, t3))
	buf.AppendEvent(runID, makeEvent(runID, t4))
	buf.AppendEvent(runID, makeEvent(runID, t5))

	result := buf.QuerySince(runID, t2)
	if len(result) != 3 {
		t.Fatalf("expected 3 events after t2, got %d", len(result))
	}
	for _, ev := range result {
		if !ev.Timestamp.After(t2) {
			t.Errorf("event at %v should be after %v", ev.Timestamp, t2)
		}
	}
}

// TestNarrationBuffer_QueryAll verifies that QuerySince with zero time returns all events.
func TestNarrationBuffer_QueryAll(t *testing.T) {
	buf := NewNarrationBuffer(200, time.Hour)
	runID := uint64(99)
	base := time.Now()

	for i := 0; i < 5; i++ {
		buf.AppendEvent(runID, makeEvent(runID, base.Add(time.Duration(i)*time.Second)))
	}

	result := buf.QuerySince(runID, time.Time{})
	if len(result) != 5 {
		t.Fatalf("expected 5 events with zero since, got %d", len(result))
	}
}

// TestNarrationBuffer_PerRunIsolation verifies that events for run A don't appear in run B.
func TestNarrationBuffer_PerRunIsolation(t *testing.T) {
	buf := NewNarrationBuffer(200, time.Hour)
	ts := time.Now()

	runA := uint64(1)
	runB := uint64(2)

	buf.AppendEvent(runA, makeEvent(runA, ts))
	buf.AppendEvent(runA, makeEvent(runA, ts.Add(time.Second)))

	buf.AppendEvent(runB, makeEvent(runB, ts.Add(2*time.Second)))

	// runA should have 2 events
	aEvents := buf.QuerySince(runA, time.Time{})
	if len(aEvents) != 2 {
		t.Fatalf("run A: expected 2 events, got %d", len(aEvents))
	}
	for _, ev := range aEvents {
		if ev.RunID != runA {
			t.Errorf("run A result contains event with runID %d", ev.RunID)
		}
	}

	// runB should have 1 event
	bEvents := buf.QuerySince(runB, time.Time{})
	if len(bEvents) != 1 {
		t.Fatalf("run B: expected 1 event, got %d", len(bEvents))
	}
	if bEvents[0].RunID != runB {
		t.Errorf("run B result contains event with runID %d", bEvents[0].RunID)
	}

	// run C (not added) should return empty slice, not nil
	cEvents := buf.QuerySince(uint64(3), time.Time{})
	if cEvents == nil {
		t.Error("QuerySince for unknown run should return empty slice, not nil")
	}
	if len(cEvents) != 0 {
		t.Errorf("expected 0 events for unknown run, got %d", len(cEvents))
	}
}

// TestNarrationBuffer_GC_RemovesOldRuns verifies that GC removes runs whose
// last write was > retainFor ago.
func TestNarrationBuffer_GC_RemovesOldRuns(t *testing.T) {
	// Use very short retention for testing.
	buf := NewNarrationBuffer(200, 50*time.Millisecond)
	runID := uint64(77)
	ts := time.Now()

	buf.AppendEvent(runID, makeEvent(runID, ts))

	// Verify event is visible before GC.
	if events := buf.QuerySince(runID, time.Time{}); len(events) != 1 {
		t.Fatalf("expected 1 event before GC, got %d", len(events))
	}

	// Wait for retention to expire.
	time.Sleep(100 * time.Millisecond)
	buf.GC()

	// After GC, run should be gone.
	if events := buf.QuerySince(runID, time.Time{}); len(events) != 0 {
		t.Errorf("expected 0 events after GC, got %d", len(events))
	}
}

// TestNarrationBuffer_StartGC_EvictsPeriodically verifies the background ticker
// actually calls GC so stale runs are evicted without a manual GC() call (the leak
// the wiring fixes).
func TestNarrationBuffer_StartGC_EvictsPeriodically(t *testing.T) {
	buf := NewNarrationBuffer(200, 20*time.Millisecond) // tiny retention
	runID := uint64(91)
	buf.AppendEvent(runID, makeEvent(runID, time.Now()))

	stop := buf.StartGC(5 * time.Millisecond)
	defer stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(buf.QuerySince(runID, time.Time{})) == 0 {
			return // evicted by the ticker — success
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("StartGC ticker did not evict the stale run within 2s")
}

// TestNarrationBuffer_ConcurrentSafe verifies that 100 concurrent goroutines
// appending events don't cause races (run with -race flag).
func TestNarrationBuffer_ConcurrentSafe(t *testing.T) {
	buf := NewNarrationBuffer(200, time.Hour)

	var wg sync.WaitGroup
	numGoroutines := 100
	runID := uint64(55)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ts := time.Now().Add(time.Duration(i) * time.Millisecond)
			buf.AppendEvent(runID, makeEvent(runID, ts))
		}(i)
	}

	wg.Wait()

	// At most maxPerRun events survive due to eviction.
	events := buf.QuerySince(runID, time.Time{})
	if len(events) > 200 {
		t.Errorf("buffer overflow: got %d events, max is 200", len(events))
	}
}

// TestNarrationBuffer_Cap verifies that the ring cap evicts oldest events.
func TestNarrationBuffer_Cap(t *testing.T) {
	buf := NewNarrationBuffer(3, time.Hour)
	runID := uint64(11)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		buf.AppendEvent(runID, makeEvent(runID, base.Add(time.Duration(i)*time.Second)))
	}

	// Only the 3 most recent events should be present.
	events := buf.QuerySince(runID, time.Time{})
	if len(events) != 3 {
		t.Fatalf("expected 3 events after cap eviction, got %d", len(events))
	}
	// Verify ordering: t2 < t3 < t4 (0-indexed: events 2,3,4 remain)
	if !events[0].Timestamp.Equal(base.Add(2 * time.Second)) {
		t.Errorf("first remaining event has wrong timestamp: %v", events[0].Timestamp)
	}
}
