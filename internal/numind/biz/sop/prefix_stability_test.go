// Package sop — prefix_stability_test.go
//
// Prefix-stability regression guard for the LLM prompt-cache foundation
// (llm-prompt-cache §4.8, D4 SOP verdict).
//
// Batch A providers (DeepSeek / OpenAI GPT via DMXAPI) auto-cache the LONGEST
// COMMON PREFIX of the serialized request. A cache hit only fires when the head
// of the rendered message slice is byte-identical to a previous call. The D4
// audit found the SOP Gateway path already stable: buildGatewayMessages appends
// the current node instruction into the FINAL user message (the tail), and
// growing history adds turns at the tail — neither perturbs the head. These
// tests LOCK that invariant so a future refactor cannot silently regress
// cache-hit rate (there is no runtime error if the prefix drifts — caching just
// silently stops, so a static guard is the only protection).
//
// No production code is changed for SOP; this file is a pure regression guard.
package sop

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
)

// headFragmentSignature renders the leading fragment's cache-relevant bytes
// (Role, Source, Content) into a single comparable string. These are exactly the
// fields that determine the serialized prompt prefix; Order/Importance affect
// downstream sorting/budgeting, not the head identity.
func headFragmentSignature(t *testing.T, frags []contextbudget.ContextFragment) string {
	t.Helper()
	require.NotEmpty(t, frags, "fragment slice must not be empty")
	h := frags[0]
	return string(h.Role) + "\x00" + string(h.Source) + "\x00" + h.Content
}

// TestBuildSOPGatewayFragments_HeadByteStable verifies that the SOP Gateway
// prompt head is byte-identical (a) across two consecutive builds of the same
// inputs and (b) when history grows by one trailing turn. If either drifts, the
// provider prefix-cache stops hitting.
func TestBuildSOPGatewayFragments_HeadByteStable(t *testing.T) {
	node := &model.SopNode{Name: "创作节点", Prompt: "你现切换为【三维融合创作引擎】，执行融合创作"}

	// History as assembled by sop.go: a leading template system prompt followed by
	// prior-step user/assistant turns. The template system prompt is the prefix head.
	history := []LLMMessage{
		{Role: "system", Content: "模板预处理提示词——这是稳定的系统前缀，跨调用必须逐字节一致"},
		{Role: "user", Content: "你现在切换为【爆款解构外科医生】\n\n第二步素材"},
		{Role: "assistant", Content: "第二步爆款拆解输出"},
	}
	input := "第三步要创作的新素材"

	build := func(h []LLMMessage) []contextbudget.ContextFragment {
		return buildSOPGatewayFragments(buildGatewayMessages(node, input, h))
	}

	// (a) Determinism: two builds of identical inputs yield an identical head.
	first := build(history)
	second := build(history)
	require.NotEmpty(t, first)
	assert.Equal(t, headFragmentSignature(t, first), headFragmentSignature(t, second),
		"SOP gateway prompt head must be byte-identical across consecutive builds of the same inputs")

	// The head must be the template system prompt (the immutable prefix), NOT the
	// current node instruction (which lives in the FINAL user message, the tail).
	assert.Equal(t, string(contextbudget.RoleImmutable), string(first[0].Role),
		"the head fragment must be the immutable system prefix")
	assert.Equal(t, history[0].Content, first[0].Content,
		"the head content must be the template system prompt, not the current node instruction")
	assert.NotContains(t, first[0].Content, node.Prompt,
		"the current node instruction must stay in the tail, never the prefix head")

	// (b) Append-only growth: adding one more trailing user/assistant turn must
	// not change the head bytes (cache prefix survives a longer conversation).
	grown := make([]LLMMessage, len(history))
	copy(grown, history)
	grown = append(grown,
		LLMMessage{Role: "user", Content: "追加的第三步素材"},
		LLMMessage{Role: "assistant", Content: "追加的第三步输出"},
	)
	withGrowth := build(grown)
	assert.Equal(t, headFragmentSignature(t, first), headFragmentSignature(t, withGrowth),
		"growing history by a trailing turn must NOT change the prefix head (else the cache misses)")

	// The current node instruction must remain present in the LAST message (the
	// adjacency invariant the step-crossing fix relies on) — confirming the head
	// stability is achieved by tail-placement, not by dropping the instruction.
	last := withGrowth[len(withGrowth)-1]
	assert.Contains(t, last.Content, node.Prompt,
		"current node instruction must travel in the final user message (tail), keeping the head stable")
	assert.Contains(t, last.Content, input,
		"current input must travel in the final user message (tail)")
}
