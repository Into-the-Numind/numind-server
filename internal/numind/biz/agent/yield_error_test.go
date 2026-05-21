package agent

import (
	"errors"
	"testing"
)

func TestYieldError_Is_SentinelMatch(t *testing.T) {
	ye := &yieldError{
		Payload: YieldPayload{
			Question: "Which option?",
			Options: []YieldOption{
				{Key: "a", Label: "Option A"},
				{Key: "b", Label: "Option B"},
			},
			MultiSelect: false,
		},
	}
	if !errors.Is(ye, ErrYieldForUserQuestion) {
		t.Errorf("errors.Is(yieldError, ErrYieldForUserQuestion) = false, want true")
	}
}

func TestYieldError_Is_DoesNotMatchOtherErrors(t *testing.T) {
	ye := &yieldError{Payload: YieldPayload{Question: "q?"}}
	other := errors.New("some other error")
	if errors.Is(ye, other) {
		t.Errorf("errors.Is(yieldError, otherErr) = true, want false")
	}
}

func TestYieldError_As_UnwrapsPayload(t *testing.T) {
	wantPayload := YieldPayload{
		Question: "Pick one",
		Options: []YieldOption{
			{Key: "x", Label: "X option", Description: "desc x"},
		},
		Header:      "Test header",
		MultiSelect: true,
	}
	original := &yieldError{Payload: wantPayload}

	var target *yieldError
	if !errors.As(original, &target) {
		t.Fatalf("errors.As(yieldError, &target) = false, want true")
	}
	if target.Payload.Question != wantPayload.Question {
		t.Errorf("Payload.Question = %q, want %q", target.Payload.Question, wantPayload.Question)
	}
	if target.Payload.Header != wantPayload.Header {
		t.Errorf("Payload.Header = %q, want %q", target.Payload.Header, wantPayload.Header)
	}
	if target.Payload.MultiSelect != wantPayload.MultiSelect {
		t.Errorf("Payload.MultiSelect = %v, want %v", target.Payload.MultiSelect, wantPayload.MultiSelect)
	}
	if len(target.Payload.Options) != 1 || target.Payload.Options[0].Key != "x" {
		t.Errorf("Payload.Options = %+v, want [{Key:x ...}]", target.Payload.Options)
	}
}

func TestYieldError_Unwrap_ReturnsSentinel(t *testing.T) {
	ye := &yieldError{Payload: YieldPayload{Question: "q?"}}
	unwrapped := ye.Unwrap()
	if unwrapped != ErrYieldForUserQuestion {
		t.Errorf("Unwrap() = %v, want ErrYieldForUserQuestion", unwrapped)
	}
}

func TestYieldError_Error_String(t *testing.T) {
	ye := &yieldError{}
	if ye.Error() != ErrYieldForUserQuestion.Error() {
		t.Errorf("Error() = %q, want %q", ye.Error(), ErrYieldForUserQuestion.Error())
	}
}
