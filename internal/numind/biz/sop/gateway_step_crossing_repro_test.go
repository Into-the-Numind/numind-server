// Package sop — gateway_step_crossing_repro_test.go
//
// Customer-reported bug reproduction (NDF Rule 11), SOP Gateway path.
//
// Symptom (two independent prod reports): running step N of a multi-step SOP,
// the model produces step N-1's output and its thinking explicitly reasons about
// step N-1's instruction. Confirmed in prod (run 3039 / node-run 8896): a sort=3
// "创作" node emitted a sort=2 "爆款拆解" result — with NO context compression
// (context_budget_event id 2710: status=ok, 21k tokens vs 835k budget). So the
// previously-shipped compression-ordering fix cannot be the cause.
//
// Root cause: in the Gateway path (production, modelKey != ""), the CURRENT node's
// instruction (node.Prompt) was hoisted to a leading system message while the
// current step's input was appended as a BARE user message at the end. The
// conversation history (built in sop.go) embeds each previous step as a user
// message of "nodePrompt + input" followed by its assistant output — i.e. explicit
// persona-switch instructions. With the current step's instruction far away at the
// top and only a bare input at the generation point, the model intermittently
// follows the most-recent in-history instruction (step N-1) and crosses steps.
//
// The legacy ExecuteNodeStreamWithThinking path does NOT have this bug: it appends
// "node.Prompt + input" as the LAST user message, keeping the current instruction
// adjacent to its input. This test pins the Gateway path to that same structure.
package sop

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// TestBuildGatewayMessages_CurrentInstructionAdjacentToInput is the failing
// reproduction: for a node-execution turn (input != ""), the current node's
// instruction must travel with its input in the FINAL user message, and must NOT
// be left detached as a standalone leading system message while the last message
// is bare input.
func TestBuildGatewayMessages_CurrentInstructionAdjacentToInput(t *testing.T) {
	node := &model.SopNode{Name: "AI创作口播文案", Prompt: "你现切换为【三维融合创作引擎】，执行融合创作"}
	// History mirrors sop.go's assembly: each prior step is "nodePrompt + input"
	// (a persona-switch instruction) followed by its assistant output.
	history := []LLMMessage{
		{Role: "system", Content: "模板预处理提示词"},
		{Role: "user", Content: "你现在切换为【爆款解构外科医生】，对文案进行侵入式解剖\n\n第二步素材"},
		{Role: "assistant", Content: "第二步爆款拆解输出"},
	}
	input := "第三步要创作的新素材"

	msgs := buildGatewayMessages(node, input, history)
	require.NotEmpty(t, msgs, "messages must not be empty")
	// 3 history messages + 1 merged current-turn user message, and crucially NO
	// extra standalone system message for node.Prompt — guards against a
	// double-send regression (node.Prompt emitted as system AND merged into user).
	require.Len(t, msgs, 4, "expected 3 history messages + 1 merged current-turn user message")

	last := msgs[len(msgs)-1]
	assert.Equal(t, "user", last.Role, "the final message must be the current user turn")
	assert.Contains(t, last.Content, node.Prompt,
		"current node instruction must be adjacent to its input in the FINAL user message; "+
			"otherwise the model follows the most-recent in-history instruction (step N-1) and crosses steps")
	assert.Contains(t, last.Content, input, "current input must be present in the final user message")

	// The current node instruction must NOT be hoisted to a standalone leading
	// system message (the pre-fix behaviour) while the last message is bare input.
	for _, m := range msgs[:len(msgs)-1] {
		if m.Role == "system" {
			assert.NotEqual(t, node.Prompt, m.Content,
				"current node instruction must not be detached as a leading system message")
		}
	}
}

// TestBuildGatewayMessages_EmptyPromptWithInput guards a node with no configured
// prompt: the final user message must be the plain input (no leading "\n\n"
// separator) and there must be no spurious system message.
func TestBuildGatewayMessages_EmptyPromptWithInput(t *testing.T) {
	node := &model.SopNode{Name: "无提示词节点", Prompt: ""}
	history := []LLMMessage{
		{Role: "user", Content: "历史输入"},
		{Role: "assistant", Content: "历史输出"},
	}
	input := "当前步骤输入"

	msgs := buildGatewayMessages(node, input, history)
	require.Len(t, msgs, 3, "2 history messages + 1 current-turn user message")

	last := msgs[len(msgs)-1]
	assert.Equal(t, "user", last.Role)
	assert.Equal(t, input, last.Content, "empty node prompt yields plain input with no leading separator")
}

// TestBuildGatewayMessages_ChatScenarioKeepsSystemPrompt is a guard: for the SOP
// trailing-chat scenario (input == ""), the user's question already trails
// history, so node.Prompt should remain a leading system message and the final
// user message (the question) must be left intact. This behaviour must not change.
func TestBuildGatewayMessages_ChatScenarioKeepsSystemPrompt(t *testing.T) {
	node := &model.SopNode{Name: "末节点", Prompt: "节点系统提示"}
	history := []LLMMessage{
		{Role: "user", Content: "历史问题1"},
		{Role: "assistant", Content: "历史回答1"},
		{Role: "user", Content: "用户当前追问"}, // chat question already trails history
	}

	msgs := buildGatewayMessages(node, "", history)
	require.NotEmpty(t, msgs)

	assert.Equal(t, "system", msgs[0].Role, "chat scenario keeps a leading system message")
	assert.Equal(t, node.Prompt, msgs[0].Content, "chat scenario keeps node prompt as leading system message")

	last := msgs[len(msgs)-1]
	assert.Equal(t, "user", last.Role)
	assert.Equal(t, "用户当前追问", last.Content, "chat scenario leaves the trailing user question intact")
}
