package config

import (
	"context"
	"strconv"

	"numind-server/internal/pkg/log"

	"github.com/spf13/viper"
)

// ConfigReader 配置读取器，提供统一的配置读取接口
type ConfigReader struct {
	biz ConfigBiz
}

// NewConfigReader 创建配置读取器
func NewConfigReader(biz ConfigBiz) *ConfigReader {
	return &ConfigReader{biz: biz}
}

// GetString 获取字符串配置（优先从Redis/数据库读取，未命中则从viper读取）
func (r *ConfigReader) GetString(ctx context.Context, key string) string {
	// 先尝试从数据库/Redis读取
	config, err := r.biz.GetByKey(ctx, key)
	if err == nil && config != nil && config.Value != "" {
		log.Debugw("Config read from database/Redis", "key", key)
		return config.Value
	}

	// 如果数据库中没有，从viper读取（兼容旧配置）
	log.Debugw("Config not found in database, reading from viper", "key", key)
	return viper.GetString(key)
}

// GetInt 获取整数配置
func (r *ConfigReader) GetInt(ctx context.Context, key string) int {
	config, err := r.biz.GetByKey(ctx, key)
	if err == nil && config != nil && config.Value != "" {
		if val, err := strconv.Atoi(config.Value); err == nil {
			log.Debugw("Config read from database/Redis", "key", key)
			return val
		}
	}

	return viper.GetInt(key)
}

// GetFloat64 获取浮点数配置
func (r *ConfigReader) GetFloat64(ctx context.Context, key string) float64 {
	config, err := r.biz.GetByKey(ctx, key)
	if err == nil && config != nil && config.Value != "" {
		if val, err := strconv.ParseFloat(config.Value, 64); err == nil {
			log.Debugw("Config read from database/Redis", "key", key)
			return val
		}
	}

	return viper.GetFloat64(key)
}

// GetTextProcessingPrompt 获取文本处理提示词
func (r *ConfigReader) GetTextProcessingPrompt(ctx context.Context) string {
	return r.GetString(ctx, "ai_prompts.text_processing")
}

// GetVolcModel 获取火山引擎模型
func (r *ConfigReader) GetVolcModel(ctx context.Context) string {
	model := r.GetString(ctx, "volc.model")
	if model == "" {
		return "doubao-seed-1-6-flash-250828" // 默认值
	}
	return model
}

// GetVolcTemperature 获取火山引擎温度参数
func (r *ConfigReader) GetVolcTemperature(ctx context.Context) float64 {
	temp := r.GetFloat64(ctx, "volc.temperature")
	if temp == 0 {
		return 0.5 // 默认值
	}
	return temp
}

// GetVolcTokens 获取火山引擎Token数量
func (r *ConfigReader) GetVolcTokens(ctx context.Context) int {
	tokens := r.GetInt(ctx, "volc.tokens")
	if tokens == 0 {
		return 2000 // 默认值
	}
	return tokens
}

// GetAliTextModel 获取阿里云文本模型
func (r *ConfigReader) GetAliTextModel(ctx context.Context) string {
	model := r.GetString(ctx, "ali.text.model")
	if model == "" {
		return "qwen-turbo" // 默认值
	}
	return model
}
