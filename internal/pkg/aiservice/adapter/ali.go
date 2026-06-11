package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/httpclient"
)

// Compile-time interface checks.
var _ ChatAdapter = (*AliAdapter)(nil)
var _ EmbedAdapter = (*AliAdapter)(nil)
var _ RerankAdapter = (*AliAdapter)(nil)

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

// dashscopeRerankRequest is the DashScope NATIVE text-rerank request. Verified
// by live probe (2026-06-11): the native endpoint requires the nested
// input/parameters shape (a flat {model,query,documents} returns HTTP 400
// "Field required: input.query"). qwen3-rerank and gte-rerank-v2 both accept it.
type dashscopeRerankRequest struct {
	Model string `json:"model"`
	Input struct {
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
	} `json:"input"`
	Parameters struct {
		ReturnDocuments bool `json:"return_documents,omitempty"`
		TopN            int  `json:"top_n,omitempty"`
	} `json:"parameters"`
}

// dashscopeRerankResponse is the DashScope NATIVE text-rerank response.
// Verified shape: {"output":{"results":[{"index":0,"relevance_score":0.86,
// "document":{"text":"..."}}]},"usage":{"total_tokens":49},"request_id":"..."}.
type dashscopeRerankResponse struct {
	Output struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
			Document       struct {
				Text string `json:"text"`
			} `json:"document"`
		} `json:"results"`
	} `json:"output"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	RequestID string `json:"request_id,omitempty"`
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
func (a *AliAdapter) Capabilities() []string { return []string{"chat", "embed", "rerank"} }

// Chat performs a non-streaming chat completion against the DashScope
// OpenAI-compatible endpoint.
func (a *AliAdapter) Chat(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	body, err := json.Marshal(oaiChatRequest{
		Model:          route.ProviderModelID,
		Messages:       buildOAIMessages(req.Messages),
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
		Stream:         false,
		ResponseFormat: translateResponseFormat(req.ResponseFormat),
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
			PromptTokens:       oaiResp.Usage.PromptTokens,
			CompletionTokens:   oaiResp.Usage.CompletionTokens,
			TotalTokens:        oaiResp.Usage.TotalTokens,
			CachedPromptTokens: oaiResp.Usage.extractCachedPromptTokens(),
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
		ResponseFormat: translateResponseFormat(req.ResponseFormat),
	})
	if err != nil {
		return nil, fmt.Errorf("ali.ChatStream: marshal: %w", err)
	}

	httpResp, err := a.doStream(ctx, route, "/chat/completions", body)
	if err != nil {
		return nil, fmt.Errorf("ali.ChatStream: %w", err)
	}

	ch := make(chan aiservice.ChatChunk, 64)
	// ali adapter does not currently populate TraceMetadata (Thinking gating
	// lives in the DMXAPI adapter); pass nil so the terminal chunk omits it.
	go runOAIStream(httpResp.Body, ch, a.Name(), route.ProviderModelID, nil)
	return ch, nil
}

// Embed converts texts to vectors using the DashScope native embedding API.
//
// DashScope exposes two base paths:
//   - OpenAI-compatible: {base}/compatible-mode/v1  (used for Chat/ChatStream)
//   - Native:            {base}/api/v1              (required for Embed)
//
// The OpenAI-compatible /embeddings endpoint does not support the `dimension`
// parameter required by text-embedding-v4, so we must call the native path.
// We derive the native base from Provider.BaseURL by replacing the compat
// path segment; this avoids hardcoding the hostname and keeps the URL in sync
// with config changes.
func (a *AliAdapter) Embed(ctx context.Context, route *registry.ResolvedRoute, req aiservice.EmbedRequest) (*aiservice.EmbedResponse, error) {
	if len(req.Texts) == 0 {
		return &aiservice.EmbedResponse{Provider: a.Name()}, nil
	}

	// DashScope has two endpoints: OAI compatible (compatible-mode/v1) and native (api/v1).
	// Embedding must use the native path because the OAI-compat path does not
	// support the dimension parameter required for text-embedding-v4.
	nativeBase := strings.Replace(route.Provider.BaseURL, "/compatible-mode/v1", "/api/v1", 1)
	embedURL := nativeBase + "/services/embeddings/text-embedding/text-embedding"

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

// Rerank re-scores and re-orders documents using DashScope's NATIVE text-rerank
// endpoint. We derive the native base from Provider.BaseURL the same way Embed
// does (replace /compatible-mode/v1 with /api/v1) so the URL stays in sync with
// config. The model (e.g. qwen3-rerank, gte-rerank-v2) comes from the route's
// ProviderModelID, so adding a new DashScope rerank model is registry-only.
func (a *AliAdapter) Rerank(ctx context.Context, route *registry.ResolvedRoute, req aiservice.RerankRequest) (*aiservice.RerankResponse, error) {
	if len(req.Documents) == 0 {
		return &aiservice.RerankResponse{Provider: a.Name()}, nil
	}

	// Same native-base derivation as Embed: the DashScope native rerank path lives
	// under /api/v1, while the configured BaseURL is the OpenAI-compatible
	// /compatible-mode/v1 base. ASSUMPTION (consistent with Embed): the provider
	// BaseURL is the DashScope compatible-mode base; a non-DashScope custom base
	// without that segment would no-op the replace and append the service path
	// verbatim. All registered ali-dashscope providers use the standard base.
	nativeBase := strings.Replace(route.Provider.BaseURL, "/compatible-mode/v1", "/api/v1", 1)
	rerankURL := nativeBase + "/services/rerank/text-rerank/text-rerank"

	var dsReq dashscopeRerankRequest
	dsReq.Model = route.ProviderModelID
	dsReq.Input.Query = req.Query
	dsReq.Input.Documents = req.Documents
	dsReq.Parameters.ReturnDocuments = true
	if req.TopN > 0 && req.TopN <= len(req.Documents) {
		dsReq.Parameters.TopN = req.TopN
	}

	body, err := json.Marshal(dsReq)
	if err != nil {
		return nil, fmt.Errorf("ali.Rerank: marshal: %w", err)
	}

	respBytes, err := a.doRawPost(ctx, route.Provider.APIKey, rerankURL, body)
	if err != nil {
		return nil, fmt.Errorf("ali.Rerank: %w", err)
	}

	var dsResp dashscopeRerankResponse
	if err := json.Unmarshal(respBytes, &dsResp); err != nil {
		return nil, fmt.Errorf("ali.Rerank: decode: %w", err)
	}
	if dsResp.Code != "" {
		return nil, fmt.Errorf("ali.Rerank: provider error [%s]: %s", dsResp.Code, dsResp.Message)
	}

	results := make([]aiservice.RerankResult, 0, len(dsResp.Output.Results))
	for _, r := range dsResp.Output.Results {
		doc := r.Document.Text
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
		Provider: a.Name(),
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
		return nil, wrapHTTPClientErr(fmt.Sprintf("doPost %s", path), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, wrapHTTPStatusErr(fmt.Sprintf("doPost %s", path), resp.StatusCode, b)
	}

	return io.ReadAll(resp.Body)
}

// doStream sends a streaming POST and returns the raw *http.Response so the
// caller can read the SSE body.  The caller is responsible for closing resp.Body.
//
// We disable retries (MaxRetries: 0) because a streaming response cannot be replayed.
func (a *AliAdapter) doStream(ctx context.Context, route *registry.ResolvedRoute, path string, body []byte) (*http.Response, error) {
	url := route.Provider.BaseURL + path

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
		return nil, wrapHTTPClientErr(fmt.Sprintf("doStream %s", path), err)
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, wrapHTTPStatusErr(fmt.Sprintf("doStream %s", path), resp.StatusCode, b)
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
		return nil, wrapHTTPClientErr("doRawPost", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, wrapHTTPStatusErr("doRawPost", resp.StatusCode, b)
	}
	return io.ReadAll(resp.Body)
}

// ----------------------------------------------------------------------------
// Shared HTTP error helpers (package-level, used by all three adapters)
// ----------------------------------------------------------------------------

// wrapHTTPClientErr maps network/transport errors to typed errno values so that
// middleware/retry.go's retryableError() can identify retriable failures.
//   - net.Error timeout or context.DeadlineExceeded → ErrAIProviderTimeout
//   - other transport errors                        → ErrAIProviderError
func wrapHTTPClientErr(op string, err error) error {
	if isTimeoutErr(err) {
		return errno.ErrAIProviderTimeout.SetMessage("%s: %v", op, err)
	}
	return errno.ErrAIProviderError.SetMessage("%s: %v", op, err)
}

// wrapHTTPStatusErr maps HTTP status codes to typed errno values.
//   - 5xx → ErrAIProviderError (retriable)
//   - 429 Too Many Requests / 408 Request Timeout → ErrAIProviderError (retriable):
//     these are TRANSIENT — a rate-limited model (e.g. the free bge-reranker
//     5 req/min tier) or a server-side request-receipt/idle timeout should engage
//     the middleware Retry/Fallback so the request can fail over to another
//     provider, not hard-fail. (rerank-routing T3)
//   - other 4xx → plain fmt.Errorf (not retriable — genuine client/config errors)
//
// Shared by the ali, dmxapi and volc adapters (defined once here), so all three
// gain 429/408 fail-over behavior uniformly.
//
// Interaction with httpclient transport retries: httpclient.shouldRetryByStatus
// already retries 429 at the transport layer up to the adapter's RetryPolicy
// MaxRetries (ali rerank uses doRawPost with MaxRetries=0 → single attempt;
// dmxapi doPost uses a small MaxRetries). After those are exhausted the 429
// surfaces here and now (T3) triggers a cross-provider fail-over instead of a
// hard error — that fail-over is the intended new behavior, not double-retry of
// the SAME provider.
func wrapHTTPStatusErr(op string, statusCode int, body []byte) error {
	if statusCode >= 500 || statusCode == http.StatusTooManyRequests || statusCode == http.StatusRequestTimeout {
		return errno.ErrAIProviderError.SetMessage("%s: HTTP %d: %s", op, statusCode, string(body))
	}
	return fmt.Errorf("%s: HTTP %d: %s", op, statusCode, string(body))
}

// isTimeoutErr returns true for network timeouts and context deadline exceeded.
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return true
	}
	return false
}
