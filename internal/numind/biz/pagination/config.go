package pagination

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

// LoadConfigFromViper 从Viper配置中加载分页配置
func LoadConfigFromViper() *PaginationConfig {
	config := GetDefaultConfig()

	// 首先尝试从新的card配置结构加载
	loadFromCardConfig(config)

	// 然后尝试从旧的pagination配置结构加载（向后兼容）
	loadFromLegacyPaginationConfig(config)

	// 应用段间距调整
	applySpacingAdjustments(config)

	return config
}

// loadFromCardConfig 从新的card配置结构加载配置
func loadFromCardConfig(config *PaginationConfig) {
	// 加载卡片基础配置
	if viper.IsSet("card.dimensions.width") {
		config.Card.Width = viper.GetInt("card.dimensions.width")
	}
	if viper.IsSet("card.dimensions.height") {
		config.Card.Height = viper.GetInt("card.dimensions.height")
	}
	if viper.IsSet("card.dimensions.padding.top") {
		config.Card.Padding.Top = viper.GetInt("card.dimensions.padding.top")
	}
	if viper.IsSet("card.dimensions.padding.right") {
		config.Card.Padding.Right = viper.GetInt("card.dimensions.padding.right")
	}
	if viper.IsSet("card.dimensions.padding.bottom") {
		config.Card.Padding.Bottom = viper.GetInt("card.dimensions.padding.bottom")
	}
	if viper.IsSet("card.dimensions.padding.left") {
		config.Card.Padding.Left = viper.GetInt("card.dimensions.padding.left")
	}

	// 加载分页算法参数
	if viper.IsSet("card.pagination.char_width_factor") {
		config.CharWidthFactor = viper.GetFloat64("card.pagination.char_width_factor")
	}
	if viper.IsSet("card.pagination.overflow_tolerance") {
		config.OverflowTolerance = viper.GetFloat64("card.pagination.overflow_tolerance")
	}
	if viper.IsSet("card.pagination.high_utilization_threshold") {
		config.HighUtilizationThreshold = viper.GetFloat64("card.pagination.high_utilization_threshold")
	}
	if viper.IsSet("card.pagination.min_chars_per_line") {
		config.MinCharsPerLine = viper.GetInt("card.pagination.min_chars_per_line")
	}
	if viper.IsSet("card.pagination.list_item_spacing") {
		config.ListItemSpacing = viper.GetInt("card.pagination.list_item_spacing")
	}

	// 加载样式配置（从新的typography结构）
	loadStyleConfigFromTypography(config, ElementTypeTitle, "card.typography")
	loadStyleConfigFromTypography(config, ElementTypeSubtitle, "card.typography")
	loadStyleConfigFromTypography(config, ElementTypeBody, "card.typography")
	loadStyleConfigFromTypography(config, ElementTypeList, "card.typography")
	loadStyleConfigFromTypography(config, ElementTypeQuote, "card.typography")
	loadStyleConfigFromTypography(config, ElementTypeTag, "card.typography")
	loadStyleConfigFromTypography(config, ElementTypeNumber, "card.typography")

	// 加载页码配置
	loadPageNumberConfig(config)
}

// loadFromLegacyPaginationConfig 从旧的pagination配置结构加载配置（向后兼容）
func loadFromLegacyPaginationConfig(config *PaginationConfig) {
	// 加载卡片基础配置（旧格式）
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

	// 加载新增的可配置参数（旧格式）
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

	// 加载样式配置（旧格式）
	loadStyleConfig(config, ElementTypeTitle, "pagination.styles.title")
	loadStyleConfig(config, ElementTypeSubtitle, "pagination.styles.subtitle")
	loadStyleConfig(config, ElementTypeBody, "pagination.styles.body")
	loadStyleConfig(config, ElementTypeList, "pagination.styles.list")
	loadStyleConfig(config, ElementTypeQuote, "pagination.styles.quote")
	loadStyleConfig(config, ElementTypeTag, "pagination.styles.tag")
	loadStyleConfig(config, ElementTypeNumber, "pagination.styles.number")
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

// loadStyleConfigFromTypography 从新的typography配置结构加载样式配置
func loadStyleConfigFromTypography(config *PaginationConfig, elementType ElementType, typographyPath string) {
	elementName := strings.ToLower(string(elementType))

	// 如果没有typography配置，跳过
	if !viper.IsSet(typographyPath) {
		return
	}

	style := config.Styles[elementType]

	// 从sizes配置加载字体大小
	if viper.IsSet(typographyPath + ".sizes." + elementName) {
		style.FontSize = viper.GetInt(typographyPath + ".sizes." + elementName)
	}

	// 从line_heights配置加载行高
	if viper.IsSet(typographyPath + ".line_heights." + elementName) {
		style.LineHeight = viper.GetInt(typographyPath + ".line_heights." + elementName)
	}

	// 从spacing配置加载边距（基础 + 精细化覆盖）
	if viper.IsSet(typographyPath + ".spacing.base_margin") {
		baseMargin := viper.GetInt(typographyPath + ".spacing.base_margin")
		style.MarginTop = baseMargin
		style.MarginBottom = baseMargin
	}
	// 精细化：card.typography.spacing.margins.{element}_top / {element}_bottom
	if viper.IsSet(typographyPath + ".spacing.margins." + elementName + "_top") {
		style.MarginTop = viper.GetInt(typographyPath + ".spacing.margins." + elementName + "_top")
	}
	if viper.IsSet(typographyPath + ".spacing.margins." + elementName + "_bottom") {
		style.MarginBottom = viper.GetInt(typographyPath + ".spacing.margins." + elementName + "_bottom")
	}

	// 从colors配置加载颜色
	if elementType == ElementTypeTitle || elementType == ElementTypeSubtitle || elementType == ElementTypeBody || elementType == ElementTypeList {
		if viper.IsSet(typographyPath + ".colors.text") {
			style.Color = viper.GetString(typographyPath + ".colors.text")
		}
	} else if elementType == ElementTypeQuote || elementType == ElementTypeTag || elementType == ElementTypeNumber {
		if viper.IsSet(typographyPath + ".colors.accent") {
			style.Color = viper.GetString(typographyPath + ".colors.accent")
		}
	}

	// 特殊处理副标题颜色
	if elementType == ElementTypeSubtitle && viper.IsSet(typographyPath+".colors.secondary") {
		style.Color = viper.GetString(typographyPath + ".colors.secondary")
	}

	// 从alignments配置加载对齐方式
	if viper.IsSet(typographyPath + ".alignments." + elementName) {
		style.Align = viper.GetString(typographyPath + ".alignments." + elementName)
	}

	// 列表缩进
	if elementType == ElementTypeList && viper.IsSet(typographyPath+".styles.list_indent") {
		style.Indent = viper.GetInt(typographyPath + ".styles.list_indent")
	}

	// 首行缩进（像素），可配置：card.typography.first_line_indent 或 per-element：card.typography.styles.{element}.first_line_indent
	if viper.IsSet(typographyPath + ".styles." + elementName + ".first_line_indent") {
		style.FirstLineIndent = viper.GetInt(typographyPath + ".styles." + elementName + ".first_line_indent")
	} else if viper.IsSet(typographyPath + ".first_line_indent") {
		style.FirstLineIndent = viper.GetInt(typographyPath + ".first_line_indent")
	}

	config.Styles[elementType] = style
}

// loadPageNumberConfig 加载页码配置
func loadPageNumberConfig(config *PaginationConfig) {
	if viper.IsSet("card.typography.page_number.enabled") {
		config.PageNumber.Enabled = viper.GetBool("card.typography.page_number.enabled")
	}
	if viper.IsSet("card.typography.page_number.font_size") {
		config.PageNumber.FontSize = viper.GetInt("card.typography.page_number.font_size")
	}
	if viper.IsSet("card.typography.page_number.color") {
		config.PageNumber.Color = viper.GetString("card.typography.page_number.color")
	}
	if viper.IsSet("card.typography.page_number.font_weight") {
		config.PageNumber.FontWeight = viper.GetString("card.typography.page_number.font_weight")
	}
	if viper.IsSet("card.typography.page_number.position.bottom") {
		config.PageNumber.Position.Bottom = viper.GetInt("card.typography.page_number.position.bottom")
	}
	if viper.IsSet("card.typography.page_number.position.right") {
		config.PageNumber.Position.Right = viper.GetInt("card.typography.page_number.position.right")
	}
	if viper.IsSet("card.typography.page_number.format") {
		config.PageNumber.Format = viper.GetString("card.typography.page_number.format")
	}
}

// applySpacingAdjustments 应用段间距调整
func applySpacingAdjustments(config *PaginationConfig) {
	// 获取全局段间距倍数，优先使用新配置，然后是旧配置
	globalMultiplier := 1.0
	if viper.IsSet("card.typography.spacing.global_multiplier") {
		globalMultiplier = viper.GetFloat64("card.typography.spacing.global_multiplier")
	} else if viper.IsSet("pagination.spacing.global_multiplier") {
		globalMultiplier = viper.GetFloat64("pagination.spacing.global_multiplier")
	}

	// 定义元素类型映射
	elementTypes := map[string]ElementType{
		"title":    ElementTypeTitle,
		"subtitle": ElementTypeSubtitle,
		"body":     ElementTypeBody,
		"list":     ElementTypeList,
		"quote":    ElementTypeQuote,
		"tag":      ElementTypeTag,
	}

	// 为每个元素类型应用段间距调整
	for elementName, elementType := range elementTypes {
		style := config.Styles[elementType]

		// 获取该元素类型的段间距倍数
		marginTopMultiplier := globalMultiplier
		marginBottomMultiplier := globalMultiplier
		lineHeightMultiplier := globalMultiplier

		// 检查是否有特定元素的段间距调整
		adjustmentPath := "pagination.spacing.adjustments." + elementName
		if viper.IsSet(adjustmentPath) {
			if viper.IsSet(adjustmentPath + ".margin_top_multiplier") {
				marginTopMultiplier = viper.GetFloat64(adjustmentPath + ".margin_top_multiplier")
			}
			if viper.IsSet(adjustmentPath + ".margin_bottom_multiplier") {
				marginBottomMultiplier = viper.GetFloat64(adjustmentPath + ".margin_bottom_multiplier")
			}
			if viper.IsSet(adjustmentPath + ".line_height_multiplier") {
				lineHeightMultiplier = viper.GetFloat64(adjustmentPath + ".line_height_multiplier")
			}
		}

		// 应用倍数调整
		style.MarginTop = int(float64(style.MarginTop) * marginTopMultiplier)
		style.MarginBottom = int(float64(style.MarginBottom) * marginBottomMultiplier)
		style.LineHeight = int(float64(style.LineHeight) * lineHeightMultiplier)

		// 更新配置
		config.Styles[elementType] = style
	}
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

	// 应用段间距调整
	applySpacingAdjustmentsFromViper(config, v)

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

// applySpacingAdjustmentsFromViper 从指定的viper实例应用段间距调整
func applySpacingAdjustmentsFromViper(config *PaginationConfig, v *viper.Viper) {
	// 获取全局段间距倍数
	globalMultiplier := 1.0
	if v.IsSet("pagination.spacing.global_multiplier") {
		globalMultiplier = v.GetFloat64("pagination.spacing.global_multiplier")
	}

	// 定义元素类型映射
	elementTypes := map[string]ElementType{
		"title":    ElementTypeTitle,
		"subtitle": ElementTypeSubtitle,
		"body":     ElementTypeBody,
		"list":     ElementTypeList,
		"quote":    ElementTypeQuote,
		"tag":      ElementTypeTag,
	}

	// 为每个元素类型应用段间距调整
	for elementName, elementType := range elementTypes {
		style := config.Styles[elementType]

		// 获取该元素类型的段间距倍数
		marginTopMultiplier := globalMultiplier
		marginBottomMultiplier := globalMultiplier
		lineHeightMultiplier := globalMultiplier

		// 检查是否有特定元素的段间距调整
		adjustmentPath := "pagination.spacing.adjustments." + elementName
		if v.IsSet(adjustmentPath) {
			if v.IsSet(adjustmentPath + ".margin_top_multiplier") {
				marginTopMultiplier = v.GetFloat64(adjustmentPath + ".margin_top_multiplier")
			}
			if v.IsSet(adjustmentPath + ".margin_bottom_multiplier") {
				marginBottomMultiplier = v.GetFloat64(adjustmentPath + ".margin_bottom_multiplier")
			}
			if v.IsSet(adjustmentPath + ".line_height_multiplier") {
				lineHeightMultiplier = v.GetFloat64(adjustmentPath + ".line_height_multiplier")
			}
		}

		// 应用倍数调整
		style.MarginTop = int(float64(style.MarginTop) * marginTopMultiplier)
		style.MarginBottom = int(float64(style.MarginBottom) * marginBottomMultiplier)
		style.LineHeight = int(float64(style.LineHeight) * lineHeightMultiplier)

		// 更新配置
		config.Styles[elementType] = style
	}
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
