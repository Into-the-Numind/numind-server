package agent

import (
	"errors"
	"fmt"
	"testing"

	aierr "numind-server/internal/pkg/aiservice/aierr"
)

func TestHandleLLMError_PTL(t *testing.T) {
	err := errors.New("context_length_exceeded: 12000 > 8192")
	if got := HandleLLMError(&LoopState{}, err); got != LoopEventLLMErrPTL {
		t.Errorf("got %v, want LoopEventLLMErrPTL", got)
	}
}

func TestHandleLLMError_MaxOutput(t *testing.T) {
	err := errors.New("max_tokens reached")
	if got := HandleLLMError(&LoopState{}, err); got != LoopEventLLMErrMaxOutput {
		t.Errorf("got %v, want LoopEventLLMErrMaxOutput", got)
	}
}

func TestHandleLLMError_Image(t *testing.T) {
	err := errors.New("image_decode failed: corrupt PNG")
	if got := HandleLLMError(&LoopState{}, err); got != LoopEventLLMErrImage {
		t.Errorf("got %v, want LoopEventLLMErrImage", got)
	}
}

func TestHandleLLMError_Generic(t *testing.T) {
	err := errors.New("model returned 500")
	if got := HandleLLMError(&LoopState{}, err); got != LoopEventLLMErrModel {
		t.Errorf("got %v, want LoopEventLLMErrModel", got)
	}
}

func TestHandleLLMError_Nil(t *testing.T) {
	if got := HandleLLMError(&LoopState{}, nil); got != LoopEventInvalid {
		t.Errorf("got %v, want LoopEventInvalid for nil err", got)
	}
}

// PTL 优先级测试：err 同时含 PTL 和 max_output 关键字 → 应优先 PTL
func TestHandleLLMError_PriorityPTLOverMaxOutput(t *testing.T) {
	err := errors.New("prompt_too_long: also max_tokens exceeded")
	if got := HandleLLMError(&LoopState{}, err); got != LoopEventLLMErrPTL {
		t.Errorf("PTL must win, got %v", got)
	}
}

// PTL chain transitions：state.Transition 配合
func TestPTLChain_Step1_CollapseDrain(t *testing.T) {
	s := &LoopState{}
	_, c, isTerm := s.Transition(LoopEventLLMErrPTL)
	if isTerm {
		t.Fatal("PTLRetries=1 should not be terminal")
	}
	if c != ContinueCollapseDrainRetry {
		t.Errorf("got %v, want CollapseDrainRetry", c)
	}
	if s.PTLRetries != 1 {
		t.Errorf("PTLRetries=%d, want 1", s.PTLRetries)
	}
}

func TestPTLChain_Step2_ReactiveCompact(t *testing.T) {
	s := &LoopState{PTLRetries: 1}
	_, c, isTerm := s.Transition(LoopEventLLMErrPTL)
	if isTerm {
		t.Fatal("PTLRetries=2 should not be terminal")
	}
	if c != ContinueReactiveCompactRetry {
		t.Errorf("got %v, want ReactiveCompactRetry", c)
	}
}

func TestPTLChain_TerminalAfterMaxRetries(t *testing.T) {
	s := &LoopState{PTLRetries: 2}
	term, _, isTerm := s.Transition(LoopEventLLMErrPTL)
	if !isTerm {
		t.Fatal("PTLRetries=3 should be terminal")
	}
	if term != TerminalPromptTooLong {
		t.Errorf("got %v, want TerminalPromptTooLong", term)
	}
}

func TestMaxOutputChain_Step1_Escalate(t *testing.T) {
	s := &LoopState{}
	_, c, _ := s.Transition(LoopEventLLMErrMaxOutput)
	if c != ContinueMaxOutputEscalate {
		t.Errorf("got %v, want MaxOutputEscalate", c)
	}
}

func TestMaxOutputChain_Step2_Recovery(t *testing.T) {
	s := &LoopState{MaxOutputRetries: 1}
	_, c, _ := s.Transition(LoopEventLLMErrMaxOutput)
	if c != ContinueMaxOutputRecovery {
		t.Errorf("got %v, want MaxOutputRecovery", c)
	}
}

func TestMaxOutputChain_TerminalAfterMaxRetries(t *testing.T) {
	s := &LoopState{MaxOutputRetries: 2}
	term, _, isTerm := s.Transition(LoopEventLLMErrMaxOutput)
	if !isTerm {
		t.Fatal("should be terminal")
	}
	if term != TerminalErrorMaxBudget {
		t.Errorf("got %v, want TerminalErrorMaxBudget", term)
	}
}

// Mutual exclusion: PTL retries 不影响 MaxOutput retries
func TestChains_Independent(t *testing.T) {
	s := &LoopState{PTLRetries: 1, MaxOutputRetries: 0}
	s.Transition(LoopEventLLMErrMaxOutput)
	if s.PTLRetries != 1 {
		t.Errorf("MaxOutput transition must not affect PTLRetries; got %d", s.PTLRetries)
	}
	if s.MaxOutputRetries != 1 {
		t.Errorf("MaxOutputRetries should be 1; got %d", s.MaxOutputRetries)
	}
}

// 结构化路径：provider adapter 附加的 aierr.ProviderError 直接被识别为 PTL，
// 不依赖 message substring（message 这里故意不含任何关键字）。
func TestHandleLLMError_StructuredPTL(t *testing.T) {
	err := aierr.New(0, "context_length_exceeded", "", "", nil)
	if got := HandleLLMError(&LoopState{}, err); got != LoopEventLLMErrPTL {
		t.Errorf("structured context_length_exceeded must classify as PTL; got %v", got)
	}
}

// 结构化路径经 fmt.Errorf("%w") 包裹后，errors.As 仍能穿透找到 ProviderError。
func TestHandleLLMError_StructuredPTL_Wrapped(t *testing.T) {
	inner := aierr.New(0, "context_length_exceeded", "", "", nil)
	err := fmt.Errorf("dmxapi.Chat: %w", inner)
	if got := HandleLLMError(&LoopState{}, err); got != LoopEventLLMErrPTL {
		t.Errorf("wrapped structured PTL must traverse via errors.As; got %v", got)
	}
}

// 结构化路径：max_output / image 语义码也走结构化分类。
func TestHandleLLMError_StructuredMaxOutput(t *testing.T) {
	err := aierr.New(0, "", "", "max_output reached", nil) // classifies to CodeMaxOutputTokens
	if got := HandleLLMError(&LoopState{}, err); got != LoopEventLLMErrMaxOutput {
		t.Errorf("structured max_output must classify as MaxOutput; got %v", got)
	}
}

func TestHandleLLMError_StructuredImage(t *testing.T) {
	err := aierr.New(0, "", "", "image_decode failed", nil) // classifies to CodeImageError
	if got := HandleLLMError(&LoopState{}, err); got != LoopEventLLMErrImage {
		t.Errorf("structured image error must classify as Image; got %v", got)
	}
}
