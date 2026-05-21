// Package callctx provides a context key for propagating a per-LLM-call
// identifier from runner.go → aiservice adapter → tool hooks. This enables
// PostToolCall in budgetgate (#14/A8b) to look up the actual token usage
// recorded by the adapter during the most recent aiservice.Chat call.
package callctx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type callCtxKey int

const callIDKey callCtxKey = iota

// NewCallID generates an 8-byte hex random call identifier (16 hex chars).
// Cheap, collision-resistant for the lifetime of a single agent_run.
func NewCallID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// WithCallID injects the given call id into ctx. Runner.Run() calls this
// before each einoAgent.Generate iteration so the adapter can stash Usage.
func WithCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callIDKey, id)
}

// CallIDFromCtx returns the call id from ctx, or "" if absent.
// PostToolCall hooks call this to retrieve the id for adapter.LookupUsage().
func CallIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(callIDKey).(string)
	return v
}
