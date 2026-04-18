// Package aiservice provides the unified AI Gateway for all AI capability calls
// (Chat, Embed, Rerank, OCR, ASR). Business layers call this package rather than
// individual provider packages directly.
//
// This file defines the common request/response types used across all providers.
package aiservice

// MessageRole represents the role of a chat message participant.
type MessageRole string

const (
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

// MessagePartType distinguishes text content from image content in a multipart message.
type MessagePartType string

const (
	MessagePartTypeText     MessagePartType = "text"
	MessagePartTypeImageURL MessagePartType = "image_url"
)

// ImageURL holds the URL or base64 data URI of an image part.
type ImageURL struct {
	// URL is a publicly accessible image URL or a base64 data URI
	// in the form "data:<mime>;base64,<encoded>".
	URL string `json:"url"`
}

// MessagePart represents a single content part within a multipart message.
// Use Type == "text" for plain text parts and Type == "image_url" for image parts.
type MessagePart struct {
	// Type identifies the part kind ("text" or "image_url").
	Type MessagePartType `json:"type"`
	// Text holds the text content when Type == "text".
	Text string `json:"text,omitempty"`
	// ImageURL holds the image reference when Type == "image_url".
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// MessageContent holds either a plain text string or a multipart slice.
// When Parts is non-empty it takes precedence over Text.
// Use Text for simple text-only messages; use Parts for multimodal (vision) messages.
type MessageContent struct {
	// Text is a plain text message (used when there are no image/file parts).
	Text string `json:"text,omitempty"`
	// Parts holds multipart content (text + images). Used for vision calls.
	Parts []MessagePart `json:"parts,omitempty"`
}

// ToolFunction describes the function signature within a Tool definition.
type ToolFunction struct {
	// Name is the function identifier (must match what the model will call).
	Name string `json:"name"`
	// Description is a human-readable explanation of what the function does.
	Description string `json:"description,omitempty"`
	// Parameters is a JSON Schema object describing the function parameters.
	Parameters map[string]interface{} `json:"parameters,omitempty"`
}

// Tool represents a single callable tool exposed to the LLM.
type Tool struct {
	// Type is always "function" for function-calling tools.
	Type string `json:"type"`
	// Function contains the function signature.
	Function ToolFunction `json:"function"`
}

// ToolCallFunction holds the function name and serialised arguments from a tool-call response.
type ToolCallFunction struct {
	// Name identifies which function the model wants to call.
	Name string `json:"name"`
	// Arguments is the JSON-encoded arguments string (as returned by the provider).
	Arguments string `json:"arguments"`
}

// ToolCall represents a single tool invocation requested by the model in a ChatResponse.
type ToolCall struct {
	// ID is the provider-assigned tool-call identifier, used when submitting tool results.
	ID string `json:"id"`
	// Type is always "function".
	Type string `json:"type"`
	// Function contains the invocation details.
	Function ToolCallFunction `json:"function"`
}

// ChatMessage is a single turn in a conversation.
type ChatMessage struct {
	Role    MessageRole    `json:"role"`
	Content MessageContent `json:"content"`
}

// TokenUsage reports the token consumption of a Chat call.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// ReasoningTokens is non-zero for thinking-capable models (e.g. deepseek-r1, o1).
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// ResponseFormatType enumerates the structured-output modes the Gateway
// knows how to translate for OpenAI-compatible providers.
type ResponseFormatType string

const (
	// ResponseFormatText is the provider default (free-form text). Leave empty
	// or set to this explicitly.
	ResponseFormatText ResponseFormatType = "text"
	// ResponseFormatJSONObject asks the provider to guarantee the response is a
	// valid JSON object. Maps to OpenAI's {"type":"json_object"} response_format.
	// Supported by: Ali DashScope (compatible-mode), Volc Ark, DMXAPI-proxied
	// DeepSeek / Qwen. Gemini uses responseMimeType="application/json" natively
	// but when called via DMXAPI OpenAI-compatible endpoint this hint is still
	// honoured. Callers should still include an explicit "return JSON" instruction
	// in the prompt for best results.
	ResponseFormatJSONObject ResponseFormatType = "json_object"
)

// ChatRequest is the unified request type for Chat and Vision calls.
// Vision is handled transparently: include image parts in MessageContent.Parts.
type ChatRequest struct {
	// Messages is the full conversation history.
	Messages []ChatMessage `json:"messages"`
	// MaxTokens caps the number of tokens the model may generate. 0 = provider default.
	MaxTokens int `json:"max_tokens,omitempty"`
	// Temperature controls output randomness (0.0–2.0). 0 = provider default.
	Temperature float64 `json:"temperature,omitempty"`
	// Tools is an optional list of function-calling tools.
	Tools []Tool `json:"tools,omitempty"`
	// ModelOverride allows the caller to request a specific model key.
	// If empty, the Task Profile's default service is used.
	ModelOverride string `json:"model_override,omitempty"`
	// ResponseFormat asks the provider for a specific output shape.
	// Empty = provider default (free text). See ResponseFormatType constants.
	// The json tag is for trace/log serialisation only; ChatRequest is an
	// internal type, never bound from HTTP.
	ResponseFormat ResponseFormatType `json:"response_format,omitempty"`
}

// ChatResponse is the unified response type for non-streaming Chat calls.
type ChatResponse struct {
	// Content is the primary text response from the model.
	Content string `json:"content"`
	// ReasoningContent contains the internal chain-of-thought for thinking models.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// ToolCalls lists any tool invocations requested by the model.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// FinishReason is the provider's stop reason ("stop", "length", "tool_calls", etc.).
	FinishReason string `json:"finish_reason,omitempty"`
	// Usage reports token consumption.
	Usage    TokenUsage `json:"usage"`
	Model    string     `json:"model"`
	Provider string     `json:"provider"`
}

// ChatChunk is a single streamed chunk emitted by ChatStream.
type ChatChunk struct {
	// Delta is the incremental text fragment for this chunk.
	Delta string `json:"delta"`
	// ReasoningDelta is the incremental reasoning fragment (thinking models only).
	ReasoningDelta string `json:"reasoning_delta,omitempty"`
	// Index is the chunk sequence number (0-based).
	Index int `json:"index"`
	// FinishReason is non-empty on the final chunk.
	FinishReason string `json:"finish_reason,omitempty"`
	// IsFinal marks the last chunk in the stream.
	IsFinal bool `json:"is_final"`
	// Usage is populated on the final chunk (IsFinal=true). Pointer so that
	// the field is omitted from JSON on non-final chunks (struct zero value
	// does not satisfy omitempty for a non-pointer struct).
	Usage *TokenUsage `json:"usage,omitempty"`
	// Model is the actual model name reported by the provider for this stream.
	// Populated on every chunk once the provider reports it; falls back to the
	// configured ProviderModelID when the provider omits it.
	Model string `json:"model,omitempty"`
	// Provider is the adapter name that produced this chunk (e.g. "ali", "volc").
	Provider string `json:"provider,omitempty"`
	// Err carries a mid-stream failure distinctly from "stream ended normally".
	// Only populated on the terminal chunk (IsFinal=true). Consumers should check
	// `if chunk.IsFinal && chunk.Err != nil` to distinguish graceful end from
	// error termination. Before this field was added, errors were signaled only
	// by a FinishReason string prefix ("parse_error:", "scan_error:") which
	// forced consumers to string-match to differentiate. The json tag is "-"
	// because `error` does not round-trip through JSON; for wire/log use,
	// FinishReason still carries a human-readable summary.
	Err error `json:"-"`
}

// EmbedRequest is the unified request for text embedding calls.
type EmbedRequest struct {
	// Texts is the list of strings to embed. Batching is handled by the adapter.
	Texts []string `json:"texts"`
	// Dimension is the requested output dimension. 0 = use provider default.
	Dimension int `json:"dimension,omitempty"`
}

// EmbedResponse is the unified response for embedding calls.
type EmbedResponse struct {
	// Embeddings is a parallel slice to the input Texts; each inner slice is one vector.
	Embeddings  [][]float32 `json:"embeddings"`
	Dimension   int         `json:"dimension"`
	Model       string      `json:"model"`
	Provider    string      `json:"provider"`
	TotalTokens int         `json:"total_tokens"`
}

// RerankRequest is the unified request for rerank calls.
type RerankRequest struct {
	// Query is the reference query string.
	Query string `json:"query"`
	// Documents is the list of candidate documents to rank.
	Documents []string `json:"documents"`
	// TopN limits how many results to return. 0 = return all.
	TopN int `json:"top_n,omitempty"`
}

// RerankResult is a single reranked document with its relevance score.
type RerankResult struct {
	// Index is the position of this document in the original Documents slice.
	Index int `json:"index"`
	// Score is the relevance score (higher = more relevant).
	Score float64 `json:"score"`
	// Document echoes the original document text for convenience.
	Document string `json:"document"`
}

// RerankResponse is the unified response for rerank calls.
type RerankResponse struct {
	// Results are sorted by descending relevance score.
	Results  []RerankResult `json:"results"`
	Model    string         `json:"model"`
	Provider string         `json:"provider"`
}

// OCRWord represents a single recognised word with its bounding box.
type OCRWord struct {
	// Word is the recognised text for this word.
	Word string `json:"word"`
	// BoundingBox is [left, top, right, bottom] in pixels, relative to the original image.
	// May be nil if the provider does not return positional data.
	BoundingBox []int `json:"bounding_box,omitempty"`
	// Confidence is the recognition confidence in [0, 1]. 0 means unavailable.
	Confidence float64 `json:"confidence,omitempty"`
}

// OCRRequest is the unified request for OCR calls.
type OCRRequest struct {
	// ImageURL is a publicly accessible image URL or a base64-encoded data URI.
	ImageURL string `json:"image_url,omitempty"`
	// ImageBytes holds raw image bytes. Used when ImageURL is empty.
	ImageBytes []byte `json:"image_bytes,omitempty"`
	// ImageFormat hints the image format ("jpg" | "png" | "bmp" | "pdf").
	// Providers may auto-detect if omitted.
	ImageFormat string `json:"image_format,omitempty"`
}

// OCRResponse is the unified response for OCR calls.
type OCRResponse struct {
	// Text is the full recognised text, concatenated from all words.
	Text string `json:"text"`
	// Words provides per-word detail with bounding boxes (provider-specific availability).
	Words    []OCRWord `json:"words,omitempty"`
	Provider string    `json:"provider"`
}

// ASRRequest is the unified request for ASR (speech-to-text) calls.
type ASRRequest struct {
	// AudioURL is a URL to the audio file. Mutually exclusive with AudioBytes.
	AudioURL string `json:"audio_url,omitempty"`
	// AudioBytes holds raw audio bytes when a URL is not available.
	AudioBytes []byte `json:"audio_bytes,omitempty"`
	// AudioFormat hints the audio container format ("wav" | "mp3" | "m4a").
	AudioFormat string `json:"audio_format,omitempty"`
	// Language is a BCP-47 language code hint (e.g. "zh", "en").
	// Leave empty to use provider auto-detection.
	Language string `json:"language,omitempty"`
}

// ASRResponse is the unified response for ASR calls.
type ASRResponse struct {
	// Text is the full transcription.
	Text string `json:"text"`
	// DurationSeconds is the audio duration consumed, used for per-second billing.
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	Provider        string  `json:"provider"`
}

// FileUploadRequest is the unified request for file upload calls.
type FileUploadRequest struct {
	// FileBytes holds the raw file contents to upload.
	FileBytes []byte `json:"file_bytes,omitempty"`
	// FileName is the logical file name (with extension), e.g. "document.pdf".
	FileName string `json:"file_name"`
	// MimeType is the MIME content type, e.g. "application/pdf" or "text/plain".
	// Providers may auto-detect if empty.
	MimeType string `json:"mime_type,omitempty"`
}

// FileUploadResponse is the unified response for file upload calls.
type FileUploadResponse struct {
	// FileID is the provider-assigned stable identifier for the uploaded file.
	// Use this ID when referencing the file in subsequent LLM calls.
	FileID     string `json:"file_id"`
	Provider   string `json:"provider"`
	UploadedAt string `json:"uploaded_at,omitempty"` // RFC3339 timestamp, if provided by the provider
}
