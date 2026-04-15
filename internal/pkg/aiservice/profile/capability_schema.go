package profile

import "fmt"

// CapabilityField describes a single field in a service capability JSON document.
type CapabilityField struct {
	// Name is the JSON key of the field (snake_case).
	Name string
	// Type describes the logical data type. Recognised values:
	//   "modalities"    — []string, constrained to known modality values
	//   "string_list"   — []string, free-form
	//   "int"           — integer (capacity or dimension)
	//   "bool"          — boolean flag
	//   "feature_map"   — map[string]bool
	Type string
	// Required indicates whether this field must be present for a valid capability document.
	Required bool
	// EnumValues, when non-empty, lists the only accepted string values for this field.
	EnumValues []string
	// Description is a short human-readable annotation used in admin UI tooltips.
	Description string
}

// CapabilitySchema defines the expected shape of capability_json for a given service_type.
type CapabilitySchema struct {
	// ServiceType must match ai_service.service_type (llm | ocr | asr).
	ServiceType string
	// Fields lists all recognised capability fields for this service type.
	Fields []CapabilityField
}

// llmSchema is the capability schema for service_type = "llm".
// It covers chat, embedding, rerank and vision services.
// External callers should use SchemaFor("llm") instead of accessing this directly.
var llmSchema = CapabilitySchema{
	ServiceType: "llm",
	Fields: []CapabilityField{
		{
			Name:        "input_modalities",
			Type:        "modalities",
			Required:    true,
			EnumValues:  []string{"text", "image", "audio"},
			Description: "输入模态列表，如 [\"text\", \"image\"]",
		},
		{
			Name:        "output_modalities",
			Type:        "modalities",
			Required:    false,
			EnumValues:  []string{"text", "image"},
			Description: "输出模态列表，通常为 [\"text\"]",
		},
		{
			Name:        "context_window",
			Type:        "int",
			Required:    false,
			Description: "最大上下文窗口（token 数）",
		},
		{
			Name:        "max_output_tokens",
			Type:        "int",
			Required:    false,
			Description: "单次最大输出 token 数",
		},
		{
			Name:        "capabilities",
			Type:        "string_list",
			Required:    false,
			EnumValues:  []string{"chat", "embedding", "rerank", "vision"},
			Description: "服务支持的能力大类",
		},
		{
			Name:        "dimension",
			Type:        "int",
			Required:    false,
			Description: "Embedding 向量维度（embedding 类服务必填）",
		},
		{
			Name:        "features",
			Type:        "feature_map",
			Required:    false,
			Description: "特性开关，如 {\"tool_use\":true, \"streaming\":true, \"vision\":true, \"json_mode\":true, \"thinking\":false}",
		},
	},
}

// ocrSchema is the capability schema for service_type = "ocr".
// External callers should use SchemaFor("ocr") instead of accessing this directly.
var ocrSchema = CapabilitySchema{
	ServiceType: "ocr",
	Fields: []CapabilityField{
		{
			Name:        "image_formats",
			Type:        "string_list",
			Required:    true,
			EnumValues:  []string{"jpg", "png", "bmp", "pdf", "tiff", "gif", "webp"},
			Description: "支持的图像格式列表",
		},
		{
			Name:        "max_resolution",
			Type:        "int",
			Required:    false,
			Description: "支持的最大图像长/宽像素数",
		},
		{
			Name:        "max_file_size_mb",
			Type:        "int",
			Required:    false,
			Description: "支持的单图最大文件大小（MB）",
		},
		{
			Name:        "capabilities",
			Type:        "string_list",
			Required:    false,
			EnumValues:  []string{"ocr", "table", "formula", "handwriting"},
			Description: "OCR 特殊能力标签",
		},
	},
}

// asrSchema is the capability schema for service_type = "asr".
// External callers should use SchemaFor("asr") instead of accessing this directly.
var asrSchema = CapabilitySchema{
	ServiceType: "asr",
	Fields: []CapabilityField{
		{
			Name:        "audio_formats",
			Type:        "string_list",
			Required:    true,
			EnumValues:  []string{"wav", "mp3", "m4a", "flac", "ogg", "aac"},
			Description: "支持的音频格式列表",
		},
		{
			Name:        "max_duration_sec",
			Type:        "int",
			Required:    false,
			Description: "单次识别最大音频时长（秒）",
		},
		{
			Name:        "languages",
			Type:        "string_list",
			Required:    false,
			Description: "支持的语言代码列表，如 [\"zh\", \"en\"]",
		},
		{
			Name:        "realtime",
			Type:        "bool",
			Required:    false,
			Description: "是否支持实时流式识别",
		},
		{
			Name:        "capabilities",
			Type:        "string_list",
			Required:    false,
			EnumValues:  []string{"asr", "speaker_diarization", "punctuation"},
			Description: "ASR 特殊能力标签",
		},
	},
}

// allSchemas is the single source of truth for registered CapabilitySchemas,
// keyed by service_type. Consumed by SchemaFor. External callers should use
// SchemaFor rather than accessing this map directly.
var allSchemas = map[string]*CapabilitySchema{
	"llm": &llmSchema,
	"ocr": &ocrSchema,
	"asr": &asrSchema,
}

// SchemaFor returns the CapabilitySchema for the given service_type.
// Returns an error if the service_type is not recognised.
func SchemaFor(serviceType string) (*CapabilitySchema, error) {
	s, ok := allSchemas[serviceType]
	if !ok {
		return nil, fmt.Errorf("unknown service_type %q: supported types are llm, ocr, asr", serviceType)
	}
	return s, nil
}
