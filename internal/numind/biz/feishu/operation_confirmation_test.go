package feishu

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/require"
)

func TestOperationConfirmationRequester_HighRiskWaitsForDurableConfirmation(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli-existing")
	requester, err := NewOperationConfirmationRequester(h.dataStore)
	require.NoError(t, err)
	service, err := NewFeishuOperationService(OperationServiceDeps{
		Accounts: h.dataStore.ThirdPartyAccounts(), Operations: h.dataStore.FeishuWorkspace(),
		Catalog: NewCommandCatalog(), Receipts: h.receipts, Recovery: h.recovery,
		Confirmation: requester, Vault: h.vault, Runner: h.runner, Cipher: h.cipher,
		Now: h.service.now, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)

	result, err := service.Execute(context.Background(), operationDocsOverwriteRequest(
		7, 190, "confirmation-requester", "doxcnABCDEFG123",
	))
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, result.State)
	require.NotNil(t, result.Action)
	require.Equal(t, "confirmation", result.Action.Phase)
	require.NotEmpty(t, result.Action.SessionID)
	require.True(t, result.Action.ExpiresAt.After(h.service.now()))

	stored, err := h.dataStore.FeishuWorkspace().GetOperationForUser(context.Background(), 7, 1, result.OperationID)
	require.NoError(t, err)
	require.Equal(t, model.FeishuOperationWaitingConfirmation, stored.State)
	require.Empty(t, stored.LeaseOwner)
	require.Nil(t, stored.LeaseUntil)
}

func TestOperationConfirmationRequester_RejectsNonHighRiskWithoutSilentSuccess(t *testing.T) {
	h := newOperationHarness(t)
	requester, err := NewOperationConfirmationRequester(h.dataStore)
	require.NoError(t, err)

	action, err := requester.RequestConfirmation(context.Background(), "operation-read", ConfirmationSummary{
		CommandPath: "docs +fetch",
		Domain:      "docs",
		Action:      "fetch",
		Risk:        RiskRead,
	})
	require.Error(t, err)
	require.Nil(t, action)
}
