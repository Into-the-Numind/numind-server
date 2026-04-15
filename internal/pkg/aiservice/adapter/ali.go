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

// Compile-time interface checks.
var _ ChatAdapter = (*AliAdapter)(nil)
var _ EmbedAdapter = (*AliAdapter)(nil)

// dashscopeEmbedRequest is the DashScope native embedding request (not OpenAI compatible).
type dashscopeEmbedRequest struct {
	Model string `json:"model"`
	Input struct {
		Texts []string `json:"texts"`
	} `json:"input"`
	Parameters struct {
		Dimension int `json:"dimension,omitempty"`
	} `json:"parameters"`
}

// dashscopeEmbedResponse is the DashScope native embedding response.
type dashscopeEmbedResponse struct {
	Output struct {
		Embeddings []struct {
			Embedding []float32 `json:"embedding"`
			TextIndex int       `json:"text_index"`
		} `json:"embeddings"`
	} `json:"output"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// AliAdapter implements ChatAdapter and EmbedAdapter for Alibaba Cloud
// DashScope (OpenAI-compatible chat endpoint + native embedding endpoint).
//
// The adapter calls:
//   - Chat/ChatStream: https://{base_url}/chat/completions  (OpenAI compatible)
//   - Embed:           https://dashscope.aliyuncs.com/api/v1/services/embeddings/...
//     (DashScope native — the OpenAI-compat /embeddings path does not support
//     the dimension parameter required for text-embedding-v4)
type AliAdapter struct {
	client *httpclient.Client
}

// NewAliAdapter creates an AliAdapter backed by the shared httpclient pool.
func NewAliAdapter() *AliAdapter {
	return &AliAdapter{
		client: httpclient.NewClient(nil), // uses DefaultConfig
	}
}

// Name returns the adapter identifier.
func (a *AliAdapter) Name() string { return "ali" }

// ProviderType returns the provider category.
func (a *AliAdapter) ProviderType() string { return "dashscope" }

// Capabilities lists the capabilities this adapter supports.
func (a *AliAdapter) Capabilities() []string { return []string{"chat", "embed"} }

// Chat performs a non-streaming chat completion against the DashScope
// OpenAI-compatible endpoint.
func (a *AliAdapter) Chat(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	body, err := json.Marshal(oaiChatRequest{
		Model:       route.ProviderModelID,
		Messages:    buildOAIMessages(req.Messages),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      false,
	})
	if err != nil {
		return nil, fmt.Errorf("ali.Chat: marshal: %w", err)
	}

	respBytes, err := a.doPost(ctx, route, "/chat/completions", body)
	if err != nil {
		return nil, fmt.Errorf("ali.Chat: %w", err)
	}

	var oaiResp oaiChatResponse
	if err := json.Unmarshal(respBytes, &oaiResp); err != nil {
		return nil, fmt.Errorf("ali.Chat: decode: %w", err)
	}
	if oaiResp.Error != nil {
		return nil, fmt.Errorf("ali.Chat: provider error: %s", oaiResp.Error.Message)
	}
	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("ali.Chat: empty choices")
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
		Provider:         a.Name(),
	}, nil
}

// ChatStream starts a streaming chat completion and returns a channel of chunks.
// The final chunk (IsFinal=true) carries the accumulated TokenUsage (obtained
// from stream_options.include_usage=true in the request).
func (a *AliAdapter) ChatStream(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	body, err := json.Marshal(oaiChatRequest{
		Model:       route.ProviderModelID,
		Messages:    buildOAIMessages(req.Messages),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      true,
		StreamOptions: &oaiStreamOptions{
			IncludeUsage: true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("ali.ChatStream: marshal: %w", err)
	}

	httpResp, err := a.doStream(ctx, route, "/chat/completions", body)
	if err != nil {
		return nil, fmt.Errorf("ali.ChatStream: %w", err)
	}

	ch := make(chan aiservice.ChatChunk, 64)
	go runOAIStream(httpResp.Body, ch, a.Name(), route.ProviderModelID)
	return ch, nil
}

// Embed converts texts to vectors using the DashScope native embedding API.
// The OpenAI-compatible /embeddings endpoint does not support the dimension
// parameter required for text-embedding-v4, so we call the native path.
func (a *AliAdapter) Embed(ctx context.Context, route *registry.ResolvedRoute, req aiservice.EmbedRequest) (*aiservice.EmbedResponse, error) {
	if len(req.Texts) == 0 {
		return &aiservice.EmbedResponse{Provider: a.Name()}, nil
	}

	// DashScope native embedding endpoint — not OpenAI-compat.
	embedURL := "https://dashscope.aliyuncs.com/api/v1/services/embeddings/text-embedding/text-embedding"

	var dsReq dashscopeEmbedRequest
	dsReq.Model = route.ProviderModelID
	dsReq.Input.Texts = req.Texts
	if req.Dimension > 0 {
		dsReq.Parameters.Dimension = req.Dimension
	}

	body, err := json.Marshal(dsReq)
	if err != nil {
		return nil, fmt.Errorf("ali.Embed: marshal: %w", err)
	}

	respBytes, err := a.doRawPost(ctx, route.Provider.APIKey, embedURL, body)
	if err != nil {
		return nil, fmt.Errorf("ali.Embed: %w", err)
	}

	var dsResp dashscopeEmbedResponse
	if err := json.Unmarshal(respBytes, &dsResp); err != nil {
		return nil, fmt.Errorf("ali.Embed: decode: %w", err)
	}
	if dsResp.Code != "" {
		return nil, fmt.Errorf("ali.Embed: provider error [%s]: %s", dsResp.Code, dsResp.Message)
	}
	if len(dsResp.Output.Embeddings) == 0 {
		return nil, fmt.Errorf("ali.Embed: empty embeddings")
	}

	// Build parallel slice (order by text_index).
	embeddings := make([][]float32, len(req.Texts))
	dim := 0
	for _, e := range dsResp.Output.Embeddings {
		if e.TextIndex < len(embeddings) {
			embeddings[e.TextIndex] = e.Embedding
			if d := len(e.Embedding); d > dim {
				dim = d
			}
		}
	}

	return &aiservice.EmbedResponse{
		Embeddings:  embeddings,
		Dimension:   dim,
		Model:       route.ProviderModelID,
		Provider:    a.Name(),
		TotalTokens: dsResp.Usage.TotalTokens,
	}, nil
}

// ----------------------------------------------------------------------------
// Internal helpers
// ----------------------------------------------------------------------------

// doPost sends a POST to the DashScope OpenAI-compatible endpoint and returns
// the response body bytes.
func (a *AliAdapter) doPost(ctx context.Context, route *registry.ResolvedRoute, path string, body []byte) ([]byte, error) {
	url := route.Provider.BaseURL + path

	resp, err := a.client.Do(&httpclient.Request{
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
		return nil, fmt.Errorf("doPost %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("doPost %s: HTTP %d: %s", path, resp.StatusCode, string(b))
	}

	return io.ReadAll(resp.Body)
}

// doStream sends a streaming POST and returns the raw *http.Response so the
// caller can read the SSE body.  The caller is responsible for closing resp.Body.
func (a *AliAdapter) doStream(ctx context.Context, route *registry.ResolvedRoute, path string, body []byte) (*http.Response, error) {
	url := route.Provider.BaseURL + path

	// For streaming we bypass the httpclient retry layer (retry cannot replay a
	// streaming body) and call the underlying transport directly via a zero-retry
	// Request.
	resp, err := a.client.Do(&httpclient.Request{
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
		return nil, fmt.Errorf("doStream %s: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("doStream %s: HTTP %d: %s", path, resp.StatusCode, string(b))
	}

	return resp, nil
}

// doRawPost posts to an arbitrary URL (used for DashScope native embed path).
func (a *AliAdapter) doRawPost(ctx context.Context, apiKey, url string, body []byte) ([]byte, error) {
	resp, err := a.client.Do(&httpclient.Request{
		Method:  "POST",
		URL:     url,
		Body:    bytes.NewReader(body),
		Context: ctx,
		Headers: map[string]string{
			"Authorization": "Bearer " + apiKey,
			"Content-Type":  "application/json",
		},
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		return nil, fmt.Errorf("doRawPost: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("doRawPost: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return io.ReadAll(resp.Body)
}
