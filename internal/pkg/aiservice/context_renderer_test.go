package aiservice

import (
	"testing"

	"numind-server/internal/pkg/contextbudget"
)

// TestRenderContextFragmentsPreservesOrderAndRoles verifies that:
//   - Fragment order is preserved in the output ChatMessage slice
//   - Source role mapping is applied correctly for all source types
//   - Content is passed through verbatim
//   - Fragments with empty Content are skipped
func TestRenderContextFragmentsPreservesOrderAndRoles(t *testing.T) {
	fragments := []contextbudget.ContextFragment{
		{
			ID:      "f1",
			Source:  contextbudget.SourceSystem,
			Content: "You are a helpful assistant.",
			Order:   0,
		},
		{
			ID:      "f2",
			Source:  contextbudget.SourceUser,
			Content: "What is the capital of France?",
			Order:   1,
		},
		{
			ID:      "f3",
			Source:  contextbudget.SourceAssistant,
			Content: "The capital of France is Paris.",
			Order:   2,
		},
		{
			ID:      "f4",
			Source:  contextbudget.SourceTool,
			Content: `{"result": "Paris, population 2.1M"}`,
			Order:   3,
		},
		{
			ID:      "f5",
			Source:  contextbudget.SourceFile,
			Content: "Contents of an attached document.",
			Order:   4,
		},
	}

	got := RenderContextFragments(fragments)

	if len(got) != 5 {
		t.Fatalf("expected 5 ChatMessages, got %d", len(got))
	}

	cases := []struct {
		wantRole    MessageRole
		wantContent string
	}{
		{MessageRoleSystem, "You are a helpful assistant."},
		{MessageRoleUser, "What is the capital of France?"},
		{MessageRoleAssistant, "The capital of France is Paris."},
		{MessageRole("tool"), `{"result": "Paris, population 2.1M"}`},
		{MessageRoleUser, "Contents of an attached document."},
	}

	for i, want := range cases {
		msg := got[i]
		if msg.Role != want.wantRole {
			t.Errorf("msg[%d] role: got %q, want %q", i, msg.Role, want.wantRole)
		}
		if msg.Content.Text != want.wantContent {
			t.Errorf("msg[%d] content: got %q, want %q", i, msg.Content.Text, want.wantContent)
		}
	}
}

// TestRenderContextFragments_SkipsEmptyContent verifies that fragments with
// empty Content are not rendered (e.g. Compressibility=Reference fragments
// whose body was replaced by a SourceReference pointer).
func TestRenderContextFragments_SkipsEmptyContent(t *testing.T) {
	fragments := []contextbudget.ContextFragment{
		{ID: "f1", Source: contextbudget.SourceSystem, Content: "System prompt.", Order: 0},
		{
			ID:              "f2",
			Source:          contextbudget.SourceKB,
			Content:         "", // empty — reference-only fragment
			Compressibility: contextbudget.CompressReference,
			SourceReference: "kb://doc/123",
			Order:           1,
		},
		{ID: "f3", Source: contextbudget.SourceUser, Content: "User message.", Order: 2},
	}

	got := RenderContextFragments(fragments)

	if len(got) != 2 {
		t.Fatalf("expected 2 ChatMessages (empty fragment skipped), got %d", len(got))
	}
	if got[0].Content.Text != "System prompt." {
		t.Errorf("got[0].content: got %q, want %q", got[0].Content.Text, "System prompt.")
	}
	if got[1].Content.Text != "User message." {
		t.Errorf("got[1].content: got %q, want %q", got[1].Content.Text, "User message.")
	}
}

// TestRenderContextFragments_SourceMapping verifies all non-system/assistant/tool
// sources map to "user" role.
func TestRenderContextFragments_SourceMapping(t *testing.T) {
	userSources := []contextbudget.FragmentSource{
		contextbudget.SourceUser,
		contextbudget.SourceFile,
		contextbudget.SourceKB,
		contextbudget.SourceDB,
		contextbudget.SourceWeb,
		contextbudget.SourceInternal,
	}

	for _, src := range userSources {
		frags := []contextbudget.ContextFragment{
			{ID: "x", Source: src, Content: "content", Order: 0},
		}
		got := RenderContextFragments(frags)
		if len(got) != 1 {
			t.Fatalf("source %q: expected 1 message, got %d", src, len(got))
		}
		if got[0].Role != MessageRoleUser {
			t.Errorf("source %q: got role %q, want %q", src, got[0].Role, MessageRoleUser)
		}
	}
}

// TestRenderContextFragments_Nil verifies that a nil slice returns an empty (non-nil) slice.
func TestRenderContextFragments_Nil(t *testing.T) {
	got := RenderContextFragments(nil)
	if got == nil {
		t.Error("expected non-nil empty slice for nil input")
	}
	if len(got) != 0 {
		t.Errorf("expected 0 messages for nil input, got %d", len(got))
	}
}
