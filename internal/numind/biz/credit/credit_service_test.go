package credit_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
)

// TestFinalizeReservation_NilIsNoOp verifies FinalizeReservation(rsv=nil) is a
// safe no-op — callers that opt out of reservation (e.g. ops that never debit)
// pass nil to the defer.
func TestFinalizeReservation_NilIsNoOp(t *testing.T) {
	db := newCreditTestDB(t)
	ds := store.NewTestStore(db)
	svc := credit.NewCreditService(ds, credit.NewCreditBiz(ds), nil, nil)

	var opErr error
	var actual int64
	err := svc.FinalizeReservation(context.Background(), nil, &actual, &opErr)
	require.NoError(t, err)
}
