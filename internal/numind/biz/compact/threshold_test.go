package compact

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDefaultConfig_QwenPlus(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, 128_000, cfg.ContextWindow, "qwen-plus context window")
	assert.Equal(t, 120_000, cfg.EffectiveContextWindow, "effective = context - maxOutput 8k")
	assert.Equal(t, 107_000, cfg.AutoCompactThreshold, "threshold = effective - 13k buffer")
	assert.Equal(t, 3, cfg.MaxConsecutiveAutoCompactFailures)
	assert.Equal(t, 8_000, cfg.MaxCompactOutputTokens)
	assert.InDelta(t, 0.95, cfg.ContextWindowSafetyMargin, 1e-9)
	assert.Equal(t, 4, cfg.PTLCollapseKeepTurns, "blueprint §4.1.6 collapse keep last 4 turns")
}
