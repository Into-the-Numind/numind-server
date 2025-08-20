package book

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/pkg/log"
)

// callQianwenWithEnhancedRetry 增强版阿里千问API调用
func (p *AsyncBookProcessor) callQianwenWithEnhancedRetry(
	ctx context.Context,
	messages []map[string]string,
	maxTokens int,
	temperature float64,
	bookID uint,
	paramOptimizer *APIParametersOptimizer,
) (string, error) {
	maxAttempts := 3
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// 优化API参数
		optimizedMaxTokens, optimizedTemperature, paramErr := paramOptimizer.OptimizeParametersForAPI(
			ctx, "ali", calculateInputLength(messages), attempt, bookID)

		if paramErr != nil {
			log.C(ctx).Warnw("⚠️ 千问API参数优化失败，使用传入值", "error", paramErr.Error())
			optimizedMaxTokens = maxTokens
			optimizedTemperature = temperature
		}

		log.C(ctx).Infow("🔄 阿里千问API尝试",
			"book_id", bookID,
			"attempt", attempt,
			"max_attempts", maxAttempts,
			"optimized_max_tokens", optimizedMaxTokens,
			"optimized_temperature", optimizedTemperature)

		// 调用阿里千问API
		result, err := p.biz.Ali().QianwenTextStream(messages, optimizedMaxTokens, optimizedTemperature)
		if err != nil {
			lastErr = err
			log.C(ctx).Warnw("⚠️ 阿里千问API调用失败",
				"book_id", bookID,
				"attempt", attempt,
				"error", err.Error())

			// 如果不是最后一次尝试，等待后重试
			if attempt < maxAttempts {
				delay := time.Duration(paramOptimizer.GetRecommendedRetryDelay(attempt, "ali")) * time.Second
				log.C(ctx).Infow("⏳ 等待阿里千问API重试", "delay", delay)
				time.Sleep(delay)
			}
			continue
		}

		// 验证响应有效性
		isValid, reason := paramOptimizer.ValidateAPIResponse(ctx, result, "ali", bookID)
		if !isValid {
			lastErr = fmt.Errorf("阿里千问API响应无效: %s", reason)
			log.C(ctx).Warnw("⚠️ 阿里千问API响应验证失败",
				"book_id", bookID,
				"attempt", attempt,
				"reason", reason,
				"response_length", len(result))

			// 响应无效也重试
			if attempt < maxAttempts {
				delay := time.Duration(2*attempt) * time.Second
				time.Sleep(delay)
			}
			continue
		}

		log.C(ctx).Infow("✅ 阿里千问API调用成功",
			"book_id", bookID,
			"attempt", attempt,
			"response_length", len(result))

		return result, nil
	}

	return "", fmt.Errorf("阿里千问API重试%d次后仍失败: %w", maxAttempts, lastErr)
}

// callVolcWithEnhancedRetry 增强版火山引擎API调用
func (p *AsyncBookProcessor) callVolcWithEnhancedRetry(
	ctx context.Context,
	messages []map[string]string,
	maxTokens int,
	temperature float64,
	bookID uint,
	paramOptimizer *APIParametersOptimizer,
) (string, error) {
	maxAttempts := 3
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// 优化API参数
		optimizedMaxTokens, optimizedTemperature, paramErr := paramOptimizer.OptimizeParametersForAPI(
			ctx, "volc", calculateInputLength(messages), attempt, bookID)

		if paramErr != nil {
			log.C(ctx).Warnw("⚠️ 火山引擎API参数优化失败，使用传入值", "error", paramErr.Error())
			optimizedMaxTokens = maxTokens
			optimizedTemperature = temperature
		}

		log.C(ctx).Infow("🔄 火山引擎API尝试",
			"book_id", bookID,
			"attempt", attempt,
			"max_attempts", maxAttempts,
			"optimized_max_tokens", optimizedMaxTokens,
			"optimized_temperature", optimizedTemperature)

		// 调用火山引擎API
		result, err := p.biz.Volc().VolcTextStream(ctx, messages, optimizedMaxTokens, optimizedTemperature)
		if err != nil {
			lastErr = err
			log.C(ctx).Warnw("⚠️ 火山引擎API调用失败",
				"book_id", bookID,
				"attempt", attempt,
				"error", err.Error())

			// 如果不是最后一次尝试，等待后重试
			if attempt < maxAttempts {
				delay := time.Duration(paramOptimizer.GetRecommendedRetryDelay(attempt, "volc")) * time.Second
				log.C(ctx).Infow("⏳ 等待火山引擎API重试", "delay", delay)
				time.Sleep(delay)
			}
			continue
		}

		// 验证响应有效性
		isValid, reason := paramOptimizer.ValidateAPIResponse(ctx, result, "volc", bookID)
		if !isValid {
			lastErr = fmt.Errorf("火山引擎API响应无效: %s", reason)
			log.C(ctx).Warnw("⚠️ 火山引擎API响应验证失败",
				"book_id", bookID,
				"attempt", attempt,
				"reason", reason,
				"response_length", len(result))

			// 响应无效也重试
			if attempt < maxAttempts {
				delay := time.Duration(2*attempt) * time.Second
				time.Sleep(delay)
			}
			continue
		}

		log.C(ctx).Infow("✅ 火山引擎API调用成功",
			"book_id", bookID,
			"attempt", attempt,
			"response_length", len(result))

		return result, nil
	}

	return "", fmt.Errorf("火山引擎API重试%d次后仍失败: %w", maxAttempts, lastErr)
}

// calculateInputLength 计算输入消息的总长度
func calculateInputLength(messages []map[string]string) int {
	totalLength := 0
	for _, msg := range messages {
		if content, exists := msg["content"]; exists {
			totalLength += len(content)
		}
	}
	return totalLength
}
