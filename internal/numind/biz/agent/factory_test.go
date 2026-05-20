package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// 编译期断言：mockFactory 必须满足 ToolFactory interface
type mockFactory struct {
	id       string
	src      string
	tools    []FullTool
	metadata []ToolMetadata
	watchErr error
}

func (m *mockFactory) FactoryID() string   { return m.id }
func (m *mockFactory) Source() string      { return m.src }
func (m *mockFactory) DisplayName() string { return "mock-" + m.id }
func (m *mockFactory) LoadTools(_ context.Context) ([]FullTool, []ToolMetadata, error) {
	return m.tools, m.metadata, nil
}
func (m *mockFactory) Watch(_ context.Context, _ func(diff ToolDiff)) error { return m.watchErr }

var _ ToolFactory = (*mockFactory)(nil)

func TestToolMetadata_Fields(t *testing.T) {
	md := ToolMetadata{
		ToolName:    "echo",
		Source:      "platform",
		RiskLevel:   "safe",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	if md.ToolName != "echo" || md.Source != "platform" {
		t.Error("field assignment")
	}
}

func TestToolDiff_Construction(t *testing.T) {
	d := ToolDiff{
		Added:   []ToolMetadata{{ToolName: "new"}},
		Removed: []string{"old"},
	}
	if len(d.Added) != 1 || len(d.Removed) != 1 {
		t.Error("diff fields")
	}
}

func TestMockFactory_ImplementsToolFactory(t *testing.T) {
	f := &mockFactory{id: "mock-1", src: "platform"}
	if f.FactoryID() != "mock-1" {
		t.Error("id")
	}
	if f.Source() != "platform" {
		t.Error("src")
	}
	if f.DisplayName() != "mock-mock-1" {
		t.Error("display")
	}
	tools, metadata, err := f.LoadTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 0 || len(metadata) != 0 {
		t.Error("empty default")
	}
	if f.Watch(context.Background(), nil) != nil {
		t.Error("watch nil")
	}
}
