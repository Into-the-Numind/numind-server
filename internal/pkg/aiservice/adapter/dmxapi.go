package adapter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/aierr"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/httpclient"
)

// Note: HTTP error helpers (wrapHTTPClientErr, wrapHTTPStatusErr, isTimeoutErr)
// are defined in ali.go (same package) and shared across all three adapters.

// dmxapiPostMaxRetries is the per-request retry budget for non-streaming DMXAPI
// calls (doPost). Set to 3 because dmxapi.cn is a third-party aggregator that
// occasionally has transient header-timeout blips lasting <30s — single-attempt
// was too brittle (see dev incident: agent_run 41/40/38 terminated immediately
// on the first timeout). 3 retries with httpclient's exponential backoff
// (~1s + 2s + 4s) recovers from transient blips without unbounded waiting.
//
// doStream still uses 0 because partial SSE bodies cannot be safely replayed
// once chunks have been forwarded to the caller (see doStream comment).
const dmxapiPostMaxRetries = 3

// Compile-time interface checks.
var _ ChatAdapter = (*DMXAPIAdapter)(nil)
var _ EmbedAdapter = (*DMXAPIAdapter)(nil)
var _ RerankAdapter = (*DMXAPIAdapter)(nil)
var _ ImageGenAdapter = (*DMXAPIAdapter)(nil)

// imageGenHTTPTimeout caps a single text-to-image request. Image generation is a
// one-shot (non-streaming) call, so capping the whole request (body read included)
// is safe. 90s matches the value the legacy raw-HTTP image_gen path used.
const imageGenHTTPTimeout = 90 * time.Second

// defaultImageGenModel is the image model used when route.ProviderModelID is empty
// (e.g. a route mis-seeded without a provider_model_id). DMXAPI serves gpt-image-2
// via the OpenAI-compatible /v1/images/generations endpoint.
const defaultImageGenModel = "gpt-image-2"

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
	// client serves non-streaming calls (doPost: chat-non-stream, embed, rerank).
	// Keeps the LLMConfig 600s total request timeout — a one-shot body is a safe
	// thing to cap.
	client *httpclient.Client
	// streamClient serves streaming chat (doStream). It uses LLMStreamConfig,
	// which drops the total http.Client.Timeout: that cap also bounds body reads,
	// so it would truncate a healthy long SSE stream (prod incident 2026-06-16:
	// claude-opus-4-6 thinking streamed >10min and got cut at the 600s cap).
	// Stream liveness/ceiling are governed by ResponseHeaderTimeout + idle
	// watchdog + the caller's context deadline instead.
	streamClient *httpclient.Client
	// imageGenClient serves the non-OpenAI-compatible Gemini image endpoint
	// (x-goog-api-key + /v1beta/models/<model>:generateContent). Image generation
	// is a one-shot call (no streaming) with a higher latency floor than chat, so
	// it carries its own 90s total-request timeout independent of the chat clients.
	imageGenClient *httpclient.Client
}

// NewDMXAPIAdapter creates a DMXAPIAdapter backed by the shared httpclient pool.
//
// Uses httpclient.LLMConfig (180s ResponseHeaderTimeout) instead of DefaultConfig
// because dmxapi's deepseek-v4-pro thinking-mode header TTFB is routinely
// 90-120s on large prompts (dev incident 2026-05-27: agent_run 42 timed out
// at 60s while dmxapi was still computing; manual repro measured 97.9s TTFB
// on a 24k-token fresh prompt with max_tokens=8000).
//
// Streaming chat uses a separate client (LLMStreamConfig) with no total request
// timeout — see streamClient field and httpclient.LLMStreamConfig.
func NewDMXAPIAdapter() *DMXAPIAdapter {
	imgCfg := httpclient.DefaultConfig()
	imgCfg.Timeout = imageGenHTTPTimeout
	imgCfg.ResponseHeaderTimeout = imageGenHTTPTimeout
	// NOTE: single-attempt (non-idempotent image gen) is enforced via the request's
	// RetryPolicy{MaxRetries:0} in ImageGen — Config.MaxRetries is ignored by the
	// httpclient retry loop, so we do NOT rely on it here.
	return &DMXAPIAdapter{
		client:         httpclient.NewClient(httpclient.LLMConfig()),
		streamClient:   httpclient.NewClient(httpclient.LLMStreamConfig()),
		imageGenClient: httpclient.NewClient(imgCfg),
	}
}

// Name returns the adapter identifier.
func (d *DMXAPIAdapter) Name() string { return "dmxapi" }

// ProviderType returns the provider category.
func (d *DMXAPIAdapter) ProviderType() string { return "dmxapi" }

// Capabilities lists the capabilities this adapter supports.
func (d *DMXAPIAdapter) Capabilities() []string {
	return []string{"chat", "embed", "rerank", "image_gen"}
}

// buildOAIRequest assembles the OpenAI-compatible chat request payload and the
// trace metadata record. Centralises per-family dispatch so that Chat and
// ChatStream stay DRY and share the same thinking/temperature/max-tokens
// decisions. See plan §3.2 "AiHubMix thinking gating decision table".
//
// Rules encoded here (stable contract tested by dmxapi_thinking_test.go):
//
//   - Family dispatch for max tokens: OpenAI reasoning family (gpt-5, o1, o3,
//     o4) uses max_completion_tokens; all other families use max_tokens.
//     Independent of the Thinking flag (P1-1) so that regular GPT-5 completions
//     still use the correct field.
//   - reasoning_effort injection: only when req.Thinking=true AND
//     route.SupportsThinking=true AND route.ThinkingOnly=false (the
//     optional-thinking set). Value is hardcoded "medium" for now; a future
//     openrouter-provider feature will make this user-configurable.
//   - Intrinsic-thinking models (route.ThinkingOnly=true) record the "intrinsic"
//     sentinel in TraceMetadata but never inject the wire field (Q8=B; Gemini
//     3.1 Pro rejects "minimal"/"none" and some AiHubMix intrinsic variants
//     error on reasoning_effort at all).
//   - Claude base + Thinking=true forces temperature=1 on the wire (Q4=A)
//     because AiHubMix returns a 400 "claude thinking requires temperature=1"
//     when the two conflict. The Claude -think suffix variant is detected as a
//     separate family and intentionally skipped here — AiHubMix server-side
//     forces temperature=1 for that slug variant so our adapter leaves the
//     caller value alone.
func (d *DMXAPIAdapter) buildOAIRequest(
	route *registry.ResolvedRoute,
	req aiservice.ChatRequest,
	stream bool,
) (oaiChatRequest, *aiservice.TraceMetadata) {
	family := InferModelFamily(route.ProviderModelID)
	meta := &aiservice.TraceMetadata{
		ResolvedModelFamily: string(family),
	}

	oaiReq := oaiChatRequest{
		Model:          route.ProviderModelID,
		Messages:       buildOAIMessages(req.Messages),
		Temperature:    req.Temperature,
		Stream:         stream,
		ResponseFormat: translateResponseFormat(req.ResponseFormat),
		Tools:          buildOAITools(req.Tools),
	}
	if stream {
		oaiReq.StreamOptions = &oaiStreamOptions{IncludeUsage: true}
	}

	// Max tokens dispatch (family-based, independent of Thinking flag — P1-1).
	if family == ModelFamilyOpenAIReasoning {
		oaiReq.MaxCompletionTokens = req.MaxTokens
	} else {
		oaiReq.MaxTokens = req.MaxTokens
	}

	// Thinking activation gating (spec §3.2 decision table). HOW thinking is
	// activated on the wire is driven by route.ThinkingStyle (ai_service.thinking_style).
	if req.Thinking && route.SupportsThinking {
		if route.ThinkingOnly {
			// Intrinsic-thinking model: record sentinel, do not inject wire field.
			meta.ResolvedReasoningEffort = "intrinsic"
		} else {
			// Optional-thinking model: inject the activation field its provider expects.
			switch route.ThinkingStyle {
			case "enable_thinking_kwarg":
				// Qwen/vLLM convention (e.g. agnes-2.0-flash): the model only thinks
				// when chat_template_kwargs.enable_thinking is set; it ignores reasoning_effort.
				oaiReq.ChatTemplateKwargs = map[string]interface{}{"enable_thinking": true}
				meta.ResolvedReasoningEffort = "enable_thinking_kwarg"
			case "none":
				// Supports thinking but no activation field should be injected (rare; reserved).
				// Record the sentinel so Langfuse can distinguish this from "" (thinking off / unsupported).
				meta.ResolvedReasoningEffort = "none"
			default:
				// "" (legacy) or "reasoning_effort": OpenAI-style. Empty string preserves
				// today's behavior exactly (all prior optional-thinking models sent this).
				oaiReq.ReasoningEffort = "medium"
				meta.ResolvedReasoningEffort = "medium"
			}
		}
	} else if !req.Thinking && route.SupportsThinking && !route.ThinkingOnly {
		// Explicit NON-thinking on an optional-thinking model. Some providers default
		// thinking ON unless told otherwise — e.g. deepseek-v4-flash at DMXAPI returns
		// reasoning_content (and runs ~2x slower) unless enable_thinking=false is sent.
		// Mirror the activation switch above to inject the DEACTIVATION field for styles
		// that have one. Only fires when the caller explicitly opts out of thinking
		// (req.Thinking==false), which today is backend tasks like session.title;
		// chatbot/sop force Thinking=true (llmrouter default) so they never reach here.
		switch route.ThinkingStyle {
		case "enable_thinking_kwarg":
			// Qwen/vLLM/DeepSeek-hybrid convention: chat_template_kwargs.enable_thinking=false
			// suppresses the chain-of-thought. Verified against DMXAPI deepseek-v4-flash.
			oaiReq.ChatTemplateKwargs = map[string]interface{}{"enable_thinking": false}
			meta.ResolvedReasoningEffort = "enable_thinking_kwarg_off"
		default:
			// "" / "reasoning_effort" / "none": these models DON'T think when no activation
			// field is sent, so no explicit deactivation is needed (omit field).
		}
	}

	// Claude base + Thinking=true → force temperature=1 (Q4=A).
	// Note: Claude -think suffix variant has family=ModelFamilyClaudeThinkingSlug
	// and falls outside this branch; AiHubMix forces temp=1 server-side for it.
	// S3 v2 review P2-A fix: also gate on route.SupportsThinking so we don't force
	// temp=1 on a Claude route whose reasoning_effort branch was skipped (no sense
	// forcing temp without injection — would create inconsistent wire state).
	if family == ModelFamilyClaude && req.Thinking && route.SupportsThinking && req.Temperature != 1 {
		oaiReq.Temperature = 1
		meta.TempOverridden = true
	}

	return oaiReq, meta
}

// Chat performs a non-streaming chat completion against the DMXAPI OpenAI-compatible endpoint.
func (d *DMXAPIAdapter) Chat(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	oaiReq, traceMeta := d.buildOAIRequest(route, req, false)

	body, err := json.Marshal(oaiReq)
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
		return nil, fmt.Errorf("dmxapi.Chat: %w",
			aierr.New(0, fmt.Sprint(oaiResp.Error.Code), oaiResp.Error.Type, oaiResp.Error.Message, nil))
	}
	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("dmxapi.Chat: empty choices")
	}

	usage := aiservice.TokenUsage{}
	if oaiResp.Usage != nil {
		usage = aiservice.TokenUsage{
			PromptTokens:       oaiResp.Usage.PromptTokens,
			CompletionTokens:   oaiResp.Usage.CompletionTokens,
			TotalTokens:        oaiResp.Usage.TotalTokens,
			ReasoningTokens:    oaiResp.Usage.extractReasoningTokens(),
			CachedPromptTokens: oaiResp.Usage.extractCachedPromptTokens(),
		}
	}

	return &aiservice.ChatResponse{
		Content:          oaiResp.Choices[0].Message.Content,
		ReasoningContent: oaiResp.Choices[0].Message.ReasoningContent,
		ToolCalls:        extractToolCalls(oaiResp.Choices[0].Message.ToolCalls),
		FinishReason:     oaiResp.Choices[0].FinishReason,
		Usage:            usage,
		Model:            oaiResp.Model,
		Provider:         d.Name(),
		TraceMetadata:    traceMeta,
	}, nil
}

// ChatStream starts a streaming chat completion.
// stream_options.include_usage=true ensures the final SSE chunk carries usage.
func (d *DMXAPIAdapter) ChatStream(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	oaiReq, traceMeta := d.buildOAIRequest(route, req, true)

	body, err := json.Marshal(oaiReq)
	if err != nil {
		return nil, fmt.Errorf("dmxapi.ChatStream: marshal: %w", err)
	}

	httpResp, err := d.doStream(ctx, route, "/chat/completions", body)
	if err != nil {
		return nil, fmt.Errorf("dmxapi.ChatStream: %w", err)
	}

	ch := make(chan aiservice.ChatChunk, 64)
	go runOAIStream(httpResp.Body, ch, d.Name(), route.ProviderModelID, traceMeta)
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
		// dmxapiRerankResponse.Error carries only Message (no structured code/type),
		// so classification falls back to message substrings.
		return nil, fmt.Errorf("dmxapi.Rerank: %w",
			aierr.New(0, "", "", rrResp.Error.Message, nil))
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

// ImageGen generates an image from a text prompt via DMXAPI's OpenAI-compatible
// images endpoint (POST {baseURL}/images/generations, Authorization: Bearer),
// serving the gpt-image-2 model. The image comes back as base64 in
// data[0].b64_json. Model + base URL + key come from route.Provider
// (registry-resolved). It uses the bespoke imageGenClient (longer timeout +
// single-attempt) rather than the shared doPost, which retries — image gen is
// non-idempotent (see RetryPolicy below).
func (d *DMXAPIAdapter) ImageGen(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ImageGenRequest) (*aiservice.ImageGenResponse, error) {
	if route == nil {
		return nil, errors.New("dmxapi.ImageGen: nil route")
	}
	if route.Provider.APIKey == "" {
		return nil, errors.New("dmxapi.ImageGen: provider API key is not configured")
	}

	model := strings.TrimSpace(route.ProviderModelID)
	if model == "" {
		model = defaultImageGenModel
	}

	reqBody := map[string]interface{}{
		"model":  model,
		"prompt": req.Prompt,
		"n":      1,
		"size":   sizeFromAspectRatio(req.AspectRatio),
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("dmxapi.ImageGen: marshal: %w", err)
	}

	resp, err := d.imageGenClient.Do(&httpclient.Request{
		Method:  "POST",
		URL:     imageGenEndpoint(route.Provider.BaseURL),
		Body:    bytes.NewReader(body),
		Context: ctx,
		Headers: map[string]string{
			"Authorization": "Bearer " + route.Provider.APIKey,
			"Content-Type":  "application/json",
		},
		// Image generation is NON-idempotent — a retry would make the provider
		// generate (and bill on its side) the same prompt again. Single attempt
		// (MaxRetries: 0), mirroring the streaming client. NOTE: the retry budget is
		// THIS req.RetryPolicy, NOT the client Config.MaxRetries — the httpclient
		// retry loop reads req.RetryPolicy.MaxRetries (client.go), so setting the
		// config knob alone does nothing.
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: 0},
	})
	if err != nil {
		return nil, wrapHTTPClientErr("dmxapi.ImageGen", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, wrapHTTPStatusErr("dmxapi.ImageGen", resp.StatusCode, b)
	}

	var result struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("dmxapi.ImageGen: decode: %w", err)
	}
	// OpenAI-compatible APIs can return a 200 with a populated error object.
	if result.Error != nil && result.Error.Message != "" {
		return nil, fmt.Errorf("dmxapi.ImageGen: provider error: %s", result.Error.Message)
	}
	if len(result.Data) == 0 || result.Data[0].B64JSON == "" {
		return nil, errors.New("dmxapi.ImageGen: no image data returned (data[0].b64_json empty; possibly blocked by moderation)")
	}
	base64Data := result.Data[0].B64JSON
	// Validate the base64 is decodable here so the caller can trust ImageBase64.
	if _, err := base64.StdEncoding.DecodeString(base64Data); err != nil {
		return nil, fmt.Errorf("dmxapi.ImageGen: invalid base64 image data: %w", err)
	}

	return &aiservice.ImageGenResponse{
		ImageBase64: base64Data,
		ContentType: "image/png",
		Model:       model,
		Provider:    d.Name(),
	}, nil
}

// imageGenEndpoint builds the OpenAI-compatible images URL: {baseURL}/images/
// generations. baseURL is the provider base (e.g. https://www.dmxapi.cn/v1); a
// trailing slash is trimmed. Defaults to the public dmxapi.cn /v1 host when empty.
func imageGenEndpoint(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://www.dmxapi.cn/v1"
	}
	return baseURL + "/images/generations"
}

// sizeFromAspectRatio maps an aspect-ratio hint to a gpt-image-2 size string.
// gpt-image-2 supports 1024x1024 (square), 1536x1024 (landscape) and 1024x1536
// (portrait). Unknown / empty → square.
func sizeFromAspectRatio(ar string) string {
	switch strings.TrimSpace(ar) {
	case "16:9", "landscape", "3:2":
		return "1536x1024"
	case "9:16", "portrait", "2:3":
		return "1024x1536"
	default:
		return "1024x1024"
	}
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
		RetryPolicy: &httpclient.RetryPolicy{MaxRetries: dmxapiPostMaxRetries},
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
//
// Uses d.streamClient (no total http.Client.Timeout) so a healthy long SSE
// stream is not truncated mid-read — see the streamClient field comment.
func (d *DMXAPIAdapter) doStream(ctx context.Context, route *registry.ResolvedRoute, path string, body []byte) (*http.Response, error) {
	url := route.Provider.BaseURL + path

	resp, err := d.streamClient.Do(&httpclient.Request{
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
