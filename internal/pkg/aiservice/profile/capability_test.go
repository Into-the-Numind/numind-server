package profile

import (
	"testing"
)

// llmSvcFullText is a generic text-only LLM service (e.g. deepseek-v3) with tool_use + streaming.
var llmSvcFullText = ServiceCapability{
	InputModalities:  []string{"text"},
	OutputModalities: []string{"text"},
	ContextWindow:    65536,
	MaxOutputTokens:  8192,
	Capabilities:     []string{"chat"},
	Features: map[string]bool{
		"tool_use":  true,
		"streaming": true,
		"json_mode": true,
		"vision":    false,
	},
}

// llmSvcVision is an LLM service that supports image input and vision feature.
var llmSvcVision = ServiceCapability{
	InputModalities:  []string{"text", "image"},
	OutputModalities: []string{"text"},
	ContextWindow:    32768,
	MaxOutputTokens:  4096,
	Capabilities:     []string{"chat", "vision"},
	Features: map[string]bool{
		"streaming": true,
		"vision":    true,
	},
}

// llmSvcEmbed1024 is an embedding service with 1024-dimensional vectors.
var llmSvcEmbed1024 = ServiceCapability{
	InputModalities: []string{"text"},
	Capabilities:    []string{"embedding"},
	Dimension:       1024,
}

// llmSvcEmbed768 is an embedding service with 768-dimensional vectors.
var llmSvcEmbed768 = ServiceCapability{
	InputModalities: []string{"text"},
	Capabilities:    []string{"embedding"},
	Dimension:       768,
}

// llmSvcSmallContext has only 8192 tokens of context window.
var llmSvcSmallContext = ServiceCapability{
	InputModalities: []string{"text"},
	ContextWindow:   8192,
	Capabilities:    []string{"chat"},
	Features: map[string]bool{
		"streaming": true,
	},
}

// ocrSvcBaidu mirrors the seed data for baidu-ocr-accurate.
var ocrSvcBaidu = ServiceCapability{
	ImageFormats:  []string{"jpg", "png", "bmp"},
	MaxResolution: 4096,
	MaxFileSizeMB: 10,
	Capabilities:  []string{"ocr"},
}

// asrSvcFunASR mirrors the seed data for funasr-paraformer.
var asrSvcFunASR = ServiceCapability{
	AudioFormats:   []string{"wav", "mp3", "m4a"},
	MaxDurationSec: 3600,
	Languages:      []string{"zh", "en"},
	Realtime:       false,
	Capabilities:   []string{"asr"},
}

func TestMatch_LLMCompatibleTextToolUse(t *testing.T) {
	// sop.text requirements vs deepseek-v3 style service → compatible
	req := Requirements{
		InputModalities: []string{"text"},
		MinContext:      8192,
		Features:        []string{"tool_use", "streaming"},
	}
	result := Match("llm", req, llmSvcFullText)
	if !result.Compatible {
		t.Errorf("expected compatible, got reasons: %v", result.Reasons)
	}
	if len(result.Reasons) != 0 {
		t.Errorf("expected no reasons, got: %v", result.Reasons)
	}
}

func TestMatch_LLMIncompatibleVisionRequired(t *testing.T) {
	// sop.vision requirements vs text-only model → incompatible: missing image modality + vision feature
	req := Requirements{
		InputModalities: []string{"text", "image"},
		MinContext:      8192,
		Features:        []string{"streaming", "vision"},
	}
	result := Match("llm", req, llmSvcFullText)
	if result.Compatible {
		t.Error("expected incompatible but got compatible")
	}
	mustContain(t, result.Reasons, "缺少输入模态: image")
	mustContain(t, result.Reasons, "缺少特性: vision")
}

func TestMatch_LLMEmbeddingDimensionMatch(t *testing.T) {
	// salesrag.embed with dimension=1024 vs embedding service dimension=1024 → compatible
	req := Requirements{
		Capability: "embedding",
		Dimension:  1024,
	}
	result := Match("llm", req, llmSvcEmbed1024)
	if !result.Compatible {
		t.Errorf("expected compatible, got reasons: %v", result.Reasons)
	}
}

func TestMatch_LLMEmbeddingDimensionMismatch(t *testing.T) {
	// salesrag.embed with dimension=1024 vs embedding service dimension=768 → incompatible
	req := Requirements{
		Capability: "embedding",
		Dimension:  1024,
	}
	result := Match("llm", req, llmSvcEmbed768)
	if result.Compatible {
		t.Error("expected incompatible but got compatible")
	}
	mustContain(t, result.Reasons, "embedding 维度不匹配（需要 1024，服务提供 768）")
}

func TestMatch_LLMInsufficientContext(t *testing.T) {
	// salesrag.chat requires min_context=16384, service has only 8192 → incompatible
	req := Requirements{
		InputModalities: []string{"text"},
		MinContext:      16384,
		Features:        []string{"streaming"},
	}
	result := Match("llm", req, llmSvcSmallContext)
	if result.Compatible {
		t.Error("expected incompatible but got compatible")
	}
	mustContain(t, result.Reasons, "上下文窗口不足（需要 ≥16384，服务提供 8192）")
}

func TestMatch_LLMVisionCompatible(t *testing.T) {
	// sop.vision requirements vs a vision-capable service → compatible
	req := Requirements{
		InputModalities: []string{"text", "image"},
		MinContext:      8192,
		Features:        []string{"streaming", "vision"},
	}
	result := Match("llm", req, llmSvcVision)
	if !result.Compatible {
		t.Errorf("expected compatible, got reasons: %v", result.Reasons)
	}
}

func TestMatch_OCRCompatible(t *testing.T) {
	// ocr.baidu requirements vs baidu-ocr-accurate capabilities → compatible
	req := Requirements{
		ImageFormats:  []string{"jpg", "png", "bmp"},
		MaxResolution: 4096,
	}
	result := Match("ocr", req, ocrSvcBaidu)
	if !result.Compatible {
		t.Errorf("expected compatible, got reasons: %v", result.Reasons)
	}
}

func TestMatch_OCRIncompatibleFormat(t *testing.T) {
	// task requires tiff, service only supports jpg/png → incompatible
	req := Requirements{
		ImageFormats: []string{"jpg", "tiff"},
	}
	result := Match("ocr", req, ocrSvcBaidu)
	if result.Compatible {
		t.Error("expected incompatible but got compatible")
	}
	mustContain(t, result.Reasons, "不支持图像格式: tiff")
}

func TestMatch_ASRIncompatibleDuration(t *testing.T) {
	// task requires max_duration_sec=7200, service max is 3600 → incompatible
	req := Requirements{
		AudioFormats:   []string{"wav"},
		MaxDurationSec: 7200,
	}
	result := Match("asr", req, asrSvcFunASR)
	if result.Compatible {
		t.Error("expected incompatible but got compatible")
	}
	mustContain(t, result.Reasons, "服务音频时长上限不足")
}

func TestMatch_ASRCompatible(t *testing.T) {
	// monitor.transcribe requirements vs funasr-paraformer → compatible
	req := Requirements{
		AudioFormats:   []string{"wav", "mp3", "m4a"},
		MaxDurationSec: 3600,
	}
	result := Match("asr", req, asrSvcFunASR)
	if !result.Compatible {
		t.Errorf("expected compatible, got reasons: %v", result.Reasons)
	}
}

func TestMatch_ASRIncompatibleLanguage(t *testing.T) {
	// task requires Japanese support, service only has zh/en → incompatible
	req := Requirements{
		AudioFormats: []string{"wav"},
		Languages:    []string{"zh", "ja"},
	}
	result := Match("asr", req, asrSvcFunASR)
	if result.Compatible {
		t.Error("expected incompatible but got compatible")
	}
	mustContain(t, result.Reasons, "不支持语言: ja")
}

func TestMatch_LLMMultipleIncompatibilities(t *testing.T) {
	// service has 8192 context, no vision, no image modality
	// task needs 16384 context + image + vision + json_mode
	req := Requirements{
		InputModalities: []string{"text", "image"},
		MinContext:      16384,
		Features:        []string{"vision", "json_mode"},
	}
	result := Match("llm", req, llmSvcSmallContext)
	if result.Compatible {
		t.Error("expected incompatible but got compatible")
	}
	if len(result.Reasons) < 3 {
		t.Errorf("expected ≥3 reasons, got %d: %v", len(result.Reasons), result.Reasons)
	}
	mustContain(t, result.Reasons, "缺少输入模态: image")
	mustContain(t, result.Reasons, "上下文窗口不足（需要 ≥16384，服务提供 8192）")
	mustContain(t, result.Reasons, "缺少特性: vision")
}

func TestMatch_UnknownServiceType(t *testing.T) {
	result := Match("tts", Requirements{}, ServiceCapability{})
	if result.Compatible {
		t.Error("expected incompatible for unknown service_type")
	}
	if len(result.Reasons) == 0 {
		t.Error("expected at least one reason for unknown service_type")
	}
}

func TestAllTaskIDs_Count(t *testing.T) {
	ids := AllTaskIDs()
	if len(ids) != 14 {
		t.Errorf("expected 14 task IDs, got %d", len(ids))
	}
	// Verify no duplicates.
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate task ID: %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestSchemaFor_KnownTypes(t *testing.T) {
	for _, st := range []string{"llm", "ocr", "asr"} {
		s, err := SchemaFor(st)
		if err != nil {
			t.Errorf("SchemaFor(%q) returned error: %v", st, err)
		}
		if s == nil {
			t.Errorf("SchemaFor(%q) returned nil schema", st)
		}
	}
}

func TestSchemaFor_UnknownType(t *testing.T) {
	_, err := SchemaFor("tts")
	if err == nil {
		t.Error("expected error for unknown service_type, got nil")
	}
}

// mustContain is a test helper that fails the test if needle is not in haystack.
func mustContain(t *testing.T, haystack []string, needle string) {
	t.Helper()
	for _, s := range haystack {
		if s == needle {
			return
		}
	}
	t.Errorf("expected reason %q in %v", needle, haystack)
}
