// Package adapter_test exercises the three provider adapters (ali, volc, dmxapi)
// using httptest.Server to intercept outbound HTTP calls.  No real provider
// credentials are needed.
package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
