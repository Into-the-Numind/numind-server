package callctx

import (
	"context"
	"regexp"
	"testing"
)

// TestNewCallID_Format verifies result is exactly 16 lowercase hex chars.
func TestNewCallID_Format(t *testing.T) {
	id := NewCallID()
	if len(id) != 16 {
		t.Errorf("NewCallID() length: got %d, want 16 (8 bytes hex)", len(id))
	}
	hexPattern := regexp.MustCompile(`^[0-9a-f]{16}$`)
	if !hexPattern.MatchString(id) {
		t.Errorf("NewCallID() = %q, want 16 lowercase hex chars", id)
	}
}

// TestNewCallID_Unique verifies 1000 calls all return distinct values.
func TestNewCallID_Unique(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := NewCallID()
		if _, exists := seen[id]; exists {
			t.Fatalf("NewCallID() collision after %d calls: duplicate id %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

// TestWithCallID_RoundTrip verifies inject + extract returns the same value.
func TestWithCallID_RoundTrip(t *testing.T) {
	want := "abcdef0123456789"
	ctx := WithCallID(context.Background(), want)
	got := CallIDFromCtx(ctx)
	if got != want {
		t.Errorf("CallIDFromCtx after WithCallID: got %q, want %q", got, want)
	}
}

// TestCallIDFromCtx_Absent verifies empty ctx returns "".
func TestCallIDFromCtx_Absent(t *testing.T) {
	got := CallIDFromCtx(context.Background())
	if got != "" {
		t.Errorf("CallIDFromCtx on empty ctx: got %q, want empty string", got)
	}
}
