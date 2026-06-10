package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/errno"
)

// T5/C7: a non-member calling a 0-priced model is blocked by EnforceModelMembership
// in the middleware — BEFORE the fragment passthrough and the ChargeUser guard. The
// request carries NO fragments (a path that would otherwise pass through entirely
// uncharged), proving AC3 is airtight regardless of ContextFragments or ChargeUser.
func TestContextBudgetCredits_FreeModelNonMember_BlockedRegardlessOfFragmentsOrCharge(t *testing.T) {
	creditSvc := &mockCreditService{enforceErr: errno.ErrModelMembershipOnly}
	deps := Deps{
		ContextBudget: &mockContextBudgetService{},
		CreditService: creditSvc,
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	adapterCalled := false
	adapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		adapterCalled = true
		return &aiservice.ChatResponse{}, nil
	})
	handler := ContextBudgetCredits(deps)(adapter)

	req := aiservice.ChatRequest{} // no fragments → would normally passthrough uncharged
	ctx := billing.WithBillingMeta(context.Background(), 7, "chatbot_chat", nil)
	ctx = WithUserID(ctx, 7)

	_, err := handler(ctx, budgetRoute(), req)
	require.Error(t, err)
	require.True(t, errors.Is(err, errno.ErrModelMembershipOnly), "got %v", err)
	require.False(t, adapterCalled, "provider must NOT be called when a non-member is blocked from a free model")
}

// Counterpart: when EnforceModelMembership returns nil (member, or paid model), the
// gate is transparent — a no-fragment chat passes through to the provider as before.
func TestContextBudgetCredits_NoBlock_NoFragmentPassthroughUnchanged(t *testing.T) {
	deps := Deps{
		ContextBudget: &mockContextBudgetService{},
		CreditService: &mockCreditService{enforceErr: nil},
		Logger:        &mockLogger{},
		Clock:         fixedClock{t: time.Now()},
	}

	adapterCalled := false
	adapter := Handler(func(_ context.Context, _ *registry.ResolvedRoute, _ interface{}) (interface{}, error) {
		adapterCalled = true
		return &aiservice.ChatResponse{}, nil
	})
	handler := ContextBudgetCredits(deps)(adapter)

	req := aiservice.ChatRequest{} // no fragments → passthrough after the (transparent) gate
	ctx := billing.WithBillingMeta(context.Background(), 7, "chatbot_chat", nil)
	ctx = WithUserID(ctx, 7)

	_, err := handler(ctx, budgetRoute(), req)
	require.NoError(t, err)
	require.True(t, adapterCalled, "transparent gate must not disturb the no-fragment passthrough")
}
