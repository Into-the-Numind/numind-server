package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/httpclient"
)

// Note: HTTP error helpers (wrapHTTPClientErr, wrapHTTPStatusErr, isTimeoutErr)
// are defined in ali.go (same package) and shared across all three adapters.

// Compile-time interface checks.
var _ ChatAdapter = (*VolcAdapter)(nil)
var _ EmbedAdapter = (*VolcAdapter)(nil)

// VolcAdapter implements ChatAdapter and EmbedAdapter for Volcengine Ark
// (Doubao / DeepSeek models) using the OpenAI-compatible API.
//
// All HTTP calls go through internal/pkg/httpclient.Client — no bare
// net/http request construction is used (verified by CI grep check).
type VolcAdapter struct {
	client *httpclient.Client
}

// NewVolcAdapter creates a VolcAdapter backed by the shared httpclient pool.
func NewVolcAdapter() *VolcAdapter {
	return &VolcAdapter{
		client: httpclient.NewClient(nil), // uses DefaultConfig
	}
}

// Name returns the adapter identifier.
func (v *VolcAdapter) Name() string { return "volc" }

// ProviderType returns the provider category.
func (v *VolcAdapter) ProviderType() string { return "ark" }

// Capabilities lists the capabilities this adapter supports.
func (v *VolcAdapter) Capabilities() []string { return []string{"chat", "embed"} }

// Chat performs a non-streaming chat completion against the Volcengine Ark
// OpenAI-compatible endpoint.
func (v *VolcAdapter) Chat(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	body, err := json.Marshal(oaiChatRequest{
		Model:          route.ProviderModelID,
		Messages:       buildOAIMessages(req.Messages),
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
		Stream:         false,
		ResponseFormat: translateResponseFormat(req.ResponseFormat),
	})
	if err != nil {
		return nil, fmt.Errorf("volc.Chat: marshal: %w", err)
	}

	respBytes, err := v.doPost(ctx, route, "/chat/completions", body)
	if err != nil {
		return nil, fmt.Errorf("volc.Chat: %w", err)
	}

	var oaiResp oaiChatResponse
	if err := json.Unmarshal(respBytes, &oaiResp); err != nil {
		return nil, fmt.Errorf("volc.Chat: decode: %w", err)
	}
	if oaiResp.Error != nil {
		return nil, fmt.Errorf("volc.Chat: provider error: %s", oaiResp.Error.Message)
	}
	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("volc.Chat: empty choices")
	}

	usage := aiservice.TokenUsage{}
	if oaiResp.Usage != nil {
		usage = aiservice.TokenUsage{
			PromptTokens:     oaiResp.Usage.PromptTokens,
			CompletionTokens: oaiResp.Usage.CompletionTokens,
			TotalTokens:      oaiResp.Usage.TotalTokens,
		}
	}

	return &aiservice.ChatResponse{
		Content:          oaiResp.Choices[0].Message.Content,
		ReasoningContent: oaiResp.Choices[0].Message.ReasoningContent,
		FinishReason:     oaiResp.Choices[0].FinishReason,
		Usage:            usage,
		Model:            oaiResp.Model,
		Provider:         v.Name(),
	}, nil
}

// ChatStream starts a streaming chat completion and returns a channel of chunks.
// stream_options.include_usage=true ensures the final SSE chunk carries usage data.
func (v *VolcAdapter) ChatStream(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	body, err := json.Marshal(oaiChatRequest{
		Model:       route.ProviderModelID,
		Messages:    buildOAIMessages(req.Messages),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      true,
		StreamOptions: &oaiStreamOptions{
			IncludeUsage: true,
		},
		ResponseFormat: translateResponseFormat(req.ResponseFormat),
	})
	if err != nil {
		return nil, fmt.Errorf("volc.ChatStream: marshal: %w", err)
	}

	httpResp, err := v.doStream(ctx, route, "/chat/completions", body)
	if err != nil {
		return nil, fmt.Errorf("volc.ChatStream: %w", err)
	}

	ch := make(chan aiservice.ChatChunk, 64)
	// volc adapter does not currently populate TraceMetadata (Thinking gating
	// lives in the DMXAPI adapter); pass nil so the terminal chunk omits it.
	go runOAIStream(httpResp.Body, ch, v.Name(), route.ProviderModelID, nil)
	return ch, nil
}

// Embed converts texts to vectors using the Volcengine Ark OpenAI-compatible
// embedding endpoint.
func (v *VolcAdapter) Embed(ctx context.Context, route *registry.ResolvedRoute, req aiservice.EmbedRequest) (*aiservice.EmbedResponse, error) {
	if len(req.Texts) == 0 {
		return &aiservice.EmbedResponse{Provider: v.Name()}, nil
	}

	var input interface{}
	if len(req.Texts) == 1 {
		input = req.Texts[0]
	} else {
		input = req.Texts
	}

	body, err := json.Marshal(oaiEmbedRequest{
		Model:      route.ProviderModelID,
		Input:      input,
		Dimensions: req.Dimension,
	})
	if err != nil {
		return nil, fmt.Errorf("volc.Embed: marshal: %w", err)
	}

	respBytes, err := v.doPost(ctx, route, "/embeddings", body)
	if err != nil {
		return nil, fmt.Errorf("volc.Embed: %w", err)
	}

	var oaiResp oaiEmbedResponse
	if err := json.Unmarshal(respBytes, &oaiResp); err != nil {
		return nil, fmt.Errorf("volc.Embed: decode: %w", err)
	}
	if len(oaiResp.Data) == 0 {
		return nil, fmt.Errorf("volc.Embed: empty embeddings")
	}

	// Build parallel slice ordered by index.
	embeddings := make([][]float32, len(req.Texts))
	dim := 0
	for _, e := range oaiResp.Data {
		if e.Index < len(embeddings) {
			embeddings[e.Index] = e.Embedding
			if d := len(e.Embedding); d > dim {
				dim = d
			}
		}
	}

	return &aiservice.EmbedResponse{
		Embeddings:  embeddings,
		Dimension:   dim,
		Model:       oaiResp.Model,
		Provider:    v.Name(),
		TotalTokens: oaiResp.Usage.TotalTokens,
	}, nil
}

// ----------------------------------------------------------------------------
// Internal helpers
// ----------------------------------------------------------------------------

// doPost sends a POST to the Ark OpenAI-compatible endpoint and returns the body.
func (v *VolcAdapter) doPost(ctx context.Context, route *registry.ResolvedRoute, path string, body []byte) ([]byte, error) {
	url := route.Provider.BaseURL + path

	resp, err := v.client.Do(&httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewReader(body),
		Context: ctx,
		Headers: map[string]string{
			"Authorization": "Bearer " + route.Provider.APIKey,
			"Content-Type":  "application/json",
		},
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		return nil, wrapHTTPClientErr(fmt.Sprintf("doPost %s", path), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, wrapHTTPStatusErr(fmt.Sprintf("doPost %s", path), resp.StatusCode, b)
	}

	return io.ReadAll(resp.Body)
}

// doStream sends a streaming POST and returns the raw *http.Response.
// The caller is responsible for closing resp.Body.
//
// We disable retries (MaxRetries: 0) because a streaming response cannot be replayed.
func (v *VolcAdapter) doStream(ctx context.Context, route *registry.ResolvedRoute, path string, body []byte) (*http.Response, error) {
	url := route.Provider.BaseURL + path

	resp, err := v.client.Do(&httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewReader(body),
		Context: ctx,
		Headers: map[string]string{
			"Authorization": "Bearer " + route.Provider.APIKey,
			"Content-Type":  "application/json",
			"Accept":        "text/event-stream",
		},
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		return nil, wrapHTTPClientErr(fmt.Sprintf("doStream %s", path), err)
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, wrapHTTPStatusErr(fmt.Sprintf("doStream %s", path), resp.StatusCode, b)
	}

	return resp, nil
}
