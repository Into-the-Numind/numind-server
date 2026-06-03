package agent

import (
	"context"
	"fmt"
	"strconv"

	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/billing"
)

// injectAgentBillingCtx wires the billing context for an agent run so that every
// aiservice.Chat call made under this ctx — the ReAct main loop AND tool-internal
// calls (vision describe/annotate, compaction) that inherit it — is billed via
// the ContextBudgetCredits gateway in bill-only mode (no compression, no message
// replacement, so ReAct tool structure survives).
//
// Billing target is the run INITIATOR (req.UserID = agent_run.user_id) — a
// sub-account running a parent's agent is charged its own pool, aligned with
// b2b2c-student-agent-access. IsTest (parent Builder 试聊) routes to the
// admin_test pool (credit_admin_test_grant) instead of the three-pool.
func injectAgentBillingCtx(ctx context.Context, req RunRequest, runID uint64) context.Context {
	ctx = billing.WithBillingMeta(ctx, req.UserID, "agent_run",
		billing.Metadata("run_id", strconv.FormatUint(runID, 10)))
	ctx = aismw.WithReservationRef(ctx, fmt.Sprintf("agent_run:%d", runID))
	pool := ""
	if req.IsTest {
		pool = aismw.BillingPoolAdminTest
	}
	ctx = aismw.WithBillingPool(ctx, pool)
	// Agent manages its own context → bill, but never compress/replace Messages.
	ctx = aismw.WithGatewayBillingOnly(ctx)
	return ctx
}
