package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"numind-server/internal/numind/biz/agent/stream"
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
