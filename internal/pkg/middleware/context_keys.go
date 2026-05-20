package middleware

import "context"

// ctxKey 是 context value 的类型安全 key，避免字符串冲突
type ctxKey string

const (
	// CtxKeyUserID 用户 ID 的 context key
	CtxKeyUserID ctxKey = "userID"
	// CtxKeyAgentDefinitionID agent_definition.id 的 context key。
	// #7 memory-system: 注入供 memory_write 工具记录 source_agent_definition_id。
	// 由 runner.go Step 4（AgentDefinitionID>0 时）注入；取出用 AgentDefinitionIDFromCtx。
	CtxKeyAgentDefinitionID ctxKey = "agentDefinitionID"
)

// NewContextWithUserID 将 userID 写入 context
func NewContextWithUserID(ctx context.Context, userID uint) context.Context {
	return context.WithValue(ctx, CtxKeyUserID, userID)
}

// UserIDFromCtx 从 context 中提取 userID，未找到返回 0, false
func UserIDFromCtx(ctx context.Context) (uint, bool) {
	uid, ok := ctx.Value(CtxKeyUserID).(uint)
	return uid, ok
}

// NewContextWithAgentDefinitionID 将 agent_definition.id 写入 context。
// #7 memory-system: 由 runner.go 在 AgentDefinitionID>0 时注入；
// memory_write 工具通过 AgentDefinitionIDFromCtx 读取并写入 source_agent_definition_id。
func NewContextWithAgentDefinitionID(ctx context.Context, id uint64) context.Context {
	return context.WithValue(ctx, CtxKeyAgentDefinitionID, id)
}

// AgentDefinitionIDFromCtx 从 context 中提取 agent_definition_id。
// 未找到或值为零时返回 0, false。
func AgentDefinitionIDFromCtx(ctx context.Context) (uint64, bool) {
	v, ok := ctx.Value(CtxKeyAgentDefinitionID).(uint64)
	return v, ok
}
