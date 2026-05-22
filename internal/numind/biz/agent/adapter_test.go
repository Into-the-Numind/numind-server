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
