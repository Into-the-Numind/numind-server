package validators

import (
	"context"

	"numind-server/internal/numind/biz/permission"
)

type SandboxOverride struct{}

func NewSandboxOverride() permission.Validator { return &SandboxOverride{} }
func (v *SandboxOverride) ID() string          { return "SandboxOverride" }
func (v *SandboxOverride) Validate(_ context.Context, _ permission.PermissionRequest) permission.PermissionResult {
	return permission.Passthrough(v.ID(), permission.DecisionReasonSandboxOverride, "v1 stub")
}
