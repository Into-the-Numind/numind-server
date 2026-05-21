package memory

import (
	"context"

	"numind-server/internal/pkg/aiservice"
)

// chatFn is a seam for testing SyncTurn without a real aiservice.Gateway.
// Tests override this variable to inject a mock implementation.
var chatFn = func(ctx context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
	return aiservice.Chat(ctx, taskID, req)
}
