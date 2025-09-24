package util

import (
	"github.com/spf13/viper"
)

// CardRenderingConfig 统一的卡片渲染配置
type CardRenderingConfig struct {
	// 尺寸配置
	Width  int `json:"width"`
	Height int `json:"height"`

	// 内边距配置
	PaddingTop    int `json:"padding_top"`
	PaddingRight  int `json:"padding_right"`
	PaddingBottom int `json:"padding_bottom"`
	PaddingLeft   int `json:"padding_left"`

	// 渲染器配置
	Quality        int     `json:"quality"`
	Format         string  `json:"format"`
	Zoom           float64 `json:"zoom"`
	TimeoutSeconds int     `json:"timeout_seconds"`
}

// GetCardRenderingConfig 获取统一的卡片渲染配置
// 优先从card_rendering配置读取，回退到原有配置项
func GetCardRenderingConfig() *CardRenderingConfig {
	config := &CardRenderingConfig{
		// 默认值
		Width:          1080,
		Height:         1440,
		PaddingTop:     60,
		PaddingRight:   50,
		PaddingBottom:  40,
		PaddingLeft:    50,
		Quality:        85,
		Format:         "webp",
		Zoom:           1.0,
		TimeoutSeconds: 30,
	}

	// 优先从统一配置读取
	if viper.IsSet("card_rendering.dimensions.width") {
		config.Width = viper.GetInt("card_rendering.dimensions.width")
	} else if viper.IsSet("pagination.card.width") {
		// 回退到原有配置
		config.Width = viper.GetInt("pagination.card.width")
	} else if viper.IsSet("renderer.width") {
		// 回退到渲染器配置
		config.Width = viper.GetInt("renderer.width")
	}

	if viper.IsSet("card_rendering.dimensions.height") {
		config.Height = viper.GetInt("card_rendering.dimensions.height")
	} else if viper.IsSet("pagination.card.height") {
		config.Height = viper.GetInt("pagination.card.height")
	} else if viper.IsSet("renderer.height") {
		config.Height = viper.GetInt("renderer.height")
	}

	// 内边距配置
	if viper.IsSet("card_rendering.dimensions.padding.top") {
		config.PaddingTop = viper.GetInt("card_rendering.dimensions.padding.top")
	} else if viper.IsSet("pagination.card.padding.top") {
		config.PaddingTop = viper.GetInt("pagination.card.padding.top")
	}

	if viper.IsSet("card_rendering.dimensions.padding.right") {
		config.PaddingRight = viper.GetInt("card_rendering.dimensions.padding.right")
	} else if viper.IsSet("pagination.card.padding.right") {
		config.PaddingRight = viper.GetInt("pagination.card.padding.right")
	}

	if viper.IsSet("card_rendering.dimensions.padding.bottom") {
		config.PaddingBottom = viper.GetInt("card_rendering.dimensions.padding.bottom")
	} else if viper.IsSet("pagination.card.padding.bottom") {
		config.PaddingBottom = viper.GetInt("pagination.card.padding.bottom")
	}

	if viper.IsSet("card_rendering.dimensions.padding.left") {
		config.PaddingLeft = viper.GetInt("card_rendering.dimensions.padding.left")
	} else if viper.IsSet("pagination.card.padding.left") {
		config.PaddingLeft = viper.GetInt("pagination.card.padding.left")
	}

	// 渲染器配置
	if viper.IsSet("card_rendering.renderer.quality") {
		config.Quality = viper.GetInt("card_rendering.renderer.quality")
	} else if viper.IsSet("renderer.quality") {
		config.Quality = viper.GetInt("renderer.quality")
	}

	if viper.IsSet("card_rendering.renderer.format") {
		config.Format = viper.GetString("card_rendering.renderer.format")
	} else if viper.IsSet("renderer.format") {
		config.Format = viper.GetString("renderer.format")
	}

	if viper.IsSet("card_rendering.renderer.zoom") {
		config.Zoom = viper.GetFloat64("card_rendering.renderer.zoom")
	} else if viper.IsSet("renderer.zoom") {
		config.Zoom = viper.GetFloat64("renderer.zoom")
	}

	if viper.IsSet("card_rendering.renderer.timeout_seconds") {
		config.TimeoutSeconds = viper.GetInt("card_rendering.renderer.timeout_seconds")
	} else if viper.IsSet("renderer.timeout_seconds") {
		config.TimeoutSeconds = viper.GetInt("renderer.timeout_seconds")
	}

	return config
}

// GetAvailableWidth 获取可用宽度（扣除左右内边距）
func (c *CardRenderingConfig) GetAvailableWidth() int {
	return c.Width - c.PaddingLeft - c.PaddingRight
}

// GetAvailableHeight 获取可用高度（扣除上下内边距）
func (c *CardRenderingConfig) GetAvailableHeight() int {
	return c.Height - c.PaddingTop - c.PaddingBottom
}

// GetTotalPadding 获取总内边距（上下内边距之和）
func (c *CardRenderingConfig) GetTotalPadding() int {
	return c.PaddingTop + c.PaddingBottom
}

// FontConfig 字体配置
type FontConfig struct {
	TitleSize          int     `json:"title_size"`
	SubtitleSize       int     `json:"subtitle_size"`
	BodySize           int     `json:"body_size"`
	ListSize           int     `json:"list_size"`
	QuoteSize          int     `json:"quote_size"`
	TitleLineHeight    float64 `json:"title_line_height"`
	SubtitleLineHeight float64 `json:"subtitle_line_height"`
	BodyLineHeight     float64 `json:"body_line_height"`
}

// GetFontConfig 获取字体配置
// 优先从pagination.styles读取，回退到html_converter.fonts
func GetFontConfig() *FontConfig {
	config := &FontConfig{
		// 默认值
		TitleSize:          84,
		SubtitleSize:       68,
		BodySize:           56,
		ListSize:           56,
		QuoteSize:          46,
		TitleLineHeight:    1.4,
		SubtitleLineHeight: 1.5,
		BodyLineHeight:     1.6,
	}

	// 优先从pagination.styles读取
	if viper.IsSet("pagination.styles.title.font_size") {
		config.TitleSize = viper.GetInt("pagination.styles.title.font_size")
	} else if viper.IsSet("html_converter.fonts.title_size") {
		config.TitleSize = viper.GetInt("html_converter.fonts.title_size")
	}

	if viper.IsSet("pagination.styles.subtitle.font_size") {
		config.SubtitleSize = viper.GetInt("pagination.styles.subtitle.font_size")
	} else if viper.IsSet("html_converter.fonts.subtitle_size") {
		config.SubtitleSize = viper.GetInt("html_converter.fonts.subtitle_size")
	}

	if viper.IsSet("pagination.styles.body.font_size") {
		config.BodySize = viper.GetInt("pagination.styles.body.font_size")
	} else if viper.IsSet("html_converter.fonts.body_size") {
		config.BodySize = viper.GetInt("html_converter.fonts.body_size")
	}

	if viper.IsSet("pagination.styles.list.font_size") {
		config.ListSize = viper.GetInt("pagination.styles.list.font_size")
	} else if viper.IsSet("html_converter.fonts.list_size") {
		config.ListSize = viper.GetInt("html_converter.fonts.list_size")
	}

	if viper.IsSet("pagination.styles.quote.font_size") {
		config.QuoteSize = viper.GetInt("pagination.styles.quote.font_size")
	} else if viper.IsSet("html_converter.fonts.quote_size") {
		config.QuoteSize = viper.GetInt("html_converter.fonts.quote_size")
	}

	// 行高配置
	if viper.IsSet("pagination.styles.title.line_height") {
		// pagination中存储的是像素值，需要计算倍数
		lineHeightPx := viper.GetInt("pagination.styles.title.line_height")
		config.TitleLineHeight = float64(lineHeightPx) / float64(config.TitleSize)
	} else if viper.IsSet("html_converter.line_heights.title") {
		config.TitleLineHeight = viper.GetFloat64("html_converter.line_heights.title")
	}

	if viper.IsSet("pagination.styles.subtitle.line_height") {
		lineHeightPx := viper.GetInt("pagination.styles.subtitle.line_height")
		config.SubtitleLineHeight = float64(lineHeightPx) / float64(config.SubtitleSize)
	} else if viper.IsSet("html_converter.line_heights.subtitle") {
		config.SubtitleLineHeight = viper.GetFloat64("html_converter.line_heights.subtitle")
	}

	if viper.IsSet("pagination.styles.body.line_height") {
		lineHeightPx := viper.GetInt("pagination.styles.body.line_height")
		config.BodyLineHeight = float64(lineHeightPx) / float64(config.BodySize)
	} else if viper.IsSet("html_converter.line_heights.body") {
		config.BodyLineHeight = viper.GetFloat64("html_converter.line_heights.body")
	}

	return config
}
