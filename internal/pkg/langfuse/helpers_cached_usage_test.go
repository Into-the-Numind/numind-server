package langfuse

import "testing"

// TestWithGenCachedUsage_SetsCachedInput verifies the cache-aware usage option
// populates the UsageData.CachedInput field alongside the standard input/output/
// total counts, so Langfuse can render the prompt-cache HIT count.
func TestWithGenCachedUsage_SetsCachedInput(t *testing.T) {
	g := &GenerationBody{}
	WithGenCachedUsage(1000, 200, 400)(g)

	if g.Usage == nil {
		t.Fatal("Usage not set by WithGenCachedUsage")
	}
	if g.Usage.Input != 1000 {
		t.Errorf("Usage.Input = %d, want 1000", g.Usage.Input)
	}
	if g.Usage.Output != 200 {
		t.Errorf("Usage.Output = %d, want 200", g.Usage.Output)
	}
	if g.Usage.Total != 1200 {
		t.Errorf("Usage.Total = %d, want 1200", g.Usage.Total)
	}
	if g.Usage.CachedInput != 400 {
		t.Errorf("Usage.CachedInput = %d, want 400", g.Usage.CachedInput)
	}
}

// TestWithGenUsage_CachedInputStaysZero is the zero-regression control: the
// legacy WithGenUsage helper leaves CachedInput at 0 (no cache field emitted),
// so non-cache generations stay byte-identical to pre-cache behavior.
func TestWithGenUsage_CachedInputStaysZero(t *testing.T) {
	g := &GenerationBody{}
	WithGenUsage(1000, 200)(g)

	if g.Usage == nil {
		t.Fatal("Usage not set by WithGenUsage")
	}
	if g.Usage.CachedInput != 0 {
		t.Errorf("Usage.CachedInput = %d, want 0 (legacy helper must not set it)", g.Usage.CachedInput)
	}
}
