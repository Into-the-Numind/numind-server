// Package capability provides a strongly-typed capability matrix for AI services,
// including helpers for modality routing and fallback policy resolution.
//
// The capability data is stored in ai_service.capability_json (a JSON column)
// and is projected into the Capabilities struct by this package. A 5-minute
// in-memory cache (sync.Map) reduces DB lookups in the hot path.
//
// Usage:
//
//	caps, err := capability.GetCapabilities("glm-5-1")
//	if err != nil { ... }
//
//	ok, err := capability.CanAcceptModality("glm-5-1", capability.MediaImage)
//
//	policy := capability.ResolveFallbackBehavior("glm-5-1", capability.MediaImage)
//	// → FallbackToText
package capability

// Capabilities is the structured projection of ai_service.capability_json.
// Zero-value is the conservative default (all false, no size limit).
type Capabilities struct {
	// AcceptsImageInline indicates the model can receive image bytes directly
	// in the messages payload (base64 or URL).
	AcceptsImageInline bool `json:"accepts_image_inline"`

	// AcceptsPDFInline indicates the model can receive PDF bytes directly
	// in the messages payload.
	AcceptsPDFInline bool `json:"accepts_pdf_inline"`

	// AcceptsAudioInline indicates the model can receive audio bytes directly
	// in the messages payload.
	AcceptsAudioInline bool `json:"accepts_audio_inline"`

	// MaxInlineSizeBytes is the maximum byte size of a single inline attachment.
	// 0 means "no inline attachments supported" (AcceptsXxxInline should also be false).
	MaxInlineSizeBytes int64 `json:"max_inline_size_bytes"`

	// SupportsVisionToolCalling indicates the model can invoke tool calls
	// while processing image input.
	SupportsVisionToolCalling bool `json:"supports_vision_tool_calling"`

	// PreferredImageFormat is the preferred encoding for image inline content.
	// Valid values: "base64" | "url". Defaults to "base64" if empty.
	PreferredImageFormat string `json:"preferred_image_format"`
}

// MediaType represents the type of an attachment or inline media block.
type MediaType string

const (
	// MediaImage is a raster image (PNG, JPEG, WebP, etc.).
	MediaImage MediaType = "image"

	// MediaPDF is a PDF document.
	MediaPDF MediaType = "pdf"

	// MediaAudio is an audio file (WAV, MP3, etc.).
	MediaAudio MediaType = "audio"
)

// FallbackPolicy describes how the system should handle an attachment when
// the target model does not natively support the media type inline.
type FallbackPolicy string

const (
	// FallbackInline means the model accepts the media type directly — send as-is.
	FallbackInline FallbackPolicy = "inline"

	// FallbackToText means convert the attachment to its text_fallback representation
	// (vision_description + ocr_text) before sending to the model.
	FallbackToText FallbackPolicy = "to_text"

	// FallbackToOCROnly means strip the visual content and send only the OCR-extracted
	// text (used for PDFs when the model cannot parse PDF bytes).
	FallbackToOCROnly FallbackPolicy = "to_ocr_only"

	// FallbackReject means the system cannot process this media type with the current
	// model and no acceptable fallback exists (e.g. audio in V1.5).
	FallbackReject FallbackPolicy = "reject"
)

// ErrModelNotFound is returned by GetCapabilities / CanAcceptModality when
// no ai_service row with the given model_key exists (or the DB lookup fails).
// Callers should treat this the same as a conservative default (FallbackToText).
var ErrModelNotFound = errModelNotFound("model not found in ai_service")

// errModelNotFound is a sentinel error type for missing model keys.
type errModelNotFound string

func (e errModelNotFound) Error() string { return string(e) }

// defaultConservative is the zero-value Capabilities returned when the model
// does not exist in the DB or capability_json is empty / unparseable.
// All inline modalities are disabled to prevent sending unknown content types
// to a model that may reject them.
var defaultConservative = Capabilities{
	AcceptsImageInline:        false,
	AcceptsPDFInline:          false,
	AcceptsAudioInline:        false,
	MaxInlineSizeBytes:        0,
	SupportsVisionToolCalling: false,
	PreferredImageFormat:      "base64",
}
