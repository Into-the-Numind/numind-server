package pagination

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// LoadConfigFromViper 从Viper配置中加载分页配置
func LoadConfigFromViper() *PaginationConfig {
	config := GetDefaultConfig()

	// 加载卡片基础配置
	if viper.IsSet("pagination.card.width") {
		config.Card.Width = viper.GetInt("pagination.card.width")
	}
	if viper.IsSet("pagination.card.height") {
		config.Card.Height = viper.GetInt("pagination.card.height")
	}
	if viper.IsSet("pagination.card.padding.top") {
		config.Card.Padding.Top = viper.GetInt("pagination.card.padding.top")
	}
	if viper.IsSet("pagination.card.padding.right") {
		config.Card.Padding.Right = viper.GetInt("pagination.card.padding.right")
	}
	if viper.IsSet("pagination.card.padding.bottom") {
		config.Card.Padding.Bottom = viper.GetInt("pagination.card.padding.bottom")
	}
	if viper.IsSet("pagination.card.padding.left") {
		config.Card.Padding.Left = viper.GetInt("pagination.card.padding.left")
	}

	// 加载新增的可配置参数
	if viper.IsSet("pagination.char_width_factor") {
		config.CharWidthFactor = viper.GetFloat64("pagination.char_width_factor")
	}
	if viper.IsSet("pagination.overflow_tolerance") {
		config.OverflowTolerance = viper.GetFloat64("pagination.overflow_tolerance")
	}
	if viper.IsSet("pagination.high_utilization_threshold") {
		config.HighUtilizationThreshold = viper.GetFloat64("pagination.high_utilization_threshold")
	}
	if viper.IsSet("pagination.min_chars_per_line") {
		config.MinCharsPerLine = viper.GetInt("pagination.min_chars_per_line")
	}
	if viper.IsSet("pagination.list_item_spacing") {
		config.ListItemSpacing = viper.GetInt("pagination.list_item_spacing")
	}

	// 加载样式配置
	loadStyleConfig(config, ElementTypeTitle, "pagination.styles.title")
	loadStyleConfig(config, ElementTypeSubtitle, "pagination.styles.subtitle")
	loadStyleConfig(config, ElementTypeBody, "pagination.styles.body")
	loadStyleConfig(config, ElementTypeList, "pagination.styles.list")
	loadStyleConfig(config, ElementTypeQuote, "pagination.styles.quote")
	loadStyleConfig(config, ElementTypeTag, "pagination.styles.tag")
	loadStyleConfig(config, ElementTypeNumber, "pagination.styles.number")

	return config
}

// loadStyleConfig 加载特定元素类型的样式配置
func loadStyleConfig(config *PaginationConfig, elementType ElementType, configPath string) {
	if !viper.IsSet(configPath) {
		return
	}

	style := config.Styles[elementType]

	if viper.IsSet(configPath + ".font_size") {
		style.FontSize = viper.GetInt(configPath + ".font_size")
	}
	if viper.IsSet(configPath + ".line_height") {
		style.LineHeight = viper.GetInt(configPath + ".line_height")
	}
	if viper.IsSet(configPath + ".margin_top") {
		style.MarginTop = viper.GetInt(configPath + ".margin_top")
	}
	if viper.IsSet(configPath + ".margin_bottom") {
		style.MarginBottom = viper.GetInt(configPath + ".margin_bottom")
	}
	if viper.IsSet(configPath + ".color") {
		style.Color = viper.GetString(configPath + ".color")
	}
	if viper.IsSet(configPath + ".align") {
		style.Align = viper.GetString(configPath + ".align")
	}
	if viper.IsSet(configPath + ".indent") {
		style.Indent = viper.GetInt(configPath + ".indent")
	}

	config.Styles[elementType] = style
}

// LoadConfigFromYAML 从YAML文件加载配置
func LoadConfigFromYAML(configPath string) (*PaginationConfig, error) {
	// 创建新的viper实例
	v := viper.New()
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	config := GetDefaultConfig()

	// 加载卡片基础配置
	if v.IsSet("pagination.card.width") {
		config.Card.Width = v.GetInt("pagination.card.width")
	}
	if v.IsSet("pagination.card.height") {
		config.Card.Height = v.GetInt("pagination.card.height")
	}
	if v.IsSet("pagination.card.padding.top") {
		config.Card.Padding.Top = v.GetInt("pagination.card.padding.top")
	}
	if v.IsSet("pagination.card.padding.right") {
		config.Card.Padding.Right = v.GetInt("pagination.card.padding.right")
	}
	if v.IsSet("pagination.card.padding.bottom") {
		config.Card.Padding.Bottom = v.GetInt("pagination.card.padding.bottom")
	}
	if v.IsSet("pagination.card.padding.left") {
		config.Card.Padding.Left = v.GetInt("pagination.card.padding.left")
	}

	// 加载样式配置
	loadStyleConfigFromViper(config, ElementTypeTitle, "pagination.styles.title", v)
	loadStyleConfigFromViper(config, ElementTypeSubtitle, "pagination.styles.subtitle", v)
	loadStyleConfigFromViper(config, ElementTypeBody, "pagination.styles.body", v)
	loadStyleConfigFromViper(config, ElementTypeList, "pagination.styles.list", v)
	loadStyleConfigFromViper(config, ElementTypeQuote, "pagination.styles.quote", v)
	loadStyleConfigFromViper(config, ElementTypeTag, "pagination.styles.tag", v)
	loadStyleConfigFromViper(config, ElementTypeNumber, "pagination.styles.number", v)

	return config, nil
}

// loadStyleConfigFromViper 从指定的viper实例加载样式配置
func loadStyleConfigFromViper(config *PaginationConfig, elementType ElementType, configPath string, v *viper.Viper) {
	if !v.IsSet(configPath) {
		return
	}

	style := config.Styles[elementType]

	if v.IsSet(configPath + ".font_size") {
		style.FontSize = v.GetInt(configPath + ".font_size")
	}
	if v.IsSet(configPath + ".line_height") {
		style.LineHeight = v.GetInt(configPath + ".line_height")
	}
	if v.IsSet(configPath + ".margin_top") {
		style.MarginTop = v.GetInt(configPath + ".margin_top")
	}
	if v.IsSet(configPath + ".margin_bottom") {
		style.MarginBottom = v.GetInt(configPath + ".margin_bottom")
	}
	if v.IsSet(configPath + ".color") {
		style.Color = v.GetString(configPath + ".color")
	}
	if v.IsSet(configPath + ".align") {
		style.Align = v.GetString(configPath + ".align")
	}
	if v.IsSet(configPath + ".indent") {
		style.Indent = v.GetInt(configPath + ".indent")
	}

	config.Styles[elementType] = style
}

// GetConfigSummary 获取配置摘要信息
func GetConfigSummary(config *PaginationConfig) string {
	var summary strings.Builder

	summary.WriteString("=== Pagination Configuration Summary ===\n")
	summary.WriteString(fmt.Sprintf("Card Dimensions: %dx%d\n", config.Card.Width, config.Card.Height))
	summary.WriteString(fmt.Sprintf("Card Padding: T:%d R:%d B:%d L:%d\n",
		config.Card.Padding.Top, config.Card.Padding.Right,
		config.Card.Padding.Bottom, config.Card.Padding.Left))

	summary.WriteString("\nAdvanced Parameters:\n")
	summary.WriteString(fmt.Sprintf("  CharWidthFactor: %.3f\n", config.CharWidthFactor))
	summary.WriteString(fmt.Sprintf("  OverflowTolerance: %.3f\n", config.OverflowTolerance))
	summary.WriteString(fmt.Sprintf("  HighUtilizationThreshold: %.1f\n", config.HighUtilizationThreshold))
	summary.WriteString(fmt.Sprintf("  MinCharsPerLine: %d\n", config.MinCharsPerLine))
	summary.WriteString(fmt.Sprintf("  ListItemSpacing: %d\n", config.ListItemSpacing))

	summary.WriteString("\nStyle Configurations:\n")
	for elementType, style := range config.Styles {
		summary.WriteString(fmt.Sprintf("  %s: FontSize:%d, LineHeight:%d, Margins:T%d/B%d, Color:%s, Align:%s\n",
			elementType, style.FontSize, style.LineHeight,
			style.MarginTop, style.MarginBottom, style.Color, style.Align))
		if style.Indent > 0 {
			summary.WriteString(fmt.Sprintf("    Indent: %d\n", style.Indent))
		}
	}

	return summary.String()
}

// LoadDynamicConfigFromViper 从Viper配置中加载动态分页配置
func LoadDynamicConfigFromViper() *DynamicPaginationConfig {
	baseConfig := LoadConfigFromViper()
	dynamicConfig := &DynamicPaginationConfig{
		PaginationConfig: baseConfig,
	}

	// 加载动态分页特有参数
	if viper.IsSet("pagination.dynamic.min_height") {
		dynamicConfig.MinHeight = viper.GetInt("pagination.dynamic.min_height")
	}
	if viper.IsSet("pagination.dynamic.max_height") {
		dynamicConfig.MaxHeight = viper.GetInt("pagination.dynamic.max_height")
	}
	if viper.IsSet("pagination.dynamic.min_bottom_padding") {
		dynamicConfig.MinBottomPadding = viper.GetInt("pagination.dynamic.min_bottom_padding")
	}
	if viper.IsSet("pagination.dynamic.max_images_per_card") {
		dynamicConfig.MaxImagesPerCard = viper.GetInt("pagination.dynamic.max_images_per_card")
	}
	if viper.IsSet("pagination.dynamic.max_text_length") {
		dynamicConfig.MaxTextLength = viper.GetInt("pagination.dynamic.max_text_length")
	}
	if viper.IsSet("pagination.dynamic.base_height") {
		dynamicConfig.BaseHeight = viper.GetInt("pagination.dynamic.base_height")
	}
	if viper.IsSet("pagination.dynamic.char_width_factor") {
		dynamicConfig.CharWidthFactor = viper.GetFloat64("pagination.dynamic.char_width_factor")
	}
	if viper.IsSet("pagination.dynamic.overflow_tolerance") {
		dynamicConfig.OverflowTolerance = viper.GetFloat64("pagination.dynamic.overflow_tolerance")
	}
	if viper.IsSet("pagination.dynamic.high_utilization_threshold") {
		dynamicConfig.HighUtilizationThreshold = viper.GetFloat64("pagination.dynamic.high_utilization_threshold")
	}
	if viper.IsSet("pagination.dynamic.base_image_height") {
		dynamicConfig.BaseImageHeight = viper.GetInt("pagination.dynamic.base_image_height")
	}
	if viper.IsSet("pagination.dynamic.image_margin_top") {
		dynamicConfig.ImageMarginTop = viper.GetInt("pagination.dynamic.image_margin_top")
	}
	if viper.IsSet("pagination.dynamic.image_margin_bottom") {
		dynamicConfig.ImageMarginBottom = viper.GetInt("pagination.dynamic.image_margin_bottom")
	}
	if viper.IsSet("pagination.dynamic.min_chars_per_line") {
		dynamicConfig.MinCharsPerLine = viper.GetInt("pagination.dynamic.min_chars_per_line")
	}
	if viper.IsSet("pagination.dynamic.list_item_spacing") {
		dynamicConfig.ListItemSpacing = viper.GetInt("pagination.dynamic.list_item_spacing")
	}

	return dynamicConfig
}
