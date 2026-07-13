package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"numind-server/internal/pkg/middleware"
)

func TestToolCallID_ContextRoundTripAndIsolation(t *testing.T) {
	parent := middleware.NewContextWithUserID(WithRunID(context.Background(), 73), 19)
	child := WithToolCallID(parent, "call-123")

	assert.Empty(t, ToolCallIDFromContext(parent), "adding a tool call ID must not mutate the parent context")
	assert.Equal(t, "call-123", ToolCallIDFromContext(child))
	assert.Equal(t, uint64(73), RunIDFromContext(child), "tool-call context must preserve run identity")
	userID, ok := middleware.UserIDFromCtx(child)
	assert.True(t, ok)
	assert.Equal(t, uint(19), userID, "tool-call context must preserve user identity")

	sibling := WithToolCallID(parent, "call-456")
	assert.Equal(t, "call-123", ToolCallIDFromContext(child))
	assert.Equal(t, "call-456", ToolCallIDFromContext(sibling), "sibling calls must stay isolated")
}

//nolint:staticcheck // Nil is intentional: the helper's public contract is nil-safe.
func TestToolCallID_AbsentOrNilContextReturnsEmpty(t *testing.T) {
	assert.Empty(t, ToolCallIDFromContext(context.Background()))
	assert.Empty(t, ToolCallIDFromContext(nil))

	ctx := WithToolCallID(nil, "call-from-nil")
	assert.Equal(t, "call-from-nil", ToolCallIDFromContext(ctx))
}
