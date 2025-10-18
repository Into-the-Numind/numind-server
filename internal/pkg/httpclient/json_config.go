package httpclient

import (
	"github.com/spf13/viper"
)

// JSONProcessingConfig JSON处理配置
type JSONProcessingConfig struct {
	CharacterFiltering CharacterFilteringConfig `json:"character_filtering"`
	JSONRepair         JSONRepairConfig         `json:"json_repair"`
	ResponseProcessing ResponseProcessingConfig `json:"response_processing"`
}

// CharacterFilteringConfig 字符过滤配置
type CharacterFilteringConfig struct {
	StrictControlChars       bool                `json:"strict_control_chars"`
	FilterExtendedASCII      bool                `json:"filter_extended_ascii"`
	FilterUnicodeReplacement bool                `json:"filter_unicode_replacement"`
	AllowedControlChars      []string            `json:"allowed_control_chars"`
	AllowedUnicodeRanges     UnicodeRangesConfig `json:"allowed_unicode_ranges"`
}

// UnicodeRangesConfig Unicode范围配置
type UnicodeRangesConfig struct {
	Chinese            []int   `json:"chinese"`
	ChinesePunctuation []int   `json:"chinese_punctuation"`
	Fullwidth          []int   `json:"fullwidth"`
	LatinExtended      [][]int `json:"latin_extended"`
	Arabic             []int   `json:"arabic"`
	Cyrillic           []int   `json:"cyrillic"`
	Greek              []int   `json:"greek"`
	Hebrew             []int   `json:"hebrew"`
	Thai               []int   `json:"thai"`
	Korean             []int   `json:"korean"`
	JapaneseHiragana   []int   `json:"japanese_hiragana"`
	JapaneseKatakana   []int   `json:"japanese_katakana"`
}

// JSONRepairConfig JSON修复配置
type JSONRepairConfig struct {
	EnableDeepRepair           bool `json:"enable_deep_repair"`
	EnableFieldBasedExtraction bool `json:"enable_field_based_extraction"`
	EnableConservativeFix      bool `json:"enable_conservative_fix"`
	MaxRepairAttempts          int  `json:"max_repair_attempts"`
	EnableLogging              bool `json:"enable_logging"`
}

// ResponseProcessingConfig 响应处理配置
type ResponseProcessingConfig struct {
	CheckContentLength     bool   `json:"check_content_length"`
	EnableResponseRecovery bool   `json:"enable_response_recovery"`
	Timeout                string `json:"timeout"`
	MaxResponseSize        int64  `json:"max_response_size"`
}

// LoadJSONProcessingConfig 从Viper配置加载JSON处理配置
func LoadJSONProcessingConfig() *JSONProcessingConfig {
	config := &JSONProcessingConfig{
		CharacterFiltering: CharacterFilteringConfig{
			StrictControlChars:       true,
			FilterExtendedASCII:      true,
			FilterUnicodeReplacement: true,
			AllowedControlChars:      []string{"\n", "\t"},
			AllowedUnicodeRanges: UnicodeRangesConfig{
				Chinese:            []int{0x4E00, 0x9FFF},
				ChinesePunctuation: []int{0x3000, 0x303F},
				Fullwidth:          []int{0xFF00, 0xFFEF},
				LatinExtended: [][]int{
					{0x00C0, 0x00FF},
					{0x0100, 0x017F},
					{0x0180, 0x024F},
				},
				Arabic:           []int{0x0600, 0x06FF},
				Cyrillic:         []int{0x0400, 0x04FF},
				Greek:            []int{0x0370, 0x03FF},
				Hebrew:           []int{0x0590, 0x05FF},
				Thai:             []int{0x0E00, 0x0E7F},
				Korean:           []int{0xAC00, 0xD7AF},
				JapaneseHiragana: []int{0x3040, 0x309F},
				JapaneseKatakana: []int{0x30A0, 0x30FF},
			},
		},
		JSONRepair: JSONRepairConfig{
			EnableDeepRepair:           true,
			EnableFieldBasedExtraction: true,
			EnableConservativeFix:      true,
			MaxRepairAttempts:          3,
			EnableLogging:              true,
		},
		ResponseProcessing: ResponseProcessingConfig{
			CheckContentLength:     true,
			EnableResponseRecovery: true,
			Timeout:                "30s",
			MaxResponseSize:        1048576, // 1MB
		},
	}

	// 从Viper配置覆盖默认值
	if viper.IsSet("json_processing.character_filtering.strict_control_chars") {
		config.CharacterFiltering.StrictControlChars = viper.GetBool("json_processing.character_filtering.strict_control_chars")
	}
	if viper.IsSet("json_processing.character_filtering.filter_extended_ascii") {
		config.CharacterFiltering.FilterExtendedASCII = viper.GetBool("json_processing.character_filtering.filter_extended_ascii")
	}
	if viper.IsSet("json_processing.character_filtering.filter_unicode_replacement") {
		config.CharacterFiltering.FilterUnicodeReplacement = viper.GetBool("json_processing.character_filtering.filter_unicode_replacement")
	}
	if viper.IsSet("json_processing.character_filtering.allowed_control_chars") {
		config.CharacterFiltering.AllowedControlChars = viper.GetStringSlice("json_processing.character_filtering.allowed_control_chars")
	}

	// 加载Unicode范围配置
	loadUnicodeRangesConfig(config)

	// 加载JSON修复配置
	if viper.IsSet("json_processing.json_repair.enable_deep_repair") {
		config.JSONRepair.EnableDeepRepair = viper.GetBool("json_processing.json_repair.enable_deep_repair")
	}
	if viper.IsSet("json_processing.json_repair.enable_field_based_extraction") {
		config.JSONRepair.EnableFieldBasedExtraction = viper.GetBool("json_processing.json_repair.enable_field_based_extraction")
	}
	if viper.IsSet("json_processing.json_repair.enable_conservative_fix") {
		config.JSONRepair.EnableConservativeFix = viper.GetBool("json_processing.json_repair.enable_conservative_fix")
	}
	if viper.IsSet("json_processing.json_repair.max_repair_attempts") {
		config.JSONRepair.MaxRepairAttempts = viper.GetInt("json_processing.json_repair.max_repair_attempts")
	}
	if viper.IsSet("json_processing.json_repair.enable_logging") {
		config.JSONRepair.EnableLogging = viper.GetBool("json_processing.json_repair.enable_logging")
	}

	// 加载响应处理配置
	if viper.IsSet("json_processing.response_processing.check_content_length") {
		config.ResponseProcessing.CheckContentLength = viper.GetBool("json_processing.response_processing.check_content_length")
	}
	if viper.IsSet("json_processing.response_processing.enable_response_recovery") {
		config.ResponseProcessing.EnableResponseRecovery = viper.GetBool("json_processing.response_processing.enable_response_recovery")
	}
	if viper.IsSet("json_processing.response_processing.timeout") {
		config.ResponseProcessing.Timeout = viper.GetString("json_processing.response_processing.timeout")
	}
	if viper.IsSet("json_processing.response_processing.max_response_size") {
		config.ResponseProcessing.MaxResponseSize = viper.GetInt64("json_processing.response_processing.max_response_size")
	}

	return config
}

// loadUnicodeRangesConfig 加载Unicode范围配置
func loadUnicodeRangesConfig(config *JSONProcessingConfig) {
	// 加载中文字符范围
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.chinese") {
		config.CharacterFiltering.AllowedUnicodeRanges.Chinese = viper.GetIntSlice("json_processing.character_filtering.allowed_unicode_ranges.chinese")
	}
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.chinese_punctuation") {
		config.CharacterFiltering.AllowedUnicodeRanges.ChinesePunctuation = viper.GetIntSlice("json_processing.character_filtering.allowed_unicode_ranges.chinese_punctuation")
	}
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.fullwidth") {
		config.CharacterFiltering.AllowedUnicodeRanges.Fullwidth = viper.GetIntSlice("json_processing.character_filtering.allowed_unicode_ranges.fullwidth")
	}
	// 注意：latin_extended 是嵌套数组，需要特殊处理
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.latin_extended") {
		// 这里需要手动处理嵌套数组，暂时保持默认值
		// TODO: 实现嵌套数组的配置加载
	}
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.arabic") {
		config.CharacterFiltering.AllowedUnicodeRanges.Arabic = viper.GetIntSlice("json_processing.character_filtering.allowed_unicode_ranges.arabic")
	}
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.cyrillic") {
		config.CharacterFiltering.AllowedUnicodeRanges.Cyrillic = viper.GetIntSlice("json_processing.character_filtering.allowed_unicode_ranges.cyrillic")
	}
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.greek") {
		config.CharacterFiltering.AllowedUnicodeRanges.Greek = viper.GetIntSlice("json_processing.character_filtering.allowed_unicode_ranges.greek")
	}
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.hebrew") {
		config.CharacterFiltering.AllowedUnicodeRanges.Hebrew = viper.GetIntSlice("json_processing.character_filtering.allowed_unicode_ranges.hebrew")
	}
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.thai") {
		config.CharacterFiltering.AllowedUnicodeRanges.Thai = viper.GetIntSlice("json_processing.character_filtering.allowed_unicode_ranges.thai")
	}
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.korean") {
		config.CharacterFiltering.AllowedUnicodeRanges.Korean = viper.GetIntSlice("json_processing.character_filtering.allowed_unicode_ranges.korean")
	}
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.japanese_hiragana") {
		config.CharacterFiltering.AllowedUnicodeRanges.JapaneseHiragana = viper.GetIntSlice("json_processing.character_filtering.allowed_unicode_ranges.japanese_hiragana")
	}
	if viper.IsSet("json_processing.character_filtering.allowed_unicode_ranges.japanese_katakana") {
		config.CharacterFiltering.AllowedUnicodeRanges.JapaneseKatakana = viper.GetIntSlice("json_processing.character_filtering.allowed_unicode_ranges.japanese_katakana")
	}
}
