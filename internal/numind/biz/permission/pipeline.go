package permission

import "context"

// PermissionPipeline 是 validator 责任链（蓝本 §4.4.3）。
//
// 调用约定：从前往后逐个调 Validate，第一个返回非 passthrough 的结果立即返回；
// 全 passthrough 则默认返回 Allow + DecisionReasonOther + ValidatorID="DefaultAllow"（白名单兜底）。
type PermissionPipeline struct {
	validators []Validator
}

// NewPipeline 构造 pipeline。validators 顺序即评估顺序。
func NewPipeline(validators ...Validator) *PermissionPipeline {
	return &PermissionPipeline{validators: validators}
}

// Check 串行执行 validators，命中即返。
func (p *PermissionPipeline) Check(ctx context.Context, req PermissionRequest) PermissionResult {
	for _, v := range p.validators {
		result := v.Validate(ctx, req)
		if result.Behavior != BehaviorPassthrough {
			return result
		}
	}
	return Allow("DefaultAllow", DecisionReasonOther, "all validators passthrough")
}
