// Package adapter_test exercises the three provider adapters (ali, volc, dmxapi)
// using httptest.Server to intercept outbound HTTP calls.  No real provider
// credentials are needed.
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
)

// ----------------------------------------------------------------------------
// Shared test helpers
// ----------------------------------------------------------------------------

// mockRoute builds a ResolvedRoute pointing at the given test server URL.
func mockRoute(serverURL, apiKey, modelID string) *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		ProviderModelID: modelID,
		Provider: registry.ProviderInfo{
			BaseURL: serverURL,
			APIKey:  apiKey,
		},
	}
}

// writeChatJSON writes an OpenAI-compatible non-streaming chat response.
func writeChatJSON(w http.ResponseWriter, content, model string, promptToks, completionToks int) {
	resp := oaiChatResponse{
		ID:    "chatcmpl-test",
		Model: model,
		Choices: []struct {
			Message struct {
				Content          string        `json:"content"`
				ReasoningContent string        `json:"reasoning_content"`
				ToolCalls        []oaiToolCall `json:"tool_calls,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Message: struct {
					Content          string        `json:"content"`
					ReasoningContent string        `json:"reasoning_content"`
					ToolCalls        []oaiToolCall `json:"tool_calls,omitempty"`
				}{Content: content},
				FinishReason: "stop",
			},
		},
		Usage: &oaiUsage{
			PromptTokens:     promptToks,
			CompletionTokens: completionToks,
			TotalTokens:      promptToks + completionToks,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// writeChatStream writes a minimal OpenAI-compatible SSE stream.
func writeChatStream(w http.ResponseWriter, content, model string, promptToks, completionToks int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	// Content chunk.
	chunk := oaiStreamChunk{
		ID:    "chatcmpl-test",
		Model: model,
		Choices: []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Delta: struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				}{Content: content},
				FinishReason: "stop",
			},
		},
		Usage: &oaiUsage{
			PromptTokens:     promptToks,
			CompletionTokens: completionToks,
			TotalTokens:      promptToks + completionToks,
		},
	}
	b, _ := json.Marshal(chunk)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeEmbedJSON writes an OpenAI-compatible embedding response.
func writeEmbedJSON(w http.ResponseWriter, model string, vecs [][]float32) {
	data := make([]struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	}, len(vecs))
	for i, v := range vecs {
		data[i] = struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{Embedding: v, Index: i}
	}

	resp := oaiEmbedResponse{
		Data:  data,
		Model: model,
	}
	resp.Usage.TotalTokens = 10
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// sampleMessages builds a minimal chat request messages slice.
func sampleMessages() []aiservice.ChatMessage {
	return []aiservice.ChatMessage{
		{
			Role:    aiservice.MessageRoleUser,
			Content: aiservice.MessageContent{Text: "hello"},
		},
	}
}

// drainStream collects all chunks from a ChatStream channel.
func drainStream(t *testing.T, ch <-chan aiservice.ChatChunk) (content string, usage *aiservice.TokenUsage) {
	t.Helper()
	for c := range ch {
		content += c.Delta
		if c.IsFinal && c.Usage != nil {
			usage = c.Usage
		}
	}
	return
}

// ----------------------------------------------------------------------------
// AliAdapter tests
// ----------------------------------------------------------------------------

func TestAliAdapter_Chat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeChatJSON(w, "pong", "qwen-turbo", 5, 3)
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen-turbo")

	resp, err := a.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("Chat: unexpected error: %v", err)
	}
	if resp.Content != "pong" {
		t.Errorf("Content = %q; want %q", resp.Content, "pong")
	}
	if resp.Usage.PromptTokens != 5 {
		t.Errorf("PromptTokens = %d; want 5", resp.Usage.PromptTokens)
	}
	if resp.Provider != "ali" {
		t.Errorf("Provider = %q; want ali", resp.Provider)
	}
}

func TestAliAdapter_ChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatStream(w, "streaming response", "qwen-turbo", 4, 6)
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen-turbo")

	ch, err := a.ChatStream(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("ChatStream: unexpected error: %v", err)
	}

	content, usage := drainStream(t, ch)
	if content != "streaming response" {
		t.Errorf("streamed content = %q; want %q", content, "streaming response")
	}
	if usage == nil {
		t.Error("final chunk usage is nil; want non-nil")
	} else if usage.PromptTokens != 4 {
		t.Errorf("usage.PromptTokens = %d; want 4", usage.PromptTokens)
	}
}

// TestAliAdapter_Chat_ResponseFormat_JSON verifies the ResponseFormat field
// is translated to OpenAI-compatible {"type":"json_object"} on the wire and
// omitted entirely when the caller doesn't set it.
func TestAliAdapter_Chat_ResponseFormat_JSON(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		writeChatJSON(w, `{"k":"v"}`, "qwen-turbo", 1, 1)
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen-turbo")

	// With json_object → body must contain "response_format":{"type":"json_object"}.
	_, err := a.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages:       sampleMessages(),
		ResponseFormat: aiservice.ResponseFormatJSONObject,
	})
	if err != nil {
		t.Fatalf("Chat (json_object): %v", err)
	}
	if !bytes.Contains(gotBody, []byte(`"response_format":{"type":"json_object"}`)) {
		t.Errorf("json_object body missing response_format; got: %s", gotBody)
	}

	// Without ResponseFormat → field must be omitted (not "text", not empty object).
	gotBody = nil
	_, err = a.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("Chat (default): %v", err)
	}
	if bytes.Contains(gotBody, []byte(`"response_format"`)) {
		t.Errorf("default body should omit response_format; got: %s", gotBody)
	}
}

func TestAliAdapter_Chat_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal"}`))
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "key", "qwen-turbo")

	_, err := a.Chat(context.Background(), route, aiservice.ChatRequest{Messages: sampleMessages()})
	if err == nil {
		t.Error("expected error for HTTP 500; got nil")
	}
}

// ----------------------------------------------------------------------------
// VolcAdapter tests
// ----------------------------------------------------------------------------

func TestVolcAdapter_Chat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		writeChatJSON(w, "volc response", "deepseek-v3", 10, 8)
	}))
	defer srv.Close()

	v := NewVolcAdapter()
	route := mockRoute(srv.URL, "volc-key", "deepseek-v3")

	resp, err := v.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("Chat: unexpected error: %v", err)
	}
	if resp.Content != "volc response" {
		t.Errorf("Content = %q; want %q", resp.Content, "volc response")
	}
	if resp.Provider != "volc" {
		t.Errorf("Provider = %q; want volc", resp.Provider)
	}
}

func TestVolcAdapter_ChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatStream(w, "volc stream", "deepseek-v3", 7, 5)
	}))
	defer srv.Close()

	v := NewVolcAdapter()
	route := mockRoute(srv.URL, "volc-key", "deepseek-v3")

	ch, err := v.ChatStream(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("ChatStream: unexpected error: %v", err)
	}

	content, usage := drainStream(t, ch)
	if content != "volc stream" {
		t.Errorf("streamed content = %q; want %q", content, "volc stream")
	}
	if usage == nil {
		t.Error("final chunk usage is nil; want non-nil")
	}
}

func TestVolcAdapter_Embed(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3, 0.4}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		writeEmbedJSON(w, "doubao-embedding", [][]float32{vec})
	}))
	defer srv.Close()

	v := NewVolcAdapter()
	route := mockRoute(srv.URL, "volc-key", "doubao-embedding")

	resp, err := v.Embed(context.Background(), route, aiservice.EmbedRequest{
		Texts: []string{"hello world"},
	})
	if err != nil {
		t.Fatalf("Embed: unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("embeddings count = %d; want 1", len(resp.Embeddings))
	}
	if resp.Embeddings[0][0] != 0.1 {
		t.Errorf("first dim = %f; want 0.1", resp.Embeddings[0][0])
	}
	if resp.Provider != "volc" {
		t.Errorf("Provider = %q; want volc", resp.Provider)
	}
}

func TestVolcAdapter_Chat_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	v := NewVolcAdapter()
	route := mockRoute(srv.URL, "key", "model")

	_, err := v.Chat(context.Background(), route, aiservice.ChatRequest{Messages: sampleMessages()})
	if err == nil {
		t.Error("expected error for HTTP 502; got nil")
	}
}

// ----------------------------------------------------------------------------
// DMXAPIAdapter tests
// ----------------------------------------------------------------------------

func TestDMXAPIAdapter_Chat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		writeChatJSON(w, "dmx response", "qwen-turbo-latest", 6, 4)
	}))
	defer srv.Close()

	d := NewDMXAPIAdapter()
	route := mockRoute(srv.URL, "dmx-key", "qwen-turbo-latest")

	resp, err := d.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("Chat: unexpected error: %v", err)
	}
	if resp.Content != "dmx response" {
		t.Errorf("Content = %q; want %q", resp.Content, "dmx response")
	}
	if resp.Provider != "dmxapi" {
		t.Errorf("Provider = %q; want dmxapi", resp.Provider)
	}
}

func TestDMXAPIAdapter_ChatStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatStream(w, "dmx stream", "qwen-turbo-latest", 3, 9)
	}))
	defer srv.Close()

	d := NewDMXAPIAdapter()
	route := mockRoute(srv.URL, "dmx-key", "qwen-turbo-latest")

	ch, err := d.ChatStream(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("ChatStream: unexpected error: %v", err)
	}

	content, usage := drainStream(t, ch)
	if content != "dmx stream" {
		t.Errorf("streamed content = %q; want %q", content, "dmx stream")
	}
	if usage == nil {
		t.Error("final chunk usage is nil; want non-nil")
	}
}

func TestDMXAPIAdapter_Embed(t *testing.T) {
	vec := []float32{0.5, 0.6, 0.7}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		writeEmbedJSON(w, "qwen-embed", [][]float32{vec})
	}))
	defer srv.Close()

	d := NewDMXAPIAdapter()
	route := mockRoute(srv.URL, "dmx-key", "qwen-embed")

	resp, err := d.Embed(context.Background(), route, aiservice.EmbedRequest{
		Texts: []string{"test text"},
	})
	if err != nil {
		t.Fatalf("Embed: unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("embeddings count = %d; want 1", len(resp.Embeddings))
	}
	if resp.Embeddings[0][0] != 0.5 {
		t.Errorf("first dim = %f; want 0.5", resp.Embeddings[0][0])
	}
}

func TestDMXAPIAdapter_Rerank(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			http.NotFound(w, r)
			return
		}
		// Parse request to validate model and query fields.
		var req dmxapiRerankRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		rrResp := dmxapiRerankResponse{
			Results: []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
				Document       string  `json:"document,omitempty"`
			}{
				{Index: 1, RelevanceScore: 0.9, Document: req.Documents[1]},
				{Index: 0, RelevanceScore: 0.4, Document: req.Documents[0]},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(rrResp)
	}))
	defer srv.Close()

	d := NewDMXAPIAdapter()
	route := mockRoute(srv.URL, "dmx-key", "qwen3-rerank")

	resp, err := d.Rerank(context.Background(), route, aiservice.RerankRequest{
		Query:     "best result",
		Documents: []string{"doc A", "doc B"},
		TopN:      2,
	})
	if err != nil {
		t.Fatalf("Rerank: unexpected error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("results count = %d; want 2", len(resp.Results))
	}
	// First result should be the higher-scored doc (index 1).
	if resp.Results[0].Index != 1 {
		t.Errorf("Results[0].Index = %d; want 1", resp.Results[0].Index)
	}
	if resp.Results[0].Score != 0.9 {
		t.Errorf("Results[0].Score = %f; want 0.9", resp.Results[0].Score)
	}
	if resp.Provider != "dmxapi" {
		t.Errorf("Provider = %q; want dmxapi", resp.Provider)
	}
}

// ----------------------------------------------------------------------------
// Vision (multipart message) tests
// ----------------------------------------------------------------------------

func TestAliAdapter_Chat_Vision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request body contains image_url content part.
		var body oaiChatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(body.Messages) == 0 {
			http.Error(w, "no messages", http.StatusBadRequest)
			return
		}
		// Content should be a slice (multipart) for vision.
		contentBytes, _ := json.Marshal(body.Messages[0].Content)
		var parts []oaiContentPart
		if err := json.Unmarshal(contentBytes, &parts); err != nil || len(parts) < 2 {
			http.Error(w, "expected multipart content", http.StatusBadRequest)
			return
		}
		writeChatJSON(w, "image described", "qwen-vl", 20, 10)
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen-vl")

	resp, err := a.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role: aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{
					Parts: []aiservice.MessagePart{
						{Type: aiservice.MessagePartTypeText, Text: "describe this image"},
						{
							Type:     aiservice.MessagePartTypeImageURL,
							ImageURL: &aiservice.ImageURL{URL: "https://example.com/img.jpg"},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Vision Chat: unexpected error: %v", err)
	}
	if resp.Content != "image described" {
		t.Errorf("Content = %q; want %q", resp.Content, "image described")
	}
}

// ----------------------------------------------------------------------------
// Interface compliance — compile-time assertions in production files, but
// also verified here with explicit type assertions.
// ----------------------------------------------------------------------------

func TestAdapterInterfaceCompliance(t *testing.T) {
	var _ ChatAdapter = NewAliAdapter()
	var _ EmbedAdapter = NewAliAdapter()

	var _ ChatAdapter = NewVolcAdapter()
	var _ EmbedAdapter = NewVolcAdapter()

	var _ ChatAdapter = NewDMXAPIAdapter()
	var _ EmbedAdapter = NewDMXAPIAdapter()
	var _ RerankAdapter = NewDMXAPIAdapter()
}

// ----------------------------------------------------------------------------
// Stream: no spurious duplicate IsFinal chunk (P1#2)
// ----------------------------------------------------------------------------

// writeChatStreamWithSeparateFinish simulates a provider that sends a
// finish_reason chunk first, then a [DONE] line — the common OpenAI behaviour.
// The test verifies that runOAIStream emits exactly one IsFinal=true chunk.
func writeChatStreamWithSeparateFinish(w http.ResponseWriter, content, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	// Chunk 1: content delta with no finish_reason.
	delta := oaiStreamChunk{
		ID:    "chatcmpl-test",
		Model: model,
		Choices: []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Delta: struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				}{Content: content},
			},
		},
	}
	b, _ := json.Marshal(delta)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)

	// Chunk 2: finish_reason="stop" with usage.
	finish := oaiStreamChunk{
		ID:    "chatcmpl-test",
		Model: model,
		Choices: []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				FinishReason: "stop",
			},
		},
		Usage: &oaiUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	}
	b, _ = json.Marshal(finish)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)

	// [DONE] sentinel follows the finish_reason chunk.
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeChatStreamWithLateUsage simulates a provider that sends a
// finish_reason chunk first, THEN a separate usage-only chunk (choices=[]),
// THEN [DONE]. This mirrors real DMXAPI-proxied DeepSeek / some Claude
// variants. Before the 2026-04-17 fix the late usage chunk was silently
// dropped, causing sop_node_run.total_tokens to stay 0 in production.
func writeChatStreamWithLateUsage(w http.ResponseWriter, content, model string, prompt, completion int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	// Chunk 1: content delta, no finish_reason, no usage.
	delta := oaiStreamChunk{
		ID:    "chatcmpl-test",
		Model: model,
		Choices: []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		}{
			{Delta: struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			}{Content: content}},
		},
	}
	b, _ := json.Marshal(delta)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)

	// Chunk 2: finish_reason="stop" WITHOUT usage.
	finish := oaiStreamChunk{
		ID:    "chatcmpl-test",
		Model: model,
		Choices: []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		}{
			{FinishReason: "stop"},
		},
	}
	b, _ = json.Marshal(finish)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)

	// Chunk 3: usage-only chunk (choices=[]).
	usageOnly := oaiStreamChunk{
		ID:    "chatcmpl-test",
		Model: model,
		Usage: &oaiUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion},
	}
	b, _ = json.Marshal(usageOnly)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", b)

	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// TestRunOAIStream_LateUsageChunkCaptured regression-tests the late-usage bug
// where a provider sends usage in a separate chunk after the finish_reason
// chunk. Before the fix, the usage-only chunk was silently dropped and
// sop_node_run.total_tokens stayed 0. The fix defers IsFinal to a single
// terminal chunk emitted after the loop that carries the aggregated usage.
func TestRunOAIStream_LateUsageChunkCaptured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatStreamWithLateUsage(w, "hello", "test-model", 5, 3)
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "test-model")

	ch, err := a.ChatStream(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("ChatStream: unexpected error: %v", err)
	}

	content, usage := drainStream(t, ch)
	if content != "hello" {
		t.Errorf("content = %q; want %q", content, "hello")
	}
	if usage == nil {
		t.Fatal("usage = nil; want non-nil (late usage chunk should be captured)")
	}
	if usage.TotalTokens != 8 {
		t.Errorf("usage.TotalTokens = %d; want 8", usage.TotalTokens)
	}
	if usage.PromptTokens != 5 {
		t.Errorf("usage.PromptTokens = %d; want 5", usage.PromptTokens)
	}
	if usage.CompletionTokens != 3 {
		t.Errorf("usage.CompletionTokens = %d; want 3", usage.CompletionTokens)
	}
}

// TestRunOAIStream_ParseError_PopulatesErrField checks that a malformed SSE
// frame produces a terminal chunk with Err set to a non-nil error — so
// consumers can do `if chunk.IsFinal && chunk.Err != nil` instead of string-
// matching FinishReason prefixes like "parse_error:".
func TestRunOAIStream_ParseError_PopulatesErrField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {not valid json\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "test-model")

	ch, err := a.ChatStream(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("ChatStream: unexpected error: %v", err)
	}

	var terminal aiservice.ChatChunk
	for c := range ch {
		if c.IsFinal {
			terminal = c
		}
	}

	if terminal.Err == nil {
		t.Fatal("terminal.Err is nil; expected non-nil on parse error")
	}
	if !strings.Contains(terminal.FinishReason, "parse_error") {
		t.Errorf("FinishReason = %q; expected to contain 'parse_error'", terminal.FinishReason)
	}
}

func TestRunOAIStream_NoSpuriousDuplicateFinalChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatStreamWithSeparateFinish(w, "hello", "test-model")
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "test-model")

	ch, err := a.ChatStream(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("ChatStream: unexpected error: %v", err)
	}

	finalCount := 0
	for c := range ch {
		if c.IsFinal {
			finalCount++
		}
	}

	if finalCount != 1 {
		t.Errorf("IsFinal chunk count = %d; want exactly 1 (no spurious duplicate)", finalCount)
	}
}

// ----------------------------------------------------------------------------
// Stream: Provider and Model fields propagated on every chunk (P1#1)
// ----------------------------------------------------------------------------

func TestRunOAIStream_ProviderModelPropagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatStream(w, "chunk content", "actual-model-from-provider", 4, 6)
	}))
	defer srv.Close()

	a := NewAliAdapter()
	// The route has a different default model to confirm provider-reported model wins.
	route := mockRoute(srv.URL, "test-key", "default-model")

	ch, err := a.ChatStream(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("ChatStream: unexpected error: %v", err)
	}

	// Contract: Provider and Model propagate on EVERY emitted chunk (final and non-final).
	// Model tracks the provider-reported value (resolvedModel) with the route's
	// default as the initial fallback, so it's never empty.
	for c := range ch {
		if c.Provider != "ali" {
			t.Errorf("chunk.Provider = %q; want %q", c.Provider, "ali")
		}
		if c.Model != "actual-model-from-provider" {
			t.Errorf("chunk.Model = %q; want %q (IsFinal=%v)", c.Model, "actual-model-from-provider", c.IsFinal)
		}
	}
}

// ----------------------------------------------------------------------------
// AliAdapter.Embed: roundtrip test with httptest server (P1#4)
// ----------------------------------------------------------------------------

// writeDashScopeEmbedJSON writes a DashScope native embedding response.
func writeDashScopeEmbedJSON(w http.ResponseWriter, vecs [][]float32, totalTokens int) {
	type embedding struct {
		TextIndex int       `json:"text_index"`
		Embedding []float32 `json:"embedding"`
	}
	type output struct {
		Embeddings []embedding `json:"embeddings"`
	}
	type usage struct {
		TotalTokens int `json:"total_tokens"`
	}
	type resp struct {
		Output output `json:"output"`
		Usage  usage  `json:"usage"`
	}

	r := resp{Usage: usage{TotalTokens: totalTokens}}
	for i, v := range vecs {
		r.Output.Embeddings = append(r.Output.Embeddings, embedding{TextIndex: i, Embedding: v})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(r)
}

func TestAliAdapter_Embed_Roundtrip(t *testing.T) {
	vec := []float32{0.1, 0.2, 0.3}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify it hits the native embed path (derived from BaseURL).
		if !strings.Contains(r.URL.Path, "text-embedding") {
			http.NotFound(w, r)
			return
		}
		// Verify request body has the model and texts.
		body, _ := io.ReadAll(r.Body)
		var req dashscopeEmbedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Model != "text-embedding-v4" {
			http.Error(w, "unexpected model", http.StatusBadRequest)
			return
		}
		if len(req.Input.Texts) != 1 || req.Input.Texts[0] != "hello embed" {
			http.Error(w, "unexpected texts", http.StatusBadRequest)
			return
		}
		writeDashScopeEmbedJSON(w, [][]float32{vec}, 10)
	}))
	defer srv.Close()

	a := NewAliAdapter()
	// BaseURL uses the OAI-compat path; Embed should replace it to derive native path.
	route := &registry.ResolvedRoute{
		ProviderModelID: "text-embedding-v4",
		Provider: registry.ProviderInfo{
			// Simulate the compatible-mode base URL that production uses.
			BaseURL: srv.URL + "/compatible-mode/v1",
			APIKey:  "test-key",
		},
	}

	resp, err := a.Embed(context.Background(), route, aiservice.EmbedRequest{
		Texts:     []string{"hello embed"},
		Dimension: 3,
	})
	if err != nil {
		t.Fatalf("Embed: unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("embeddings count = %d; want 1", len(resp.Embeddings))
	}
	if resp.Embeddings[0][0] != 0.1 {
		t.Errorf("Embeddings[0][0] = %f; want 0.1", resp.Embeddings[0][0])
	}
	if resp.Dimension != 3 {
		t.Errorf("Dimension = %d; want 3", resp.Dimension)
	}
	if resp.TotalTokens != 10 {
		t.Errorf("TotalTokens = %d; want 10", resp.TotalTokens)
	}
	if resp.Provider != "ali" {
		t.Errorf("Provider = %q; want ali", resp.Provider)
	}
}

// ----------------------------------------------------------------------------
// buildOAIMessages: table-driven tests (P2#6)
// ----------------------------------------------------------------------------

func TestBuildOAIMessages_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		input   []aiservice.ChatMessage
		wantLen int
		check   func(t *testing.T, out []oaiMessage)
	}{
		{
			name: "text-only message",
			input: []aiservice.ChatMessage{
				{Role: aiservice.MessageRoleUser, Content: aiservice.MessageContent{Text: "hello"}},
			},
			wantLen: 1,
			check: func(t *testing.T, out []oaiMessage) {
				t.Helper()
				if out[0].Content != "hello" {
					t.Errorf("content = %v; want %q", out[0].Content, "hello")
				}
				if out[0].Role != "user" {
					t.Errorf("role = %q; want user", out[0].Role)
				}
			},
		},
		{
			name: "text + image_url parts",
			input: []aiservice.ChatMessage{
				{
					Role: aiservice.MessageRoleUser,
					Content: aiservice.MessageContent{
						Parts: []aiservice.MessagePart{
							{Type: aiservice.MessagePartTypeText, Text: "describe"},
							{Type: aiservice.MessagePartTypeImageURL, ImageURL: &aiservice.ImageURL{URL: "https://example.com/img.jpg"}},
						},
					},
				},
			},
			wantLen: 1,
			check: func(t *testing.T, out []oaiMessage) {
				t.Helper()
				parts, ok := out[0].Content.([]oaiContentPart)
				if !ok {
					t.Fatalf("content is not []oaiContentPart; got %T", out[0].Content)
				}
				if len(parts) != 2 {
					t.Fatalf("parts count = %d; want 2", len(parts))
				}
				if parts[0].Type != "text" || parts[0].Text != "describe" {
					t.Errorf("parts[0] = %+v; want text 'describe'", parts[0])
				}
				if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "https://example.com/img.jpg" {
					t.Errorf("parts[1] = %+v; want image_url", parts[1])
				}
			},
		},
		{
			name: "empty Parts falls back to Text",
			input: []aiservice.ChatMessage{
				{
					Role: aiservice.MessageRoleAssistant,
					Content: aiservice.MessageContent{
						Text:  "fallback text",
						Parts: nil,
					},
				},
			},
			wantLen: 1,
			check: func(t *testing.T, out []oaiMessage) {
				t.Helper()
				if out[0].Content != "fallback text" {
					t.Errorf("content = %v; want %q", out[0].Content, "fallback text")
				}
			},
		},
		{
			name: "ImageURL nil guard — image_url part without ImageURL is skipped",
			input: []aiservice.ChatMessage{
				{
					Role: aiservice.MessageRoleUser,
					Content: aiservice.MessageContent{
						Parts: []aiservice.MessagePart{
							{Type: aiservice.MessagePartTypeText, Text: "text only"},
							{Type: aiservice.MessagePartTypeImageURL, ImageURL: nil}, // nil → should be skipped
						},
					},
				},
			},
			wantLen: 1,
			check: func(t *testing.T, out []oaiMessage) {
				t.Helper()
				parts, ok := out[0].Content.([]oaiContentPart)
				if !ok {
					t.Fatalf("content is not []oaiContentPart; got %T", out[0].Content)
				}
				// The nil image_url part should be skipped — only the text part remains.
				if len(parts) != 1 {
					t.Errorf("parts count = %d; want 1 (nil image_url skipped)", len(parts))
				}
				if parts[0].Type != "text" {
					t.Errorf("parts[0].Type = %q; want text", parts[0].Type)
				}
			},
		},
		{
			// Hotfix aiservice-tool-message-roundtrip: regression test for the
			// DMXAPI HTTP 400 "missing field tool_call_id" symptom. Before the
			// fix, oaiMessage had no ToolCallID field and any role=tool message
			// posted by the ReAct loop landed in the wire body without the id,
			// terminating runs with model_error before the user saw any tool
			// output. This case proves buildOAIMessage propagates the field.
			name: "role=tool propagates ToolCallID",
			input: []aiservice.ChatMessage{
				{
					Role:       aiservice.MessageRoleTool,
					Content:    aiservice.MessageContent{Text: `{"result":"sunny"}`},
					ToolCallID: "call_abc123",
				},
			},
			wantLen: 1,
			check: func(t *testing.T, out []oaiMessage) {
				t.Helper()
				if out[0].Role != "tool" {
					t.Errorf("role = %q; want tool", out[0].Role)
				}
				if out[0].ToolCallID != "call_abc123" {
					t.Errorf("ToolCallID = %q; want call_abc123 (root cause: DMXAPI rejects missing field)", out[0].ToolCallID)
				}
				if out[0].Content != `{"result":"sunny"}` {
					t.Errorf("content = %v; want JSON tool result", out[0].Content)
				}
			},
		},
		{
			// Companion to ToolCallID: when the assistant turn in the prior
			// round requested a tool call, Eino reposts that message verbatim
			// alongside the tool result so the provider can correlate. The
			// tool_calls array on the assistant message must survive the
			// aiservice → OAI translation.
			name: "role=assistant propagates ToolCalls array",
			input: []aiservice.ChatMessage{
				{
					Role:    aiservice.MessageRoleAssistant,
					Content: aiservice.MessageContent{Text: ""},
					ToolCalls: []aiservice.ToolCall{
						{
							ID:   "call_abc123",
							Type: "function",
							Function: aiservice.ToolCallFunction{
								Name:      "web_search",
								Arguments: `{"query":"weather"}`,
							},
						},
					},
				},
			},
			wantLen: 1,
			check: func(t *testing.T, out []oaiMessage) {
				t.Helper()
				if len(out[0].ToolCalls) != 1 {
					t.Fatalf("ToolCalls count = %d; want 1", len(out[0].ToolCalls))
				}
				tc := out[0].ToolCalls[0]
				if tc.ID != "call_abc123" {
					t.Errorf("ToolCalls[0].ID = %q; want call_abc123", tc.ID)
				}
				if tc.Type != "function" {
					t.Errorf("ToolCalls[0].Type = %q; want function", tc.Type)
				}
				if tc.Function.Name != "web_search" {
					t.Errorf("ToolCalls[0].Function.Name = %q; want web_search", tc.Function.Name)
				}
				if tc.Function.Arguments != `{"query":"weather"}` {
					t.Errorf("ToolCalls[0].Function.Arguments = %q; want JSON args", tc.Function.Arguments)
				}
			},
		},
		{
			// Backward-compat guard: pre-Agent-mode callers (SOP / chatbot)
			// never set ToolCallID or ToolCalls. omitempty must keep the wire
			// shape identical so the marshaled body stays byte-for-byte the
			// same as before this hotfix.
			name: "non-tool message omits tool_call_id and tool_calls when empty",
			input: []aiservice.ChatMessage{
				{
					Role:    aiservice.MessageRoleUser,
					Content: aiservice.MessageContent{Text: "plain question"},
				},
			},
			wantLen: 1,
			check: func(t *testing.T, out []oaiMessage) {
				t.Helper()
				if out[0].ToolCallID != "" {
					t.Errorf("ToolCallID should be empty for non-tool role; got %q", out[0].ToolCallID)
				}
				if out[0].ToolCalls != nil {
					t.Errorf("ToolCalls should be nil for assistant without tool_calls; got %+v", out[0].ToolCalls)
				}
				// Marshal-time verification: omitempty must drop both fields
				// from the JSON wire format.
				body, err := json.Marshal(out[0])
				if err != nil {
					t.Fatalf("json.Marshal: %v", err)
				}
				bodyStr := string(body)
				if strings.Contains(bodyStr, "tool_call_id") {
					t.Errorf("marshaled body contains tool_call_id; want field dropped via omitempty: %s", bodyStr)
				}
				if strings.Contains(bodyStr, "tool_calls") {
					t.Errorf("marshaled body contains tool_calls; want field dropped via omitempty: %s", bodyStr)
				}
			},
		},
		{
			name: "unknown MessagePartType is skipped",
			input: []aiservice.ChatMessage{
				{
					Role: aiservice.MessageRoleUser,
					Content: aiservice.MessageContent{
						Parts: []aiservice.MessagePart{
							{Type: aiservice.MessagePartTypeText, Text: "valid"},
							{Type: aiservice.MessagePartType("unknown_type"), Text: "ignored"},
						},
					},
				},
			},
			wantLen: 1,
			check: func(t *testing.T, out []oaiMessage) {
				t.Helper()
				parts, ok := out[0].Content.([]oaiContentPart)
				if !ok {
					t.Fatalf("content is not []oaiContentPart; got %T", out[0].Content)
				}
				// Unknown part type should be silently skipped.
				if len(parts) != 1 {
					t.Errorf("parts count = %d; want 1 (unknown type skipped)", len(parts))
				}
				if parts[0].Text != "valid" {
					t.Errorf("parts[0].Text = %q; want valid", parts[0].Text)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := buildOAIMessages(tc.input)
			if len(out) != tc.wantLen {
				t.Fatalf("len(out) = %d; want %d", len(out), tc.wantLen)
			}
			tc.check(t, out)
		})
	}
}

// ----------------------------------------------------------------------------
// oaiUsage.extractReasoningTokens: T3 protocol audit coverage
// ----------------------------------------------------------------------------

// TestOAIUsage_ExtractReasoningTokens covers the five wire-path variants the
// T2 AiHubMix protocol audit found in production:
//   - nested OpenAI-style (gpt-5/o1/o3/o4)
//   - flat DeepSeek-style
//   - neither path present (Claude folds into completion_tokens)
//   - nil receiver safety
//   - nested=0 with non-zero flat (fallback)
func TestOAIUsage_ExtractReasoningTokens(t *testing.T) {
	cases := []struct {
		name     string
		payload  string // JSON to unmarshal
		useNil   bool   // if true, call on nil receiver
		expected int
	}{
		{
			name:     "nested OpenAI-style",
			payload:  `{"prompt_tokens":10,"completion_tokens":200,"total_tokens":210,"completion_tokens_details":{"reasoning_tokens":42}}`,
			expected: 42,
		},
		{
			name:     "flat DeepSeek-style",
			payload:  `{"prompt_tokens":10,"completion_tokens":200,"total_tokens":210,"reasoning_tokens":17}`,
			expected: 17,
		},
		{
			name:     "none (Claude)",
			payload:  `{"prompt_tokens":10,"completion_tokens":200,"total_tokens":210}`,
			expected: 0,
		},
		{
			name:     "nil receiver",
			useNil:   true,
			expected: 0,
		},
		{
			name:     "nested=0 fallback to flat=5",
			payload:  `{"completion_tokens_details":{"reasoning_tokens":0},"reasoning_tokens":5}`,
			expected: 5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got int
			if tc.useNil {
				var u *oaiUsage
				got = u.extractReasoningTokens()
			} else {
				var u oaiUsage
				if err := json.Unmarshal([]byte(tc.payload), &u); err != nil {
					t.Fatalf("unmarshal failed: %v", err)
				}
				got = u.extractReasoningTokens()
			}
			if got != tc.expected {
				t.Errorf("extractReasoningTokens() = %d, want %d", got, tc.expected)
			}
		})
	}
}

// TestOAIChatRequest_ReasoningEffortOmitempty verifies that an empty
// ReasoningEffort and MaxCompletionTokens=0 are omitted from the marshalled
// JSON body so providers that don't know these fields just see a clean request.
func TestOAIChatRequest_ReasoningEffortOmitempty(t *testing.T) {
	req := oaiChatRequest{
		Model:    "test-model",
		Messages: []oaiMessage{{Role: "user", Content: "hi"}},
		Stream:   false,
		// ReasoningEffort and MaxCompletionTokens intentionally zero.
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if bytes.Contains(b, []byte(`"reasoning_effort"`)) {
		t.Errorf("empty reasoning_effort should be omitted; got: %s", b)
	}
	if bytes.Contains(b, []byte(`"max_completion_tokens"`)) {
		t.Errorf("zero max_completion_tokens should be omitted; got: %s", b)
	}

	// Sanity: when set, they must appear.
	req.ReasoningEffort = "high"
	req.MaxCompletionTokens = 1024
	b, err = json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal (set) failed: %v", err)
	}
	if !bytes.Contains(b, []byte(`"reasoning_effort":"high"`)) {
		t.Errorf("set reasoning_effort should appear; got: %s", b)
	}
	if !bytes.Contains(b, []byte(`"max_completion_tokens":1024`)) {
		t.Errorf("set max_completion_tokens should appear; got: %s", b)
	}
}

// ----------------------------------------------------------------------------
// Tool forwarding (regression for the Agent-mode "no tools to LLM" gap, 2026-05-22)
// ----------------------------------------------------------------------------
//
// Until this fix, aiservice.ChatRequest.Tools was a declared but un-wired
// field: the three OpenAI-compatible adapters (ali / volc / dmxapi) marshaled
// the request without including `tools`, so any caller that bound function-
// calling schemas (notably the Agent-mode Eino ReAct runner) silently shipped
// a request that omitted the tools entirely. LLMs then responded "I cannot
// search the internet" because they were never told they had web_search etc.
//
// These tests pin three invariants per adapter:
//
//  1. When req.Tools is non-empty, the marshaled JSON body MUST include the
//     `tools` array with each function's name + JSON-schema parameters.
//  2. When req.Tools is empty, the `tools` key MUST be omitted entirely (the
//     omitempty tag preserves pre-Agent-mode wire shape for SOP / chatbot).
//  3. When the provider responds with tool_calls, the adapter MUST surface
//     them on ChatResponse.ToolCalls so the Eino bridge can drive the next
//     ReAct step.

// captureBody is a writeChatJSON variant that copies the request body into the
// pointer before responding, so a test can inspect what the adapter sent.
func captureBody(t *testing.T, captured *[]byte) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		*captured = body
		writeChatJSON(w, "ok", "test-model", 1, 1)
	}
}

// writeChatJSONWithToolCalls returns a provider-style response where the
// assistant emits one tool_call instead of plain text. Used to exercise the
// extractToolCalls path.
func writeChatJSONWithToolCalls(w http.ResponseWriter, callID, name, args, model string) {
	resp := oaiChatResponse{
		ID:    "chatcmpl-test",
		Model: model,
		Choices: []struct {
			Message struct {
				Content          string        `json:"content"`
				ReasoningContent string        `json:"reasoning_content"`
				ToolCalls        []oaiToolCall `json:"tool_calls,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Message: struct {
					Content          string        `json:"content"`
					ReasoningContent string        `json:"reasoning_content"`
					ToolCalls        []oaiToolCall `json:"tool_calls,omitempty"`
				}{
					ToolCalls: []oaiToolCall{
						{
							ID:       callID,
							Type:     "function",
							Function: oaiToolCallFunction{Name: name, Arguments: args},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
		Usage: &oaiUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// sampleTools is a representative slice mirroring what the Agent runner binds:
// one tool with name + description + a minimal JSON Schema parameters map.
func sampleTools() []aiservice.Tool {
	return []aiservice.Tool{
		{
			Type: "function",
			Function: aiservice.ToolFunction{
				Name:        "web_search",
				Description: "Search the web for recent news",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{"type": "string"},
					},
					"required": []string{"query"},
				},
			},
		},
	}
}

// assertToolsForwarded checks that a captured request body contains the tools
// array and the expected function name + a parameters object. Provider-level
// JSON ordering differences are tolerated by substring checks.
func assertToolsForwarded(t *testing.T, captured []byte, fnName string) {
	t.Helper()
	if !bytes.Contains(captured, []byte(`"tools":`)) {
		t.Fatalf("tools key missing from wire body: %s", captured)
	}
	if !bytes.Contains(captured, []byte(`"name":"`+fnName+`"`)) {
		t.Fatalf("function name %q missing from wire body: %s", fnName, captured)
	}
	if !bytes.Contains(captured, []byte(`"parameters":`)) {
		t.Fatalf("parameters object missing from wire body: %s", captured)
	}
}

// assertNoTools checks the opposite — request body has no tools key at all.
func assertNoTools(t *testing.T, captured []byte) {
	t.Helper()
	if bytes.Contains(captured, []byte(`"tools"`)) {
		t.Fatalf("tools key should be omitted when req.Tools is empty; got: %s", captured)
	}
}

func TestAliAdapter_Chat_ForwardsTools(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(captureBody(t, &captured))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen-turbo")
	_, err := a.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
		Tools:    sampleTools(),
	})
	if err != nil {
		t.Fatalf("Chat: unexpected error: %v", err)
	}
	assertToolsForwarded(t, captured, "web_search")
}

func TestVolcAdapter_Chat_ForwardsTools(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(captureBody(t, &captured))
	defer srv.Close()

	v := NewVolcAdapter()
	route := mockRoute(srv.URL, "test-key", "deepseek-v3")
	_, err := v.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
		Tools:    sampleTools(),
	})
	if err != nil {
		t.Fatalf("Chat: unexpected error: %v", err)
	}
	assertToolsForwarded(t, captured, "web_search")
}

func TestDMXAPIAdapter_Chat_ForwardsTools(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(captureBody(t, &captured))
	defer srv.Close()

	d := NewDMXAPIAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen-turbo-latest")
	_, err := d.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
		Tools:    sampleTools(),
	})
	if err != nil {
		t.Fatalf("Chat: unexpected error: %v", err)
	}
	assertToolsForwarded(t, captured, "web_search")
}

func TestAliAdapter_Chat_EmptyToolsOmitted(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(captureBody(t, &captured))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen-turbo")
	_, err := a.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
	})
	if err != nil {
		t.Fatalf("Chat: unexpected error: %v", err)
	}
	assertNoTools(t, captured)
}

func TestAliAdapter_Chat_ExtractsToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatJSONWithToolCalls(w, "call-001", "web_search", `{"query":"AI news"}`, "qwen-turbo")
	}))
	defer srv.Close()

	a := NewAliAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen-turbo")
	resp, err := a.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
		Tools:    sampleTools(),
	})
	if err != nil {
		t.Fatalf("Chat: unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls length = %d; want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call-001" {
		t.Errorf("ToolCall.ID = %q; want call-001", tc.ID)
	}
	if tc.Function.Name != "web_search" {
		t.Errorf("ToolCall.Function.Name = %q; want web_search", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"query":"AI news"}` {
		t.Errorf("ToolCall.Function.Arguments = %q; want %q", tc.Function.Arguments, `{"query":"AI news"}`)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q; want tool_calls", resp.FinishReason)
	}
}

func TestVolcAdapter_Chat_ExtractsToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatJSONWithToolCalls(w, "call-volc-1", "kb_search", `{"query":"GORM index"}`, "deepseek-v3")
	}))
	defer srv.Close()

	v := NewVolcAdapter()
	route := mockRoute(srv.URL, "test-key", "deepseek-v3")
	resp, err := v.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
		Tools:    sampleTools(),
	})
	if err != nil {
		t.Fatalf("Chat: unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "kb_search" {
		t.Fatalf("ToolCalls not surfaced: %+v", resp.ToolCalls)
	}
}

func TestDMXAPIAdapter_Chat_ExtractsToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatJSONWithToolCalls(w, "call-dmx-1", "image_gen", `{"prompt":"a cat"}`, "qwen-turbo-latest")
	}))
	defer srv.Close()

	d := NewDMXAPIAdapter()
	route := mockRoute(srv.URL, "test-key", "qwen-turbo-latest")
	resp, err := d.Chat(context.Background(), route, aiservice.ChatRequest{
		Messages: sampleMessages(),
		Tools:    sampleTools(),
	})
	if err != nil {
		t.Fatalf("Chat: unexpected error: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Function.Name != "image_gen" {
		t.Fatalf("ToolCalls not surfaced: %+v", resp.ToolCalls)
	}
}

func TestBuildOAITools_EmptyReturnsNil(t *testing.T) {
	if got := buildOAITools(nil); got != nil {
		t.Errorf("buildOAITools(nil) = %v; want nil", got)
	}
	if got := buildOAITools([]aiservice.Tool{}); got != nil {
		t.Errorf("buildOAITools([]) = %v; want nil", got)
	}
}

func TestExtractToolCalls_EmptyReturnsNil(t *testing.T) {
	if got := extractToolCalls(nil); got != nil {
		t.Errorf("extractToolCalls(nil) = %v; want nil", got)
	}
	if got := extractToolCalls([]oaiToolCall{}); got != nil {
		t.Errorf("extractToolCalls([]) = %v; want nil", got)
	}
}
