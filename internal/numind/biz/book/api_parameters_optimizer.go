package book

import (
	"context"
	"fmt"
	"strings"

	"numind-server/internal/pkg/log"

	"github.com/spf13/viper"
)

// getAliConfig 获取Ali配置，支持服务特定配置和通用配置回退
func getAliConfig(service string, key string) string {
	// 先尝试服务特定配置
	serviceKey := fmt.Sprintf("ali.%s.%s", service, key)
	if viper.IsSet(serviceKey) {
		return viper.GetString(serviceKey)
	}

	// 回退到通用配置
	commonKey := fmt.Sprintf("ali.%s", key)
	if viper.IsSet(commonKey) {
		return viper.GetString(commonKey)
	}

	// 如果都没有，返回空字符串
	return ""
}

// APIParametersOptimizer API参数优化器
type APIParametersOptimizer struct {
	// 不同API的最大token限制
	apiMaxTokensLimits map[string]int
	// 推荐的temperature范围
	temperatureRange [2]float64
	// 基础配置
	baseMaxTokens   int
	baseTemperature float64
}

// NewAPIParametersOptimizer 创建API参数优化器
func NewAPIParametersOptimizer() *APIParametersOptimizer {
	return &APIParametersOptimizer{
		apiMaxTokensLimits: map[string]int{
			"qianwen-turbo":   8192,   // 阿里千问Turbo
			"qianwen-plus":    32768,  // 阿里千问Plus
			"qianwen-max":     8192,   // 阿里千问Max
			"qwen-turbo":      8192,   // 千问兼容模式
			"deepseek-v3":     32768,  // 火山引擎DeepSeek V3
			"doubao-pro-4k":   4096,   // 火山引擎豆包Pro 4K
			"doubao-pro-32k":  32768,  // 火山引擎豆包Pro 32K
			"doubao-pro-128k": 131072, // 火山引擎豆包Pro 128K
			"default":         8192,   // 默认限制
		},
		temperatureRange: [2]float64{0.3, 0.8}, // 合理的temperature范围
		baseMaxTokens:    4096,                 // 基础maxTokens
		baseTemperature:  0.5,                  // 基础temperature
	}
}

// OptimizeParametersForAPI 为特定API优化参数
func (opt *APIParametersOptimizer) OptimizeParametersForAPI(
	ctx context.Context,
	apiType string, // "ali" 或 "volc"
	inputTextLength int,
	attempt int,
	bookID uint,
) (maxTokens int, temperature float64, err error) {
	log.C(ctx).Infow("🔧 开始优化API参数",
		"book_id", bookID,
		"api_type", apiType,
		"input_length", inputTextLength,
		"attempt", attempt)

	// 获取当前使用的模型
	var modelName string
	var apiLimit int

	switch apiType {
	case "ali":
		modelName = getAliConfig("text", "model")
		if modelName == "" {
			modelName = "qwen-turbo" // 默认模型
		}
	case "volc":
		modelName = viper.GetString("volc.model")
		if modelName == "" {
			modelName = "deepseek-v3" // 默认模型
		}
	default:
		return 0, 0, fmt.Errorf("不支持的API类型: %s", apiType)
	}

	// 获取该模型的最大token限制
	if limit, exists := opt.apiMaxTokensLimits[modelName]; exists {
		apiLimit = limit
	} else {
		// 模糊匹配
		for model, limit := range opt.apiMaxTokensLimits {
			if strings.Contains(modelName, model) || strings.Contains(model, modelName) {
				apiLimit = limit
				break
			}
		}
		if apiLimit == 0 {
			apiLimit = opt.apiMaxTokensLimits["default"]
			log.C(ctx).Warnw("⚠️ 未知模型，使用默认限制", "model", modelName, "default_limit", apiLimit)
		}
	}

	// 计算合理的maxTokens
	maxTokens = opt.calculateOptimalMaxTokens(inputTextLength, apiLimit, attempt)

	// 计算合理的temperature
	temperature = opt.calculateOptimalTemperature(attempt)

	log.C(ctx).Infow("✅ API参数优化完成",
		"book_id", bookID,
		"api_type", apiType,
		"model", modelName,
		"api_limit", apiLimit,
		"optimized_max_tokens", maxTokens,
		"optimized_temperature", temperature,
		"input_length", inputTextLength,
		"attempt", attempt)

	return maxTokens, temperature, nil
}

// calculateOptimalMaxTokens 计算最优的maxTokens
func (opt *APIParametersOptimizer) calculateOptimalMaxTokens(inputLength, apiLimit, attempt int) int {
	// 基于输入长度计算期望输出长度（通常为输入的1.5-2倍）
	expectedOutput := inputLength * 2

	// 基础maxTokens（不超过API限制的80%）
	baseTokens := minInt(expectedOutput, int(float64(apiLimit)*0.8))

	// 确保不小于最小值
	baseTokens = maxInt(baseTokens, 1024)

	// 根据重试次数调整策略
	switch attempt {
	case 1:
		// 第一次尝试：使用计算的基础值
		return baseTokens
	case 2:
		// 第二次尝试：减少到70%，避免可能的限制问题
		return int(float64(baseTokens) * 0.7)
	case 3:
		// 第三次尝试：使用保守值
		return minInt(baseTokens/2, 2048)
	default:
		// 第四次及以后：使用最保守值
		return minInt(1024, int(float64(apiLimit)*0.3))
	}
}

// calculateOptimalTemperature 计算最优的temperature
func (opt *APIParametersOptimizer) calculateOptimalTemperature(attempt int) float64 {
	switch attempt {
	case 1:
		// 第一次尝试：使用中等创造性
		return 0.5
	case 2:
		// 第二次尝试：降低创造性，提高稳定性
		return 0.3
	case 3:
		// 第三次尝试：进一步降低
		return 0.2
	default:
		// 第四次及以后：最低创造性，最高稳定性
		return 0.1
	}
}

// ValidateAPIResponse 验证API响应有效性
func (opt *APIParametersOptimizer) ValidateAPIResponse(
	ctx context.Context,
	response string,
	apiType string,
	bookID uint,
) (isValid bool, reason string) {
	// 检查响应长度
	responseLength := len(response)

	log.C(ctx).Debugw("🔍 验证API响应",
		"book_id", bookID,
		"api_type", apiType,
		"response_length", responseLength)

	if responseLength == 0 {
		return false, "API返回空响应"
	}

	if responseLength < 10 {
		return false, fmt.Sprintf("API响应过短: %d字符", responseLength)
	}

	// 检查是否包含错误信息
	lowerResponse := strings.ToLower(response)
	errorKeywords := []string{
		"error", "错误", "failed", "失败",
		"invalid", "无效", "timeout", "超时",
		"rate limit", "限流", "quota", "配额",
	}

	for _, keyword := range errorKeywords {
		if strings.Contains(lowerResponse, keyword) {
			return false, fmt.Sprintf("响应包含错误关键词: %s", keyword)
		}
	}

	// 检查是否有基本的文本结构
	if !strings.Contains(response, "{") && !strings.Contains(response, "[") {
		// 如果完全没有JSON结构，可能是纯文本错误
		if responseLength < 100 {
			return false, "响应缺少JSON结构且过短"
		}
	}

	log.C(ctx).Debugw("✅ API响应验证通过",
		"book_id", bookID,
		"api_type", apiType,
		"response_length", responseLength)

	return true, "响应有效"
}

// GetRecommendedRetryDelay 获取推荐的重试延迟
func (opt *APIParametersOptimizer) GetRecommendedRetryDelay(attempt int, apiType string) int {
	// 基础延迟（秒）
	baseDelay := 2

	// 根据API类型调整
	switch apiType {
	case "ali":
		baseDelay = 3 // 阿里API可能需要更长延迟
	case "volc":
		baseDelay = 2 // 火山引擎相对较快
	}

	// 指数退避，但有上限
	delay := baseDelay * (1 << (attempt - 1)) // 2^(attempt-1)

	// 最大延迟不超过30秒
	if delay > 30 {
		delay = 30
	}

	return delay
}

// minInt 返回两个整数中的较小值
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// maxInt 返回两个整数中的较大值
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
