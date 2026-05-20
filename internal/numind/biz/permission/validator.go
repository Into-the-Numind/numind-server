package permission

import "context"

// Validator — pipeline 中单个权限校验器
type Validator interface {
	ID() string
	Validate(ctx context.Context, req PermissionRequest) PermissionResult
}

// Passthrough — validator 弃权（pipeline 传给下一个）
func Passthrough(validatorID string, reason DecisionReasonType, why string) PermissionResult {
	return PermissionResult{
		Behavior:       BehaviorPassthrough,
		DecisionReason: reason,
		ValidatorID:    validatorID,
		Message:        why,
	}
}

// Allow — 直接放行
func Allow(validatorID string, reason DecisionReasonType, why string) PermissionResult {
	return PermissionResult{
		Behavior:       BehaviorAllow,
		DecisionReason: reason,
		ValidatorID:    validatorID,
		Message:        why,
	}
}

// Deny — 拒绝 + 友好理由
func Deny(validatorID string, reason DecisionReasonType, message string) PermissionResult {
	return PermissionResult{
		Behavior:       BehaviorDeny,
		DecisionReason: reason,
		ValidatorID:    validatorID,
		Message:        message,
	}
}

// Ask — 让学员确认（v1 直接 deny；UI 在 #11）
func Ask(validatorID string, reason DecisionReasonType, message string) PermissionResult {
	return PermissionResult{
		Behavior:       BehaviorAsk,
		DecisionReason: reason,
		ValidatorID:    validatorID,
		Message:        message,
	}
}
