package agent

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Vision tool per-run quotas.
// These are intentionally conservative to control LLM costs.
// Adjust after observing real usage data in dev/prod.
const (
	// analyzeImageMaxPerRun is the maximum number of analyze_image calls allowed per agent run.
	analyzeImageMaxPerRun = 10

	// annotateImageMaxPerRun is the maximum number of annotate_image calls allowed per agent run.
	annotateImageMaxPerRun = 5
)

// visionQuotaKey is the sync.Map entry key for a (runID, toolName) pair.
type visionQuotaKey struct {
	runID    uint64
	toolName string
}

// visionQuotaStore holds per-run counters for vision tool calls.
// Keys are visionQuotaKey; values are *int64 (atomic counter).
// Entries are created on first use and live for the process lifetime
// (acceptable for V1.5 — run IDs are unique and the map stays small).
var visionQuotaStore sync.Map

// visionQuotaGet returns the atomic counter for (runID, toolName), creating it if absent.
func visionQuotaGet(runID uint64, toolName string) *int64 {
	key := visionQuotaKey{runID: runID, toolName: toolName}
	v, _ := visionQuotaStore.LoadOrStore(key, new(int64))
	return v.(*int64)
}

// visionQuotaClearRun removes a run's vision quota counters. Without this the
// process-lifetime sync.Map accumulates one entry per (runID, vision-tool) forever.
// Called from the run finalizers via defer so it fires on every exit path (including
// panics). Safe for runID 0 (no-op) and for runs that never used a vision tool.
func visionQuotaClearRun(runID uint64) {
	if runID == 0 {
		return
	}
	visionQuotaStore.Delete(visionQuotaKey{runID: runID, toolName: "analyze_image"})
	visionQuotaStore.Delete(visionQuotaKey{runID: runID, toolName: "annotate_image"})
}

// checkAndIncVisionQuota atomically increments the call counter for (runID, toolName)
// and returns an error if the quota would be exceeded.
//
// Returns nil when the increment is allowed (counter is now within quota).
// Returns a descriptive error (which the caller should surface as a graceful message,
// not a hard tool error) when the quota is already at or above limit.
//
// Quota constants:
//   - analyze_image: analyzeImageMaxPerRun calls per run
//   - annotate_image: annotateImageMaxPerRun calls per run
func checkAndIncVisionQuota(runID uint64, toolName string) error {
	if runID == 0 {
		// runID 0 = no run context (unit tests or non-agent code paths).
		// Allow the call freely — quota enforcement only applies inside real agent runs.
		return nil
	}

	var limit int64
	switch toolName {
	case "analyze_image":
		limit = analyzeImageMaxPerRun
	case "annotate_image":
		limit = annotateImageMaxPerRun
	default:
		return nil // unknown tool: skip quota
	}

	counter := visionQuotaGet(runID, toolName)
	// Atomically read the current count BEFORE incrementing.
	// If already at or above limit, deny without incrementing.
	current := atomic.LoadInt64(counter)
	if current >= limit {
		return fmt.Errorf("vision tool quota exceeded: %s is limited to %d calls per run (current: %d)",
			toolName, limit, current)
	}
	// NOTE: Load + Add is not atomic — under concurrent calls for the same runID,
	// quota may overshoot by up to (N concurrent callers - 1). Acceptable for
	// cost-control use case in V1.5.
	atomic.AddInt64(counter, 1)
	return nil
}
