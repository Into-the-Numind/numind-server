package httpclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestLLMConfig_ResponseHeaderTimeoutIs180s regression-locks the 180s value.
//
// Why a separate const-style test: dev incident 2026-05-27 (agent_run 42)
// proved the 60s DefaultConfig.ResponseHeaderTimeout was too tight for
// thinking-model LLM calls (real header TTFB measured 97.9s on a 24k-token
// fresh prompt with max_tokens=8000). If anyone trims this back to 60s,
// agent runs will start failing with model_error again — fail this test
// loudly instead.
func TestLLMConfig_ResponseHeaderTimeoutIs180s(t *testing.T) {
	c := LLMConfig()
	assert.Equal(t, 180*time.Second, c.ResponseHeaderTimeout,
		"LLM provider header TTFB needs ≥180s; do not regress to DefaultConfig 60s")
}

// TestLLMConfig_InheritsDefaults verifies LLMConfig changes ONLY the header
// timeout and inherits everything else from DefaultConfig. This guards against
// accidental drift (e.g. someone copying the struct and forgetting to update
// IdleConnTimeout when DefaultConfig changes).
func TestLLMConfig_InheritsDefaults(t *testing.T) {
	def := DefaultConfig()
	llm := LLMConfig()

	assert.Equal(t, def.Timeout, llm.Timeout)
	assert.Equal(t, def.ConnectTimeout, llm.ConnectTimeout)
	assert.Equal(t, def.TLSHandshakeTimeout, llm.TLSHandshakeTimeout)
	assert.Equal(t, def.IdleConnTimeout, llm.IdleConnTimeout)
	assert.Equal(t, def.MaxIdleConns, llm.MaxIdleConns)
	assert.Equal(t, def.MaxIdleConnsPerHost, llm.MaxIdleConnsPerHost)
	assert.Equal(t, def.MaxRetries, llm.MaxRetries)
	assert.Equal(t, def.RetryDelay, llm.RetryDelay)
	assert.Equal(t, def.RetryBackoff, llm.RetryBackoff)
	assert.Equal(t, def.EnableCompression, llm.EnableCompression)
	assert.Equal(t, def.UserAgent, llm.UserAgent)
}
