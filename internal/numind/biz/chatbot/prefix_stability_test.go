// Package chatbot — prefix_stability_test.go
//
// Prefix-stability regression guard for the LLM prompt-cache foundation
// (llm-prompt-cache §4.8, D4 chatbot verdict).
//
// Batch A providers (DeepSeek / OpenAI GPT via DMXAPI) auto-cache the longest
// common prefix of the serialized request; a cache hit requires the rendered
// head bytes to match a prior call. The D4 audit found the chatbot fragment path
// (BuildChatContextFragments) already stable: fragment 0 is the immutable system
// prompt, and KB evidence chunks are appended AFTER history (never prepended to
// the head). These tests LOCK that invariant so a refactor cannot silently move
// KB content into the prefix and tank the cache-hit rate. There is no runtime
// error if the prefix drifts — caching just silently stops — so a static guard
// is the only protection.
//
// No production code is changed for chatbot; this file is a pure regression guard.
package chatbot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/retrieval/domain"
	cb "numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
)

// fragSignature renders a fragment's cache-relevant bytes (Role, Source, Content)
// into a single comparable string — exactly the fields that determine the
// serialized prompt prefix.
func fragSignature(f cb.ContextFragment) string {
	return string(f.Role) + "\x00" + string(f.Source) + "\x00" + f.Content
}

// TestBuildChatContextFragments_HeadStable_KBDoesNotPoisonPrefix verifies that
// the chatbot prompt head is the immutable system prompt and is byte-identical
// regardless of which KB evidence chunks are retrieved. KB content must ride a
// separate fragment placed AFTER history, never prepended to the head.
func TestBuildChatContextFragments_HeadStable_KBDoesNotPoisonPrefix(t *testing.T) {
	const systemPrompt = "你是莫小派销售助手——这是稳定的系统前缀，跨调用必须逐字节一致。"

	history := []model.ChatbotMessage{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好，有什么可以帮你？"},
	}
	currentMessage := "介绍一下你们的产品"

	// Two DIFFERENT KB retrievals — these vary call-to-call in production (different
	// query → different chunks). They must NOT change the prompt head.
	kbA := []domain.KnowledgeChunk{
		{ID: "c1", Content: "产品 A 资料：高性能 SOP 引擎。"},
		{ID: "c2", Content: "产品 B 资料：销售知识库 RAG。"},
	}
	kbB := []domain.KnowledgeChunk{
		{ID: "c9", Content: "完全不同的检索结果：定价与加量包说明。"},
	}

	fragsA := BuildChatContextFragments(systemPrompt, history, currentMessage, kbA, chatStreamRecentTurns)
	fragsB := BuildChatContextFragments(systemPrompt, history, currentMessage, kbB, chatStreamRecentTurns)
	fragsNoKB := BuildChatContextFragments(systemPrompt, history, currentMessage, nil, chatStreamRecentTurns)

	require.NotEmpty(t, fragsA)
	require.NotEmpty(t, fragsB)
	require.NotEmpty(t, fragsNoKB)

	// 1. The head fragment is the immutable system prompt, byte-for-byte.
	assert.Equal(t, string(cb.RoleImmutable), string(fragsA[0].Role),
		"the head fragment must be the immutable system prompt")
	assert.Equal(t, systemPrompt, fragsA[0].Content,
		"the head content must equal the configured system prompt exactly")

	// 2. The head is byte-identical across DIFFERENT KB retrievals and with no KB.
	assert.Equal(t, fragSignature(fragsA[0]), fragSignature(fragsB[0]),
		"changing the retrieved KB chunks must NOT change the prompt head (else the cache misses)")
	assert.Equal(t, fragSignature(fragsA[0]), fragSignature(fragsNoKB[0]),
		"presence/absence of KB chunks must NOT change the prompt head")

	// 3. Defense-in-depth: NO KB chunk content may leak into the head fragment.
	for _, chunk := range append(append([]domain.KnowledgeChunk{}, kbA...), kbB...) {
		assert.NotContains(t, fragsA[0].Content, chunk.Content,
			"KB evidence must never be prepended into the system-prompt head")
		assert.NotContains(t, fragsB[0].Content, chunk.Content,
			"KB evidence must never be prepended into the system-prompt head")
	}

	// 4. The history prefix (fragments between the system head and the KB block)
	// must also be stable across KB variation — the cache prefix extends through
	// the system prompt AND the deterministic history, up to the point where KB or
	// the current turn appends. Compare the leading fragments that precede any KB
	// evidence (RoleEvidence) fragment.
	leadA := fragmentsBeforeEvidence(fragsA)
	leadB := fragmentsBeforeEvidence(fragsB)
	require.Equal(t, len(leadA), len(leadB),
		"the non-KB leading fragment count must be identical regardless of KB retrieval")
	for i := range leadA {
		assert.Equal(t, fragSignature(leadA[i]), fragSignature(leadB[i]),
			"leading fragment %d (system+history prefix) must be byte-stable across KB variation", i)
	}
}

// fragmentsBeforeEvidence returns the leading fragments up to (but excluding) the
// first KB evidence fragment. These constitute the cache-relevant prefix that
// must stay stable across retrievals.
func fragmentsBeforeEvidence(frags []cb.ContextFragment) []cb.ContextFragment {
	for i, f := range frags {
		if f.Role == cb.RoleEvidence {
			return frags[:i]
		}
	}
	// No evidence fragment (e.g. nil KB): everything except the trailing current
	// turn is the stable prefix; return all but the last (the critical user turn).
	if len(frags) <= 1 {
		return frags
	}
	return frags[:len(frags)-1]
}
