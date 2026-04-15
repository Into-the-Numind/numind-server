// Package adapter provides concrete AI provider implementations for the
// AI Service Gateway.  Each adapter translates the unified aiservice request /
// response types into the provider-specific wire format and back.
//
// Adapter responsibilities:
//   - HTTP call to the upstream provider (via internal/pkg/httpclient)
//   - Request / response translation
//   - Returning the last-chunk Usage in ChatStream
//
// Adapters do NOT:
//   - Retry (handled by middleware/retry.go)
//   - Record Langfuse traces (handled by middleware/tracing.go)
//   - Write UsageRecord (handled by middleware/billing.go)
//
// # Interface Segregation (spec §3.4)
//
// The Adapter base interface is tiny; callers that need a specific capability
// type-assert to the relevant sub-interface (ChatAdapter, EmbedAdapter,
// RerankAdapter).  Compile-time checks live at the bottom of each provider
// file:
//
//	var _ ChatAdapter  = (*AliAdapter)(nil)
//	var _ EmbedAdapter = (*AliAdapter)(nil)
package adapter

import (
	"context"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
)

// ----------------------------------------------------------------------------
// Base interface
// ----------------------------------------------------------------------------

// Adapter is the minimal interface every provider adapter must satisfy.
type Adapter interface {
	// Name returns a human-readable identifier for this adapter (e.g. "ali", "volc").
	Name() string

	// ProviderType describes the provider category (e.g. "dashscope", "ark", "dmxapi").
	ProviderType() string

	// Capabilities lists the capability strings this adapter supports
	// (e.g. ["chat", "embed"] or ["chat", "embed", "rerank"]).
	Capabilities() []string
}

// ----------------------------------------------------------------------------
// Capability sub-interfaces (ISP)
// ----------------------------------------------------------------------------

// ChatAdapter is implemented by adapters that support text and vision chat.
type ChatAdapter interface {
	Adapter

	// Chat performs a non-streaming chat completion and returns the full response.
	Chat(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)

	// ChatStream starts a streaming chat completion.  Each ChatChunk is sent on
	// the returned channel.  The final chunk has IsFinal=true and a non-nil Usage
	// pointer.  The channel is always closed after the final chunk or on error.
	ChatStream(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error)
}

// EmbedAdapter is implemented by adapters that support text embedding.
type EmbedAdapter interface {
	Adapter

	// Embed converts a batch of texts into float32 vectors.
	Embed(ctx context.Context, route *registry.ResolvedRoute, req aiservice.EmbedRequest) (*aiservice.EmbedResponse, error)
}

// RerankAdapter is implemented by adapters that support passage reranking.
type RerankAdapter interface {
	Adapter

	// Rerank re-scores and re-orders the provided documents relative to the query.
	Rerank(ctx context.Context, route *registry.ResolvedRoute, req aiservice.RerankRequest) (*aiservice.RerankResponse, error)
}

// OCRAdapter is implemented by adapters that support optical character recognition.
type OCRAdapter interface {
	Adapter

	// OCR extracts text from an image, optionally returning per-word bounding boxes.
	OCR(ctx context.Context, route *registry.ResolvedRoute, req aiservice.OCRRequest) (*aiservice.OCRResponse, error)
}

// ASRAdapter is implemented by adapters that support automatic speech recognition.
type ASRAdapter interface {
	Adapter

	// ASR transcribes an audio clip to text.
	ASR(ctx context.Context, route *registry.ResolvedRoute, req aiservice.ASRRequest) (*aiservice.ASRResponse, error)
}

// FileServiceAdapter is implemented by adapters that support file upload services.
// It is distinct from LLM/OCR/ASR adapters and is used to upload files to a
// provider-managed store (e.g. Alibaba Bailian) and obtain a stable FileID that
// can be referenced in subsequent LLM calls.
type FileServiceAdapter interface {
	Adapter

	// UploadFile uploads raw file bytes (or a file at a local path) to the
	// provider and returns a FileUploadResponse containing the remote FileID.
	UploadFile(ctx context.Context, route *registry.ResolvedRoute, req aiservice.FileUploadRequest) (*aiservice.FileUploadResponse, error)
}

// ----------------------------------------------------------------------------
// OpenAI-compatible wire types (shared across ali / volc / dmxapi adapters)
// ----------------------------------------------------------------------------

// oaiMessage is the OpenAI-compatible chat message wire format.
type oaiMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string OR []oaiContentPart for vision
}

// oaiContentPart is a single part in a multipart (vision) message.
type oaiContentPart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL *oaiImageURL `json:"image_url,omitempty"`
}

// oaiImageURL wraps the URL field inside an image_url content part.
type oaiImageURL struct {
	URL string `json:"url"`
}

// oaiChatRequest is the OpenAI-compatible non-streaming chat request body.
type oaiChatRequest struct {
	Model       string       `json:"model"`
	Messages    []oaiMessage `json:"messages"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	Temperature float64      `json:"temperature,omitempty"`
	Stream      bool         `json:"stream"`
	// StreamOptions is only sent when Stream==true (include_usage for final chunk).
	StreamOptions *oaiStreamOptions `json:"stream_options,omitempty"`
}

// oaiStreamOptions instructs the provider to include usage in the final chunk.
type oaiStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// oaiChatResponse is the OpenAI-compatible non-streaming response body.
type oaiChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaiUsage `json:"usage,omitempty"`
	Error *oaiError `json:"error,omitempty"`
}

// oaiStreamChunk is a single OpenAI-compatible SSE chunk.
type oaiStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *oaiUsage `json:"usage,omitempty"`
}

// oaiUsage maps to the "usage" field in OpenAI-compatible responses.
type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// oaiError is the error payload in an OpenAI-compatible error response.
type oaiError struct {
	Code    interface{} `json:"code"`
	Message string      `json:"message"`
	Type    string      `json:"type"`
}

// oaiEmbedRequest is the OpenAI-compatible embedding request body.
type oaiEmbedRequest struct {
	Model      string      `json:"model"`
	Input      interface{} `json:"input"` // string | []string
	Dimensions int         `json:"dimensions,omitempty"`
}

// oaiEmbedResponse is the OpenAI-compatible embedding response.
type oaiEmbedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// ----------------------------------------------------------------------------
// Shared helpers
// ----------------------------------------------------------------------------

// buildOAIMessages converts aiservice.ChatMessage slice to the OpenAI-compatible
// wire format, handling both text-only and multipart (vision) messages.
func buildOAIMessages(msgs []aiservice.ChatMessage) []oaiMessage {
	out := make([]oaiMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, buildOAIMessage(m))
	}
	return out
}

// buildOAIMessage converts a single aiservice.ChatMessage.
func buildOAIMessage(m aiservice.ChatMessage) oaiMessage {
	role := string(m.Role)
	// If there are multipart content parts, build a vision message.
	if len(m.Content.Parts) > 0 {
		parts := make([]oaiContentPart, 0, len(m.Content.Parts))
		for _, p := range m.Content.Parts {
			switch p.Type {
			case aiservice.MessagePartTypeText:
				parts = append(parts, oaiContentPart{Type: "text", Text: p.Text})
			case aiservice.MessagePartTypeImageURL:
				if p.ImageURL != nil {
					parts = append(parts, oaiContentPart{
						Type:     "image_url",
						ImageURL: &oaiImageURL{URL: p.ImageURL.URL},
					})
				}
			}
		}
		return oaiMessage{Role: role, Content: parts}
	}
	// Plain text message.
	return oaiMessage{Role: role, Content: m.Content.Text}
}
