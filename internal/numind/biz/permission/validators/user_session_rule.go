package validators

import (
	"context"

	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/permission"
)

type UserSessionRule struct{}

func NewUserSessionRule() permission.Validator { return &UserSessionRule{} }
func (v *UserSessionRule) ID() string          { return "UserSessionRule" }

// v1: 仅查 IsDestructive=true 即 deny（沙箱隔离执行类豁免）；session auth 查询推迟 #11/#14
//
// Sandbox exemption: bash_exec/run_python execute inside a dedicated sandbox
// container (exclusively borrowed per call, destroyed on return — never shared)
// and cannot modify the user's data, which is the threat this rule guards
// ("该操作可能修改你的数据"). Product decision 2026-05-31
// (open-tools-skill-as-guidance): these ship un-gated in v1; sandbox +
// bashvalidator are the security boundary. Without the exemption every
// doc-generation run (docx/xlsx/pptx via run_python) dies at the permission
// gate — found by launch-sprint probe run #115 after
// remove-permission-backdoor armed the real pipeline. Name set lives in
// agent.IsSandboxIsolatedExecTool (single source of truth with the sandbox
// hook routing). Exemption stays Passthrough (not Allow) deliberately: later
// validators (AutoModeLLMValidator, fail-allow) still observe the call.
func (v *UserSessionRule) Validate(_ context.Context, req permission.PermissionRequest) permission.PermissionResult {
	if req.Tool == nil || !req.Tool.IsDestructive() {
		return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "not destructive")
	}
	if agent.IsSandboxIsolatedExecTool(req.Tool.Name()) {
		return permission.Passthrough(v.ID(), permission.DecisionReasonSandboxOverride,
			"sandbox-isolated exec tool (dedicated container per call, cannot touch user data)")
	}
	return permission.Deny(v.ID(), permission.DecisionReasonMode,
		"该操作可能修改你的数据，需要管理员授权后才能执行")
}
