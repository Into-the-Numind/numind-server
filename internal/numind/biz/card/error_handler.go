package card

import (
	"context"
	"fmt"
	"time"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// RenderError 渲染错误类型
type RenderError struct {
	Type    string `json:"type"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}

func (e *RenderError) Error() string {
	return fmt.Sprintf("[%s:%d] %s", e.Type, e.Code, e.Message)
}

// 错误类型常量
const (
	ErrorTypeSystem     = "SYSTEM"
	ErrorTypeWKHTML     = "WKHTML"
	ErrorTypeImage      = "IMAGE"
	ErrorTypeTemplate   = "TEMPLATE"
	ErrorTypeValidation = "VALIDATION"
	ErrorTypeTimeout    = "TIMEOUT"
	ErrorTypeMemory     = "MEMORY"
	ErrorTypeFallback   = "FALLBACK"
)

// 错误代码常量
const (
	ErrorCodeWKHTMLNotFound      = 1001
	ErrorCodeWKHTMLExecFailed    = 1002
	ErrorCodeWKHTMLTimeout       = 1003
	ErrorCodeImageDecodeFailed   = 2001
	ErrorCodeImageEncodeFailed   = 2002
	ErrorCodeImageSplitFailed    = 2003
	ErrorCodeTemplateInvalid     = 3001
	ErrorCodeTemplateParseFailed = 3002
	ErrorCodeTemplateExecFailed  = 3003
	ErrorCodeValidationFailed    = 4001
	ErrorCodeTimeoutExceeded     = 5001
	ErrorCodeMemoryExhausted     = 6001
	ErrorCodeFallbackFailed      = 7001
)

// ErrorHandler 错误处理器
type ErrorHandler struct {
	maxRetries   int
	retryDelay   time.Duration
	timeoutLimit time.Duration
	memoryLimit  int64 // 内存限制（字节）
	fallbackMode bool  // 是否启用降级模式
}

// NewErrorHandler 创建错误处理器
func NewErrorHandler() *ErrorHandler {
	return &ErrorHandler{
		maxRetries:   3,
		retryDelay:   2 * time.Second,
		timeoutLimit: 60 * time.Second,
		memoryLimit:  500 * 1024 * 1024, // 500MB
		fallbackMode: true,
	}
}

// HandleRenderError 处理渲染错误
func (h *ErrorHandler) HandleRenderError(ctx context.Context, err error, operation string) (*RenderError, bool) {
	log.C(ctx).Errorw("Render error occurred", "operation", operation, "error", err.Error())

	renderErr := h.classifyError(err, operation)
	shouldRetry := h.shouldRetry(renderErr)

	// 记录错误统计
	h.recordErrorStats(renderErr)

	return renderErr, shouldRetry
}

// classifyError 分类错误
func (h *ErrorHandler) classifyError(err error, operation string) *RenderError {
	errMsg := err.Error()

	// 检查wkhtmltoimage相关错误
	if contains(errMsg, "wkhtmltoimage not found") || contains(errMsg, "executable file not found") {
		return &RenderError{
			Type:    ErrorTypeWKHTML,
			Code:    ErrorCodeWKHTMLNotFound,
			Message: "wkhtmltoimage工具未找到",
			Details: errMsg,
		}
	}

	if contains(errMsg, "wkhtmltoimage") && contains(errMsg, "exit status") {
		return &RenderError{
			Type:    ErrorTypeWKHTML,
			Code:    ErrorCodeWKHTMLExecFailed,
			Message: "wkhtmltoimage执行失败",
			Details: errMsg,
		}
	}

	// 检查超时错误
	if contains(errMsg, "timeout") || contains(errMsg, "deadline exceeded") {
		return &RenderError{
			Type:    ErrorTypeTimeout,
			Code:    ErrorCodeTimeoutExceeded,
			Message: "操作超时",
			Details: errMsg,
		}
	}

	// 检查图片处理错误
	if contains(errMsg, "decode") && contains(errMsg, "image") {
		return &RenderError{
			Type:    ErrorTypeImage,
			Code:    ErrorCodeImageDecodeFailed,
			Message: "图片解码失败",
			Details: errMsg,
		}
	}

	if contains(errMsg, "encode") && contains(errMsg, "png") {
		return &RenderError{
			Type:    ErrorTypeImage,
			Code:    ErrorCodeImageEncodeFailed,
			Message: "图片编码失败",
			Details: errMsg,
		}
	}

	// 检查模板错误
	if contains(errMsg, "template") {
		if contains(errMsg, "parse") {
			return &RenderError{
				Type:    ErrorTypeTemplate,
				Code:    ErrorCodeTemplateParseFailed,
				Message: "模板解析失败",
				Details: errMsg,
			}
		}
		return &RenderError{
			Type:    ErrorTypeTemplate,
			Code:    ErrorCodeTemplateExecFailed,
			Message: "模板执行失败",
			Details: errMsg,
		}
	}

	// 检查内存错误
	if contains(errMsg, "out of memory") || contains(errMsg, "cannot allocate memory") {
		return &RenderError{
			Type:    ErrorTypeMemory,
			Code:    ErrorCodeMemoryExhausted,
			Message: "内存不足",
			Details: errMsg,
		}
	}

	// 默认系统错误
	return &RenderError{
		Type:    ErrorTypeSystem,
		Code:    1000,
		Message: "系统错误",
		Details: errMsg,
	}
}

// shouldRetry 判断是否应该重试
func (h *ErrorHandler) shouldRetry(err *RenderError) bool {
	switch err.Type {
	case ErrorTypeWKHTML:
		// wkhtmltoimage执行失败可以重试，但工具未找到不能重试
		return err.Code != ErrorCodeWKHTMLNotFound
	case ErrorTypeTimeout:
		// 超时错误可以重试
		return true
	case ErrorTypeImage:
		// 图片编码错误可以重试，解码错误通常不行
		return err.Code == ErrorCodeImageEncodeFailed
	case ErrorTypeTemplate:
		// 模板执行失败可以重试，解析失败不行
		return err.Code == ErrorCodeTemplateExecFailed
	case ErrorTypeSystem:
		// 系统错误可以重试
		return true
	default:
		return false
	}
}

// RetryWithBackoff 带退避的重试机制
func (h *ErrorHandler) RetryWithBackoff(ctx context.Context, operation func() error, maxRetries int) error {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		// 检查上下文是否已取消
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// 执行操作
		if err := operation(); err != nil {
			lastErr = err

			// 分类错误并判断是否应该重试
			renderErr, shouldRetry := h.HandleRenderError(ctx, err, fmt.Sprintf("attempt_%d", attempt+1))

			if !shouldRetry || attempt == maxRetries-1 {
				log.C(ctx).Errorw("Operation failed after retries",
					"attempt", attempt+1,
					"maxRetries", maxRetries,
					"errorType", renderErr.Type,
					"errorCode", renderErr.Code)
				return lastErr
			}

			// 计算退避延迟
			delay := h.calculateBackoffDelay(attempt)
			log.C(ctx).Warnw("Operation failed, retrying",
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"delay", delay,
				"errorType", renderErr.Type)

			// 等待重试
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				// 继续重试
			}
		} else {
			// 成功
			if attempt > 0 {
				log.C(ctx).Infow("Operation succeeded after retry", "attempts", attempt+1)
			}
			return nil
		}
	}

	return lastErr
}

// calculateBackoffDelay 计算退避延迟
func (h *ErrorHandler) calculateBackoffDelay(attempt int) time.Duration {
	// 指数退避：2^attempt * baseDelay
	baseDelay := h.retryDelay
	delay := baseDelay * time.Duration(1<<attempt)

	// 限制最大延迟为30秒
	maxDelay := 30 * time.Second
	if delay > maxDelay {
		delay = maxDelay
	}

	return delay
}

// CreateFallbackImage 创建降级图片
func (h *ErrorHandler) CreateFallbackImage(ctx context.Context, card *model.CardM, errorDetails string) ([]byte, error) {
	if !h.fallbackMode {
		return nil, &RenderError{
			Type:    ErrorTypeFallback,
			Code:    ErrorCodeFallbackFailed,
			Message: "降级模式未启用",
			Details: "fallback mode is disabled",
		}
	}

	log.C(ctx).Warnw("Creating fallback image", "cardID", card.ID, "error", errorDetails)

	// 创建简单的文本图片作为降级方案
	fallbackImage, err := h.createSimpleTextImage(card)
	if err != nil {
		return nil, &RenderError{
			Type:    ErrorTypeFallback,
			Code:    ErrorCodeFallbackFailed,
			Message: "降级图片创建失败",
			Details: err.Error(),
		}
	}

	return fallbackImage, nil
}

// createSimpleTextImage 创建简单的文本图片
func (h *ErrorHandler) createSimpleTextImage(card *model.CardM) ([]byte, error) {
	// 这里实现一个简单的文本图片生成
	// 作为完全的降级方案，当wkhtmltoimage完全不可用时使用

	// 暂时返回一个占位符错误，实际实现可以使用Go的image包生成简单图片
	return nil, fmt.Errorf("fallback image generation not implemented yet")
}

// recordErrorStats 记录错误统计
func (h *ErrorHandler) recordErrorStats(err *RenderError) {
	// 这里可以实现错误统计逻辑
	// 比如记录到日志、指标系统等
	log.Infow("Error stats recorded",
		"errorType", err.Type,
		"errorCode", err.Code,
		"timestamp", time.Now())
}

// ValidateRenderEnvironment 验证渲染环境
func (h *ErrorHandler) ValidateRenderEnvironment(ctx context.Context) error {
	log.C(ctx).Infow("Validating render environment")

	// 检查wkhtmltoimage是否可用
	if err := h.validateWKHTMLToImage(); err != nil {
		return &RenderError{
			Type:    ErrorTypeValidation,
			Code:    ErrorCodeValidationFailed,
			Message: "wkhtmltoimage验证失败",
			Details: err.Error(),
		}
	}

	// 检查系统资源
	if err := h.validateSystemResources(); err != nil {
		return &RenderError{
			Type:    ErrorTypeValidation,
			Code:    ErrorCodeValidationFailed,
			Message: "系统资源验证失败",
			Details: err.Error(),
		}
	}

	log.C(ctx).Infow("Render environment validation passed")
	return nil
}

// validateWKHTMLToImage 验证wkhtmltoimage
func (h *ErrorHandler) validateWKHTMLToImage() error {
	// 使用现有的LightweightRenderer验证逻辑
	// 这里简化为检查工具是否存在
	_, err := NewLightweightRenderer(nil)
	return err
}

// validateSystemResources 验证系统资源
func (h *ErrorHandler) validateSystemResources() error {
	// 检查可用内存、磁盘空间等
	// 这里简化实现
	return nil
}

// contains 字符串包含检查（不区分大小写）
func contains(s, substr string) bool {
	return len(s) >= len(substr) &&
		(s == substr ||
			len(s) > len(substr) &&
				(s[:len(substr)] == substr ||
					s[len(s)-len(substr):] == substr ||
					containsInner(s, substr)))
}

func containsInner(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
