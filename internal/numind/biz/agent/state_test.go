package agent

import "testing"

func TestTransition_Completed(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventLLMOKNoToolCall)
	if !isTerminal || tr != TerminalCompleted || cr != "" {
		t.Errorf("expected TerminalCompleted, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
	if !s.IsTerminal() {
		t.Error("IsTerminal() should be true")
	}
}

func TestTransition_LLMOKWithToolCall(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventLLMOKWithToolCall)
	if isTerminal || tr != "" || cr != ContinueNextTurn {
		t.Errorf("expected ContinueNextTurn, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
	if s.StepCount != 1 {
		t.Errorf("expected StepCount=1, got %d", s.StepCount)
	}
}

func TestTransition_PTLRecovery(t *testing.T) {
	s := &LoopState{}

	// 1st PTL → collapse_drain_retry
	tr, cr, isTerminal := s.Transition(LoopEventLLMErrPTL)
	if isTerminal || tr != "" || cr != ContinueCollapseDrainRetry {
		t.Errorf("1st PTL: expected ContinueCollapseDrainRetry, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}

	// 2nd PTL → reactive_compact_retry
	tr, cr, isTerminal = s.Transition(LoopEventLLMErrPTL)
	if isTerminal || tr != "" || cr != ContinueReactiveCompactRetry {
		t.Errorf("2nd PTL: expected ContinueReactiveCompactRetry, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}

	// 3rd PTL → TerminalPromptTooLong
	tr, cr, isTerminal = s.Transition(LoopEventLLMErrPTL)
	if !isTerminal || tr != TerminalPromptTooLong || cr != "" {
		t.Errorf("3rd PTL: expected TerminalPromptTooLong, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_MaxOutputRecovery(t *testing.T) {
	s := &LoopState{}

	// 1st MaxOutput → max_output_escalate
	tr, cr, isTerminal := s.Transition(LoopEventLLMErrMaxOutput)
	if isTerminal || tr != "" || cr != ContinueMaxOutputEscalate {
		t.Errorf("1st MaxOutput: expected ContinueMaxOutputEscalate, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}

	// 2nd MaxOutput → max_output_recovery
	tr, cr, isTerminal = s.Transition(LoopEventLLMErrMaxOutput)
	if isTerminal || tr != "" || cr != ContinueMaxOutputRecovery {
		t.Errorf("2nd MaxOutput: expected ContinueMaxOutputRecovery, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}

	// 3rd MaxOutput → TerminalErrorMaxBudget
	tr, cr, isTerminal = s.Transition(LoopEventLLMErrMaxOutput)
	if !isTerminal || tr != TerminalErrorMaxBudget || cr != "" {
		t.Errorf("3rd MaxOutput: expected TerminalErrorMaxBudget, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_ModelError(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventLLMErrModel)
	if !isTerminal || tr != TerminalModelError || cr != "" {
		t.Errorf("expected TerminalModelError, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_ImageError(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventLLMErrImage)
	if !isTerminal || tr != TerminalImageError || cr != "" {
		t.Errorf("expected TerminalImageError, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_ToolErr(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventToolErr)
	if !isTerminal || tr != TerminalAbortedTools || cr != "" {
		t.Errorf("expected TerminalAbortedTools, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_CtxCanceled(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventCtxCanceled)
	if !isTerminal || tr != TerminalAbortedStreaming || cr != "" {
		t.Errorf("expected TerminalAbortedStreaming, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_MaxStepsReached(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventMaxStepsReached)
	if !isTerminal || tr != TerminalMaxTurns || cr != "" {
		t.Errorf("expected TerminalMaxTurns, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_BlockingLimitHit(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventBlockingLimitHit)
	if !isTerminal || tr != TerminalBlockingLimit || cr != "" {
		t.Errorf("expected TerminalBlockingLimit, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_MaxRetriesReached(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventMaxRetriesReached)
	if !isTerminal || tr != TerminalErrorMaxRetries || cr != "" {
		t.Errorf("expected TerminalErrorMaxRetries, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_HookActionStop(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventHookActionStop)
	if !isTerminal || tr != TerminalHookStopped || cr != "" {
		t.Errorf("expected TerminalHookStopped, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_HookActionBlockStop(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventHookActionBlockStop)
	if !isTerminal || tr != TerminalStopHookPrevented || cr != "" {
		t.Errorf("expected TerminalStopHookPrevented, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_HookActionBlocking(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventHookActionBlocking)
	if isTerminal || tr != "" || cr != ContinueStopHookBlocking {
		t.Errorf("expected ContinueStopHookBlocking, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_TokenBudgetContinue(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventTokenBudgetContinue)
	if isTerminal || tr != "" || cr != ContinueTokenBudgetContinue {
		t.Errorf("expected ContinueTokenBudgetContinue, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_CollapseDrainRetry(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventCollapseDrainRetry)
	if isTerminal || tr != "" || cr != ContinueCollapseDrainRetry {
		t.Errorf("expected ContinueCollapseDrainRetry, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_MaxOutputEscalate(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventMaxOutputEscalate)
	if isTerminal || tr != "" || cr != ContinueMaxOutputEscalate {
		t.Errorf("expected ContinueMaxOutputEscalate, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestTransition_UnknownEvent(t *testing.T) {
	s := &LoopState{}
	tr, cr, isTerminal := s.Transition(LoopEventInvalid)
	if !isTerminal || tr != TerminalModelError || cr != "" {
		t.Errorf("unknown event: expected TerminalModelError, got tr=%q cr=%q isTerminal=%v", tr, cr, isTerminal)
	}
}

func TestState_IsTerminal(t *testing.T) {
	s := &LoopState{}
	if s.IsTerminal() {
		t.Error("fresh LoopState should not be terminal")
	}
	s.TerminalReason = TerminalCompleted
	if !s.IsTerminal() {
		t.Error("LoopState with TerminalReason should be terminal")
	}
}

// ── M9: TerminalPermissionDenied + LoopEventPermissionDenied tests ────────────

func TestTransition_PermissionDenied(t *testing.T) {
	s := &LoopState{}
	term, cont, isTerm := s.Transition(LoopEventPermissionDenied)
	if term != TerminalPermissionDenied {
		t.Errorf("term = %s, want %s", term, TerminalPermissionDenied)
	}
	if cont != "" {
		t.Errorf("cont = %s, want empty", cont)
	}
	if !isTerm {
		t.Errorf("expected isTerm true")
	}
	if !s.IsTerminal() {
		t.Errorf("LoopState should be terminal after Transition")
	}
	if s.TerminalReason != TerminalPermissionDenied {
		t.Errorf("s.TerminalReason mismatch")
	}
}

func TestTerminalReason_PermissionDenied_StringValue(t *testing.T) {
	if string(TerminalPermissionDenied) != "permission_denied" {
		t.Errorf("string value mismatch: %s", TerminalPermissionDenied)
	}
}

func TestTerminalReason_Count13(t *testing.T) {
	// 这个 test 仅依赖编译期 [13] 数组通过；运行期是 no-op
	// 如果 state.go 的编译期数组从 [12] 改 [13] 没改 → 本测试编译失败
	var _ = [13]TerminalReason{} // sanity
}

// ── M12 (#12 agent-mode-billing-integration): LoopEventErrorMaxBudget transition ──

func TestTransition_ErrorMaxBudget(t *testing.T) {
	s := &LoopState{}
	term, cont, isTerminal := s.Transition(LoopEventErrorMaxBudget)
	if !isTerminal {
		t.Errorf("expected isTerminal true")
	}
	if term != TerminalErrorMaxBudget {
		t.Errorf("term = %q, want %q", term, TerminalErrorMaxBudget)
	}
	if cont != "" {
		t.Errorf("cont = %q, want empty", cont)
	}
	if s.TerminalReason != TerminalErrorMaxBudget {
		t.Errorf("s.TerminalReason = %q, want %q", s.TerminalReason, TerminalErrorMaxBudget)
	}
}

func TestAllReasonsReachable(t *testing.T) {
	// Map of all expected terminal and continue reasons and how to reach them
	type testCase struct {
		name    string
		setup   func() *LoopState
		event   LoopEvent
		wantTR  TerminalReason
		wantCR  ContinueReason
		wantEnd bool
	}

	cases := []testCase{
		// 12 terminal reasons
		{name: "completed", setup: func() *LoopState { return &LoopState{} }, event: LoopEventLLMOKNoToolCall, wantTR: TerminalCompleted, wantEnd: true},
		{name: "blocking_limit", setup: func() *LoopState { return &LoopState{} }, event: LoopEventBlockingLimitHit, wantTR: TerminalBlockingLimit, wantEnd: true},
		{name: "image_error", setup: func() *LoopState { return &LoopState{} }, event: LoopEventLLMErrImage, wantTR: TerminalImageError, wantEnd: true},
		{name: "model_error", setup: func() *LoopState { return &LoopState{} }, event: LoopEventLLMErrModel, wantTR: TerminalModelError, wantEnd: true},
		{name: "aborted_streaming", setup: func() *LoopState { return &LoopState{} }, event: LoopEventCtxCanceled, wantTR: TerminalAbortedStreaming, wantEnd: true},
		{name: "prompt_too_long", setup: func() *LoopState { return &LoopState{PTLRetries: MaxPTLRetries} }, event: LoopEventLLMErrPTL, wantTR: TerminalPromptTooLong, wantEnd: true},
		{name: "stop_hook_prevented", setup: func() *LoopState { return &LoopState{} }, event: LoopEventHookActionBlockStop, wantTR: TerminalStopHookPrevented, wantEnd: true},
		{name: "aborted_tools", setup: func() *LoopState { return &LoopState{} }, event: LoopEventToolErr, wantTR: TerminalAbortedTools, wantEnd: true},
		{name: "hook_stopped", setup: func() *LoopState { return &LoopState{} }, event: LoopEventHookActionStop, wantTR: TerminalHookStopped, wantEnd: true},
		{name: "max_turns", setup: func() *LoopState { return &LoopState{} }, event: LoopEventMaxStepsReached, wantTR: TerminalMaxTurns, wantEnd: true},
		{name: "error_max_budget", setup: func() *LoopState { return &LoopState{MaxOutputRetries: MaxOutputRetriesLimit} }, event: LoopEventLLMErrMaxOutput, wantTR: TerminalErrorMaxBudget, wantEnd: true},
		{name: "error_max_retries", setup: func() *LoopState { return &LoopState{} }, event: LoopEventMaxRetriesReached, wantTR: TerminalErrorMaxRetries, wantEnd: true},
		// 7 continue reasons
		{name: "next_turn", setup: func() *LoopState { return &LoopState{} }, event: LoopEventLLMOKWithToolCall, wantCR: ContinueNextTurn, wantEnd: false},
		{name: "collapse_drain_retry", setup: func() *LoopState { return &LoopState{} }, event: LoopEventCollapseDrainRetry, wantCR: ContinueCollapseDrainRetry, wantEnd: false},
		{name: "reactive_compact_retry", setup: func() *LoopState { return &LoopState{PTLRetries: 1} }, event: LoopEventLLMErrPTL, wantCR: ContinueReactiveCompactRetry, wantEnd: false},
		{name: "max_output_escalate", setup: func() *LoopState { return &LoopState{} }, event: LoopEventMaxOutputEscalate, wantCR: ContinueMaxOutputEscalate, wantEnd: false},
		{name: "max_output_recovery", setup: func() *LoopState { return &LoopState{MaxOutputRetries: 1} }, event: LoopEventLLMErrMaxOutput, wantCR: ContinueMaxOutputRecovery, wantEnd: false},
		{name: "stop_hook_blocking", setup: func() *LoopState { return &LoopState{} }, event: LoopEventHookActionBlocking, wantCR: ContinueStopHookBlocking, wantEnd: false},
		{name: "token_budget_continue", setup: func() *LoopState { return &LoopState{} }, event: LoopEventTokenBudgetContinue, wantCR: ContinueTokenBudgetContinue, wantEnd: false},
	}

	seenTerminal := map[TerminalReason]bool{}
	seenContinue := map[ContinueReason]bool{}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.setup()
			tr, cr, isTerminal := s.Transition(tc.event)
			if isTerminal != tc.wantEnd {
				t.Errorf("isTerminal=%v want=%v", isTerminal, tc.wantEnd)
			}
			if tc.wantTR != "" {
				if tr != tc.wantTR {
					t.Errorf("tr=%q want=%q", tr, tc.wantTR)
				}
				seenTerminal[tr] = true
			}
			if tc.wantCR != "" {
				if cr != tc.wantCR {
					t.Errorf("cr=%q want=%q", cr, tc.wantCR)
				}
				seenContinue[cr] = true
			}
		})
	}

	// Verify all 12 terminal reasons reached
	allTerminal := []TerminalReason{
		TerminalCompleted, TerminalBlockingLimit, TerminalImageError, TerminalModelError,
		TerminalAbortedStreaming, TerminalPromptTooLong, TerminalStopHookPrevented, TerminalAbortedTools,
		TerminalHookStopped, TerminalMaxTurns, TerminalErrorMaxBudget, TerminalErrorMaxRetries,
	}
	for _, r := range allTerminal {
		if !seenTerminal[r] {
			t.Errorf("TerminalReason %q not reached in test cases", r)
		}
	}

	// Verify all 7 continue reasons reached
	allContinue := []ContinueReason{
		ContinueNextTurn, ContinueCollapseDrainRetry, ContinueReactiveCompactRetry,
		ContinueMaxOutputEscalate, ContinueMaxOutputRecovery, ContinueStopHookBlocking, ContinueTokenBudgetContinue,
	}
	for _, r := range allContinue {
		if !seenContinue[r] {
			t.Errorf("ContinueReason %q not reached in test cases", r)
		}
	}
}
