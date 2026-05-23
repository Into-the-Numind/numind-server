// Package metrics provides lightweight in-process counters for observability.
//
// The codebase does not (yet) depend on prometheus/client_golang. To avoid a
// new external dependency for the Task 3.3 memory extraction pipeline, this
// package exposes atomic-counter primitives. Values can be scraped by a future
// /metrics endpoint or dumped on demand via Snapshot().
//
// Naming follows Prometheus convention so that a future migration can swap in
// promauto.NewCounter without touching call sites — just rewire the helpers.
//
// All operations are safe for concurrent use.
package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

// MemoryExtractionResult enumerates the labels for memory_extraction_runs_total.
//
// Stable string values — keep in sync with Grafana dashboards / Filebeat
// pipelines that parse log lines.
type MemoryExtractionResult string

const (
	// MemoryExtractionSuccess indicates extract() ran end-to-end and persisted
	// (or dedup-promoted) at least zero valid facts without parse / LLM error.
	MemoryExtractionSuccess MemoryExtractionResult = "success"
	// MemoryExtractionParseError indicates the LLM returned a string that
	// failed JSON unmarshal or schema validation.
	MemoryExtractionParseError MemoryExtractionResult = "parse_error"
	// MemoryExtractionLLMError indicates aiservice.Chat returned an error.
	MemoryExtractionLLMError MemoryExtractionResult = "llm_error"
	// MemoryExtractionSkippedDebounce indicates the worker found a fresher
	// debounce entry and skipped processing in favour of the upcoming job.
	MemoryExtractionSkippedDebounce MemoryExtractionResult = "skipped_debounce"
)

// MemorySelectResult enumerates the labels for memory_select_runs_total.
// Task 3.4 SelectorService per-turn outcomes — used to track cache hit rate,
// LLM call success rate, and how often fallback paths fire.
//
// Stable string values — keep in sync with Grafana dashboards.
type MemorySelectResult string

const (
	// MemorySelectCacheHit indicates the 30s LRU cache returned a hit;
	// no LLM call was made and the cached fact IDs were re-used.
	MemorySelectCacheHit MemorySelectResult = "cache_hit"
	// MemorySelectShortcircuit indicates the user had ≤5 candidate facts,
	// so the selector skipped the LLM and returned the full set.
	MemorySelectShortcircuit MemorySelectResult = "shortcircuit"
	// MemorySelectLLMSuccess indicates the LLM call succeeded and the JSON
	// parsed cleanly — facts were chosen by the LLM's ranking.
	MemorySelectLLMSuccess MemorySelectResult = "llm_success"
	// MemorySelectLLMFailure indicates aiservice.Chat returned an error;
	// the selector fell back to confidence-top-5.
	MemorySelectLLMFailure MemorySelectResult = "llm_failure"
	// MemorySelectParseFailure indicates the LLM returned a string that
	// failed JSON unmarshal; the selector fell back to confidence-top-5.
	MemorySelectParseFailure MemorySelectResult = "parse_failure"
)

// MemoryDialecticResult enumerates the labels for memory_dialectic_runs_total.
// Task 3.6 CadenceService gate outcomes; task 3.7 dialectic service adds the
// `failed` label when an actual dialectic LLM call errors.
//
// Stable string values — keep in sync with Grafana dashboards.
type MemoryDialecticResult string

const (
	// MemoryDialecticRun indicates CadenceService.ShouldRunDialectic returned
	// true and the caller actually invoked the dialectic pipeline.
	MemoryDialecticRun MemoryDialecticResult = "run"
	// MemoryDialecticSkip indicates ShouldRunDialectic returned false (cooldown
	// active and new-fact delta below threshold) — caller used cached insight.
	MemoryDialecticSkip MemoryDialecticResult = "skip"
	// MemoryDialecticFailed is reserved for task 3.7: dialectic LLM call ran
	// but errored. Defined here so the label vocabulary is one source of truth.
	MemoryDialecticFailed MemoryDialecticResult = "failed"
)

// memoryMetrics holds all Task 3.3 + 3.4 + 3.6 + 3.7 counters + 1 gauge + 1 histogram.
// Internal to the package; access via the public helper functions below.
type memoryMetrics struct {
	mu                  sync.RWMutex
	extractionRuns      map[MemoryExtractionResult]*atomic.Int64 // labelled counter (task 3.3)
	factsExtractedTotal atomic.Int64
	dedupHitsTotal      atomic.Int64
	queueDepth          atomic.Int64
	selectRuns          map[MemorySelectResult]*atomic.Int64    // labelled counter (task 3.4)
	selectFactsInjected atomic.Int64                            // task 3.4 — total facts injected into prompts
	trivialCount        atomic.Int64                            // task 3.6 — trivial-input short-circuits
	dialecticRuns       map[MemoryDialecticResult]*atomic.Int64 // labelled counter (task 3.6, ext by task 3.7)
	// Task 3.7 dialectic duration histogram. Bucket boundaries (seconds): 1, 2,
	// 5, 10, 20, 30 (spec §观测). Stored as parallel slice of atomic.Int64 —
	// bucket i counts observations ≤ dialecticDurationBuckets[i]. Final +Inf
	// bucket is dialecticDurationCount minus sum of bucket counts.
	dialecticDurationBucketCounts []*atomic.Int64 // len == len(dialecticDurationBuckets)
	dialecticDurationCount        atomic.Int64    // total observation count (+Inf bucket = count - sum(buckets))
	dialecticDurationSum          atomic.Int64    // sum of observed seconds (multiplied by 1000 → store as ms for precision)
}

// dialecticDurationBuckets defines the Prometheus-style histogram boundaries
// for numind_memory_dialectic_duration_seconds (spec §观测 task 3.7).
// Values in seconds; observations are bucketed at the smallest bucket >=.
var dialecticDurationBuckets = []float64{1, 2, 5, 10, 20, 30}

// memoryReg is the package-level singleton, lazy-initialised on first use.
var memoryReg = newMemoryMetrics()

// newMemoryMetrics constructs a memoryMetrics with all label slots pre-allocated.
func newMemoryMetrics() *memoryMetrics {
	m := &memoryMetrics{
		extractionRuns:                make(map[MemoryExtractionResult]*atomic.Int64, 4),
		selectRuns:                    make(map[MemorySelectResult]*atomic.Int64, 5),
		dialecticRuns:                 make(map[MemoryDialecticResult]*atomic.Int64, 3),
		dialecticDurationBucketCounts: make([]*atomic.Int64, len(dialecticDurationBuckets)),
	}
	for i := range dialecticDurationBuckets {
		var c atomic.Int64
		m.dialecticDurationBucketCounts[i] = &c
	}
	for _, r := range []MemoryExtractionResult{
		MemoryExtractionSuccess,
		MemoryExtractionParseError,
		MemoryExtractionLLMError,
		MemoryExtractionSkippedDebounce,
	} {
		var c atomic.Int64
		m.extractionRuns[r] = &c
	}
	for _, r := range []MemorySelectResult{
		MemorySelectCacheHit,
		MemorySelectShortcircuit,
		MemorySelectLLMSuccess,
		MemorySelectLLMFailure,
		MemorySelectParseFailure,
	} {
		var c atomic.Int64
		m.selectRuns[r] = &c
	}
	for _, r := range []MemoryDialecticResult{
		MemoryDialecticRun,
		MemoryDialecticSkip,
		MemoryDialecticFailed,
	} {
		var c atomic.Int64
		m.dialecticRuns[r] = &c
	}
	return m
}

// MemoryExtractionRunsInc increments numind_memory_extraction_runs_total{result=...}.
// Unknown labels are no-op (defensive).
func MemoryExtractionRunsInc(result MemoryExtractionResult) {
	memoryReg.mu.RLock()
	c, ok := memoryReg.extractionRuns[result]
	memoryReg.mu.RUnlock()
	if !ok {
		return
	}
	c.Add(1)
}

// MemoryFactsExtractedAdd increments numind_memory_facts_extracted_total by delta.
func MemoryFactsExtractedAdd(delta int64) {
	memoryReg.factsExtractedTotal.Add(delta)
}

// MemoryDedupHitsInc increments numind_memory_dedup_hits_total by 1.
func MemoryDedupHitsInc() {
	memoryReg.dedupHitsTotal.Add(1)
}

// MemoryQueueDepthSet sets the gauge numind_memory_queue_depth to the given value.
func MemoryQueueDepthSet(v int64) {
	memoryReg.queueDepth.Store(v)
}

// MemorySelectRunsInc increments numind_memory_select_runs_total{result=...}.
// Unknown labels are no-op (defensive). Task 3.4 SelectorService outcomes.
func MemorySelectRunsInc(result MemorySelectResult) {
	memoryReg.mu.RLock()
	c, ok := memoryReg.selectRuns[result]
	memoryReg.mu.RUnlock()
	if !ok {
		return
	}
	c.Add(1)
}

// MemorySelectFactsInjectedAdd increments numind_memory_select_facts_injected_total
// by delta. Counts total facts successfully injected into agent system prompts
// — i.e., len(facts) at BuildMemorySection time, summed across all turns.
func MemorySelectFactsInjectedAdd(delta int64) {
	memoryReg.selectFactsInjected.Add(delta)
}

// MemoryTrivialCountInc increments numind_memory_trivial_total by 1.
// Task 3.6 — incremented when IsTrivial(userInput) returns true and the
// caller skips memory pipeline work for that turn.
func MemoryTrivialCountInc() {
	memoryReg.trivialCount.Add(1)
}

// MemoryDialecticRunsInc increments numind_memory_dialectic_runs_total{result=...}.
// Task 3.6 CadenceService gate outcomes (run / skip); task 3.7 adds the
// `failed` label when the dialectic LLM call errors. Unknown labels are
// no-op (defensive).
func MemoryDialecticRunsInc(result MemoryDialecticResult) {
	memoryReg.mu.RLock()
	c, ok := memoryReg.dialecticRuns[result]
	memoryReg.mu.RUnlock()
	if !ok {
		return
	}
	c.Add(1)
}

// MemoryDialecticRunCountInc is a task-3.7 convenience: increments the `run`
// label of numind_memory_dialectic_runs_total. Use when a dialectic LLM call
// returned a valid insight and was committed to cached_insight.
func MemoryDialecticRunCountInc() { MemoryDialecticRunsInc(MemoryDialecticRun) }

// MemoryDialecticSkipCountInc is a task-3.7 convenience: increments the `skip`
// label of numind_memory_dialectic_runs_total. Use when CadenceService
// gates the call (cooldown active and new-fact delta below threshold), or
// when the caller chose to defer for any other reason.
func MemoryDialecticSkipCountInc() { MemoryDialecticRunsInc(MemoryDialecticSkip) }

// MemoryDialecticFailedCountInc is a task-3.7 convenience: increments the
// `failed` label of numind_memory_dialectic_runs_total. Use for LLM error,
// JSON parse failure, validInsight rejection, DB write failure, or panic
// recovery — anything that prevents a valid insight from landing in cache.
func MemoryDialecticFailedCountInc() { MemoryDialecticRunsInc(MemoryDialecticFailed) }

// MemoryDialecticDurationObserve records one dialectic-pipeline wall-clock
// observation into the numind_memory_dialectic_duration_seconds histogram.
//
// Histogram buckets are 1, 2, 5, 10, 20, 30 seconds (+Inf implicit). Sum is
// kept in milliseconds for precision (divide by 1000 at scrape time).
//
// Concurrency: each bucket counter is an atomic.Int64; observations may race
// freely without locking. Bucket selection walks the small (6-element) slice
// in O(N) — negligible compared to LLM RTT (4-8 s for qwen-plus).
func MemoryDialecticDurationObserve(d time.Duration) {
	secs := d.Seconds()
	memoryReg.dialecticDurationCount.Add(1)
	// Store sum as milliseconds for precision (Prometheus typically sees
	// seconds; the snapshot reports float64 sec by dividing back).
	memoryReg.dialecticDurationSum.Add(d.Milliseconds())
	for i, upper := range dialecticDurationBuckets {
		if secs <= upper {
			memoryReg.dialecticDurationBucketCounts[i].Add(1)
		}
	}
}

// MemorySnapshot is a point-in-time view of all Task 3.3 + 3.4 + 3.6 + 3.7 metrics.
//
// Useful for tests (assert deltas around extract() / SelectTop5() / IsTrivial /
// MaybeRecompute) and for future /metrics handler implementation (one big
// JSON dump).
type MemorySnapshot struct {
	ExtractionRuns      map[MemoryExtractionResult]int64
	FactsExtractedTotal int64
	DedupHitsTotal      int64
	QueueDepth          int64
	SelectRuns          map[MemorySelectResult]int64
	SelectFactsInjected int64
	TrivialCount        int64                           // task 3.6
	DialecticRuns       map[MemoryDialecticResult]int64 // task 3.6 (+ task 3.7 failed label)
	// Task 3.7 dialectic duration histogram fields.
	DialecticDurationBuckets      []float64 // bucket upper bounds in seconds (copy of dialecticDurationBuckets)
	DialecticDurationBucketCounts []int64   // observations ≤ buckets[i]; len == len(buckets)
	DialecticDurationCount        int64     // total observations (use to derive +Inf bucket = count - last bucket)
	DialecticDurationSumSeconds   float64   // sum of observed durations in seconds
}

// MemoryGetSnapshot returns a consistent snapshot of all Task 3.3 + 3.4 + 3.6 + 3.7
// metrics.
//
// Snapshot is consistent per-counter (atomic load) but the relative ordering
// across counters is not guaranteed; for telemetry dumps that's acceptable.
func MemoryGetSnapshot() MemorySnapshot {
	out := MemorySnapshot{
		ExtractionRuns:                make(map[MemoryExtractionResult]int64, 4),
		SelectRuns:                    make(map[MemorySelectResult]int64, 5),
		DialecticRuns:                 make(map[MemoryDialecticResult]int64, 3),
		DialecticDurationBuckets:      append([]float64(nil), dialecticDurationBuckets...),
		DialecticDurationBucketCounts: make([]int64, len(dialecticDurationBuckets)),
	}
	memoryReg.mu.RLock()
	for label, c := range memoryReg.extractionRuns {
		out.ExtractionRuns[label] = c.Load()
	}
	for label, c := range memoryReg.selectRuns {
		out.SelectRuns[label] = c.Load()
	}
	for label, c := range memoryReg.dialecticRuns {
		out.DialecticRuns[label] = c.Load()
	}
	memoryReg.mu.RUnlock()
	out.FactsExtractedTotal = memoryReg.factsExtractedTotal.Load()
	out.DedupHitsTotal = memoryReg.dedupHitsTotal.Load()
	out.QueueDepth = memoryReg.queueDepth.Load()
	out.SelectFactsInjected = memoryReg.selectFactsInjected.Load()
	out.TrivialCount = memoryReg.trivialCount.Load()
	for i, c := range memoryReg.dialecticDurationBucketCounts {
		out.DialecticDurationBucketCounts[i] = c.Load()
	}
	out.DialecticDurationCount = memoryReg.dialecticDurationCount.Load()
	// Convert sum from milliseconds back to seconds for the snapshot consumer.
	out.DialecticDurationSumSeconds = float64(memoryReg.dialecticDurationSum.Load()) / 1000.0
	return out
}

// MemoryResetForTest resets all Task 3.3 + 3.4 + 3.6 + 3.7 counters. INTENDED
// FOR TESTS ONLY — production code must never call this.
func MemoryResetForTest() {
	memoryReg.mu.Lock()
	for _, c := range memoryReg.extractionRuns {
		c.Store(0)
	}
	for _, c := range memoryReg.selectRuns {
		c.Store(0)
	}
	for _, c := range memoryReg.dialecticRuns {
		c.Store(0)
	}
	for _, c := range memoryReg.dialecticDurationBucketCounts {
		c.Store(0)
	}
	memoryReg.mu.Unlock()
	memoryReg.factsExtractedTotal.Store(0)
	memoryReg.dedupHitsTotal.Store(0)
	memoryReg.queueDepth.Store(0)
	memoryReg.selectFactsInjected.Store(0)
	memoryReg.trivialCount.Store(0)
	memoryReg.dialecticDurationCount.Store(0)
	memoryReg.dialecticDurationSum.Store(0)
}
