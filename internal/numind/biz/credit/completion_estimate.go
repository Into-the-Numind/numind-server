// Package credit — historical-average completion-token estimator.
//
// Replaces the hardcoded `policy.ReservedOutputTokens` (= max_tokens worst case)
// used by gateway path (ContextBudgetCredits middleware) as the
// EstimatedCompletionTokens input to pricing.CalculateCost.
//
// Source: usage_record table — 30-day per-(provider, model) aggregate.
// Formula: ceil(avg(completion_tokens) * safetyMultiplier).
//
// Cache: 5-min TTL, per-(provider, model) key. The aggregate is cheap to
// recompute (small index scan) but doing it on every LLM request would add
// avoidable DB roundtrip; cache trades freshness for latency.
//
// Concurrency: singleflight deduplicates in-flight loads — when N requests
// arrive simultaneously for the same uncached key, only one DB query fires;
// the rest wait and share the result. Prevents the classic cache-miss
// thundering herd on TTL expiry.
//
// Fallback contract: returns (0, false) when no usable data exists. Caller
// MUST use ReservedOutputTokens in that case — preserves zero-regression
// guarantee for cold-start models / brand-new deployments.
package credit

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"

	"numind-server/internal/pkg/log"
)

// CompletionEstimator returns a historical per-model completion-token estimate.
// Implementations MUST return (_, false) when insufficient data is available
// so the caller falls back to the conservative max_tokens default.
type CompletionEstimator interface {
	// Estimate returns (tokens, true) when a per-(provider, model) historical
	// average is available with enough samples; (0, false) otherwise.
	Estimate(ctx context.Context, provider, model string) (int, bool)
}

// completionEstimateConfig holds tunables for the DB-backed estimator.
// Exposed as a single struct so future admin-level overrides can be wired
// without changing the constructor signature.
type completionEstimateConfig struct {
	// LookbackDays is the trailing window used for the aggregate.
	// 30 days balances recency vs sample volume — shorter windows risk
	// flapping when a model has a quiet week.
	LookbackDays int
	// MinSamples is the minimum row count required before the estimate is
	// returned. Below this, the function returns (0, false) and the caller
	// falls back to ReservedOutputTokens. 30 ≈ 1 day of moderate traffic.
	MinSamples int
	// SafetyMultiplier is applied to the mean to keep ~p80 coverage.
	// 1.2 keeps single-sigma-ish headroom without bloating the estimate.
	SafetyMultiplier float64
	// CacheTTL is how long a (provider, model) entry stays valid.
	CacheTTL time.Duration
}

// defaultCompletionEstimateConfig captures the production defaults. Keep
// here (not as package vars) so tests can copy + adjust without races.
func defaultCompletionEstimateConfig() completionEstimateConfig {
	return completionEstimateConfig{
		LookbackDays:     30,
		MinSamples:       30,
		SafetyMultiplier: 1.2,
		CacheTTL:         5 * time.Minute,
	}
}

type completionEstimateCacheEntry struct {
	tokens   int
	hasData  bool
	expireAt time.Time
}

// dbCompletionEstimator is the production implementation backed by GORM.
type dbCompletionEstimator struct {
	db     *gorm.DB
	cfg    completionEstimateConfig
	cache  map[string]completionEstimateCacheEntry
	cacheM sync.RWMutex
	// sf deduplicates concurrent loaders for the same key. Without it, N
	// goroutines arriving on a cold cache key would each issue their own
	// DB query (N-1 wasted). singleflight keeps the first goroutine doing
	// the actual work and broadcasts the result to the rest.
	sf singleflight.Group
}

// NewCompletionEstimator wires a production estimator.
// db must be non-nil; constructor panics on nil to surface wiring bugs early.
func NewCompletionEstimator(db *gorm.DB) CompletionEstimator {
	if db == nil {
		panic("credit.NewCompletionEstimator: db is nil")
	}
	return &dbCompletionEstimator{
		db:    db,
		cfg:   defaultCompletionEstimateConfig(),
		cache: make(map[string]completionEstimateCacheEntry),
	}
}

// Estimate implements CompletionEstimator. See package doc for the contract.
func (e *dbCompletionEstimator) Estimate(ctx context.Context, provider, model string) (int, bool) {
	// Empty key would aggregate across all rows — not what callers want.
	// Treat as no-data so caller falls back.
	if provider == "" || model == "" {
		return 0, false
	}

	// Cache key uses NUL separator: provider/model are tokenizer-keys and
	// model-keys from the registry — neither can contain NUL bytes, so this
	// gives an unambiguous flattening without escaping.
	key := provider + "\x00" + model
	now := time.Now()

	e.cacheM.RLock()
	entry, ok := e.cache[key]
	e.cacheM.RUnlock()
	if ok && now.Before(entry.expireAt) {
		return entry.tokens, entry.hasData
	}

	// singleflight: only one loader runs per key at a time. Concurrent
	// callers for the same uncached key all wait on the same future.
	v, _, _ := e.sf.Do(key, func() (any, error) {
		// Double-check after winning the singleflight slot — another
		// recent caller may have just populated the cache.
		e.cacheM.RLock()
		entry, ok := e.cache[key]
		e.cacheM.RUnlock()
		if ok && time.Now().Before(entry.expireAt) {
			return entry, nil
		}

		result := e.queryDB(ctx, provider, model)

		// Skip caching on transient DB error so a single hiccup doesn't
		// poison estimation for the full TTL window. Log so prod has a
		// breadcrumb when accuracy silently degrades to the fallback.
		if result.err != nil {
			log.C(ctx).Warnw("completion_estimator.db_error",
				"provider", provider, "model", model, "err", result.err)
			return completionEstimateCacheEntry{
				tokens:   0,
				hasData:  false,
				expireAt: time.Now(), // already expired — caller's return uses this entry but next call re-queries
			}, nil
		}

		entry = completionEstimateCacheEntry{
			tokens:   result.tokens,
			hasData:  result.hasData,
			expireAt: time.Now().Add(e.cfg.CacheTTL),
		}
		e.cacheM.Lock()
		e.cache[key] = entry
		e.cacheM.Unlock()
		return entry, nil
	})

	cached := v.(completionEstimateCacheEntry)
	return cached.tokens, cached.hasData
}

// queryResult bundles queryDB outputs so Estimate can distinguish a true
// "no data" result (cache the negative for TTL) from a transient DB error
// (skip caching so the next request retries).
type queryResult struct {
	tokens  int
	hasData bool
	err     error
}

// queryDB runs the actual aggregate. err is non-nil only on real DB failure;
// "not enough samples" / "zero average" are normal outcomes returned as
// hasData=false, err=nil.
func (e *dbCompletionEstimator) queryDB(ctx context.Context, provider, model string) queryResult {
	type aggRow struct {
		AvgCompletion float64
		Cnt           int64
	}
	var row aggRow
	cutoff := time.Now().AddDate(0, 0, -e.cfg.LookbackDays)

	err := e.db.WithContext(ctx).
		Table("usage_record").
		Select("AVG(completion_tokens) AS avg_completion, COUNT(*) AS cnt").
		Where("provider = ? AND model = ? AND created_at >= ? AND completion_tokens > 0",
			provider, model, cutoff).
		Scan(&row).Error
	if err != nil {
		return queryResult{err: err}
	}
	if row.Cnt < int64(e.cfg.MinSamples) {
		return queryResult{tokens: 0, hasData: false}
	}
	if row.AvgCompletion <= 0 {
		return queryResult{tokens: 0, hasData: false}
	}
	return queryResult{
		tokens:  int(math.Ceil(row.AvgCompletion * e.cfg.SafetyMultiplier)),
		hasData: true,
	}
}

// String helps log/observability surfaces show the active config without
// reaching into private fields.
func (e *dbCompletionEstimator) String() string {
	return fmt.Sprintf("dbCompletionEstimator{lookback=%dd minSamples=%d safety=%.2fx ttl=%s}",
		e.cfg.LookbackDays, e.cfg.MinSamples, e.cfg.SafetyMultiplier, e.cfg.CacheTTL)
}
