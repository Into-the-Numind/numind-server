// Package errtranslate maps biz-layer sentinel errors and wrapped *errno.Errno
// values to a single matching *errno.Errno at the controller boundary.
//
// Why: without translation, wrapped Go errors such as
//
//	"node execution failed: executeViaGateway: ChatStream: ContextBudgetCredits: credit: insufficient balance: requested 50, available 0"
//
// leak to the UI. errno.Decode (internal/pkg/errno/errno.go) uses a bare type
// assertion (`err.(*Errno)`) rather than errors.As, so wrapped errnos miss the
// fast path and fall through to the default branch which exposes err.Error().
// SSE frames are worse: chatbot/salesrag/sop controllers all hard-code
// json.Marshal(err.Error()) into the data field.
//
// This package centralizes the sentinel→errno mapping so each controller's
// error path becomes a single ToErrno or FriendlyForSSE call.
//
// Add new mappings here as new biz sentinels are introduced.
package errtranslate

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// genericServerErrorMessage is returned to the UI when err does not map to a
// known errno. The original err is logged at Error level so on-call can grep
// for it.
const genericServerErrorMessage = "AI 服务暂时不可用，请稍后重试"

// ToErrno reports whether err maps to a specific *errno.Errno.
//
// Resolution order:
//  1. errors.As — unwrap to a *errno.Errno already in the chain (matches
//     errno.ErrSubscriptionExpired and friends even when wrapped by
//     fmt.Errorf("ChatStream: %w", ...))
//  2. errors.Is — match known biz-layer sentinels (currently
//     credit.ErrInsufficientCredits)
//
// Callers that receive (nil, false) should respond with a generic
// user-facing message AND log the original err for diagnosis.
func ToErrno(err error) (*errno.Errno, bool) {
	if err == nil {
		return nil, false
	}

	var typed *errno.Errno
	if errors.As(err, &typed) {
		return typed, true
	}

	switch {
	case errors.Is(err, credit.ErrInsufficientCredits):
		return errno.ErrInsufficientCredits, true
	// Stream idle timeout / overall timeout wrap context.DeadlineExceeded
	// (see biz/sop stream timeouts). Surface a friendly "provider timed out"
	// message instead of the generic fallback.
	case errors.Is(err, context.DeadlineExceeded):
		return errno.ErrAIProviderTimeout, true
	}

	// Provider image-size/dimension rejections are string-only errors (no typed
	// error to errors.Is against), e.g. dmxapi/claude:
	//   "At least one of the image dimensions exceed max allowed size: 8000 pixels"
	// imageutil normalizes most images at upload; this is the last-resort mapping
	// so the user sees "图片过大" instead of the generic fallback (image-normalize-service).
	if msg := strings.ToLower(err.Error()); strings.Contains(msg, "dimensions exceed") ||
		strings.Contains(msg, "exceed max allowed size") ||
		(strings.Contains(msg, "image") && strings.Contains(msg, "too large")) {
		return errno.ErrImageTooLarge, true
	}

	return nil, false
}

// FriendlyForSSE returns a user-facing message string suitable for embedding
// in an SSE error frame. The caller is responsible for the SSE framing
// (`event: error\ndata: %s\n\n` for sop, `{"type":"error","data":"%s"}` for
// chatbot/salesrag — formats are pre-existing and not part of this fix).
//
// Side effects: emits a structured zap log entry on every call.
//   - Mapped (sentinel match) → Warn level, business-expected error.
//   - Unmapped (DB / panic / unknown) → Error level, surfaces in alerting.
//
// endpoint is a short identifier (e.g. "SopExecuteNodeStream") used for log
// grepping. c is the *gin.Context so the log entry inherits request_id /
// user_id from middleware.
func FriendlyForSSE(c *gin.Context, endpoint string, err error) string {
	if err == nil {
		return ""
	}

	if mapped, ok := ToErrno(err); ok {
		log.C(c).Warnw("API error translated for UI",
			"endpoint", endpoint,
			"errno_code", mapped.Code,
			"errno_http", mapped.HTTP,
			"original_error", err.Error(),
		)
		return mapped.Message
	}

	log.C(c).Errorw("API error unmapped (returned generic message to UI)",
		"endpoint", endpoint,
		"original_error", err.Error(),
	)
	return genericServerErrorMessage
}
