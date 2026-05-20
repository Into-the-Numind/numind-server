package agent

import "context"

type agentDefCtxKey struct{}

type agentDefCtxValue struct {
	AgentDefinitionID uint64
	ParentUserID      uint
}

// WithAgentDefCtx 注入 agent_definition_id + parent_user_id 到 ctx。
// runner.Run 在 skill lookup 成功后调；permission TenantAdminRuleValidator / ToolFlagValidator 用 AgentDefAndParentFromCtx 取出。
func WithAgentDefCtx(ctx context.Context, agentDefID uint64, parentUserID uint) context.Context {
	return context.WithValue(ctx, agentDefCtxKey{}, &agentDefCtxValue{
		AgentDefinitionID: agentDefID,
		ParentUserID:      parentUserID,
	})
}

// AgentDefAndParentFromCtx 取 agent_definition_id + parent_user_id；未注入时返回 (0, 0)。
func AgentDefAndParentFromCtx(ctx context.Context) (uint64, uint) {
	v, _ := ctx.Value(agentDefCtxKey{}).(*agentDefCtxValue)
	if v == nil {
		return 0, 0
	}
	return v.AgentDefinitionID, v.ParentUserID
}
