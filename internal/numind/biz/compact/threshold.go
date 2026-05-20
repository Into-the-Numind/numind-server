package compact

// Config controls compact trigger thresholds. Defaults adapt to qwen-plus
// (blueprint §4.8.4). Override via agent.WithCompactConfig RunnerOption.
type Config struct {
	// ContextWindow is the LLM's hard context window. qwen-plus = 128k.
	ContextWindow int
	// EffectiveContextWindow = ContextWindow - reserve for max output.
	EffectiveContextWindow int
	// AutoCompactThreshold is the token threshold past which tryPreLLMCompact
	// triggers a reactive compact pass. Defaults to EffectiveContextWindow minus
	// a buffer for the summary itself plus reinjected attachments (§4.8.7).
	AutoCompactThreshold int
	// MaxConsecutiveAutoCompactFailures is the circuit-breaker count; v1 sets
	// this but does not actively enforce it — #14 ReAct loop wires it.
	MaxConsecutiveAutoCompactFailures int
	// MaxCompactOutputTokens caps the summary length.
	MaxCompactOutputTokens int
	// ContextWindowSafetyMargin is the local estimation safety factor (§4.1.6).
	ContextWindowSafetyMargin float64
	// PTLCollapseKeepTurns is the CollapseDrain protection window (§4.1.6 Step 1).
	PTLCollapseKeepTurns int
}

// DefaultConfig returns the qwen-plus tuned defaults (§4.8.4).
//
//	ContextWindow                     = 128 000
//	EffectiveContextWindow            = 120 000  (128k - 8k maxOutput)
//	AutoCompactThreshold              = 107 000  (120k - 13k buffer)
//	MaxConsecutiveAutoCompactFailures = 3
//	MaxCompactOutputTokens            = 8 000
//	ContextWindowSafetyMargin         = 0.95
//	PTLCollapseKeepTurns              = 4
func DefaultConfig() Config {
	return Config{
		ContextWindow:                     128_000,
		EffectiveContextWindow:            120_000,
		AutoCompactThreshold:              107_000,
		MaxConsecutiveAutoCompactFailures: 3,
		MaxCompactOutputTokens:            8_000,
		ContextWindowSafetyMargin:         0.95,
		PTLCollapseKeepTurns:              4,
	}
}
