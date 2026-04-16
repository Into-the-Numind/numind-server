// Package adapter_test exercises the three provider adapters (ali, volc, dmxapi)
// using httptest.Server to intercept outbound HTTP calls.  No real provider
// credentials are needed.
package adapter

import (
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
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Message: struct {
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

	for c := range ch {
		if c.Provider != "ali" {
			t.Errorf("chunk.Provider = %q; want %q", c.Provider, "ali")
		}
		if c.IsFinal {
			// The model from the SSE chunk should override the default.
			if c.Model != "actual-model-from-provider" {
				t.Errorf("final chunk.Model = %q; want %q", c.Model, "actual-model-from-provider")
			}
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
