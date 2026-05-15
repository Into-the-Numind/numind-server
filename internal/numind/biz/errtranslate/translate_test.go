package errtranslate

import (
	"errors"
	"fmt"
	"testing"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/errno"
)

// TestToErrno_WrappedCreditSentinel verifies the primary leak path: SOP biz
// wraps credit.ErrInsufficientCredits via fmt.Errorf("node execution failed:
// %w", err) and the controller boundary should still map it back to
// errno.ErrInsufficientCredits (HTTP 402).
func TestToErrno_WrappedCreditSentinel(t *testing.T) {
	wrapped := fmt.Errorf("node execution failed: executeViaGateway: ChatStream: %w", credit.ErrInsufficientCredits)
	e, ok := ToErrno(wrapped)
	if !ok {
		t.Fatalf("expected ok=true for wrapped credit.ErrInsufficientCredits, got false (err=%v)", wrapped)
	}
	if e.Code != errno.ErrInsufficientCredits.Code {
		t.Fatalf("expected code %q, got %q", errno.ErrInsufficientCredits.Code, e.Code)
	}
	if e.HTTP != 402 {
		t.Fatalf("expected HTTP 402, got %d", e.HTTP)
	}
}

// TestToErrno_WrappedErrno verifies the secondary path where biz returns a
// wrapped *errno.Errno (e.g. salesrag.wrapCreditError → errno.ErrInsufficientCredits)
// that gets further wrapped by an outer fmt.Errorf.
func TestToErrno_WrappedErrno(t *testing.T) {
	wrapped := fmt.Errorf("ChatStream: %w", errno.ErrSubscriptionExpired)
	e, ok := ToErrno(wrapped)
	if !ok {
		t.Fatalf("expected ok=true for wrapped errno.ErrSubscriptionExpired, got false")
	}
	if e.Code != errno.ErrSubscriptionExpired.Code {
		t.Fatalf("expected code %q, got %q", errno.ErrSubscriptionExpired.Code, e.Code)
	}
}

func TestToErrno_DirectErrno(t *testing.T) {
	e, ok := ToErrno(errno.ErrInsufficientCredits)
	if !ok {
		t.Fatalf("expected ok=true for direct errno, got false")
	}
	if e != errno.ErrInsufficientCredits {
		t.Fatalf("expected same pointer, got %p vs %p", e, errno.ErrInsufficientCredits)
	}
}

func TestToErrno_Nil(t *testing.T) {
	if _, ok := ToErrno(nil); ok {
		t.Fatalf("expected ok=false for nil, got true")
	}
}

func TestToErrno_UnknownError(t *testing.T) {
	err := errors.New("random db error: connection refused")
	if _, ok := ToErrno(err); ok {
		t.Fatalf("expected ok=false for unknown error, got true")
	}
}

// TestToErrno_DirectCreditSentinel guards against future refactors that
// might inadvertently break the sentinel path.
func TestToErrno_DirectCreditSentinel(t *testing.T) {
	e, ok := ToErrno(credit.ErrInsufficientCredits)
	if !ok {
		t.Fatalf("expected ok=true for direct credit sentinel, got false")
	}
	if e != errno.ErrInsufficientCredits {
		t.Fatalf("expected errno.ErrInsufficientCredits pointer, got %+v", e)
	}
}

// TestToErrno_DoubleWrappedSentinel guards against errors.Is failing to
// unwrap multi-level fmt.Errorf chains. Real prod stacks look like
// "biz.Run: gateway.exec: chat.stream: credit: insufficient balance".
func TestToErrno_DoubleWrappedSentinel(t *testing.T) {
	inner := fmt.Errorf("ChatStream: %w", credit.ErrInsufficientCredits)
	outer := fmt.Errorf("node execution failed: executeViaGateway: %w", inner)
	e, ok := ToErrno(outer)
	if !ok {
		t.Fatalf("expected ok=true for double-wrapped credit sentinel, got false (err=%v)", outer)
	}
	if e.Code != errno.ErrInsufficientCredits.Code {
		t.Fatalf("expected code %q, got %q", errno.ErrInsufficientCredits.Code, e.Code)
	}
}

// TestToErrno_SetMessageThenWrap verifies that errno.ErrXxx.SetMessage("...")
// returns a NEW *Errno copy (not the global) and that errors.As still picks
// it out of a wrap chain — relied on by biz.salesrag.wrapCreditError.
func TestToErrno_SetMessageThenWrap(t *testing.T) {
	withReason := errno.ErrInsufficientCredits.SetMessage("legacy_tier 当月次数用尽")
	wrapped := fmt.Errorf("acquireSalesragCredits: %w", withReason)
	e, ok := ToErrno(wrapped)
	if !ok {
		t.Fatalf("expected ok=true for wrapped SetMessage copy, got false")
	}
	if e.Code != errno.ErrInsufficientCredits.Code {
		t.Fatalf("expected code %q, got %q", errno.ErrInsufficientCredits.Code, e.Code)
	}
	// Message should come from the wrapped copy (caller's customization),
	// not the global default. This is critical for legacy_tier users whose
	// reason ("当月次数用尽") is more specific than the global "积分不足".
	if e.Message != "legacy_tier 当月次数用尽" {
		t.Fatalf("expected wrapped message preserved, got %q", e.Message)
	}
}
