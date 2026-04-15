// Package aiservice provides the unified AI Gateway for all AI capability calls
// (Chat, Embed, Rerank, OCR, ASR). Business layers call this package rather than
// individual provider packages directly.
//
// This file defines the common request/response types used across all providers.
// Field definitions here are placeholder stubs — Task 2 will fill complete schemas.
package aiservice

// MessageRole represents the role of a chat message participant.
type MessageRole string

const (
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

// MessagePart represents a single part of a multipart message (text or image_url).
// TODO(Task 2): expand fields (type, text, image_url struct, etc.) when vision API is implemented.
type MessagePart struct {
	// TODO(Task 2): add Type, Text, ImageURL fields.
}

// MessageContent holds either a plain text string or a multipart slice.
// When Content is non-empty, Parts is ignored (and vice versa).
type MessageContent struct {
	// Text is a plain text message (used when there are no image/file parts).
	Text string `json:"text,omitempty"`
	// Parts holds multipart content (text + images). Used for vision calls.
	// TODO(Task 2): populate MessagePart fields when vision API is implemented.
	Parts []MessagePart `json:"parts,omitempty"`
}

// ChatMessage is a single turn in a conversation.
type ChatMessage struct {
	Role    MessageRole    `json:"role"`
	Content MessageContent `json:"content"`
}

// ChatRequest is the unified request type for Chat and Vision calls.
// Task 2 will add capability-specific fields (e.g. MaxTokens, Temperature, Stream).
type ChatRequest struct {
	Messages    []ChatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	// ModelOverride allows the caller to request a specific model key.
	// If empty, the Task Profile's default service is used.
	ModelOverride string `json:"model_override,omitempty"`
}

// ChatResponse is the unified response type for non-streaming Chat calls.
type ChatResponse struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	ReasoningTokens  int    `json:"reasoning_tokens,omitempty"`
	Model            string `json:"model"`
	Provider         string `json:"provider"`
}

// ChatChunk is a single streamed chunk from a ChatStream call.
type ChatChunk struct {
	Delta          string `json:"delta"`
	ReasoningDelta string `json:"reasoning_delta,omitempty"`
	IsFinal        bool   `json:"is_final"`
	// Final-only fields (populated when IsFinal=true)
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
}

// EmbedRequest is the unified request for text embedding calls.
type EmbedRequest struct {
	Texts     []string `json:"texts"`
	Dimension int      `json:"dimension,omitempty"` // 0 = use provider default
}

// EmbedResponse is the unified response for embedding calls.
type EmbedResponse struct {
	Embeddings  [][]float32 `json:"embeddings"`
	Dimension   int         `json:"dimension"`
	Model       string      `json:"model"`
	Provider    string      `json:"provider"`
	TotalTokens int         `json:"total_tokens"`
}

// RerankRequest is the unified request for rerank calls.
type RerankRequest struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n,omitempty"` // 0 = return all
}

// RerankResult is a single reranked document with its score.
type RerankResult struct {
	Index    int     `json:"index"`
	Score    float64 `json:"score"`
	Document string  `json:"document"`
}

// RerankResponse is the unified response for rerank calls.
type RerankResponse struct {
	Results  []RerankResult `json:"results"`
	Model    string         `json:"model"`
	Provider string         `json:"provider"`
}

// OCRRequest is the unified request for OCR calls.
// Task 2 will add image format, resolution hints, etc.
type OCRRequest struct {
	// ImageURL is a publicly accessible image URL, or a base64-encoded data URL.
	ImageURL string `json:"image_url,omitempty"`
	// ImageBytes holds raw image bytes when URL is not available.
	ImageBytes []byte `json:"image_bytes,omitempty"`
	// ImageFormat hints the image format (jpg | png | bmp | pdf).
	ImageFormat string `json:"image_format,omitempty"`
}

// OCRWord represents a single word with its bounding box from an OCR response.
// TODO(Task 2): add BoundingBox, Confidence, and other provider-specific fields.
type OCRWord struct {
	// TODO(Task 2): add Word, BoundingBox, Confidence fields.
}

// OCRResponse is the unified response for OCR calls.
type OCRResponse struct {
	Text     string    `json:"text"`
	Words    []OCRWord `json:"words,omitempty"` // per-word bounding boxes (provider-specific)
	Provider string    `json:"provider"`
}

// ASRRequest is the unified request for ASR (speech-to-text) calls.
type ASRRequest struct {
	// AudioURL is a URL to the audio file, or empty if AudioBytes is used.
	AudioURL string `json:"audio_url,omitempty"`
	// AudioBytes holds raw audio bytes.
	AudioBytes []byte `json:"audio_bytes,omitempty"`
	// AudioFormat hints the format (wav | mp3 | m4a).
	AudioFormat string `json:"audio_format,omitempty"`
	// Language hint (e.g. "zh", "en").
	Language string `json:"language,omitempty"`
}

// ASRResponse is the unified response for ASR calls.
type ASRResponse struct {
	Text     string  `json:"text"`
	Duration float64 `json:"duration_seconds,omitempty"` // audio duration used for billing
	Provider string  `json:"provider"`
}
