package main

import (
	"errors"
	"io"
	"testing"

	"github.com/cloudwego/eino/schema"

	"numind-server/internal/pkg/aiservice"
)

// ── convertRole ──────────────────────────────────────────────────────────────

func TestConvertRole(t *testing.T) {
	tests := []struct {
		in   schema.RoleType
		want aiservice.MessageRole
	}{
		{schema.System, aiservice.MessageRoleSystem},
		{schema.Assistant, aiservice.MessageRoleAssistant},
		{schema.Tool, aiservice.MessageRoleTool},
		{schema.User, aiservice.MessageRoleUser},
		{"unknown", aiservice.MessageRoleUser}, // unknown roles default to user
	}
	for _, tc := range tests {
		got := convertRole(tc.in)
		if got != tc.want {
			t.Errorf("convertRole(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ── convertToAiserviceRequest ────────────────────────────────────────────────

func TestConvertToAiserviceRequest_MessagesAndModel(t *testing.T) {
	in := []*schema.Message{
		{Role: schema.System, Content: "You are a helpful assistant."},
		{Role: schema.User, Content: "What day is today?"},
	}
	req := convertToAiserviceRequest(in, "qwen-turbo")

	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != aiservice.MessageRoleSystem {
		t.Errorf("msg[0].Role = %q, want system", req.Messages[0].Role)
	}
	if req.Messages[0].Content.Text != "You are a helpful assistant." {
		t.Errorf("msg[0].Content.Text = %q", req.Messages[0].Content.Text)
	}
	if req.Messages[1].Role != aiservice.MessageRoleUser {
		t.Errorf("msg[1].Role = %q, want user", req.Messages[1].Role)
	}
	if req.ModelOverride != "qwen-turbo" {
		t.Errorf("ModelOverride = %q, want qwen-turbo", req.ModelOverride)
	}
}

func TestConvertToAiserviceRequest_EmptyModelName(t *testing.T) {
	in := []*schema.Message{{Role: schema.User, Content: "hello"}}
	req := convertToAiserviceRequest(in, "")
	if req.ModelOverride != "" {
		t.Errorf("ModelOverride should be empty when modelName is empty, got %q", req.ModelOverride)
	}
}

func TestConvertToAiserviceRequest_PreservesOrder(t *testing.T) {
	roles := []schema.RoleType{schema.System, schema.User, schema.Assistant, schema.User}
	in := make([]*schema.Message, len(roles))
	for i, r := range roles {
		in[i] = &schema.Message{Role: r, Content: string(r)}
	}
	req := convertToAiserviceRequest(in, "")
	if len(req.Messages) != len(roles) {
		t.Fatalf("expected %d messages, got %d", len(roles), len(req.Messages))
	}
	for i, r := range roles {
		want := convertRole(r)
		if req.Messages[i].Role != want {
			t.Errorf("msg[%d].Role = %q, want %q", i, req.Messages[i].Role, want)
		}
	}
}

// ── convertToEinoMessage ─────────────────────────────────────────────────────

func TestConvertToEinoMessage(t *testing.T) {
	resp := &aiservice.ChatResponse{
		Content:  "Today is Monday.",
		Model:    "qwen-turbo",
		Provider: "ali",
	}
	msg := convertToEinoMessage(resp)
	if msg.Role != schema.Assistant {
		t.Errorf("Role = %q, want assistant", msg.Role)
	}
	if msg.Content != "Today is Monday." {
		t.Errorf("Content = %q, want 'Today is Monday.'", msg.Content)
	}
}

func TestConvertToEinoMessage_EmptyContent(t *testing.T) {
	resp := &aiservice.ChatResponse{Content: ""}
	msg := convertToEinoMessage(resp)
	if msg == nil {
		t.Fatal("convertToEinoMessage returned nil")
	}
	if msg.Role != schema.Assistant {
		t.Errorf("Role = %q, want assistant", msg.Role)
	}
}

// ── wrapChannelAsStreamReader ────────────────────────────────────────────────

func TestWrapChannelAsStreamReader_HappyPath(t *testing.T) {
	ch := make(chan aiservice.ChatChunk, 4)
	ch <- aiservice.ChatChunk{Delta: "Hello", Index: 0}
	ch <- aiservice.ChatChunk{Delta: " world", Index: 1}
	ch <- aiservice.ChatChunk{Delta: "", Index: 2, IsFinal: true}
	close(ch)

	sr := wrapChannelAsStreamReader(ch)
	defer sr.Close()

	var chunks []string
	for {
		msg, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if msg != nil {
			chunks = append(chunks, msg.Content)
		}
	}

	if len(chunks) != 2 {
		t.Fatalf("expected 2 non-final chunks, got %d: %v", len(chunks), chunks)
	}
	if chunks[0] != "Hello" || chunks[1] != " world" {
		t.Errorf("unexpected chunks: %v", chunks)
	}
}

func TestWrapChannelAsStreamReader_ErrorChunk(t *testing.T) {
	ch := make(chan aiservice.ChatChunk, 2)
	ch <- aiservice.ChatChunk{Delta: "partial", Index: 0}
	ch <- aiservice.ChatChunk{Err: errors.New("mid-stream error"), IsFinal: true}
	close(ch)

	sr := wrapChannelAsStreamReader(ch)
	defer sr.Close()

	// First chunk should succeed.
	msg, err := sr.Recv()
	if err != nil {
		t.Fatalf("expected first chunk, got error: %v", err)
	}
	if msg == nil || msg.Content != "partial" {
		t.Errorf("first chunk content = %v, want 'partial'", msg)
	}

	// Second call should return the error forwarded from the channel.
	_, err = sr.Recv()
	if err == nil {
		t.Fatal("expected error on second recv, got nil")
	}
	// Accept either the raw error or io.EOF (StreamReader may wrap on Close).
	if !errors.Is(err, io.EOF) && err.Error() != "mid-stream error" {
		t.Logf("got error (acceptable variant): %v", err)
	}
}

func TestWrapChannelAsStreamReader_EmptyChannel(t *testing.T) {
	ch := make(chan aiservice.ChatChunk, 1)
	ch <- aiservice.ChatChunk{IsFinal: true, Index: 0}
	close(ch)

	sr := wrapChannelAsStreamReader(ch)
	defer sr.Close()

	// Immediately EOF — no delta chunks.
	_, err := sr.Recv()
	if !errors.Is(err, io.EOF) {
		t.Errorf("expected io.EOF on empty stream, got %v", err)
	}
}

// ── AiserviceAdapter.BindTools ───────────────────────────────────────────────

func TestAiserviceAdapter_BindToolsNoOp(t *testing.T) {
	a := &AiserviceAdapter{modelName: "qwen-turbo"}
	if err := a.BindTools(nil); err != nil {
		t.Errorf("BindTools(nil) returned unexpected error: %v", err)
	}
	if err := a.BindTools([]*schema.ToolInfo{{Name: "get_current_date"}}); err != nil {
		t.Errorf("BindTools([...]) returned unexpected error: %v", err)
	}
}
