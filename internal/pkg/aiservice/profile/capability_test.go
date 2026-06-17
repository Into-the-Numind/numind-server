package profile

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Shared service-capability fixtures
// ---------------------------------------------------------------------------

// llmSvcFullText is a generic text-only LLM service (e.g. deepseek-v3) with tool_use + streaming.
var llmSvcFullText = ServiceCapability{
	ServiceType:      "llm",
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
	ServiceType:      "llm",
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
	ServiceType:     "llm",
	InputModalities: []string{"text"},
	Capabilities:    []string{"embedding"},
	Dimension:       1024,
}

// llmSvcEmbed768 is an embedding service with 768-dimensional vectors.
var llmSvcEmbed768 = ServiceCapability{
	ServiceType:     "llm",
	InputModalities: []string{"text"},
	Capabilities:    []string{"embedding"},
	Dimension:       768,
}

// llmSvcSmallContext has only 8192 tokens of context window.
var llmSvcSmallContext = ServiceCapability{
	ServiceType:     "llm",
	InputModalities: []string{"text"},
	ContextWindow:   8192,
	Capabilities:    []string{"chat"},
	Features: map[string]bool{
		"streaming": true,
	},
}

// ocrSvcBaidu mirrors the seed data for baidu-ocr-accurate.
var ocrSvcBaidu = ServiceCapability{
	ServiceType:   "ocr",
	ImageFormats:  []string{"jpg", "png", "bmp"},
	MaxResolution: 4096,
	MaxFileSizeMB: 10,
	Capabilities:  []string{"ocr"},
}

// asrSvcFunASR mirrors the seed data for funasr-paraformer.
var asrSvcFunASR = ServiceCapability{
	ServiceType:    "asr",
	AudioFormats:   []string{"wav", "mp3", "m4a"},
	MaxDurationSec: 3600,
	Languages:      []string{"zh", "en"},
	Realtime:       false,
	Capabilities:   []string{"asr"},
}

// ---------------------------------------------------------------------------
// Helper: assert that at least one reason contains each expected keyword.
// ---------------------------------------------------------------------------
func assertReasonContains(t *testing.T, reasons []string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		found := false
		for _, r := range reasons {
			if strings.Contains(r, want) {
				found = true
				break
			}
		}
		assert.True(t, found, "expected a reason containing %q; got: %v", want, reasons)
	}
}

// ---------------------------------------------------------------------------
// TestMatch_LLM — table-driven LLM scenarios
// ---------------------------------------------------------------------------

func TestMatch_LLM(t *testing.T) {
	type tc struct {
		name        string
		req         Requirements
		svc         ServiceCapability
		wantMatch   bool
		wantReasons []string // keywords that must appear in at least one reason
	}

	cases := []tc{
		{
			name: "text_tool_use_compatible",
			req: Requirements{
				InputModalities: []string{"text"},
				MinContext:      8192,
				Features:        []string{"tool_use", "streaming"},
			},
			svc:       llmSvcFullText,
			wantMatch: true,
		},
		{
			name: "vision_required_text_only_service_incompatible",
			req: Requirements{
				InputModalities: []string{"text", "image"},
				MinContext:      8192,
				Features:        []string{"streaming", "vision"},
			},
			svc:         llmSvcFullText,
			wantMatch:   false,
			wantReasons: []string{"缺少输入模态", "缺少特性"},
		},
		{
			name: "embedding_dimension_match",
			req: Requirements{
				Capability: "embedding",
				Dimension:  1024,
			},
			svc:       llmSvcEmbed1024,
			wantMatch: true,
		},
		{
			name: "embedding_dimension_mismatch",
			req: Requirements{
				Capability: "embedding",
				Dimension:  1024,
			},
			svc:         llmSvcEmbed768,
			wantMatch:   false,
			wantReasons: []string{"embedding 维度不匹配"},
		},
		{
			name: "insufficient_context",
			req: Requirements{
				InputModalities: []string{"text"},
				MinContext:      16384,
				Features:        []string{"streaming"},
			},
			svc:         llmSvcSmallContext,
			wantMatch:   false,
			wantReasons: []string{"上下文窗口不足"},
		},
		{
			name: "vision_service_compatible",
			req: Requirements{
				InputModalities: []string{"text", "image"},
				MinContext:      8192,
				Features:        []string{"streaming", "vision"},
			},
			svc:       llmSvcVision,
			wantMatch: true,
		},
		{
			name: "tool_use_feature_compatible",
			req: Requirements{
				InputModalities: []string{"text"},
				Features:        []string{"tool_use", "json_mode"},
			},
			svc:       llmSvcFullText,
			wantMatch: true,
		},
		{
			name: "multiple_incompatibilities",
			// service has 8192 context, no image modality, no vision/json_mode features
			req: Requirements{
				InputModalities: []string{"text", "image"},
				MinContext:      16384,
				Features:        []string{"vision", "json_mode"},
			},
			svc:         llmSvcSmallContext,
			wantMatch:   false,
			wantReasons: []string{"缺少输入模态", "上下文窗口不足", "缺少特性"},
		},
		{
			name: "service_type_mismatch_returns_incompatible",
			req: Requirements{
				InputModalities: []string{"text"},
			},
			svc: ServiceCapability{
				ServiceType: "ocr", // caller passes serviceType="llm" but svc says "ocr"
			},
			wantMatch:   false,
			wantReasons: []string{"service_type 不一致"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Match("llm", tc.req, tc.svc)
			assert.Equal(t, tc.wantMatch, got.Compatible, "Compatible mismatch for %s", tc.name)
			if len(tc.wantReasons) > 0 {
				assertReasonContains(t, got.Reasons, tc.wantReasons...)
			}
		})
	}
}

// TestMatch_LLM_VisionViaInputModalitiesOnly locks the vision-capability-unify
// routing contract: after the migration drops `vision` from vision-task feature
// requirements, a model is matched for image input PURELY via input_modalities
// containing "image" — it does NOT need a `vision` feature flag. Proves the
// task_profile cleanup (drop features.vision) is a no-op on routing behaviour.
func TestMatch_LLM_VisionViaInputModalitiesOnly(t *testing.T) {
	// New vision-task requirement shape (post-migration): image modality, NO vision feature.
	visionReq := Requirements{
		InputModalities: []string{"text", "image"},
		MinContext:      8192,
		Features:        []string{"streaming"}, // vision feature removed
	}

	// A vision model that signals vision ONLY via input_modalities (no features.vision,
	// no capabilities=vision) — the new single-source-of-truth shape.
	visionModelNoFlag := ServiceCapability{
		ServiceType:     "llm",
		InputModalities: []string{"text", "image"},
		ContextWindow:   32768,
		MaxOutputTokens: 4096,
		Capabilities:    []string{"chat"},
		Features:        map[string]bool{"streaming": true},
	}

	// 1. legacy vision model (still carries features.vision) matches the new req.
	got := Match("llm", visionReq, llmSvcVision)
	assert.True(t, got.Compatible, "legacy vision model should match vision req w/o features.vision: %v", got.Reasons)

	// 2. vision model signalled ONLY by input_modalities=image matches (features.vision not needed).
	got = Match("llm", visionReq, visionModelNoFlag)
	assert.True(t, got.Compatible, "input_modalities-only vision model should match: %v", got.Reasons)

	// 3. text-only model still rejected (input_modalities guard holds → non-vision not误匹配).
	got = Match("llm", visionReq, llmSvcFullText)
	assert.False(t, got.Compatible, "text-only model must NOT match a vision req")
}

// TestLLMSchema_VisionViaInputModalities locks the vision-capability-unify schema
// cleanup: the capabilities enum no longer offers "vision", and input_modalities
// is documented as THE vision control.
func TestLLMSchema_VisionViaInputModalities(t *testing.T) {
	schema, err := SchemaFor("llm")
	if err != nil {
		t.Fatalf("SchemaFor(llm): %v", err)
	}
	var caps, inputMod *CapabilityField
	for i := range schema.Fields {
		switch schema.Fields[i].Name {
		case "capabilities":
			caps = &schema.Fields[i]
		case "input_modalities":
			inputMod = &schema.Fields[i]
		}
	}
	if caps == nil || inputMod == nil {
		t.Fatal("expected capabilities + input_modalities fields in llm schema")
	}
	for _, ev := range caps.EnumValues {
		assert.NotEqual(t, "vision", ev, "capabilities enum must NOT contain retired 'vision'; got %v", caps.EnumValues)
	}
	assert.Contains(t, inputMod.Description, "唯一生效", "input_modalities should be documented as the sole vision control")
	var hasImage bool
	for _, ev := range inputMod.EnumValues {
		if ev == "image" {
			hasImage = true
		}
	}
	assert.True(t, hasImage, "input_modalities enum must still offer 'image'")
}

// ---------------------------------------------------------------------------
// TestMatch_OCR — table-driven OCR scenarios
// ---------------------------------------------------------------------------

func TestMatch_OCR(t *testing.T) {
	type tc struct {
		name        string
		req         Requirements
		svc         ServiceCapability
		wantMatch   bool
		wantReasons []string
	}

	cases := []tc{
		{
			name: "compatible_formats_and_resolution",
			req: Requirements{
				ImageFormats:  []string{"jpg", "png", "bmp"},
				MaxResolution: 4096,
			},
			svc:       ocrSvcBaidu,
			wantMatch: true,
		},
		{
			name: "unsupported_format_tiff",
			req: Requirements{
				ImageFormats: []string{"jpg", "tiff"},
			},
			svc:         ocrSvcBaidu,
			wantMatch:   false,
			wantReasons: []string{"不支持图像格式"},
		},
		{
			name: "insufficient_resolution",
			req: Requirements{
				ImageFormats:  []string{"jpg"},
				MaxResolution: 8192,
			},
			svc:         ocrSvcBaidu,
			wantMatch:   false,
			wantReasons: []string{"服务分辨率上限不足"},
		},
		{
			name: "file_size_exceeded_shows_numbers",
			req: Requirements{
				ImageFormats:  []string{"jpg"},
				MaxFileSizeMB: 20, // task needs 20 MB, service cap is 10 MB
			},
			svc:         ocrSvcBaidu,
			wantMatch:   false,
			wantReasons: []string{"服务文件大小上限不足", "MB"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Match("ocr", tc.req, tc.svc)
			assert.Equal(t, tc.wantMatch, got.Compatible, "Compatible mismatch for %s", tc.name)
			if len(tc.wantReasons) > 0 {
				assertReasonContains(t, got.Reasons, tc.wantReasons...)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestMatch_ASR — table-driven ASR scenarios
// ---------------------------------------------------------------------------

func TestMatch_ASR(t *testing.T) {
	type tc struct {
		name        string
		req         Requirements
		svc         ServiceCapability
		wantMatch   bool
		wantReasons []string
	}

	cases := []tc{
		{
			name: "compatible_formats_duration_language",
			req: Requirements{
				AudioFormats:   []string{"wav", "mp3", "m4a"},
				MaxDurationSec: 3600,
			},
			svc:       asrSvcFunASR,
			wantMatch: true,
		},
		{
			name: "duration_exceeded",
			req: Requirements{
				AudioFormats:   []string{"wav"},
				MaxDurationSec: 7200, // task needs 2h, service supports 1h
			},
			svc:         asrSvcFunASR,
			wantMatch:   false,
			wantReasons: []string{"服务音频时长上限不足"},
		},
		{
			name: "language_not_supported",
			req: Requirements{
				AudioFormats: []string{"wav"},
				Languages:    []string{"zh", "ja"}, // Japanese not in zh/en
			},
			svc:         asrSvcFunASR,
			wantMatch:   false,
			wantReasons: []string{"不支持语言"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Match("asr", tc.req, tc.svc)
			assert.Equal(t, tc.wantMatch, got.Compatible, "Compatible mismatch for %s", tc.name)
			if len(tc.wantReasons) > 0 {
				assertReasonContains(t, got.Reasons, tc.wantReasons...)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestMatch_UnknownServiceType — standalone (not a domain-specific table)
// ---------------------------------------------------------------------------

func TestMatch_UnknownServiceType(t *testing.T) {
	result := Match("tts", Requirements{}, ServiceCapability{})
	assert.False(t, result.Compatible, "expected incompatible for unknown service_type")
	assert.NotEmpty(t, result.Reasons, "expected at least one reason for unknown service_type")
}

// ---------------------------------------------------------------------------
// Meta tests — AllTaskIDs and SchemaFor
// ---------------------------------------------------------------------------

func TestAllTaskIDs_Count(t *testing.T) {
	ids := AllTaskIDs()
	assert.Equal(t, 30, len(ids), "expected 30 task IDs (14 base + 7 agent-mode #14 + 1 agent-imagegen-via-aiservice: agent.image_gen + 2 V1.5 attachment + 4 V1.5 memory: extract + select + dialectic + digest + 1 v2 marketplace: skill.marketplace.sanitize + 1 adaptive-session-titles: session.title)")

	// Verify no duplicates.
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		_, dup := seen[id]
		assert.False(t, dup, "duplicate task ID: %s", id)
		seen[id] = struct{}{}
	}
}

func TestAllTaskIDs_ReturnsCopy(t *testing.T) {
	// Mutating the returned slice must not affect subsequent calls.
	ids1 := AllTaskIDs()
	ids1[0] = "mutated"
	ids2 := AllTaskIDs()
	assert.NotEqual(t, "mutated", ids2[0], "AllTaskIDs should return a copy, not the backing slice")
}

func TestSchemaFor_KnownTypes(t *testing.T) {
	for _, st := range []string{"llm", "ocr", "asr"} {
		s, err := SchemaFor(st)
		assert.NoError(t, err, "SchemaFor(%q) returned unexpected error", st)
		assert.NotNil(t, s, "SchemaFor(%q) returned nil schema", st)
	}
}

func TestSchemaFor_UnknownType(t *testing.T) {
	_, err := SchemaFor("tts")
	assert.Error(t, err, "expected error for unknown service_type")
}
