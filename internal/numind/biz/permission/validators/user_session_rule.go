package validators

import (
	"context"

	"numind-server/internal/numind/biz/permission"
)

type UserSessionRule struct{}

func NewUserSessionRule() permission.Validator { return &UserSessionRule{} }
func (v *UserSessionRule) ID() string          { return "UserSessionRule" }

// v1: 仅查 IsDestructive=true 即 deny；session auth 查询推迟 #11/#14
func (v *UserSessionRule) Validate(_ context.Context, req permission.PermissionRequest) permission.PermissionResult {
	if req.Tool == nil || !req.Tool.IsDestructive() {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "not destructive")
	}
	return permission.Deny(v.ID(), permission.DecisionReasonMode,
		"该操作可能修改你的数据，需要管理员授权后才能执行")
}
