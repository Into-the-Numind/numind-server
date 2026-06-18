// Package aiservice provides the unified AI Gateway for all AI capability calls
// (Chat, Embed, Rerank, OCR, ASR). Business layers call this package rather than
// individual provider packages directly.
//
// This file defines the common request/response types used across all providers.
package aiservice

import "numind-server/internal/pkg/contextbudget"

// MessageRole represents the role of a chat message participant.
type MessageRole string

const (
	MessageRoleSystem    MessageRole = "system"
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
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
//
// For role=assistant messages that requested tool invocations, ToolCalls carries
// the parsed tool_calls array — the LLM-side function-call structure. For role=tool
// messages that report a tool's execution result back to the model, ToolCallID
// must reference the assistant message's tool_calls[N].id; OpenAI-compatible
// providers (DMXAPI / Ali / Volc) return HTTP 400 when a role=tool message is
// posted without this field set.
//
// Both fields use json:omitempty so non-tool turns marshal identically to the
// pre-Agent-mode wire shape (preserves SOP / chatbot byte-for-byte).
type ChatMessage struct {
	Role    MessageRole    `json:"role"`
	Content MessageContent `json:"content"`
	// ToolCallID is the id of the tool_call this message responds to. Required
	// for role=tool, ignored on all other roles.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolCalls is the assistant's requested tool invocations (role=assistant).
	// Empty / nil on any other role.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ReasoningContent is the thinking-mode chain-of-thought from the previous
	// assistant turn. For thinking-capable providers (DMXAPI deepseek-v4-pro,
	// AiHubMix o1/o3 / gpt-5), the API requires reasoning_content to be echoed
	// back in subsequent turns; otherwise the provider returns HTTP 400
	// ("The reasoning_content in the thinking mode must be passed back").
	// Empty for non-thinking models or non-assistant roles.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// TokenUsage reports the token consumption of a Chat call.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// ReasoningTokens is non-zero for thinking-capable models (e.g. deepseek-r1, o1).
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	// CachedPromptTokens is the subset of PromptTokens that the provider served
	// from its prompt-prefix cache (Batch A auto-caching: DeepSeek, GPT via the
	// DMXAPI OpenAI-compatible endpoint). It is a portion of PromptTokens, not
	// an addition to it — billing charges these tokens at the discounted cached
	// input rate. Additive field: providers that do not report cache hits leave
	// it 0 (omitempty), so cost/usage stay byte-identical to pre-cache behavior.
	CachedPromptTokens int `json:"cached_prompt_tokens,omitempty"`
	// CacheCreationTokens is the subset of PromptTokens the provider WROTE into its
	// prompt cache on this call (Anthropic cache_creation_input_tokens — a PREMIUM
	// bucket, NOT a discount). Distinct from CachedPromptTokens (read hits). Set
	// ONLY by the native Claude adapter; every other adapter leaves it 0 (omitempty),
	// so cost/usage stay byte-identical to pre-cache behavior. Gemini's implicit
	// cache has no separate creation bucket → 0 (D5).
	CacheCreationTokens int `json:"cache_creation_tokens,omitempty"`
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
	// ContextFragments is the ordered list of generic context fragments produced
	// by business code. The Gateway middleware (ContextBudgetCredits, Task 6)
	// reads them for budget/compression; RenderContextFragments flattens them into
	// ChatMessage entries when needed. May be nil for legacy callers; the
	// chain treats nil as "no fragments to budget" and falls back to Messages.
	ContextFragments []contextbudget.ContextFragment `json:"context_fragments,omitempty"`
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
	// Thinking requests that the model expose its reasoning process (chain of
	// thought) when the underlying provider/model supports it. The adapter is
	// responsible for translating this into provider-specific wire fields
	// (e.g. OpenAI-compatible reasoning_effort, DashScope enable_thinking).
	//
	// For intrinsic-thinking models (thinking_only=true), this flag is
	// advisory only — thinking always happens regardless of the value here.
	//
	// The json tag deliberately omits `omitempty`: traces must faithfully
	// show explicit `thinking=false` choices so that route-level debugging
	// can distinguish "caller did not opt in" from "field never serialised".
	Thinking bool `json:"thinking"`
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
	// TraceMetadata carries adapter-resolved routing decisions (reasoning
	// effort, model family, whether caller-supplied temperature was
	// overridden). Pointer type so the whole field is omitted when the
	// adapter did not populate it — keeps existing traces unchanged when
	// there is nothing to report.
	TraceMetadata *TraceMetadata `json:"trace_metadata,omitempty"`
}

// ToolCallArgsDelta is an OPTIONAL side-channel carried on interim ChatChunks
// to surface the incremental function.arguments fragment of an in-flight
// tool-call as it streams. It is purely additive observability for the agent
// "streaming code box" UX — it never participates in the execution contract.
// Execution still uses the fully assembled ToolCall on the IsFinal=true chunk.
//
// Only populated for tool calls the adapter recognises as code/content
// generating (see runner's isCodeStreamingTool allowlist); nil otherwise.
type ToolCallArgsDelta struct {
	// ToolCallID is the provider-assigned tool-call id (may be empty on the
	// first fragment if the provider sends id and the first arguments slice in
	// separate deltas; consumers should also key on the chunk arrival order).
	ToolCallID string `json:"tool_call_id"`
	// FunctionName is the tool name. Carried on every emitted delta (the
	// adapter remembers the per-index name from the first chunk).
	FunctionName string `json:"function_name"`
	// ArgsDelta is the incremental function.arguments JSON fragment for this
	// chunk. Concatenating all ArgsDelta values for a tool-call reconstructs
	// the full Function.Arguments surfaced on the terminal ToolCall.
	ArgsDelta string `json:"args_delta"`
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
	// ToolCalls carries assembled tool invocations the model requested.
	// OpenAI-compatible providers stream tool_calls as deltas: id + name + type
	// in the first chunk per index, arguments split across subsequent chunks.
	// The adapter accumulates the fragments and surfaces the completed
	// ToolCalls slice on the IsFinal=true chunk (when FinishReason="tool_calls"
	// or whichever chunk closes the stream after a tool-call sequence).
	// Empty for content-only streams.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
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
	// ToolCallArgsDelta is an OPTIONAL side-channel carrying the incremental
	// function.arguments fragment of an in-flight tool call (see the type doc).
	// Non-nil only on interim chunks for allowlisted code/content tools; the
	// terminal chunk still carries the fully assembled ToolCalls. Additive: a
	// nil value (the common case) changes nothing about the execution contract.
	ToolCallArgsDelta *ToolCallArgsDelta `json:"tool_call_args_delta,omitempty"`
	// Err carries a mid-stream failure distinctly from "stream ended normally".
	// Only populated on the terminal chunk (IsFinal=true). Consumers should check
	// `if chunk.IsFinal && chunk.Err != nil` to distinguish graceful end from
	// error termination. Before this field was added, errors were signaled only
	// by a FinishReason string prefix ("parse_error:", "scan_error:") which
	// forced consumers to string-match to differentiate. The json tag is "-"
	// because `error` does not round-trip through JSON; for wire/log use,
	// FinishReason still carries a human-readable summary.
	Err error `json:"-"`
	// TraceMetadata carries adapter-resolved routing decisions for the
	// stream. Only populated on the terminal chunk (IsFinal=true); nil on
	// all interim chunks. Same semantics as ChatResponse.TraceMetadata.
	TraceMetadata *TraceMetadata `json:"trace_metadata,omitempty"`
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

// ImageGenRequest is the unified request for text-to-image generation calls.
type ImageGenRequest struct {
	// Prompt is the text description of the image to generate.
	Prompt string `json:"prompt"`
	// AspectRatio hints the desired aspect ratio (e.g. "1:1", "16:9").
	// Empty defaults to the provider's default ratio.
	AspectRatio string `json:"aspect_ratio,omitempty"`
}

// ImageGenResponse is the unified response for text-to-image generation calls.
type ImageGenResponse struct {
	// ImageBase64 is the base64-encoded image payload (no data-URI prefix).
	ImageBase64 string `json:"image_base64"`
	// ContentType is the MIME type of the decoded image (e.g. "image/png").
	ContentType string `json:"content_type"`
	Model       string `json:"model"`
	Provider    string `json:"provider"`
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

// TraceMetadata captures the routing-level decisions the adapter made while
// servicing a Chat call, surfaced back to callers (and into Langfuse traces)
// so that behaviour around thinking-capable models is auditable.
//
// All fields are optional — adapters populate only the ones that apply for a
// given model. Zero values mean "not reported" (rather than "explicitly
// empty"), and the parent pointer on ChatResponse / ChatChunk is what
// actually gates JSON emission.
type TraceMetadata struct {
	// ResolvedReasoningEffort records HOW thinking was activated for this call.
	// Despite the historical name, the value is an activation-mechanism sentinel,
	// not strictly a reasoning-effort level. Possible values:
	//
	//   ""                      — not injected. Either the model does not support
	//                             thinking or the caller did not opt in.
	//   "medium"                — OpenAI-compatible `reasoning_effort: "medium"`
	//                             was injected (thinking_style ""/"reasoning_effort").
	//   "enable_thinking_kwarg" — Qwen/vLLM-style activation: the request carries
	//                             `chat_template_kwargs.enable_thinking: true`
	//                             (thinking_style="enable_thinking_kwarg", e.g. agnes-2.0-flash).
	//   "none"                  — model supports thinking and the caller opted in,
	//                             but thinking_style="none" so no activation field
	//                             was injected (distinct from "" = thinking off / unsupported).
	//   "intrinsic"             — Q8=B sentinel. The model is intrinsic-thinking
	//                             (thinking_only=true), so reasoning always happens and
	//                             no wire field is injected; records that thinking was in
	//                             effect conceptually even though the request carries no field.
	ResolvedReasoningEffort string `json:"resolved_reasoning_effort,omitempty"`
	// ResolvedModelFamily is the model family the adapter classified this
	// call into (e.g. "deepseek", "qwen", "doubao"). Useful for trace-level
	// grouping without having to parse the model name.
	ResolvedModelFamily string `json:"resolved_model_family,omitempty"`
	// TempOverridden is true when the adapter replaced the caller-supplied
	// Temperature with a model-mandated value (for example, some
	// thinking-only models require temperature=1.0). Callers can use this
	// to detect "my temperature knob was ignored".
	TempOverridden bool `json:"temp_overridden,omitempty"`
}
