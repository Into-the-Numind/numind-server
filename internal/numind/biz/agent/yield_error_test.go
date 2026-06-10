package agent

import (
	"errors"
	"testing"
)

func TestYieldError_Is_SentinelMatch(t *testing.T) {
	ye := &yieldError{
		Payload: YieldPayload{Questions: []YieldQuestion{{
			Question: "Which option?",
			Options: []YieldOption{
				{Key: "a", Label: "Option A"},
				{Key: "b", Label: "Option B"},
			},
			MultiSelect: false,
		}}},
	}
	if !errors.Is(ye, ErrYieldForUserQuestion) {
		t.Errorf("errors.Is(yieldError, ErrYieldForUserQuestion) = false, want true")
	}
}

func TestYieldError_Is_DoesNotMatchOtherErrors(t *testing.T) {
	ye := &yieldError{Payload: YieldPayload{Questions: []YieldQuestion{{Question: "q?"}}}}
	other := errors.New("some other error")
	if errors.Is(ye, other) {
		t.Errorf("errors.Is(yieldError, otherErr) = true, want false")
	}
}

func TestYieldError_As_UnwrapsPayload(t *testing.T) {
	wantQ := YieldQuestion{
		Question: "Pick one",
		Options: []YieldOption{
			{Key: "x", Label: "X option", Description: "desc x"},
		},
		Header:      "Test header",
		MultiSelect: true,
	}
	original := &yieldError{Payload: YieldPayload{Questions: []YieldQuestion{wantQ}}}

	var target *yieldError
	if !errors.As(original, &target) {
		t.Fatalf("errors.As(yieldError, &target) = false, want true")
	}
	if len(target.Payload.Questions) != 1 {
		t.Fatalf("Payload.Questions len = %d, want 1", len(target.Payload.Questions))
	}
	got := target.Payload.Questions[0]
	if got.Question != wantQ.Question {
		t.Errorf("Question = %q, want %q", got.Question, wantQ.Question)
	}
	if got.Header != wantQ.Header {
		t.Errorf("Header = %q, want %q", got.Header, wantQ.Header)
	}
	if got.MultiSelect != wantQ.MultiSelect {
		t.Errorf("MultiSelect = %v, want %v", got.MultiSelect, wantQ.MultiSelect)
	}
	if len(got.Options) != 1 || got.Options[0].Key != "x" {
		t.Errorf("Options = %+v, want [{Key:x ...}]", got.Options)
	}
}

func TestYieldError_Unwrap_ReturnsSentinel(t *testing.T) {
	ye := &yieldError{Payload: YieldPayload{Questions: []YieldQuestion{{Question: "q?"}}}}
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
