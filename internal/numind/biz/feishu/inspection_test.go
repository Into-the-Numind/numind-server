package feishu

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"numind-server/internal/pkg/model"
)

func TestOperationService_InspectConnectionReturnsOnlySafeCurrentUserState(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 4, "cli_secret_app")
	require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, ProviderLark).
		Updates(map[string]any{
			"capability_state_json": []byte(`{
				"docs":{"state":"available","last_success_at":"2026-07-13T12:00:00Z"},
				"base":{"state":"needs_user_scope"},"unknown":{"state":"available"}
			}`),
			"granted_scopes_json": []byte(`["secret:scope"]`),
		}).Error)

	got, err := h.service.Inspect(context.Background(), InspectionRequest{
		UserID: 7, AgentRunID: 101, Mode: InspectionModeConnection,
	})
	require.NoError(t, err)
	require.Equal(t, &InspectionResult{
		Mode: InspectionModeConnection, ConnectionState: model.FeishuConnectionConnected,
		Capabilities: map[string]string{
			"docs": model.FeishuCapabilityAvailable, "base": model.FeishuCapabilityNeedsUserScope,
			"wiki": model.FeishuCapabilityUnknown, "drive": model.FeishuCapabilityUnknown,
		},
	}, got)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	for _, secret := range []string{"cli_secret_app", "secret:scope", "user_id", "generation", "last_success_at"} {
		require.NotContains(t, string(encoded), secret)
	}

	missing, err := h.service.Inspect(context.Background(), InspectionRequest{
		UserID: 8, AgentRunID: 101, Mode: InspectionModeConnection,
	})
	require.NoError(t, err)
	require.Equal(t, model.FeishuConnectionNone, missing.ConnectionState)
}

func TestOperationService_InspectCommandUsesCatalogReceiptsAndReadOnlyPreflight(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 2, "cli_secret_app")
	h.preflight.steps = []operationScopePreflightStep{{result: &ScopeCheckResult{
		Granted: []string{"docx:document:readonly"}, Missing: []string{"docx:document:write_only"},
	}}}

	got, err := h.service.Inspect(context.Background(), InspectionRequest{
		UserID: 7, AgentRunID: 102, Mode: InspectionModeCommand,
		Argv: []string{
			"docs", "+update", "--doc", "doxcnABCDEFG123", "--command", "str_replace",
			"--pattern", "before", "--content", "after",
		},
		SkillReceipts: []string{"shared-receipt", "doc-receipt"},
	})
	require.NoError(t, err)
	ready := false
	require.Equal(t, &InspectionResult{
		Mode: InspectionModeCommand, CommandPath: "docs +update", Domain: "docs", Risk: RiskWrite,
		Ready: &ready, GrantedScopes: []string{"docx:document:readonly"},
		MissingScopes: []string{"docx:document:write_only"},
	}, got)
	preflightCalls, _ := h.preflight.snapshot()
	require.Equal(t, 1, preflightCalls)
	businessCalls, _ := h.runner.snapshot()
	require.Zero(t, businessCalls)
	var operations int64
	require.NoError(t, h.db.Model(&model.FeishuOperation{}).Count(&operations).Error)
	require.Zero(t, operations)
	require.Equal(t, []bool{false}, h.vault.changed)

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	for _, secret := range []string{"doxcnABCDEFG123", "before", "after", "receipt", "cli_secret_app", "user_id", "generation"} {
		require.NotContains(t, string(encoded), secret)
	}
	require.Contains(t, string(encoded), `"ready":false`)
}

func TestOperationService_InspectCommandRejectsUnsafeBoundaries(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	tests := []InspectionRequest{
		{UserID: 7, AgentRunID: 103, Mode: InspectionModeCommand, Argv: []string{"auth", "status"}, SkillReceipts: []string{"r"}},
		{UserID: 7, AgentRunID: 103, Mode: InspectionModeCommand, Argv: []string{"config", "init"}, SkillReceipts: []string{"r"}},
		{UserID: 7, AgentRunID: 103, Mode: InspectionModeCommand, Argv: []string{"im", "send"}, SkillReceipts: []string{"r"}},
		{UserID: 7, AgentRunID: 103, Mode: InspectionModeCommand, Argv: []string{"drive", "+write"}, SkillReceipts: []string{"r"}},
		{UserID: 7, AgentRunID: 0, Mode: InspectionModeCommand, Argv: []string{"docs", "+fetch"}, SkillReceipts: []string{"r"}},
		{UserID: 7, AgentRunID: 103, Mode: InspectionModeCommand, Argv: []string{"docs", "+fetch"}},
		{UserID: 7, AgentRunID: 103, Mode: InspectionModeConnection, Argv: []string{"docs"}},
	}
	for _, request := range tests {
		got, err := h.service.Inspect(context.Background(), request)
		require.Error(t, err)
		require.Nil(t, got)
	}
	preflightCalls, _ := h.preflight.snapshot()
	require.Zero(t, preflightCalls)
	businessCalls, _ := h.runner.snapshot()
	require.Zero(t, businessCalls)
}

func TestOperationService_InspectCommandRejectsDisconnectedAndCrossRunReceipts(t *testing.T) {
	h := newOperationHarness(t)
	h.createAccount(7, model.FeishuConnectionConnected, 1, "cli_existing")
	h.receipts.err = context.Canceled
	request := InspectionRequest{
		UserID: 7, AgentRunID: 104, Mode: InspectionModeCommand,
		Argv:          []string{"drive", "+search", "--query", "report", "--only-title", "--doc-types", "docx"},
		SkillReceipts: []string{"other-run-receipt"},
	}
	got, err := h.service.Inspect(context.Background(), request)
	require.ErrorIs(t, err, ErrInspectionRejected)
	require.Nil(t, got)

	h.receipts.err = nil
	require.NoError(t, h.db.Model(&model.UserThirdPartyAccount{}).
		Where("user_id = ? AND provider = ?", 7, ProviderLark).
		Updates(map[string]any{"connection_state": model.FeishuConnectionNone, "connected": false}).Error)
	got, err = h.service.Inspect(context.Background(), request)
	require.ErrorIs(t, err, ErrInspectionUnavailable)
	require.Nil(t, got)
	preflightCalls, _ := h.preflight.snapshot()
	require.Zero(t, preflightCalls)
}
