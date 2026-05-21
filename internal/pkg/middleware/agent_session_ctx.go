package middleware

import "context"

// agentSessionCtxKey is unexported to prevent ctx pollution.
type agentSessionCtxKey int

const sessionIDKey agentSessionCtxKey = iota

// WithAgentSessionID injects a session id into ctx. #14/A3: used by runner.go
// to propagate sessionID through ReAct loop into provider.SyncTurn.
func WithAgentSessionID(ctx context.Context, sid string) context.Context {
	return context.WithValue(ctx, sessionIDKey, sid)
}

// AgentSessionIDFromCtx returns the session id, or "" if absent.
func AgentSessionIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(sessionIDKey).(string)
	return v
}
