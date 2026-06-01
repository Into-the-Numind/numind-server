package credit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// TestReserveBudget_WritesReferenceID verifies that:
//  1. A non-empty ReferenceID is written verbatim to credit_reservation.reference_id.
//  2. Setting ReferenceID does NOT change the reserved amount (amount-safety net):
//     the reserved credits must equal the control reservation's reserved credits.
func TestReserveBudget_WritesReferenceID(t *testing.T) {
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db)
	svc := newCreditServiceWithMembership(ds, db, nil)

	now := time.Now()

	// Two separate users so their reserves are independent (no balance/idempotency
	// interference). Each needs enough balance for one reserve.
	userCtlID := uint(901)
	userRefID := uint(902)

	for _, uid := range []uint{userCtlID, userRefID} {
		seedPackagesAndAccount(t, db, uid, []seedPackage{
			{
				Type:          model.CreditTypeSubscription,
				TotalCredits:  2000,
				RemainCredits: 2000,
				ActivatedAt:   now,
				ExpiresAt:     now.Add(30 * 24 * time.Hour),
			},
		})
	}

	// Shared precheckIn — both reserves use the same parameters so any divergence
	// in reserved amount is caused solely by the ReferenceID field.
	precheckIn := credit.BudgetPrecheckInput{
		Operation:                 "sop_node_execute",
		EstimatedPromptTokens:     1000,
		EstimatedCompletionTokens: 200,
		Provider:                  "volc",
		Model:                     "glm-4-7-251222",
	}

	// Control: no ReferenceID.
	// NB: precheckIn is a value struct — reassigning .UserID below copies into each
	// BudgetReservationInput by value, so the two reserves don't alias.
	precheckIn.UserID = userCtlID
	userCtl := newCreditsUser(userCtlID)
	rsvCtl, err := svc.ReserveBudget(context.Background(), userCtl, credit.BudgetReservationInput{
		BudgetPrecheckInput: precheckIn,
		EstimatedCredits:    50,
	})
	require.NoError(t, err)
	require.NotNil(t, rsvCtl)

	// With ReferenceID.
	precheckIn.UserID = userRefID
	userRef := newCreditsUser(userRefID)
	rsvRef, err := svc.ReserveBudget(context.Background(), userRef, credit.BudgetReservationInput{
		BudgetPrecheckInput: precheckIn,
		EstimatedCredits:    50,
		ReferenceID:         "sop_run:5:9",
	})
	require.NoError(t, err)
	require.NotNil(t, rsvRef)

	// 🔑 Amount-safety net: ReferenceID must NOT change the reserved amount.
	assert.Equal(t, rsvCtl.ReservedCredits, rsvRef.ReservedCredits,
		"ReferenceID must not affect reserved amount")

	// ReferenceID must be persisted in the DB row.
	var row model.CreditReservation
	require.NoError(t, ds.DB().First(&row, rsvRef.ID).Error)
	assert.Equal(t, "sop_run:5:9", row.ReferenceID)
}
