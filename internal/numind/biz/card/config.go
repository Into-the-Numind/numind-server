package card

import (
	"os"
	"strconv"
)

// RendererConfig 渲染器配置
type RendererConfig struct {
	// 是否启用渲染-测量方案
	EnableRenderAndMeasure bool
	// 是否启用Chrome无头浏览器
	EnableChromeHeadless bool
	// 是否启用传统渲染器作为备用
	EnableTraditionalRenderer bool
	// 是否启用增强版渲染器（新实现）
	EnableEnhancedRenderer bool
	// Chrome调试端口
	ChromeDebugPort int
	// 渲染超时时间（秒）
	RenderTimeout int
}

// GetRendererConfig 获取渲染器配置
func GetRendererConfig() *RendererConfig {
	config := &RendererConfig{
		EnableRenderAndMeasure:    true, // 默认启用渲染-测量方案
		EnableChromeHeadless:      true, // 默认启用Chrome无头浏览器
		EnableTraditionalRenderer: true, // 默认启用传统渲染器作为备用
		EnableEnhancedRenderer:    true, // 默认启用增强版渲染器
		ChromeDebugPort:           9222, // 默认Chrome调试端口
		RenderTimeout:             300,  // 默认5分钟超时
	}

	// 从环境变量读取配置
	if env := os.Getenv("ENABLE_RENDER_AND_MEASURE"); env != "" {
		if enabled, err := strconv.ParseBool(env); err == nil {
			config.EnableRenderAndMeasure = enabled
		}
	}

	if env := os.Getenv("ENABLE_CHROME_HEADLESS"); env != "" {
		if enabled, err := strconv.ParseBool(env); err == nil {
			config.EnableChromeHeadless = enabled
		}
	}

	if env := os.Getenv("ENABLE_TRADITIONAL_RENDERER"); env != "" {
		if enabled, err := strconv.ParseBool(env); err == nil {
			config.EnableTraditionalRenderer = enabled
		}
	}

	if env := os.Getenv("ENABLE_ENHANCED_RENDERER"); env != "" {
		if enabled, err := strconv.ParseBool(env); err == nil {
			config.EnableEnhancedRenderer = enabled
		}
	}

	if env := os.Getenv("CHROME_DEBUG_PORT"); env != "" {
		if port, err := strconv.Atoi(env); err == nil {
			config.ChromeDebugPort = port
		}
	}

	if env := os.Getenv("RENDER_TIMEOUT"); env != "" {
		if timeout, err := strconv.Atoi(env); err == nil {
			config.RenderTimeout = timeout
		}
	}

	return config
}

// IsRenderAndMeasureEnabled 检查是否启用渲染-测量方案
func IsRenderAndMeasureEnabled() bool {
	return GetRendererConfig().EnableRenderAndMeasure
}

// IsChromeHeadlessEnabled 检查是否启用Chrome无头浏览器
func IsChromeHeadlessEnabled() bool {
	return GetRendererConfig().EnableChromeHeadless
}

// IsTraditionalRendererEnabled 检查是否启用传统渲染器
func IsTraditionalRendererEnabled() bool {
	return GetRendererConfig().EnableTraditionalRenderer
}

// IsEnhancedRendererEnabled 检查是否启用增强版渲染器
func IsEnhancedRendererEnabled() bool {
	// 暂时禁用增强版渲染器，优先使用修复后的传统渲染器
	// return GetRendererConfig().EnableEnhancedRenderer
	return false
}
