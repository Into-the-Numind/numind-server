package budget

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBudgetTracker_StartCloseStateIsolation(t *testing.T) {
	tr := NewTracker(nil)
	limits := Limits{MaxTurns: 10, MaxWallTime: time.Second, MaxDailyCredits: 1000}

	// Start 5 runs
	for i := uint64(1); i <= 5; i++ {
		tr.Start(context.Background(), i, uint(i), limits)
	}
	// Record step on run 3 only
	tr.RecordStep(context.Background(), 3)

	s1 := tr.Snapshot(context.Background(), 1)
	s3 := tr.Snapshot(context.Background(), 3)
	assert.Equal(t, 0, s1.Turns, "run 1 turns isolated")
	assert.Equal(t, 1, s3.Turns, "run 3 incremented")

	// Close run 3
	tr.Close(3)
	snapshotEmpty := tr.Snapshot(context.Background(), 3)
	assert.Equal(t, Snapshot{}, snapshotEmpty)
}

func TestBudgetTracker_RecordStepAccumulates(t *testing.T) {
	tr := NewTracker(nil)
	tr.Start(context.Background(), 1, 1, DefaultLimits())
	for i := 0; i < 10; i++ {
		tr.RecordStep(context.Background(), 1)
	}
	s := tr.Snapshot(context.Background(), 1)
	assert.Equal(t, 10, s.Turns)
}

func TestBudgetTracker_RecordUsageAccumulates(t *testing.T) {
	tr := NewTracker(nil)
	tr.Start(context.Background(), 1, 1, DefaultLimits())
	tr.RecordUsage(context.Background(), 1, 100)
	tr.RecordUsage(context.Background(), 1, 250)
	s := tr.Snapshot(context.Background(), 1)
	assert.Equal(t, int64(350), s.Credits)
	assert.Equal(t, int64(350), s.DailyCredits)
}

func TestBudgetTracker_RecordUsage_ZeroOrNegativeNoop(t *testing.T) {
	tr := NewTracker(nil)
	tr.Start(context.Background(), 1, 1, DefaultLimits())
	tr.RecordUsage(context.Background(), 1, 0)
	tr.RecordUsage(context.Background(), 1, -50)
	s := tr.Snapshot(context.Background(), 1)
	assert.Equal(t, int64(0), s.Credits)
}

func TestBudgetTracker_CanProceed_MaxTurnsExceeded(t *testing.T) {
	tr := NewTracker(nil)
	tr.Start(context.Background(), 1, 1, Limits{MaxTurns: 3, MaxWallTime: time.Hour, MaxDailyCredits: 10000})
	for i := 0; i < 3; i++ {
		tr.RecordStep(context.Background(), 1)
	}
	exceeded, dim, detail := tr.CanProceed(context.Background(), 1)
	assert.True(t, exceeded)
	assert.Equal(t, DimMaxTurns, dim)
	assert.Equal(t, 3, detail["used"])
	assert.Equal(t, 3, detail["limit"])
}

func TestBudgetTracker_CanProceed_MaxWallTimeExceeded(t *testing.T) {
	tr := NewTracker(nil)
	tr.Start(context.Background(), 1, 1, Limits{MaxTurns: 100, MaxWallTime: 10 * time.Millisecond, MaxDailyCredits: 10000})
	time.Sleep(15 * time.Millisecond)
	exceeded, dim, _ := tr.CanProceed(context.Background(), 1)
	assert.True(t, exceeded)
	assert.Equal(t, DimMaxWallTime, dim)
}

func TestBudgetTracker_CanProceed_MaxDailyCreditsExceeded(t *testing.T) {
	tr := NewTracker(nil)
	// Cross-Run daily aggregate — same userID across 2 runs.
	limits := Limits{MaxTurns: 100, MaxWallTime: time.Hour, MaxDailyCredits: 1000}
	tr.Start(context.Background(), 1, 42, limits)
	tr.RecordUsage(context.Background(), 1, 600)
	tr.Close(1)
	// Second run, same userID — daily cache carries over.
	tr.Start(context.Background(), 2, 42, limits)
	tr.RecordUsage(context.Background(), 2, 500) // total daily = 1100 >= 1000
	exceeded, dim, _ := tr.CanProceed(context.Background(), 2)
	assert.True(t, exceeded)
	assert.Equal(t, DimMaxDailyCredits, dim)
}

func TestBudgetTracker_CanProceed_UnknownRunIDFailOpen(t *testing.T) {
	tr := NewTracker(nil)
	exceeded, dim, detail := tr.CanProceed(context.Background(), 999)
	assert.False(t, exceeded)
	assert.Equal(t, Dimension(""), dim)
	assert.Nil(t, detail)
}

func TestBudgetTracker_CanProceed_AllInRange(t *testing.T) {
	tr := NewTracker(nil)
	tr.Start(context.Background(), 1, 1, DefaultLimits())
	tr.RecordStep(context.Background(), 1)
	tr.RecordUsage(context.Background(), 1, 50)
	exceeded, dim, _ := tr.CanProceed(context.Background(), 1)
	assert.False(t, exceeded)
	assert.Equal(t, Dimension(""), dim)
}

func TestBudgetTracker_ConcurrentRunsRaceSafe(t *testing.T) {
	tr := NewTracker(nil)
	const goroutines = 50
	limits := DefaultLimits()

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := uint64(1); i <= goroutines; i++ {
		go func(runID uint64) {
			defer wg.Done()
			tr.Start(context.Background(), runID, uint(runID), limits)
			for j := 0; j < 5; j++ {
				tr.RecordStep(context.Background(), runID)
				tr.RecordUsage(context.Background(), runID, 10)
				_, _, _ = tr.CanProceed(context.Background(), runID)
			}
			_ = tr.Snapshot(context.Background(), runID)
			tr.Close(runID)
		}(i)
	}
	wg.Wait()

	// No panic, no race-detector hits (verified by -race flag).
	// All slots should be closed.
	for i := uint64(1); i <= goroutines; i++ {
		s := tr.Snapshot(context.Background(), i)
		assert.Equal(t, Snapshot{}, s, "run %d should be closed", i)
	}
}

func TestBudgetTracker_CloseIdempotent(t *testing.T) {
	tr := NewTracker(nil)
	tr.Start(context.Background(), 1, 1, DefaultLimits())
	tr.Close(1)
	tr.Close(1) // double close must not panic
	s := tr.Snapshot(context.Background(), 1)
	assert.Equal(t, Snapshot{}, s)
}

func TestBudgetTracker_SnapshotReturnsLiveData(t *testing.T) {
	tr := NewTracker(nil)
	tr.Start(context.Background(), 1, 1, DefaultLimits())
	tr.RecordStep(context.Background(), 1)
	tr.RecordStep(context.Background(), 1)
	tr.RecordUsage(context.Background(), 1, 75)
	s := tr.Snapshot(context.Background(), 1)
	assert.Equal(t, 2, s.Turns)
	assert.Equal(t, int64(75), s.Credits)
	assert.Equal(t, int64(75), s.DailyCredits)
	assert.True(t, s.Elapsed > 0)
	assert.Equal(t, DefaultLimits(), s.Limits)
}

func TestBudgetTracker_DailyAggregateScopedByUser(t *testing.T) {
	tr := NewTracker(nil)
	limits := DefaultLimits()
	tr.Start(context.Background(), 1, 1, limits) // user 1
	tr.Start(context.Background(), 2, 2, limits) // user 2
	tr.RecordUsage(context.Background(), 1, 100)
	tr.RecordUsage(context.Background(), 2, 200)
	s1 := tr.Snapshot(context.Background(), 1)
	s2 := tr.Snapshot(context.Background(), 2)
	assert.Equal(t, int64(100), s1.DailyCredits, "user 1 daily is 100")
	assert.Equal(t, int64(200), s2.DailyCredits, "user 2 daily is 200")
}

// TestBudgetTracker_uintToString verifies the internal formatter.
func TestBudgetTracker_uintToString(t *testing.T) {
	require.Equal(t, "0", uintToString(0))
	require.Equal(t, "1", uintToString(1))
	require.Equal(t, "42", uintToString(42))
	require.Equal(t, "9999", uintToString(9999))
}

// fakeBudgetStore is an in-memory IBudgetStore for asserting the tracker
// consults the cross-instance store rather than its in-process cache.
type fakeBudgetStore struct {
	mu      sync.Mutex
	totals  map[string]int64 // key: "userID:YYYY-MM-DD"
	seedGet int64            // value returned by GetUserDailyCredits before any Add
}

func (f *fakeBudgetStore) key(userID uint, day time.Time) string {
	return uintToString(userID) + ":" + day.UTC().Format("2006-01-02")
}

func (f *fakeBudgetStore) AddUserDailyCredits(_ context.Context, userID uint, day time.Time, delta int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.totals == nil {
		f.totals = make(map[string]int64)
	}
	k := f.key(userID, day)
	if _, ok := f.totals[k]; !ok {
		f.totals[k] = f.seedGet
	}
	f.totals[k] += delta
	return f.totals[k], nil
}

func (f *fakeBudgetStore) GetUserDailyCredits(_ context.Context, userID uint, day time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.totals != nil {
		if v, ok := f.totals[f.key(userID, day)]; ok {
			return v, nil
		}
	}
	return f.seedGet, nil
}

// TestBudgetTracker_StoreBackedDailyAggregate verifies that when an IBudgetStore
// is injected, the tracker seeds the daily total from the store at Start, feeds
// RecordUsage deltas through the store, and uses the store's returned total for
// the daily-credits CanProceed check (cross-instance correctness).
func TestBudgetTracker_StoreBackedDailyAggregate(t *testing.T) {
	// Store already shows 800 credits used today (e.g. another instance).
	store := &fakeBudgetStore{seedGet: 800}
	tr := NewTracker(store)
	limits := Limits{MaxTurns: 100, MaxWallTime: time.Hour, MaxDailyCredits: 1000}

	tr.Start(context.Background(), 1, 7, limits)
	// Snapshot should reflect the store-seeded daily total.
	assert.Equal(t, int64(800), tr.Snapshot(context.Background(), 1).DailyCredits)

	// Adding 250 → store total 1050 ≥ 1000 → daily cap tripped.
	tr.RecordUsage(context.Background(), 1, 250)
	assert.Equal(t, int64(1050), tr.Snapshot(context.Background(), 1).DailyCredits)

	exceeded, dim, _ := tr.CanProceed(context.Background(), 1)
	assert.True(t, exceeded)
	assert.Equal(t, DimMaxDailyCredits, dim)
}
