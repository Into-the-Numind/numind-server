package agent

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// agentTenantAccess reports whether callerID may use the given agent definition
// under the B2B2C tenant model (spec 2026-06-03-b2b2c-student-agent-access §2).
//
// Access is granted iff:
//   - ad.ParentUserID == callerID            (caller is the owning parent), OR
//   - ad.ParentUserID == *caller.ParentUserID (caller is a child/learner of the
//     owning parent) AND ad.IsActive (R9: children may not run a de-listed agent).
//
// The owning parent retains access to their own inactive drafts (试聊); R9 only
// gates children. Every denial returns errno.ErrSkillNotFound (404) so existence
// is never revealed across tenants.
//
// Performance: the parent fast-path returns without any user lookup (covers the
// owner / 试聊 case with zero extra queries); only the child branch reads the
// caller record via an indexed primary-key get (no N+1).
//
// `users` is the narrow userByIDGetter subset of store.UserStore. When nil (unit
// tests that don't wire it), only the parent fast-path is available and every
// other caller is denied — preserving the pre-change behavior for callers that
// never wired a user store.
//
// Precondition: ad != nil (the caller owns the agent_definition store lookup).
func agentTenantAccess(ctx context.Context, users userByIDGetter, callerID uint, ad *model.AgentDefinition) error {
	// Owning parent — always allowed, including their own inactive drafts.
	if ad.ParentUserID == callerID {
		return nil
	}
	if users == nil {
		return errno.ErrSkillNotFound
	}
	caller, err := users.GetByID(ctx, callerID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrSkillNotFound
		}
		return fmt.Errorf("agentTenantAccess: load caller(%d): %w", callerID, err)
	}
	// Child/learner of the owning parent. caller.ParentUserID is *uint and nil
	// for parent/standalone accounts — guard before dereferencing (a parent
	// caller reaching this slow path would otherwise nil-panic in a detached
	// run goroutine).
	if caller.ParentUserID == nil || *caller.ParentUserID != ad.ParentUserID {
		return errno.ErrSkillNotFound
	}
	if !ad.IsActive {
		// R9: a child may not run a de-listed agent.
		return errno.ErrSkillNotFound
	}
	return nil
}
