package agent

import "testing"

// TestVisionQuotaClearRun_RemovesEntries verifies a finished run's vision quota
// counters are evicted so the process-lifetime sync.Map doesn't grow unbounded.
func TestVisionQuotaClearRun_RemovesEntries(t *testing.T) {
	runID := uint64(778899)
	if err := checkAndIncVisionQuota(runID, "analyze_image"); err != nil {
		t.Fatalf("unexpected quota error: %v", err)
	}
	if _, ok := visionQuotaStore.Load(visionQuotaKey{runID: runID, toolName: "analyze_image"}); !ok {
		t.Fatal("expected quota entry to exist after increment")
	}

	visionQuotaClearRun(runID)

	if _, ok := visionQuotaStore.Load(visionQuotaKey{runID: runID, toolName: "analyze_image"}); ok {
		t.Error("expected quota entry to be evicted after clear")
	}
	// runID 0 is a no-op and must not panic.
	visionQuotaClearRun(0)
}
