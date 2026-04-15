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
var _ ChatAdapter = (*DMXAPIAdapter)(nil)
var _ EmbedAdapter = (*DMXAPIAdapter)(nil)
var _ RerankAdapter = (*DMXAPIAdapter)(nil)

// dmxapiRerankRequest is the DMXAPI rerank request body.
type dmxapiRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"`
}

// dmxapiRerankResponse is the DMXAPI rerank response body.
type dmxapiRerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
		Document       string  `json:"document,omitempty"`
	} `json:"results"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// DMXAPIAdapter implements ChatAdapter, EmbedAdapter, and RerankAdapter for the
// DMXAPI aggregation platform (OpenAI-compatible + rerank extension).
//
// Unlike the ali and volc adapters, DMXAPIAdapter ALSO supports Rerank because
// DMXAPI exposes qwen3-rerank at /rerank.
type DMXAPIAdapter struct {
	client *httpclient.Client
}

// NewDMXAPIAdapter creates a DMXAPIAdapter backed by the shared httpclient pool.
func NewDMXAPIAdapter() *DMXAPIAdapter {
	return &DMXAPIAdapter{
		client: httpclient.NewClient(nil), // uses DefaultConfig
	}
}

// Name returns the adapter identifier.
func (d *DMXAPIAdapter) Name() string { return "dmxapi" }

// ProviderType returns the provider category.
func (d *DMXAPIAdapter) ProviderType() string { return "dmxapi" }

// Capabilities lists the capabilities this adapter supports.
func (d *DMXAPIAdapter) Capabilities() []string { return []string{"chat", "embed", "rerank"} }

// Chat performs a non-streaming chat completion against the DMXAPI OpenAI-compatible endpoint.
func (d *DMXAPIAdapter) Chat(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	body, err := json.Marshal(oaiChatRequest{
		Model:       route.ProviderModelID,
		Messages:    buildOAIMessages(req.Messages),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stream:      false,
	})
	if err != nil {
		return nil, fmt.Errorf("dmxapi.Chat: marshal: %w", err)
	}

	respBytes, err := d.doPost(ctx, route, "/chat/completions", body)
	if err != nil {
		return nil, fmt.Errorf("dmxapi.Chat: %w", err)
	}

	var oaiResp oaiChatResponse
	if err := json.Unmarshal(respBytes, &oaiResp); err != nil {
		return nil, fmt.Errorf("dmxapi.Chat: decode: %w", err)
	}
	if oaiResp.Error != nil {
		return nil, fmt.Errorf("dmxapi.Chat: provider error: %s", oaiResp.Error.Message)
	}
	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("dmxapi.Chat: empty choices")
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
		Provider:         d.Name(),
	}, nil
}

// ChatStream starts a streaming chat completion.
// stream_options.include_usage=true ensures the final SSE chunk carries usage.
func (d *DMXAPIAdapter) ChatStream(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
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
		return nil, fmt.Errorf("dmxapi.ChatStream: marshal: %w", err)
	}

	httpResp, err := d.doStream(ctx, route, "/chat/completions", body)
	if err != nil {
		return nil, fmt.Errorf("dmxapi.ChatStream: %w", err)
	}

	ch := make(chan aiservice.ChatChunk, 64)
	go runOAIStream(httpResp.Body, ch, d.Name(), route.ProviderModelID)
	return ch, nil
}

// Embed converts texts to vectors using DMXAPI's OpenAI-compatible embedding endpoint.
func (d *DMXAPIAdapter) Embed(ctx context.Context, route *registry.ResolvedRoute, req aiservice.EmbedRequest) (*aiservice.EmbedResponse, error) {
	if len(req.Texts) == 0 {
		return &aiservice.EmbedResponse{Provider: d.Name()}, nil
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
		return nil, fmt.Errorf("dmxapi.Embed: marshal: %w", err)
	}

	respBytes, err := d.doPost(ctx, route, "/embeddings", body)
	if err != nil {
		return nil, fmt.Errorf("dmxapi.Embed: %w", err)
	}

	var oaiResp oaiEmbedResponse
	if err := json.Unmarshal(respBytes, &oaiResp); err != nil {
		return nil, fmt.Errorf("dmxapi.Embed: decode: %w", err)
	}
	if len(oaiResp.Data) == 0 {
		return nil, fmt.Errorf("dmxapi.Embed: empty embeddings")
	}

	embeddings := make([][]float32, len(req.Texts))
	dim := 0
	for _, e := range oaiResp.Data {
		if e.Index < len(embeddings) {
			embeddings[e.Index] = e.Embedding
			if dl := len(e.Embedding); dl > dim {
				dim = dl
			}
		}
	}

	return &aiservice.EmbedResponse{
		Embeddings:  embeddings,
		Dimension:   dim,
		Model:       oaiResp.Model,
		Provider:    d.Name(),
		TotalTokens: oaiResp.Usage.TotalTokens,
	}, nil
}

// Rerank re-scores and re-orders documents relative to the query using
// DMXAPI's qwen3-rerank endpoint at /rerank.
func (d *DMXAPIAdapter) Rerank(ctx context.Context, route *registry.ResolvedRoute, req aiservice.RerankRequest) (*aiservice.RerankResponse, error) {
	if len(req.Documents) == 0 {
		return &aiservice.RerankResponse{Provider: d.Name()}, nil
	}

	topN := req.TopN
	if topN <= 0 || topN > len(req.Documents) {
		topN = len(req.Documents)
	}

	body, err := json.Marshal(dmxapiRerankRequest{
		Model:     route.ProviderModelID,
		Query:     req.Query,
		Documents: req.Documents,
		TopN:      topN,
	})
	if err != nil {
		return nil, fmt.Errorf("dmxapi.Rerank: marshal: %w", err)
	}

	respBytes, err := d.doPost(ctx, route, "/rerank", body)
	if err != nil {
		return nil, fmt.Errorf("dmxapi.Rerank: %w", err)
	}

	var rrResp dmxapiRerankResponse
	if err := json.Unmarshal(respBytes, &rrResp); err != nil {
		return nil, fmt.Errorf("dmxapi.Rerank: decode: %w", err)
	}
	if rrResp.Error != nil {
		return nil, fmt.Errorf("dmxapi.Rerank: provider error: %s", rrResp.Error.Message)
	}

	results := make([]aiservice.RerankResult, 0, len(rrResp.Results))
	for _, r := range rrResp.Results {
		doc := r.Document
		if doc == "" && r.Index < len(req.Documents) {
			doc = req.Documents[r.Index]
		}
		results = append(results, aiservice.RerankResult{
			Index:    r.Index,
			Score:    r.RelevanceScore,
			Document: doc,
		})
	}

	return &aiservice.RerankResponse{
		Results:  results,
		Model:    route.ProviderModelID,
		Provider: d.Name(),
	}, nil
}

// ----------------------------------------------------------------------------
// Internal helpers
// ----------------------------------------------------------------------------

// doPost sends a POST and returns the full response body.
func (d *DMXAPIAdapter) doPost(ctx context.Context, route *registry.ResolvedRoute, path string, body []byte) ([]byte, error) {
	url := route.Provider.BaseURL + path

	resp, err := d.client.Do(&httpclient.Request{
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

// doStream sends a streaming POST and returns the raw *http.Response.
// The caller is responsible for closing resp.Body.
func (d *DMXAPIAdapter) doStream(ctx context.Context, route *registry.ResolvedRoute, path string, body []byte) (*http.Response, error) {
	url := route.Provider.BaseURL + path

	resp, err := d.client.Do(&httpclient.Request{
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
