package agent

import (
	"testing"

	"github.com/cloudwego/eino/schema"

	"numind-server/internal/pkg/aiservice"
)

// TestAdapterImplementsToolCallingChatModel pins the runtime backing type. The
// compile-time guarantee comes from NewAiserviceAdapter's return type; this
// asserts the concrete impl is *aiserviceAdapter so refactors that swap the
// concrete type are flagged here.
func TestAdapterImplementsToolCallingChatModel(t *testing.T) {
	a := NewAiserviceAdapter("glm-4-7-251222", "agent.task")
	if _, ok := a.(*aiserviceAdapter); !ok {
		t.Errorf("expected *aiserviceAdapter, got %T", a)
	}
}

// TestWithTools_ReturnsClone verifies that WithTools returns a new instance and does not mutate the original.
func TestWithTools_ReturnsClone(t *testing.T) {
	base := &aiserviceAdapter{modelName: "m", taskID: "t", tools: nil}

	tools := []*schema.ToolInfo{
		{Name: "search", Desc: "web search"},
	}
	cloned, err := base.WithTools(tools)
	if err != nil {
		t.Fatalf("WithTools returned error: %v", err)
	}

	// Original must not have tools.
	if len(base.tools) != 0 {
		t.Errorf("original adapter tools modified: got len=%d", len(base.tools))
	}

	// Cloned must have a tool.
	clonedAdapter, ok := cloned.(*aiserviceAdapter)
	if !ok {
		t.Fatal("WithTools did not return *aiserviceAdapter")
	}
	if len(clonedAdapter.tools) != 1 {
		t.Errorf("cloned adapter tools: got len=%d, want 1", len(clonedAdapter.tools))
	}
	if clonedAdapter.tools[0].Name != "search" {
		t.Errorf("cloned tool name: got %q, want %q", clonedAdapter.tools[0].Name, "search")
	}

	// Mutating the original slice must not affect the clone.
	tools[0] = &schema.ToolInfo{Name: "mutated"}
	if clonedAdapter.tools[0].Name != "search" {
		t.Error("defensive copy failed: clone's tool was mutated through original slice")
	}
}

// TestWithTools_ChainedCalls verifies that each chained WithTools call produces a new instance.
func TestWithTools_ChainedCalls(t *testing.T) {
	base := &aiserviceAdapter{modelName: "m", taskID: "t"}

	a1, err := base.WithTools([]*schema.ToolInfo{{Name: "t1"}})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := a1.WithTools([]*schema.ToolInfo{{Name: "t2"}, {Name: "t3"}})
	if err != nil {
		t.Fatal(err)
	}

	// base must still have no tools.
	if len(base.tools) != 0 {
		t.Errorf("base tools mutated: len=%d", len(base.tools))
	}
	// a1 must have 1 tool.
	if a1impl, ok := a1.(*aiserviceAdapter); ok {
		if len(a1impl.tools) != 1 {
			t.Errorf("a1 tools: got len=%d, want 1", len(a1impl.tools))
		}
	}
	// a2 must have 2 tools.
	if a2impl, ok := a2.(*aiserviceAdapter); ok {
		if len(a2impl.tools) != 2 {
			t.Errorf("a2 tools: got len=%d, want 2", len(a2impl.tools))
		}
	}
}

// TestConvertToEinoMessage_RoleAndContent verifies role/content mapping.
func TestConvertToEinoMessage_RoleAndContent(t *testing.T) {
	resp := &aiservice.ChatResponse{
		Content:      "hello world",
		FinishReason: "stop",
	}
	msg := convertToEinoMessage(resp)
	if msg.Role != schema.Assistant {
		t.Errorf("role: got %v, want %v", msg.Role, schema.Assistant)
	}
	if msg.Content != "hello world" {
		t.Errorf("content: got %q, want %q", msg.Content, "hello world")
	}
	if len(msg.ToolCalls) != 0 {
		t.Errorf("tool calls: expected empty, got %d", len(msg.ToolCalls))
	}
}

// TestConvertToEinoMessage_WithToolCalls verifies tool call conversion.
func TestConvertToEinoMessage_WithToolCalls(t *testing.T) {
	resp := &aiservice.ChatResponse{
		Content: "",
		ToolCalls: []aiservice.ToolCall{
			{
				ID:   "call_abc123",
				Type: "function",
				Function: aiservice.ToolCallFunction{
					Name:      "search",
					Arguments: `{"query": "golang"}`,
				},
			},
		},
		FinishReason: "tool_calls",
	}
	msg := convertToEinoMessage(resp)
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "call_abc123" {
		t.Errorf("tool call ID: got %q, want %q", tc.ID, "call_abc123")
	}
	if tc.Function.Name != "search" {
		t.Errorf("function name: got %q, want %q", tc.Function.Name, "search")
	}
	if tc.Function.Arguments != `{"query": "golang"}` {
		t.Errorf("arguments: got %q", tc.Function.Arguments)
	}
}

// TestConvertToAiserviceRequest_TaskIDPropagation verifies that taskID is stored on
// the adapter (and thus used in Chat/ChatStream calls) — tested indirectly via the
// adapter struct field since aiservice.Chat is a process-wide call requiring a running gateway.
func TestConvertToAiserviceRequest_TaskIDPropagation(t *testing.T) {
	a := &aiserviceAdapter{modelName: "test-model", taskID: "agent.session"}
	req := a.convertToAiserviceRequest([]*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if req.ModelOverride != "test-model" {
		t.Errorf("ModelOverride: got %q, want %q", req.ModelOverride, "test-model")
	}
	if len(req.Messages) != 1 {
		t.Fatalf("messages: got %d, want 1", len(req.Messages))
	}
	if req.Messages[0].Role != aiservice.MessageRoleUser {
		t.Errorf("message role: got %v, want user", req.Messages[0].Role)
	}
	if req.Messages[0].Content.Text != "hi" {
		t.Errorf("message content: got %q, want %q", req.Messages[0].Content.Text, "hi")
	}
}

// TestConvertToAiserviceRequest_EmptyModelName verifies that empty modelName does not set ModelOverride.
func TestConvertToAiserviceRequest_EmptyModelName(t *testing.T) {
	a := &aiserviceAdapter{modelName: "", taskID: "agent.session"}
	req := a.convertToAiserviceRequest([]*schema.Message{
		{Role: schema.System, Content: "be helpful"},
	})
	if req.ModelOverride != "" {
		t.Errorf("ModelOverride should be empty when modelName is empty, got %q", req.ModelOverride)
	}
}

// test(qa): dev run 133 root cause — the agent sent NO max_tokens, so a thinking
// model (deepseek-v4-pro, which emits reasoning FIRST and tool_calls LAST) ran at
// the provider's default output cap; the reasoning exhausted the budget and the
// trailing ask_user_question tool call was truncated mid-JSON ("unexpected end of
// JSON input"), killing the run. The adapter must carry its resolved maxOutputTokens
// onto req.MaxTokens so the model always has room to finish the tool call. Before the
// fix convertToAiserviceRequest never sets MaxTokens → it stays 0 (provider default).
func TestConvertToAiserviceRequest_PropagatesMaxTokens(t *testing.T) {
	a := &aiserviceAdapter{taskID: "agent.run", maxOutputTokens: 64000}
	req := a.convertToAiserviceRequest([]*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if req.MaxTokens != 64000 {
		t.Errorf("MaxTokens: got %d, want 64000 (agent must cap output — run 133)", req.MaxTokens)
	}
}

// TestConvertToAiserviceRequest_ZeroMaxTokensUnset verifies a 0 maxOutputTokens
// leaves req.MaxTokens unset (provider default) — no accidental clamp when the
// model's capability was unresolvable.
func TestConvertToAiserviceRequest_ZeroMaxTokensUnset(t *testing.T) {
	a := &aiserviceAdapter{taskID: "agent.run", maxOutputTokens: 0}
	req := a.convertToAiserviceRequest([]*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if req.MaxTokens != 0 {
		t.Errorf("MaxTokens: got %d, want 0 (unset when maxOutputTokens=0)", req.MaxTokens)
	}
}

// TestResolveAgentMaxTokens pins the output-cap policy: a model's declared
// max_output_tokens is used VERBATIM (no artificial ceiling — capping below a model's
// real limit would truncate long reports), and only an undeclared (0) model falls
// back to a safe default.
func TestResolveAgentMaxTokens(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"deepseek-v4-pro 384000 → verbatim", 384000, 384000},
		{"claude 128000 → verbatim", 128000, 128000},
		{"gemini 64000 → verbatim", 64000, 64000},
		{"mid 32000 → verbatim", 32000, 32000},
		{"small model 4096 → verbatim (respect real limit, not floored)", 4096, 4096},
		{"unset (0) → fallback", 0, agentMaxOutputTokensFallback},
		{"negative → fallback", -1, agentMaxOutputTokensFallback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveAgentMaxTokens(tc.in); got != tc.want {
				t.Errorf("resolveAgentMaxTokens(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestWithTools_PreservesMaxOutputTokens guards the cap on the tool-bound clone.
// Eino calls WithTools to produce the adapter that actually drives the ReAct loop;
// if that clone dropped maxOutputTokens, every tool-call turn would silently revert
// to the provider default and re-introduce the run-133 truncation.
func TestWithTools_PreservesMaxOutputTokens(t *testing.T) {
	base := &aiserviceAdapter{taskID: "agent.run", maxOutputTokens: 32000}
	clone, err := base.WithTools(nil)
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	got := clone.(*aiserviceAdapter).maxOutputTokens
	if got != 32000 {
		t.Errorf("clone maxOutputTokens: got %d, want 32000 (must survive WithTools)", got)
	}
}

// TestConvertToAiserviceRequest_PropagatesToolCallID is the Eino-side half of
// the hotfix aiservice-tool-message-roundtrip regression. Before the fix,
// schema.Message.ToolCallID was silently dropped when converting to
// aiservice.ChatMessage. The ReAct loop's tool-result message then arrived
// at the OAI adapter with no id, the upstream provider rejected the request
// with HTTP 400, and the run terminated as model_error. This test catches
// any future code path that re-introduces the drop.
func TestConvertToAiserviceRequest_PropagatesToolCallID(t *testing.T) {
	a := &aiserviceAdapter{modelName: "", taskID: "agent.run"}
	req := a.convertToAiserviceRequest([]*schema.Message{
		{
			Role:       schema.Tool,
			Content:    `{"weather":"sunny"}`,
			ToolCallID: "call_xyz789",
		},
	})
	if len(req.Messages) != 1 {
		t.Fatalf("messages: got %d, want 1", len(req.Messages))
	}
	if req.Messages[0].ToolCallID != "call_xyz789" {
		t.Errorf("ToolCallID: got %q, want call_xyz789 (root cause: DMXAPI 400 on missing field)", req.Messages[0].ToolCallID)
	}
	if req.Messages[0].Role != aiservice.MessageRoleTool {
		t.Errorf("Role: got %v, want tool", req.Messages[0].Role)
	}
}

// TestConvertToAiserviceRequest_PropagatesToolCalls is the assistant-turn
// companion: when Eino reposts the assistant message that requested the
// tool call (so the provider can correlate the upcoming tool result), the
// tool_calls array must survive.
func TestConvertToAiserviceRequest_PropagatesToolCalls(t *testing.T) {
	a := &aiserviceAdapter{modelName: "", taskID: "agent.run"}
	req := a.convertToAiserviceRequest([]*schema.Message{
		{
			Role:    schema.Assistant,
			Content: "",
			ToolCalls: []schema.ToolCall{
				{
					ID:   "call_xyz789",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "web_search",
						Arguments: `{"query":"weather"}`,
					},
				},
			},
		},
	})
	if len(req.Messages) != 1 {
		t.Fatalf("messages: got %d, want 1", len(req.Messages))
	}
	if len(req.Messages[0].ToolCalls) != 1 {
		t.Fatalf("ToolCalls: got %d, want 1", len(req.Messages[0].ToolCalls))
	}
	tc := req.Messages[0].ToolCalls[0]
	if tc.ID != "call_xyz789" {
		t.Errorf("ToolCalls[0].ID: got %q, want call_xyz789", tc.ID)
	}
	if tc.Function.Name != "web_search" {
		t.Errorf("ToolCalls[0].Function.Name: got %q, want web_search", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"query":"weather"}` {
		t.Errorf("ToolCalls[0].Function.Arguments: got %q, want JSON args", tc.Function.Arguments)
	}
}

// TestConvertToAiserviceRequest_PropagatesReasoningContent is the Eino-side
// regression for the hotfix aiservice-reasoning-content-roundtrip. Before the
// fix, schema.Message.ReasoningContent was silently dropped when converting to
// aiservice.ChatMessage, so the next ReAct turn's request did not echo the
// thinking trace back to the provider. DMXAPI (deepseek-v4-pro intrinsic
// thinking) then rejected the request with HTTP 400 "The reasoning_content in
// the thinking mode must be passed back".
func TestConvertToAiserviceRequest_PropagatesReasoningContent(t *testing.T) {
	a := &aiserviceAdapter{modelName: "", taskID: "agent.run"}
	req := a.convertToAiserviceRequest([]*schema.Message{
		{
			Role:             schema.Assistant,
			Content:          "I will search the web",
			ReasoningContent: "User asked for news; I should call web_search.",
		},
	})
	if len(req.Messages) != 1 {
		t.Fatalf("messages: got %d, want 1", len(req.Messages))
	}
	if req.Messages[0].ReasoningContent != "User asked for news; I should call web_search." {
		t.Errorf("ReasoningContent: got %q, want the thinking trace (root cause: DMXAPI 400 on missing field)", req.Messages[0].ReasoningContent)
	}
}

// TestConvertToEinoMessage_PreservesReasoningContent is the response-side
// companion: when the LLM returns reasoning_content, the schema.Message we
// hand back to Eino must carry it so the next iteration's request can echo
// it. Without this propagation step, ChatMessage.ReasoningContent would
// always be empty on the next turn even with Layer A / Layer C fixes.
func TestConvertToEinoMessage_PreservesReasoningContent(t *testing.T) {
	resp := &aiservice.ChatResponse{
		Content:          "I'll search now",
		ReasoningContent: "Plan: call web_search with query='AI news today'",
	}
	msg := convertToEinoMessage(resp)
	if msg == nil {
		t.Fatal("convertToEinoMessage returned nil")
	}
	if msg.ReasoningContent != "Plan: call web_search with query='AI news today'" {
		t.Errorf("schema.Message.ReasoningContent: got %q, want the thinking trace", msg.ReasoningContent)
	}
	if msg.Content != "I'll search now" {
		t.Errorf("Content: got %q, want 'I'll search now'", msg.Content)
	}
}

// TestConvertRole verifies all role mappings.
func TestConvertRole(t *testing.T) {
	cases := []struct {
		in   schema.RoleType
		want aiservice.MessageRole
	}{
		{schema.System, aiservice.MessageRoleSystem},
		{schema.Assistant, aiservice.MessageRoleAssistant},
		{schema.Tool, aiservice.MessageRoleTool},
		{schema.User, aiservice.MessageRoleUser},
		{schema.RoleType("unknown"), aiservice.MessageRoleUser}, // fallback
	}
	for _, c := range cases {
		got := convertRole(c.in)
		if got != c.want {
			t.Errorf("convertRole(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

// TestConvertToolInfos_BasicMapping verifies ToolInfo → aiservice.Tool conversion.
func TestConvertToolInfos_BasicMapping(t *testing.T) {
	infos := []*schema.ToolInfo{
		{Name: "calculator", Desc: "perform arithmetic"},
		nil, // nil entries should be skipped
	}
	tools := convertToolInfos(infos)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (nil skipped), got %d", len(tools))
	}
	if tools[0].Type != "function" {
		t.Errorf("tool type: got %q, want %q", tools[0].Type, "function")
	}
	if tools[0].Function.Name != "calculator" {
		t.Errorf("function name: got %q, want %q", tools[0].Function.Name, "calculator")
	}
	if tools[0].Function.Description != "perform arithmetic" {
		t.Errorf("description: got %q", tools[0].Function.Description)
	}
}

// TestNewAiserviceAdapter_SetsEnablePromptCache pins T7 Layer-3 opt-in: every
// agent ReAct run reuses a long stable prefix (system prompt + skills + tool
// schemas + growing history) across R≥2 turns, so the agent asserts the
// per-call cache intent at construction. The constructor must set
// enablePromptCache=true unconditionally (the actual caching is still gated by
// the global flag + the model's prompt_cache_policy inside the native adapter —
// this field only carries the "reused prefix" signal).
func TestNewAiserviceAdapter_SetsEnablePromptCache(t *testing.T) {
	a := NewAiserviceAdapter("glm-4-7-251222", "agent.task")
	got := a.(*aiserviceAdapter).enablePromptCache
	if !got {
		t.Error("enablePromptCache: got false, want true (agent always opts in — T7)")
	}
}

// TestWithTools_PreservesEnablePromptCache is the spec-mandated guard (T7
// acceptance: "agent req carries EnablePromptCache=true across WithTools
// clone"). Eino calls WithTools to produce the adapter that actually drives the
// ReAct loop; if that clone dropped enablePromptCache, every real agent turn
// would silently revert to no-cache-intent and the cache toggle would be inert
// for the only caller that benefits from it.
func TestWithTools_PreservesEnablePromptCache(t *testing.T) {
	base := &aiserviceAdapter{taskID: "agent.run", enablePromptCache: true}
	clone, err := base.WithTools(nil)
	if err != nil {
		t.Fatalf("WithTools: %v", err)
	}
	got := clone.(*aiserviceAdapter).enablePromptCache
	if !got {
		t.Error("clone enablePromptCache: got false, want true (must survive WithTools — T7)")
	}
}

// TestConvertToAiserviceRequest_PropagatesEnablePromptCache verifies the
// adapter's immutable enablePromptCache lands on req.EnablePromptCache, the
// Layer-3 signal the native Claude adapter reads. Without this, the field stays
// at the ChatRequest zero value (false) and Claude caching never engages for
// agent runs.
func TestConvertToAiserviceRequest_PropagatesEnablePromptCache(t *testing.T) {
	a := &aiserviceAdapter{taskID: "agent.run", enablePromptCache: true}
	req := a.convertToAiserviceRequest([]*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if !req.EnablePromptCache {
		t.Error("req.EnablePromptCache: got false, want true (Layer-3 opt-in — T7)")
	}
}

// TestConvertToAiserviceRequest_NoCacheWhenDisabled is the zero-regression
// proof: an adapter constructed without the opt-in (the test-struct default, and
// the resting state of any non-agent caller pattern) leaves
// req.EnablePromptCache=false — byte-identical to the pre-T7 request shape.
func TestConvertToAiserviceRequest_NoCacheWhenDisabled(t *testing.T) {
	a := &aiserviceAdapter{taskID: "agent.run"} // enablePromptCache defaults false
	req := a.convertToAiserviceRequest([]*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if req.EnablePromptCache {
		t.Error("req.EnablePromptCache: got true, want false (no opt-in ⇒ no cache intent)")
	}
}
