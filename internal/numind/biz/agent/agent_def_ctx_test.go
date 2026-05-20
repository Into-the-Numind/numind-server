package agent

import (
	"context"
	"testing"
)

func TestAgentDefCtx_RoundTrip(t *testing.T) {
	ctx := WithAgentDefCtx(context.Background(), 42, 7)
	defID, parentID := AgentDefAndParentFromCtx(ctx)
	if defID != 42 || parentID != 7 {
		t.Errorf("RoundTrip got (%d, %d), want (42, 7)", defID, parentID)
	}
}

func TestAgentDefCtx_EmptyReturnsZero(t *testing.T) {
	defID, parentID := AgentDefAndParentFromCtx(context.Background())
	if defID != 0 || parentID != 0 {
		t.Errorf("empty ctx got (%d, %d), want (0, 0)", defID, parentID)
	}
}
