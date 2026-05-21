package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"

	"numind-server/internal/numind/biz/agent/callctx"
	"numind-server/internal/pkg/aiservice"
)

// TestAdapter_Generate_StashesUsage verifies that Generate stores Usage in usageStore
// keyed by the call-id injected via callctx, and that LookupUsage retrieves it.
func TestAdapter_Generate_StashesUsage(t *testing.T) {
	// Arrange: replace chatFn with a mock that returns controlled usage.
	origChatFn := chatFn
	t.Cleanup(func() { chatFn = origChatFn })
	chatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content:      "hello",
			Model:        "glm-4-7-251222",
			Provider:     "volc",
			FinishReason: "stop",
			Usage: aiservice.TokenUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
			},
		}, nil
	}

	adapter := NewAiserviceAdapter("glm-4-7-251222", "agent.task").(*aiserviceAdapter)

	callID := callctx.NewCallID()
	ctx := callctx.WithCallID(context.Background(), callID)

	_, err := adapter.Generate(ctx, []*schema.Message{
		{Role: schema.User, Content: "hi"},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}

	// Act + Assert: LookupUsage should return the mocked values.
	u, ok := adapter.LookupUsage(callID)
	if !ok {
		t.Fatal("LookupUsage: expected ok=true, got false")
	}
	if u.PromptTokens != 100 {
		t.Errorf("PromptTokens: got %d, want 100", u.PromptTokens)
	}
	if u.CompletionTokens != 50 {
		t.Errorf("CompletionTokens: got %d, want 50", u.CompletionTokens)
	}
	if u.Model != "glm-4-7-251222" {
		t.Errorf("Model: got %q, want %q", u.Model, "glm-4-7-251222")
	}
	if u.Provider != "volc" {
		t.Errorf("Provider: got %q, want %q", u.Provider, "volc")
	}
}

// TestAdapter_LookupUsage_NotFound verifies that LookupUsage returns (Usage{}, false)
// for an unknown call-id.
func TestAdapter_LookupUsage_NotFound(t *testing.T) {
	adapter := NewAiserviceAdapter("glm-4-7-251222", "agent.task").(*aiserviceAdapter)

	u, ok := adapter.LookupUsage("deadbeef12345678")
	if ok {
		t.Errorf("LookupUsage: expected ok=false for unknown id, got Usage=%+v", u)
	}
	if u != (Usage{}) {
		t.Errorf("LookupUsage: expected zero Usage for unknown id, got %+v", u)
	}
}

// TestAdapter_WithTools_SharesUsageStore verifies that the usageStore pointer is
// shared between an adapter and its WithTools clone, so stashed usage is visible
// on both instances.
func TestAdapter_WithTools_SharesUsageStore(t *testing.T) {
	// Arrange: replace chatFn with a mock.
	origChatFn := chatFn
	t.Cleanup(func() { chatFn = origChatFn })
	chatFn = func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content:      "ok",
			Model:        "test-model",
			Provider:     "test-provider",
			FinishReason: "stop",
			Usage: aiservice.TokenUsage{
				PromptTokens:     20,
				CompletionTokens: 10,
			},
		}, nil
	}

	// Build adapter A and stash a usage entry via Generate.
	adapterA := NewAiserviceAdapter("test-model", "agent.task").(*aiserviceAdapter)
	callID := callctx.NewCallID()
	ctx := callctx.WithCallID(context.Background(), callID)

	_, err := adapterA.Generate(ctx, []*schema.Message{{Role: schema.User, Content: "ping"}})
	if err != nil {
		t.Fatalf("adapterA.Generate error: %v", err)
	}

	// Build adapter B via WithTools — it must share the same usageStore.
	adapterBIface, err := adapterA.WithTools([]*schema.ToolInfo{{Name: "search", Desc: "web"}})
	if err != nil {
		t.Fatalf("WithTools error: %v", err)
	}
	adapterB := adapterBIface.(*aiserviceAdapter)

	// adapterB should find the record stashed by adapterA.
	u, ok := adapterB.LookupUsage(callID)
	if !ok {
		t.Fatal("adapterB.LookupUsage: expected ok=true (shared usageStore), got false")
	}
	if u.PromptTokens != 20 {
		t.Errorf("PromptTokens via adapterB: got %d, want 20", u.PromptTokens)
	}

	// Confirm it's literally the same map pointer.
	if adapterA.usageStore != adapterB.usageStore {
		t.Error("usageStore pointers differ — WithTools cloned the map instead of sharing it")
	}
}
