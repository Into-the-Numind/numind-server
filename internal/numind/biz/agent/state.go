// Package agent 提供 Agent Runtime 的状态机和核心 biz 组件。
package agent

// TerminalReason 是 agent_run 终止时写入 agent_run.state_reason 的字符串值（共 13 个）。
type TerminalReason string

const (
	TerminalCompleted         TerminalReason = "completed"
	TerminalBlockingLimit     TerminalReason = "blocking_limit"
	TerminalImageError        TerminalReason = "image_error"
	TerminalModelError        TerminalReason = "model_error"
	TerminalAbortedStreaming  TerminalReason = "aborted_streaming"
	TerminalPromptTooLong     TerminalReason = "prompt_too_long"
	TerminalStopHookPrevented TerminalReason = "stop_hook_prevented"
	TerminalAbortedTools      TerminalReason = "aborted_tools"
	TerminalHookStopped       TerminalReason = "hook_stopped"
	TerminalMaxTurns          TerminalReason = "max_turns"
	TerminalErrorMaxBudget    TerminalReason = "error_max_budget"
	TerminalErrorMaxRetries   TerminalReason = "error_max_retries"
	TerminalPermissionDenied  TerminalReason = "permission_denied" // 13 — NEW (#6 agent-mode-permission-pipeline)
)

// ContinueReason 是 loop 继续时记录的字符串值（共 7 个），便于 Langfuse trace 调试。
type ContinueReason string

const (
	ContinueNextTurn             ContinueReason = "next_turn"
	ContinueCollapseDrainRetry   ContinueReason = "collapse_drain_retry"
	ContinueReactiveCompactRetry ContinueReason = "reactive_compact_retry"
	ContinueMaxOutputEscalate    ContinueReason = "max_output_escalate"
	ContinueMaxOutputRecovery    ContinueReason = "max_output_recovery"
	ContinueStopHookBlocking     ContinueReason = "stop_hook_blocking"
	ContinueTokenBudgetContinue  ContinueReason = "token_budget_continue"
)

// 编译期不变量：长度必须 13 + 7
var _ = [13]TerminalReason{
	TerminalCompleted, TerminalBlockingLimit, TerminalImageError, TerminalModelError,
	TerminalAbortedStreaming, TerminalPromptTooLong, TerminalStopHookPrevented, TerminalAbortedTools,
	TerminalHookStopped, TerminalMaxTurns, TerminalErrorMaxBudget, TerminalErrorMaxRetries,
	TerminalPermissionDenied,
}
var _ = [7]ContinueReason{
	ContinueNextTurn, ContinueCollapseDrainRetry, ContinueReactiveCompactRetry,
	ContinueMaxOutputEscalate, ContinueMaxOutputRecovery, ContinueStopHookBlocking, ContinueTokenBudgetContinue,
}

// LoopEvent 是状态机的输入事件枚举。
type LoopEvent int

const (
	LoopEventInvalid             LoopEvent = iota
	LoopEventLLMOKWithToolCall             // → ContinueNextTurn
	LoopEventLLMOKNoToolCall               // → TerminalCompleted
	LoopEventLLMErrPTL                     // → ContinueReactiveCompactRetry (if PTLRetries < 2) else TerminalPromptTooLong
	LoopEventLLMErrMaxOutput               // → ContinueMaxOutputRecovery (if < 2) else TerminalErrorMaxBudget
	LoopEventLLMErrModel                   // → TerminalModelError
	LoopEventLLMErrImage                   // → TerminalImageError
	LoopEventToolErr                       // → TerminalAbortedTools
	LoopEventCtxCanceled                   // → TerminalAbortedStreaming
	LoopEventMaxStepsReached               // → TerminalMaxTurns
	LoopEventBlockingLimitHit              // → TerminalBlockingLimit
	LoopEventMaxRetriesReached             // → TerminalErrorMaxRetries
	LoopEventHookActionStop                // → TerminalHookStopped
	LoopEventHookActionBlockStop           // → TerminalStopHookPrevented
	LoopEventHookActionBlocking            // → ContinueStopHookBlocking
	LoopEventTokenBudgetContinue           // → ContinueTokenBudgetContinue
	LoopEventCollapseDrainRetry            // → ContinueCollapseDrainRetry
	LoopEventMaxOutputEscalate             // → ContinueMaxOutputEscalate (17)
	LoopEventPermissionDenied              // → TerminalPermissionDenied (18) — NEW (#6 agent-mode-permission-pipeline)
	LoopEventErrorMaxBudget                // → TerminalErrorMaxBudget (19) — NEW (#12 agent-mode-billing-integration) — BudgetTracker 4 维任一超限
)

// LoopState 是 Runtime 单 run 内存中状态。
type LoopState struct {
	StepCount        int
	TerminalReason   TerminalReason // 空值表示尚未终止
	ContinueReason   ContinueReason // last continue
	PTLRetries       int
	MaxOutputRetries int
}

const (
	MaxPTLRetries         = 2 // 蓝本 §4.1.6
	MaxOutputRetriesLimit = 2
)

// IsTerminal 当前是否已终止。
func (s *LoopState) IsTerminal() bool { return s.TerminalReason != "" }

// Transition 接收事件，返回 (newTerminalReason, newContinueReason, isTerminal)。
// 副作用：更新 s 自身字段（StepCount / PTLRetries / MaxOutputRetries）。
func (s *LoopState) Transition(event LoopEvent) (TerminalReason, ContinueReason, bool) {
	switch event {
	case LoopEventLLMOKWithToolCall:
		s.StepCount++
		s.ContinueReason = ContinueNextTurn
		return "", ContinueNextTurn, false

	case LoopEventLLMOKNoToolCall:
		s.TerminalReason = TerminalCompleted
		return TerminalCompleted, "", true

	case LoopEventLLMErrPTL:
		s.PTLRetries++
		if s.PTLRetries > MaxPTLRetries {
			s.TerminalReason = TerminalPromptTooLong
			return TerminalPromptTooLong, "", true
		}
		// PTLRetries==1 → collapse_drain, PTLRetries==2 → reactive_compact
		if s.PTLRetries == 1 {
			s.ContinueReason = ContinueCollapseDrainRetry
			return "", ContinueCollapseDrainRetry, false
		}
		s.ContinueReason = ContinueReactiveCompactRetry
		return "", ContinueReactiveCompactRetry, false

	case LoopEventLLMErrMaxOutput:
		s.MaxOutputRetries++
		if s.MaxOutputRetries > MaxOutputRetriesLimit {
			s.TerminalReason = TerminalErrorMaxBudget
			return TerminalErrorMaxBudget, "", true
		}
		if s.MaxOutputRetries == 1 {
			s.ContinueReason = ContinueMaxOutputEscalate
			return "", ContinueMaxOutputEscalate, false
		}
		s.ContinueReason = ContinueMaxOutputRecovery
		return "", ContinueMaxOutputRecovery, false

	case LoopEventLLMErrModel:
		s.TerminalReason = TerminalModelError
		return TerminalModelError, "", true

	case LoopEventLLMErrImage:
		s.TerminalReason = TerminalImageError
		return TerminalImageError, "", true

	case LoopEventToolErr:
		s.TerminalReason = TerminalAbortedTools
		return TerminalAbortedTools, "", true

	case LoopEventCtxCanceled:
		s.TerminalReason = TerminalAbortedStreaming
		return TerminalAbortedStreaming, "", true

	case LoopEventMaxStepsReached:
		s.TerminalReason = TerminalMaxTurns
		return TerminalMaxTurns, "", true

	case LoopEventBlockingLimitHit:
		s.TerminalReason = TerminalBlockingLimit
		return TerminalBlockingLimit, "", true

	case LoopEventMaxRetriesReached:
		s.TerminalReason = TerminalErrorMaxRetries
		return TerminalErrorMaxRetries, "", true

	case LoopEventHookActionStop:
		s.TerminalReason = TerminalHookStopped
		return TerminalHookStopped, "", true

	case LoopEventHookActionBlockStop:
		s.TerminalReason = TerminalStopHookPrevented
		return TerminalStopHookPrevented, "", true

	case LoopEventPermissionDenied:
		s.TerminalReason = TerminalPermissionDenied
		return TerminalPermissionDenied, "", true

	case LoopEventErrorMaxBudget:
		s.TerminalReason = TerminalErrorMaxBudget
		return TerminalErrorMaxBudget, "", true

	case LoopEventHookActionBlocking:
		s.ContinueReason = ContinueStopHookBlocking
		return "", ContinueStopHookBlocking, false

	case LoopEventTokenBudgetContinue:
		s.ContinueReason = ContinueTokenBudgetContinue
		return "", ContinueTokenBudgetContinue, false

	case LoopEventCollapseDrainRetry:
		s.ContinueReason = ContinueCollapseDrainRetry
		return "", ContinueCollapseDrainRetry, false

	case LoopEventMaxOutputEscalate:
		s.ContinueReason = ContinueMaxOutputEscalate
		return "", ContinueMaxOutputEscalate, false

	default:
		// Unknown event treated as model error
		s.TerminalReason = TerminalModelError
		return TerminalModelError, "", true
	}
}
