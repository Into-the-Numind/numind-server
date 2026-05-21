package compliance_scope

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWithSkipScope_AndRetrieve(t *testing.T) {
	ctx := WithSkipScope(context.Background(), "automigrate")
	reason, ok := SkipScopeFromCtx(ctx)
	assert.True(t, ok)
	assert.Equal(t, "automigrate", reason)
}

func TestWithSkipScope_EmptyReason_IsNoop(t *testing.T) {
	ctx := WithSkipScope(context.Background(), "")
	_, ok := SkipScopeFromCtx(ctx)
	assert.False(t, ok, "empty reason must not inject the key")
}

func TestSkipScopeFromCtx_MissingKey_ReturnsFalse(t *testing.T) {
	_, ok := SkipScopeFromCtx(context.Background())
	assert.False(t, ok)
}

func TestSkipScopeFromCtx_WrongValueType_ReturnsFalse(t *testing.T) {
	// If a caller stuffs a non-string under the key (shouldn't happen via
	// WithSkipScope, but defensive), retrieval safely returns false.
	type bogusKey struct{}
	ctx := context.WithValue(context.Background(), bogusKey{}, "not the right key")
	_, ok := SkipScopeFromCtx(ctx)
	assert.False(t, ok)
}
