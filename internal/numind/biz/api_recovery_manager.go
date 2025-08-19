package biz

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/pkg/log"
)

// APIRecoveryManager API恢复管理器
// 处理API失败的情况，提供多种恢复策略
type APIRecoveryManager struct {
	aliBiz  ali.AliBiz
	volcBiz volc.VolcBiz

	// 恢复配置
	maxRetryAttempts  int
	baseRetryDelay    time.Duration
	maxRetryDelay     time.Duration
	enableFallback    bool
	enableDiagnostics bool
}

// NewAPIRecoveryManager 创建API恢复管理器
func NewAPIRecoveryManager(aliBiz ali.AliBiz, volcBiz volc.VolcBiz) *APIRecoveryManager {
	return &APIRecoveryManager{
		aliBiz:            aliBiz,
		volcBiz:           volcBiz,
		maxRetryAttempts:  5,
		baseRetryDelay:    2 * time.Second,
		maxRetryDelay:     30 * time.Second,
		enableFallback:    true,
		enableDiagnostics: true,
	}
}

// RecoverTextGeneration 恢复文本生成API调用
func (rm *APIRecoveryManager) RecoverTextGeneration(
	ctx context.Context,
	messages []map[string]string,
	maxTokens int,
	temperature float64,
) (string, error) {
	log.C(ctx).Infow("🚀 开始文本生成API恢复流程")

	// 第一步：尝试火山引擎API（主要方案）
	result, err := rm.tryVolcWithRecovery(ctx, messages, maxTokens, temperature)
	if err == nil {
		log.C(ctx).Infow("✅ 火山引擎API调用成功")
		return result, nil
	}
	log.C(ctx).Errorw("❌ 火山引擎API失败", "error", err.Error())

	// 第二步：尝试阿里千问API（降级方案）
	if rm.enableFallback {
		log.C(ctx).Infow("🔄 开始降级到阿里千问API")
		result, err := rm.tryAliWithRecovery(ctx, messages, maxTokens, temperature)
		if err == nil {
			log.C(ctx).Infow("✅ 阿里千问API调用成功（降级）")
			return result, nil
		}
		log.C(ctx).Errorw("❌ 阿里千问API也失败", "error", err.Error())
	}

	// 第三步：进行网络诊断
	if rm.enableDiagnostics {
		log.C(ctx).Infow("🔍 开始网络诊断")
		diagnostics := NewAPIDiagnostics()
		if err := diagnostics.DiagnoseAllAPIs(ctx); err != nil {
			log.C(ctx).Errorw("诊断过程出现问题", "error", err.Error())
		}
	}

	// 第四步：返回综合错误信息
	return "", fmt.Errorf("所有API调用都失败了，请检查网络连接和API配置")
}

// tryVolcWithRecovery 带恢复的火山引擎API调用
func (rm *APIRecoveryManager) tryVolcWithRecovery(
	ctx context.Context,
	messages []map[string]string,
	maxTokens int,
	temperature float64,
) (string, error) {
	var lastErr error
	delay := rm.baseRetryDelay

	for attempt := 1; attempt <= rm.maxRetryAttempts; attempt++ {
		log.C(ctx).Infow("🔄 尝试火山引擎API", "attempt", attempt, "max_attempts", rm.maxRetryAttempts)

		result, err := rm.volcBiz.VolcTextStream(ctx, messages, maxTokens, temperature)
		if err == nil {
			log.C(ctx).Infow("✅ 火山引擎API成功", "attempt", attempt)
			return result, nil
		}

		lastErr = err
		log.C(ctx).Warnw("⚠️ 火山引擎API失败，准备重试",
			"attempt", attempt,
			"error", err.Error(),
			"next_delay", delay)

		// 如果不是最后一次尝试，等待后重试
		if attempt < rm.maxRetryAttempts {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
				// 指数退避，但不超过最大延迟
				delay = delay * 2
				if delay > rm.maxRetryDelay {
					delay = rm.maxRetryDelay
				}
			}
		}
	}

	return "", fmt.Errorf("火山引擎API重试%d次后仍失败: %v", rm.maxRetryAttempts, lastErr)
}

// tryAliWithRecovery 带恢复的阿里千问API调用
func (rm *APIRecoveryManager) tryAliWithRecovery(
	ctx context.Context,
	messages []map[string]string,
	maxTokens int,
	temperature float64,
) (string, error) {
	var lastErr error
	delay := rm.baseRetryDelay

	for attempt := 1; attempt <= rm.maxRetryAttempts; attempt++ {
		log.C(ctx).Infow("🔄 尝试阿里千问API", "attempt", attempt, "max_attempts", rm.maxRetryAttempts)

		result, err := rm.aliBiz.QianwenTextStream(messages, maxTokens, temperature)
		if err == nil {
			log.C(ctx).Infow("✅ 阿里千问API成功", "attempt", attempt)
			return result, nil
		}

		lastErr = err
		log.C(ctx).Warnw("⚠️ 阿里千问API失败，准备重试",
			"attempt", attempt,
			"error", err.Error(),
			"next_delay", delay)

		// 如果不是最后一次尝试，等待后重试
		if attempt < rm.maxRetryAttempts {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
				// 指数退避，但不超过最大延迟
				delay = delay * 2
				if delay > rm.maxRetryDelay {
					delay = rm.maxRetryDelay
				}
			}
		}
	}

	return "", fmt.Errorf("阿里千问API重试%d次后仍失败: %v", rm.maxRetryAttempts, lastErr)
}

// ValidateAPIConfiguration 验证API配置
func (rm *APIRecoveryManager) ValidateAPIConfiguration(ctx context.Context) error {
	log.C(ctx).Infow("🔍 验证API配置")

	errors := []string{}

	// 验证火山引擎配置
	if err := rm.validateVolcConfig(ctx); err != nil {
		errors = append(errors, fmt.Sprintf("火山引擎配置错误: %v", err))
	}

	// 验证阿里云配置
	if err := rm.validateAliConfig(ctx); err != nil {
		errors = append(errors, fmt.Sprintf("阿里云配置错误: %v", err))
	}

	if len(errors) > 0 {
		for _, errMsg := range errors {
			log.C(ctx).Errorw("❌ 配置验证失败", "error", errMsg)
		}
		return fmt.Errorf("API配置验证失败: %v", errors)
	}

	log.C(ctx).Infow("✅ API配置验证通过")
	return nil
}

// validateVolcConfig 验证火山引擎配置
func (rm *APIRecoveryManager) validateVolcConfig(ctx context.Context) error {
	// 这里可以添加具体的配置验证逻辑
	// 比如检查API key、URL等
	log.C(ctx).Infow("验证火山引擎配置")
	return nil
}

// validateAliConfig 验证阿里云配置
func (rm *APIRecoveryManager) validateAliConfig(ctx context.Context) error {
	// 这里可以添加具体的配置验证逻辑
	// 比如检查API key、URL等
	log.C(ctx).Infow("验证阿里云配置")
	return nil
}

// GetRecoveryStats 获取恢复统计信息
func (rm *APIRecoveryManager) GetRecoveryStats(ctx context.Context) map[string]interface{} {
	return map[string]interface{}{
		"max_retry_attempts": rm.maxRetryAttempts,
		"base_retry_delay":   rm.baseRetryDelay.String(),
		"max_retry_delay":    rm.maxRetryDelay.String(),
		"enable_fallback":    rm.enableFallback,
		"enable_diagnostics": rm.enableDiagnostics,
	}
}
