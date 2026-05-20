package agent

import (
	"errors"
	"strings"
)

// Withhold recovery（spec §7）：把 LLM 错误分类为可恢复的 chain 事件或终止事件。
// 优先级：PTL > max_output_tokens（PTL 必须先 compact 才能 retry max_output）。

// HandleLLMError 给定 LLM 返回的 err，结合当前 LoopState，返回应该转化的 LoopEvent。
// 调用方：runner 主流程 → 收到 LLM err → HandleLLMError → 把结果丢给 state.Transition。
func HandleLLMError(state *LoopState, err error) LoopEvent {
	if err == nil {
		return LoopEventInvalid
	}
	// PTL 优先：context window 满，必须 compact 才能 retry
	if isPromptTooLong(err) {
		return LoopEventLLMErrPTL
	}
	if isMaxOutputTokens(err) {
		return LoopEventLLMErrMaxOutput
	}
	if isImageError(err) {
		return LoopEventLLMErrImage
	}
	return LoopEventLLMErrModel
}

// isPromptTooLong 判定 err 是否是 PromptTooLong (context window 超限)。
// 实现：检查 err message 关键字 + 错误类型 wrapping。
// 注：aiservice 真实错误格式由 #1 Phase 0 V2 demo 已确认（"context_length_exceeded" / "prompt_too_long" 等）；
// 这里用 substring match 兜底，#9 compact feature 时改为基于结构化 ErrCode。
func isPromptTooLong(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "context_length_exceeded") ||
		strings.Contains(s, "prompt_too_long") ||
		strings.Contains(s, "context window") ||
		strings.Contains(s, "token limit exceeded")
}

// isMaxOutputTokens 判定 err 是否是 max_tokens / max_output 错误。
func isMaxOutputTokens(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "max_tokens") ||
		strings.Contains(s, "max_output") ||
		strings.Contains(s, "output_too_long")
}

// isImageError 判定 err 是否是图像处理错误（vision tool 失败）。
func isImageError(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "image_decode") ||
		strings.Contains(s, "image_format") ||
		strings.Contains(s, "invalid_image")
}

// 自定义错误类型供 runner / test 使用
type LLMError struct {
	Code string
	Msg  string
}

func (e *LLMError) Error() string {
	if e.Msg != "" {
		return e.Code + ": " + e.Msg
	}
	return e.Code
}

// 标准错误码常量
var (
	ErrPromptTooLong   = errors.New("prompt_too_long")
	ErrMaxOutputTokens = errors.New("max_output_tokens")
	ErrImageDecode     = errors.New("image_decode_failed")
)
