package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/errno"
)

// engineerTokens are fragments that must NEVER appear in a user-facing message.
var engineerTokens = []string{
	"dmxapi", "doPost", "chat/completions", "HTTP 403", "HTTP 402",
	"AllocationQuota", "FreeTierOnly", "net/http", "NodeRunError",
	"node path", "ResolveTask", "gateway.", "http://", "https://",
}

// TestEmitStreamErrorEvents_UserFacingMessage reproduces the User-reported bug
// (dev 2026-06-08): agent-mode errors surfaced raw Go/provider strings (e.g.
// 'dmxapi.Chat: doPost /chat/completions: HTTP 403 ... AllocationQuota.FreeTierOnly')
// directly to end users. The SSE EventError must carry a friendly Chinese message,
// not err.Error(). Pre-fix this FAILS (Message is the raw error).
func TestEmitStreamErrorEvents_UserFacingMessage(t *testing.T) {
	ch := make(chan stream.Event, 8)
	rawErr := errors.New(`dmxapi.Chat: doPost /chat/completions: HTTP 403: {"error":{"message":"The free tier of the model has been exhausted","code":"AllocationQuota.FreeTierOnly"}}`)

	emitStreamErrorEvents(ch, 1, rawErr, TerminalModelError, time.Now())

	var errMsg string
	for {
		select {
		case ev := <-ch:
			if ev.Type == stream.EventError {
				var p stream.ErrorPayload
				if err := json.Unmarshal(ev.Data, &p); err != nil {
					t.Fatalf("decode ErrorPayload: %v", err)
				}
				errMsg = p.Message
			}
			continue
		default:
		}
		break
	}

	if errMsg == "" {
		t.Fatal("expected an EventError with a non-empty message")
	}
	for _, tok := range engineerTokens {
		if strings.Contains(errMsg, tok) {
			t.Errorf("user-facing error message leaks engineer token %q: %s", tok, errMsg)
		}
	}
}

// TestUserFacingErrorMessage_Classification covers the translation branches and
// guarantees no engineer text ever leaks.
func TestUserFacingErrorMessage_Classification(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantSub string // substring the friendly message should contain
	}{
		{"errno curated", errno.ErrAIProviderTimeout, "超时"},
		{"insufficient credits errno", errno.ErrInsufficientCredits, "积分"},
		// The REAL production shape: the adapter injects the raw provider string
		// into the errno Message via SetMessage. Code-based mapping must still yield
		// friendly text (regression guard for the errors.As leak).
		{"errno SetMessage raw", errno.ErrAIProviderTimeout.SetMessage(
			`doPost /chat/completions: Post "https://www.dmxapi.cn/v1/chat/completions": net/http: timeout awaiting response headers`), "超时"},
		{"free tier 403", errors.New(`ali.Chat: HTTP 403 AllocationQuota.FreeTierOnly`), "额度"},
		{"raw timeout", errors.New("dmxapi.Chat: net/http: timeout awaiting response headers"), "超时"},
		{"task profile", errors.New("[NodeRunError] gateway.ResolveTask: Task Profile 不存在 node path: [chat]"), "联系老师"},
		{"prompt too long", errors.New("context_length_exceeded: too many tokens"), "太长"},
		{"unknown", errors.New("some weird internal panic xyz"), "稍后再试"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := UserFacingErrorMessage(c.err)
			if !strings.Contains(got, c.wantSub) {
				t.Errorf("UserFacingErrorMessage = %q, want substring %q", got, c.wantSub)
			}
			for _, tok := range engineerTokens {
				if strings.Contains(got, tok) {
					t.Errorf("leaks engineer token %q: %s", tok, got)
				}
			}
		})
	}
	if UserFacingErrorMessage(nil) != "" {
		t.Error("nil error should yield empty string")
	}
}

// TestUserFacingTerminalMessage_AllReasons ensures every error TerminalReason maps
// to non-empty Chinese, and success/waiting reasons map to empty.
func TestUserFacingTerminalMessage_AllReasons(t *testing.T) {
	empty := map[TerminalReason]bool{TerminalCompleted: true, TerminalWaitingForUserChoice: true}
	allReasons := []TerminalReason{
		TerminalCompleted, TerminalBlockingLimit, TerminalImageError, TerminalModelError,
		TerminalAbortedStreaming, TerminalPromptTooLong, TerminalStopHookPrevented, TerminalAbortedTools,
		TerminalHookStopped, TerminalMaxTurns, TerminalErrorMaxBudget, TerminalErrorMaxRetries,
		TerminalPermissionDenied, TerminalWaitingForUserChoice,
	}
	for _, r := range allReasons {
		got := UserFacingTerminalMessage(r)
		if empty[r] {
			if got != "" {
				t.Errorf("%s should map to empty user message, got %q", r, got)
			}
			continue
		}
		if got == "" {
			t.Errorf("error reason %s must map to a non-empty user message", r)
		}
		if string(r) == got {
			t.Errorf("reason %s leaked its machine code as the user message", r)
		}
	}
}
