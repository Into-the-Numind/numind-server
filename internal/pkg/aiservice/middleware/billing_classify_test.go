package middleware

import (
	"testing"

	"numind-server/internal/pkg/aiservice"
)

// ----------------------------------------------------------------------------
// classifyServiceType unit tests
// ----------------------------------------------------------------------------

// TestClassifyServiceType_ChatText verifies that a ChatRequest with text-only
// messages resolves to "llm_chat".
func TestClassifyServiceType_ChatText(t *testing.T) {
	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role: aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{
					Text: "Hello, world",
				},
			},
		},
	}
	got := classifyServiceType(req, "llm")
	if got != "llm_chat" {
		t.Errorf("ChatText: got %q, want %q", got, "llm_chat")
	}
}

// TestClassifyServiceType_ChatWithImage verifies that a ChatRequest containing
// at least one image_url part in any message resolves to "llm_vision".
func TestClassifyServiceType_ChatWithImage(t *testing.T) {
	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role: aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{
					Parts: []aiservice.MessagePart{
						{Type: aiservice.MessagePartTypeText, Text: "Describe this image"},
						{
							Type:     aiservice.MessagePartTypeImageURL,
							ImageURL: &aiservice.ImageURL{URL: "https://example.com/img.png"},
						},
					},
				},
			},
		},
	}
	got := classifyServiceType(req, "llm")
	if got != "llm_vision" {
		t.Errorf("ChatWithImage: got %q, want %q", got, "llm_vision")
	}
}

// TestClassifyServiceType_ChatWithImage_Pointer verifies the *ChatRequest pointer variant.
func TestClassifyServiceType_ChatWithImage_Pointer(t *testing.T) {
	req := &aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role: aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{
					Parts: []aiservice.MessagePart{
						{
							Type:     aiservice.MessagePartTypeImageURL,
							ImageURL: &aiservice.ImageURL{URL: "data:image/png;base64,abc"},
						},
					},
				},
			},
		},
	}
	got := classifyServiceType(req, "llm")
	if got != "llm_vision" {
		t.Errorf("ChatWithImage pointer: got %q, want %q", got, "llm_vision")
	}
}

// TestClassifyServiceType_Embed verifies that an EmbedRequest resolves to "embedding".
func TestClassifyServiceType_Embed(t *testing.T) {
	req := aiservice.EmbedRequest{Texts: []string{"hello"}}
	got := classifyServiceType(req, "llm")
	if got != "embedding" {
		t.Errorf("Embed: got %q, want %q", got, "embedding")
	}
}

// TestClassifyServiceType_Rerank verifies that a RerankRequest resolves to "rerank".
func TestClassifyServiceType_Rerank(t *testing.T) {
	req := aiservice.RerankRequest{
		Query:     "what is AI",
		Documents: []string{"AI is...", "Machine learning..."},
	}
	got := classifyServiceType(req, "llm")
	if got != "rerank" {
		t.Errorf("Rerank: got %q, want %q", got, "rerank")
	}
}

// TestClassifyServiceType_OCR verifies that an OCRRequest resolves to "ocr".
func TestClassifyServiceType_OCR(t *testing.T) {
	req := aiservice.OCRRequest{ImageURL: "https://example.com/doc.png"}
	got := classifyServiceType(req, "llm")
	if got != "ocr" {
		t.Errorf("OCR: got %q, want %q", got, "ocr")
	}
}

// TestClassifyServiceType_ASR verifies that an ASRRequest resolves to "asr".
func TestClassifyServiceType_ASR(t *testing.T) {
	req := aiservice.ASRRequest{AudioURL: "https://example.com/audio.mp3"}
	got := classifyServiceType(req, "llm")
	if got != "asr" {
		t.Errorf("ASR: got %q, want %q", got, "asr")
	}
}

// TestClassifyServiceType_Unknown_FallbackToRegistry verifies that an unknown
// request type returns the fallbackServiceType passed in (registry coarse value).
func TestClassifyServiceType_Unknown_FallbackToRegistry(t *testing.T) {
	type unknownReq struct{ Field string }
	req := unknownReq{Field: "data"}
	got := classifyServiceType(req, "llm")
	if got != "llm" {
		t.Errorf("Unknown fallback: got %q, want %q", got, "llm")
	}
}

// TestClassifyServiceType_Unknown_FallbackOCR verifies fallback with a different
// coarse service type (ocr) from the registry.
func TestClassifyServiceType_Unknown_FallbackOCR(t *testing.T) {
	got := classifyServiceType(nil, "ocr")
	if got != "ocr" {
		t.Errorf("nil req with ocr fallback: got %q, want %q", got, "ocr")
	}
}

// TestClassifyServiceType_NilChatPointer verifies that a nil *ChatRequest
// pointer falls back to the fallbackServiceType.
func TestClassifyServiceType_NilChatPointer(t *testing.T) {
	var req *aiservice.ChatRequest
	got := classifyServiceType(req, "llm")
	if got != "llm" {
		t.Errorf("nil *ChatRequest: got %q, want %q", got, "llm")
	}
}

// TestClassifyServiceType_EmptyChatMessages verifies that a ChatRequest with
// no messages (empty slice) resolves to "llm_chat".
func TestClassifyServiceType_EmptyChatMessages(t *testing.T) {
	req := aiservice.ChatRequest{Messages: []aiservice.ChatMessage{}}
	got := classifyServiceType(req, "llm")
	if got != "llm_chat" {
		t.Errorf("empty messages: got %q, want %q", got, "llm_chat")
	}
}

// TestClassifyServiceType_MultiMessageFirstTextSecondImage verifies that
// vision is detected even when the image appears in the second message.
func TestClassifyServiceType_MultiMessageFirstTextSecondImage(t *testing.T) {
	req := aiservice.ChatRequest{
		Messages: []aiservice.ChatMessage{
			{
				Role:    aiservice.MessageRoleSystem,
				Content: aiservice.MessageContent{Text: "You are a vision assistant"},
			},
			{
				Role: aiservice.MessageRoleUser,
				Content: aiservice.MessageContent{
					Parts: []aiservice.MessagePart{
						{
							Type:     aiservice.MessagePartTypeImageURL,
							ImageURL: &aiservice.ImageURL{URL: "https://example.com/photo.jpg"},
						},
					},
				},
			},
		},
	}
	got := classifyServiceType(req, "llm")
	if got != "llm_vision" {
		t.Errorf("image in second message: got %q, want %q", got, "llm_vision")
	}
}
