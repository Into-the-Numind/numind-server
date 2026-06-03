package agent

import (
	"context"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// agentTenantAccess reports whether callerID may use the given agent definition
// under the B2B2C tenant model.
//
// STUB (RED phase, b2b2c-student-agent-access Task 1): this temporary body only
// reproduces the OLD parent-only behavior so the matrix test in
// tenant_access_test.go fails on the child-of-owner case (the bug). The real
// implementation replaces this in the GREEN commit.
func agentTenantAccess(ctx context.Context, users userByIDGetter, callerID uint, ad *model.AgentDefinition) error {
	if ad.ParentUserID == callerID {
		return nil
	}
	return errno.ErrSkillNotFound
}
