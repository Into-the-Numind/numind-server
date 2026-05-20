package budget

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// BudgetTracker tracks the 4-dimensional budget state per agent run.
// Implementations must be safe for concurrent Runs (different runIDs use
// disjoint state slots) and concurrent in-Run hook calls.
//
// Spec deviation from §4.1: Start takes a userID parameter (not in original
// signature) — required so the daily aggregate can be keyed by user across
// Runs. runner.Run knows req.UserID at call time.
type BudgetTracker interface {
	Start(ctx context.Context, runID uint64, userID uint, limits Limits)
	Close(runID uint64)
	RecordStep(ctx context.Context, runID uint64)
	CanProceed(ctx context.Context, runID uint64) (exceeded bool, dim Dimension, detail map[string]any)
	RecordUsage(ctx context.Context, runID uint64, tokens int)
	Snapshot(ctx context.Context, runID uint64) Snapshot
}

// IBudgetStore is the optional persistence layer (v1: nil OK).
// TODO(#14): wire a Redis-backed implementation for multi-instance daily aggregate.
type IBudgetStore interface {
	// GetUserDailyCredits returns the user's accumulated daily credits.
	// Returns 0 + nil when no row yet for today.
	GetUserDailyCredits(ctx context.Context, userID uint, day time.Time) (int64, error)
	// SetUserDailyCredits writes the daily aggregate (upsert).
	SetUserDailyCredits(ctx context.Context, userID uint, day time.Time, credits int64) error
}

// runState is the per-Run state held by budgetTracker.
type runState struct {
	Limits    Limits
	StartedAt time.Time
	UserID    uint

	// Atomic counters; safe for concurrent in-Run hook calls.
	turnCount    atomic.Int32
	creditUsed   atomic.Int64
	dailyCredits atomic.Int64 // running tally for the user's day (initialized from cache at Start)
}

// budgetTracker is the in-memory implementation of BudgetTracker.
// TODO(#14): replace dailyCache with Redis INCRBY when prod becomes multi-instance.
type budgetTracker struct {
	mu     sync.RWMutex
	states map[uint64]*runState

	// dailyCache is the in-process aggregate per (userID, dayUTC).
	// Updated on every RecordUsage call. No lazy-sync to store in v1 (no persistence).
	dailyMu    sync.RWMutex
	dailyCache map[string]*dailyEntry // key: "userID:YYYY-MM-DD"

	store IBudgetStore
}

type dailyEntry struct {
	Credits    atomic.Int64
	LastSyncAt atomic.Int64 // unix nano; reserved for future store sync
}

// NewTracker constructs a fresh in-memory BudgetTracker.
// store may be nil — v1 dev environment uses nil (no persistence; daily aggregate
// is per-process only). #14 will wire a Redis-backed implementation.
func NewTracker(store IBudgetStore) BudgetTracker {
	return &budgetTracker{
		states:     make(map[uint64]*runState),
		dailyCache: make(map[string]*dailyEntry),
		store:      store,
	}
}

// Start initialises a new Run slot. Concurrent calls with different runIDs are safe.
func (t *budgetTracker) Start(ctx context.Context, runID uint64, userID uint, limits Limits) {
	st := &runState{
		Limits:    limits,
		StartedAt: time.Now(),
		UserID:    userID,
	}
	// Seed dailyCredits from the shared daily cache for this user.
	if userID > 0 {
		de := t.getOrCreateDailyEntry(userID, time.Now().UTC())
		st.dailyCredits.Store(de.Credits.Load())
	}
	t.mu.Lock()
	t.states[runID] = st
	t.mu.Unlock()
}

// Close removes a Run's state slot. Idempotent — double-close is safe.
func (t *budgetTracker) Close(runID uint64) {
	t.mu.Lock()
	delete(t.states, runID)
	t.mu.Unlock()
}

// RecordStep increments the step/turn counter for the Run.
func (t *budgetTracker) RecordStep(ctx context.Context, runID uint64) {
	t.mu.RLock()
	st := t.states[runID]
	t.mu.RUnlock()
	if st == nil {
		return
	}
	st.turnCount.Add(1)
}

// RecordUsage adds tokens to the Run's credit counter and the user's daily aggregate.
// Non-positive values are silently ignored.
func (t *budgetTracker) RecordUsage(ctx context.Context, runID uint64, tokens int) {
	if tokens <= 0 {
		return
	}
	t.mu.RLock()
	st := t.states[runID]
	t.mu.RUnlock()
	if st == nil {
		return
	}
	delta := int64(tokens)
	st.creditUsed.Add(delta)
	if st.UserID > 0 {
		de := t.getOrCreateDailyEntry(st.UserID, time.Now().UTC())
		newVal := de.Credits.Add(delta)
		// Keep the run's cached view of the daily total in sync.
		st.dailyCredits.Store(newVal)
	}
}

// CanProceed checks all 4 budget dimensions independently.
// Returns (false, "", nil) when all limits are within bounds.
// Returns (true, dim, detail) on the first exceeded dimension (checked in priority order:
// turns → credits → wall_time → daily_credits).
// Unknown runID returns (false, "", nil) — fail-open; caller must call Start first.
func (t *budgetTracker) CanProceed(ctx context.Context, runID uint64) (bool, Dimension, map[string]any) {
	t.mu.RLock()
	st := t.states[runID]
	t.mu.RUnlock()
	if st == nil {
		// Unknown run — fail open; caller should have called Start.
		return false, "", nil
	}

	// 1. Max turns
	turns := int(st.turnCount.Load())
	if turns >= st.Limits.MaxTurns {
		return true, DimMaxTurns, map[string]any{
			"used":  turns,
			"limit": st.Limits.MaxTurns,
		}
	}

	// 2. Per-Run credits
	credits := st.creditUsed.Load()
	if credits >= st.Limits.MaxCredits {
		return true, DimMaxCredits, map[string]any{
			"used":  credits,
			"limit": st.Limits.MaxCredits,
		}
	}

	// 3. Wall time
	elapsed := time.Since(st.StartedAt)
	if elapsed >= st.Limits.MaxWallTime {
		return true, DimMaxWallTime, map[string]any{
			"used":  elapsed.Milliseconds(),
			"limit": st.Limits.MaxWallTime.Milliseconds(),
		}
	}

	// 4. Daily credits (cross-Run for the same user)
	daily := st.dailyCredits.Load()
	if daily >= st.Limits.MaxDailyCredits {
		return true, DimMaxDailyCredits, map[string]any{
			"used":  daily,
			"limit": st.Limits.MaxDailyCredits,
		}
	}

	return false, "", nil
}

// Snapshot returns an immutable view of the Run's current budget state.
// Returns zero Snapshot for an unknown/closed runID.
func (t *budgetTracker) Snapshot(ctx context.Context, runID uint64) Snapshot {
	t.mu.RLock()
	st := t.states[runID]
	t.mu.RUnlock()
	if st == nil {
		return Snapshot{}
	}
	return Snapshot{
		Turns:        int(st.turnCount.Load()),
		Credits:      st.creditUsed.Load(),
		Elapsed:      time.Since(st.StartedAt),
		DailyCredits: st.dailyCredits.Load(),
		Limits:       st.Limits,
		StartedAt:    st.StartedAt,
	}
}

// getOrCreateDailyEntry returns the shared daily aggregate entry for (userID, day).
// Double-checked locking ensures at-most-one entry per key.
func (t *budgetTracker) getOrCreateDailyEntry(userID uint, day time.Time) *dailyEntry {
	key := dailyKey(userID, day)
	t.dailyMu.RLock()
	de := t.dailyCache[key]
	t.dailyMu.RUnlock()
	if de != nil {
		return de
	}
	t.dailyMu.Lock()
	defer t.dailyMu.Unlock()
	// Re-check under write lock (another goroutine may have inserted).
	if de = t.dailyCache[key]; de != nil {
		return de
	}
	de = &dailyEntry{}
	// v1: no store-side seed — daily aggregate is per-process only.
	t.dailyCache[key] = de
	return de
}

// dailyKey produces the map key "userID:YYYY-MM-DD" for the daily cache.
func dailyKey(userID uint, t time.Time) string {
	return uintToString(userID) + ":" + t.UTC().Format("2006-01-02")
}

// uintToString converts a uint to its decimal string representation
// without importing strconv (keeps the package dependency surface minimal).
func uintToString(u uint) string {
	if u == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	n := u
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
