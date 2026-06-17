package agent

import (
	"errors"
	"strings"

	aierr "numind-server/internal/pkg/aiservice/aierr"
	"numind-server/internal/pkg/errno"
)

// user_error.go — the single place that turns internal errors / machine-code
// terminal reasons into friendly Chinese messages safe to show end users.
//
// Why centralize: agent-mode error events used to surface raw err.Error() (with
// provider names, HTTP codes, URLs, Eino node paths) and machine-code
// TerminalReasons directly to learners (User-reported, dev 2026-06-08). These two
// functions guarantee the user never sees engineer text; the raw error must be
// logged separately for ops debugging.

// userFacingFallback is shown when no more specific cause can be determined.
// It never echoes the raw error.
const userFacingFallback = "服务暂时不可用，请稍后再试，或换种问法。"

// UserFacingErrorMessage translates an internal error into a friendly Chinese
// message. It NEVER returns err.Error(). Order: curated errno message → content
// classification (reusing withhold.go classifiers + provider keywords) → fallback.
func UserFacingErrorMessage(err error) string {
	if err == nil {
		return ""
	}

	// 1. Known *errno.Errno → map by STABLE Code to curated friendly text.
	//    We must NOT return en.Message: provider adapters inject the raw upstream
	//    string into Message via SetMessage (e.g. ErrAIProviderTimeout.SetMessage(
	//    "doPost https://www.dmxapi.cn/... net/http: timeout awaiting response
	//    headers")), so returning en.Message would leak engineer text. The Code is
	//    stable regardless of SetMessage.
	var en *errno.Errno
	if errors.As(err, &en) {
		switch en.Code {
		case "Credits.Insufficient":
			return "积分不足，请充值后再试。"
		case "AIService.ProviderTimeout":
			return "AI 服务响应超时，请稍后再试。"
		case "AIService.ProviderError", "AIService.FallbackExhausted",
			"AIService.ServiceDeprecated", "AIService.Unbound", "AIService.ServiceNotFound":
			return "AI 服务暂时不可用，请稍后再试。"
		case "AIService.TaskNotFound", "AIService.CapabilityMismatch":
			return "AI 服务暂未配置好，请联系老师。"
		}
		// Unmapped errno code → fall through to content classification (do NOT
		// return en.Message; it may be a raw provider string).
	}

	// 1b. Structured semantic code (aierr.ProviderError) attached by provider
	//     adapters from upstream error.code / error.type / httpStatus. Switch on
	//     the stable code instead of fragile substring matching. PTL / image are
	//     handled by the content classifiers below (which themselves read the
	//     structured code first), so here we only cover the remaining codes.
	switch aierr.CodeOf(err) {
	case aierr.CodeRateLimited:
		return "请求太频繁，请稍后再试。"
	case aierr.CodeAuthError:
		return "AI 服务鉴权失败，请联系管理员。"
	case aierr.CodeContentFilter:
		return "内容被安全策略拦截，请调整后重试。"
	case aierr.CodeProviderTimeout:
		return "AI 服务响应超时，请稍后再试。"
	}

	// 2. Classify by content (handles raw errors AND errnos whose Message carries
	//    the upstream string).
	if isPromptTooLong(err) {
		return "本次对话内容太长了，请精简后再发送。"
	}
	if isImageError(err) {
		return "图片处理失败，请确认图片清晰、格式常见后重试。"
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "allocationquota"), strings.Contains(s, "freetieronly"), strings.Contains(s, "free tier"):
		return "当前模型额度已用完，请稍后再试或联系老师。"
	case strings.Contains(s, "insufficient") && strings.Contains(s, "credit"), strings.Contains(s, "积分不足"):
		return "积分不足，请充值后再试。"
	case strings.Contains(s, "timeout"), strings.Contains(s, "deadline exceeded"), strings.Contains(s, "timed out"):
		return "AI 服务响应超时，请稍后再试。"
	case strings.Contains(s, "task profile"), strings.Contains(s, "resolvetask"):
		return "AI 服务暂未配置好，请联系老师。"
	case strings.Contains(s, "rate limit"), strings.Contains(s, "too many requests"), strings.Contains(s, "http 429"), strings.Contains(s, "status 429"):
		return "请求太频繁了，请稍后再试。"
	}
	return userFacingFallback
}

// UserFacingTerminalMessage maps a machine TerminalReason to a friendly Chinese
// message. Returns "" for non-error reasons (completed / waiting for user choice)
// so callers can skip showing an error bubble.
func UserFacingTerminalMessage(reason TerminalReason) string {
	switch reason {
	case TerminalCompleted, TerminalWaitingForUserChoice:
		return ""
	case TerminalModelError, TerminalErrorMaxRetries:
		return "AI 一时没能完成这次任务，请稍后再试或换个问法。"
	case TerminalPromptTooLong:
		return "本次对话内容太长了，请精简后再发送。"
	case TerminalImageError:
		return "图片处理失败，请确认图片清晰、格式常见后重试。"
	case TerminalErrorMaxBudget:
		return "本次任务消耗的积分已达上限，已自动停止。"
	case TerminalMaxTurns, TerminalBlockingLimit:
		return "任务步骤过多，已自动停止。可以拆成更小的问题再试。"
	case TerminalAbortedTools, TerminalAbortedStreaming:
		return "任务已中断。"
	case TerminalPermissionDenied:
		return "该操作未被允许。"
	case TerminalHookStopped, TerminalStopHookPrevented:
		return "任务被安全策略中止。"
	default:
		return "任务未能正常完成，请稍后再试。"
	}
}
