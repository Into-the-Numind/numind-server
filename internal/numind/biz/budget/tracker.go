package budget

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"numind-server/internal/pkg/log"
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
	// RecordUsage accumulates CREDITS (already converted from tokens by the
	// caller — budgetgate.creditsForUsage). Never pass raw token counts.
	RecordUsage(ctx context.Context, runID uint64, credits int)
	Snapshot(ctx context.Context, runID uint64) Snapshot
}

// IBudgetStore is the optional cross-instance persistence layer for the daily
// aggregate. nil → in-process only (tests / dev without Redis).
type IBudgetStore interface {
	// AddUserDailyCredits atomically adds delta to the user's daily total for `day`
	// and returns the new total. Implementations must refresh a TTL so the key
	// auto-expires after the day rolls over.
	AddUserDailyCredits(ctx context.Context, userID uint, day time.Time, delta int64) (int64, error)
	// GetUserDailyCredits returns the user's current daily total (0 when absent).
	GetUserDailyCredits(ctx context.Context, userID uint, day time.Time) (int64, error)
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
	// Seed dailyCredits for this user. With a cross-instance store (Redis) seed
	// from the shared counter so the daily cap reflects all app instances; without
	// one, fall back to the in-process daily cache (tests / dev).
	if userID > 0 {
		if t.store != nil {
			if v, err := t.store.GetUserDailyCredits(ctx, userID, time.Now().UTC()); err != nil {
				log.Warnw("budget: GetUserDailyCredits failed, falling back to in-process daily cache",
					"user_id", userID, "run_id", runID, "error", err)
				de := t.getOrCreateDailyEntry(userID, time.Now().UTC())
				st.dailyCredits.Store(de.Credits.Load())
			} else {
				st.dailyCredits.Store(v)
			}
		} else {
			de := t.getOrCreateDailyEntry(userID, time.Now().UTC())
			st.dailyCredits.Store(de.Credits.Load())
		}
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

// RecordUsage adds credits to the Run's credit counter (telemetry) and the
// user's daily aggregate (the daily-credits dimension). Callers must convert
// token usage to credits first (budgetgate creditsForUsage) — the daily
// dimension is credit-denominated. Non-positive values are silently ignored.
func (t *budgetTracker) RecordUsage(ctx context.Context, runID uint64, credits int) {
	if credits <= 0 {
		return
	}
	t.mu.RLock()
	st := t.states[runID]
	t.mu.RUnlock()
	if st == nil {
		return
	}
	delta := int64(credits)
	// Per-run credits-used: telemetry only (no cap). Always accumulate.
	st.creditUsed.Add(delta)
	if st.UserID > 0 {
		if t.store != nil {
			// Cross-instance daily counter (Redis INCRBY). On error, fall back to
			// the in-process cache so usage is never silently dropped.
			if newVal, err := t.store.AddUserDailyCredits(ctx, st.UserID, time.Now().UTC(), delta); err != nil {
				log.Warnw("budget: AddUserDailyCredits failed, falling back to in-process daily cache",
					"user_id", st.UserID, "run_id", runID, "error", err)
				newVal := t.getOrCreateDailyEntry(st.UserID, time.Now().UTC()).Credits.Add(delta)
				st.dailyCredits.Store(newVal)
			} else {
				st.dailyCredits.Store(newVal)
			}
		} else {
			de := t.getOrCreateDailyEntry(st.UserID, time.Now().UTC())
			newVal := de.Credits.Add(delta)
			// Keep the run's cached view of the daily total in sync.
			st.dailyCredits.Store(newVal)
		}
	}
}

// CanProceed checks the budget dimensions independently.
// Returns (false, "", nil) when all limits are within bounds.
// Returns (true, dim, detail) on the first exceeded dimension (checked in priority order:
// turns → wall_time → daily_credits).
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

	// 2. Wall time
	elapsed := time.Since(st.StartedAt)
	if elapsed >= st.Limits.MaxWallTime {
		return true, DimMaxWallTime, map[string]any{
			"used":  elapsed.Milliseconds(),
			"limit": st.Limits.MaxWallTime.Milliseconds(),
		}
	}

	// 3. Daily credits (cross-Run for the same user)
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
