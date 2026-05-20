package agent

import (
	"context"
	"testing"
)

// stubFullTool embeds BaseTool (which provides 31 default methods) and implements
// the 5 required methods: Name / Description / UserFacingName / NarrationVerb / Execute.
type stubFullTool struct {
	BaseTool
	name string
}

func (s *stubFullTool) Name() string                                               { return s.name }
func (s *stubFullTool) Description() string                                        { return "" }
func (s *stubFullTool) UserFacingName() string                                     { return s.name }
func (s *stubFullTool) NarrationVerb() string                                      { return "execute" }
func (s *stubFullTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) { return nil, nil }

var _ FullTool = (*stubFullTool)(nil) // compile-time assertion

func TestFullToolMap_RoundTrip(t *testing.T) {
	tool := &stubFullTool{name: "test_tool"}
	m := map[string]FullTool{"test_tool": tool}
	ctx := WithFullToolMap(context.Background(), m)
	got := FullToolFromCtx(ctx, "test_tool")
	if got == nil {
		t.Fatalf("FullToolFromCtx returned nil for known name")
	}
	if got.Name() != "test_tool" {
		t.Errorf("got Name() = %q, want test_tool", got.Name())
	}
}

func TestFullToolMap_MissingTool(t *testing.T) {
	m := map[string]FullTool{}
	ctx := WithFullToolMap(context.Background(), m)
	got := FullToolFromCtx(ctx, "nonexistent")
	if got != nil {
		t.Errorf("expected nil for missing tool")
	}
}

func TestFullToolMap_NoCtxInjection(t *testing.T) {
	got := FullToolFromCtx(context.Background(), "any")
	if got != nil {
		t.Errorf("expected nil from bare ctx")
	}
}
