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

// memoryMetrics holds all Task 3.3 counters + 1 gauge. Internal to the
// package; access via the public helper functions below.
type memoryMetrics struct {
	mu                  sync.RWMutex
	extractionRuns      map[MemoryExtractionResult]*atomic.Int64 // labelled counter
	factsExtractedTotal atomic.Int64
	dedupHitsTotal      atomic.Int64
	queueDepth          atomic.Int64
}

// memoryReg is the package-level singleton, lazy-initialised on first use.
var memoryReg = newMemoryMetrics()

// newMemoryMetrics constructs a memoryMetrics with all label slots pre-allocated.
func newMemoryMetrics() *memoryMetrics {
	m := &memoryMetrics{
		extractionRuns: make(map[MemoryExtractionResult]*atomic.Int64, 4),
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

// MemorySnapshot is a point-in-time view of all Task 3.3 metrics.
//
// Useful for tests (assert deltas around extract() calls) and for future
// /metrics handler implementation (one big JSON dump).
type MemorySnapshot struct {
	ExtractionRuns      map[MemoryExtractionResult]int64
	FactsExtractedTotal int64
	DedupHitsTotal      int64
	QueueDepth          int64
}

// MemoryGetSnapshot returns a consistent snapshot of all Task 3.3 metrics.
//
// Snapshot is consistent per-counter (atomic load) but the relative ordering
// across counters is not guaranteed; for telemetry dumps that's acceptable.
func MemoryGetSnapshot() MemorySnapshot {
	out := MemorySnapshot{
		ExtractionRuns: make(map[MemoryExtractionResult]int64, 4),
	}
	memoryReg.mu.RLock()
	for label, c := range memoryReg.extractionRuns {
		out.ExtractionRuns[label] = c.Load()
	}
	memoryReg.mu.RUnlock()
	out.FactsExtractedTotal = memoryReg.factsExtractedTotal.Load()
	out.DedupHitsTotal = memoryReg.dedupHitsTotal.Load()
	out.QueueDepth = memoryReg.queueDepth.Load()
	return out
}

// MemoryResetForTest resets all Task 3.3 counters. INTENDED FOR TESTS ONLY —
// production code must never call this.
func MemoryResetForTest() {
	memoryReg.mu.Lock()
	for _, c := range memoryReg.extractionRuns {
		c.Store(0)
	}
	memoryReg.mu.Unlock()
	memoryReg.factsExtractedTotal.Store(0)
	memoryReg.dedupHitsTotal.Store(0)
	memoryReg.queueDepth.Store(0)
}
