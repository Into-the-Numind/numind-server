package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// 编译期断言：BaseTool 嵌入到一个仅含 5 个必须方法的 struct 后应满足 FullTool
type minimalImpl struct {
	BaseTool
}

func (minimalImpl) Name() string                                               { return "minimal" }
func (minimalImpl) Description() string                                        { return "min" }
func (minimalImpl) UserFacingName() string                                     { return "Minimal" }
func (minimalImpl) NarrationVerb() string                                      { return "执行" }
func (minimalImpl) Execute(_ context.Context, _ ToolInput) (ToolResult, error) { return nil, nil }

var _ FullTool = (*minimalImpl)(nil) // 编译期断言

func TestBaseTool_DefaultsAreSensible(t *testing.T) {
	var b BaseTool
	if !b.IsEnabled(ToolConfig{}) {
		t.Error("IsEnabled default true")
	}
	if !b.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe default true")
	}
	if !b.IsReadOnly() {
		t.Error("IsReadOnly default true")
	}
	if b.IsDestructive() {
		t.Error("IsDestructive default false")
	}
	if b.InterruptBehavior() != "cancel" {
		t.Error("InterruptBehavior default 'cancel'")
	}
	if b.IsMCP() || b.IsCLI() {
		t.Error("default not MCP/CLI")
	}
	if b.ShouldDefer() || b.AlwaysLoad() {
		t.Error("default not defer/alwaysload")
	}
	if b.MaxResultSizeChars() != 0 {
		t.Error("default no limit")
	}
	if !b.ShouldShowResultInNarration() {
		t.Error("default true")
	}
}

func TestBaseTool_InputsEquivalent(t *testing.T) {
	var b BaseTool
	a := ToolInput([]byte(`{"x":1}`))
	c := ToolInput([]byte(`{"x":1}`))
	d := ToolInput([]byte(`{"y":2}`))
	if !b.InputsEquivalent(a, c) {
		t.Error("eq")
	}
	if b.InputsEquivalent(a, d) {
		t.Error("not eq")
	}
}

func TestBaseTool_BackfillObservableInput_PassThrough(t *testing.T) {
	var b BaseTool
	in := ToolInput([]byte(`{"x":1}`))
	out := b.BackfillObservableInput(in)
	if string(out) != string(in) {
		t.Error("should pass through")
	}
}

func TestMinimalToFullAdapter_Wrap(t *testing.T) {
	minimal := &fakeMinimalImpl{name: "echo", desc: "echo input"}
	full := WrapMinimal(minimal)

	if full.Name() != "echo" {
		t.Error("name")
	}
	if full.Description() != "echo input" {
		t.Error("desc")
	}
	if full.UserFacingName() != "echo" {
		t.Error("user-facing fallback to name")
	}

	result, err := full.Execute(context.Background(), ToolInput(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(result, &got)
	if got["x"].(float64) != 1 {
		t.Error("execute pass through")
	}
}

type fakeMinimalImpl struct {
	name string
	desc string
}

func (f *fakeMinimalImpl) Name() string        { return f.name }
func (f *fakeMinimalImpl) Description() string { return f.desc }
func (f *fakeMinimalImpl) Run(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
	return input, nil
}
