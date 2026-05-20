package middleware

import (
	"context"
	"testing"
)

// TestNewContextWithUserID_GetSet verifies round-trip for userID context key.
func TestNewContextWithUserID_GetSet(t *testing.T) {
	ctx := NewContextWithUserID(context.Background(), 42)
	uid, ok := UserIDFromCtx(ctx)
	if !ok {
		t.Fatal("UserIDFromCtx: expected ok=true, got false")
	}
	if uid != 42 {
		t.Errorf("UserIDFromCtx: expected 42, got %d", uid)
	}
}

// TestUserIDFromCtx_Missing verifies missing userID returns 0, false.
func TestUserIDFromCtx_Missing(t *testing.T) {
	uid, ok := UserIDFromCtx(context.Background())
	if ok {
		t.Error("UserIDFromCtx: expected ok=false on empty context")
	}
	if uid != 0 {
		t.Errorf("UserIDFromCtx: expected 0, got %d", uid)
	}
}

// TestNewContextWithAgentDefinitionID_GetSet verifies round-trip for agentDefinitionID context key.
// M10 plan P2-5: context_keys round-trip test.
func TestNewContextWithAgentDefinitionID_GetSet(t *testing.T) {
	ctx := NewContextWithAgentDefinitionID(context.Background(), 100)
	id, ok := AgentDefinitionIDFromCtx(ctx)
	if !ok {
		t.Fatal("AgentDefinitionIDFromCtx: expected ok=true, got false")
	}
	if id != 100 {
		t.Errorf("AgentDefinitionIDFromCtx: expected 100, got %d", id)
	}
}

// TestAgentDefinitionIDFromCtx_Missing verifies missing agentDefID returns 0, false.
func TestAgentDefinitionIDFromCtx_Missing(t *testing.T) {
	id, ok := AgentDefinitionIDFromCtx(context.Background())
	if ok {
		t.Error("AgentDefinitionIDFromCtx: expected ok=false on empty context")
	}
	if id != 0 {
		t.Errorf("AgentDefinitionIDFromCtx: expected 0, got %d", id)
	}
}

// TestContextKeys_NoCollision verifies that userID and agentDefinitionID keys do not collide.
func TestContextKeys_NoCollision(t *testing.T) {
	ctx := NewContextWithUserID(context.Background(), 7)
	ctx = NewContextWithAgentDefinitionID(ctx, 99)

	uid, ok := UserIDFromCtx(ctx)
	if !ok || uid != 7 {
		t.Errorf("userID after agentDefID injection: ok=%v uid=%d", ok, uid)
	}
	id, ok := AgentDefinitionIDFromCtx(ctx)
	if !ok || id != 99 {
		t.Errorf("agentDefID: ok=%v id=%d", ok, id)
	}
}
