package feishu

import (
	"encoding/json"
	"testing"

	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/require"
)

func TestMarshalLarkToolResult_SuccessAndActionableFailure(t *testing.T) {
	success, err := MarshalLarkToolResult(&OperationResult{
		OperationID: "operation-success", State: model.FeishuOperationSucceeded,
		Data:       json.RawMessage(`{"document_id":"doc-1"}`),
		AgentRunID: 99, ToolCallID: "secret-tool-call",
		Action: &OperationAction{URL: "https://secret.example"},
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"ok":true,"state":"succeeded","operation_id":"operation-success","data":{"document_id":"doc-1"}}`, string(success))
	require.NotContains(t, string(success), "secret")

	failure, err := MarshalLarkToolResult(&OperationResult{
		OperationID: "operation-failed", State: model.FeishuOperationFailed,
		Failure: &OperationFailure{
			Code: PublicCodeNotFound, Category: "not_found", Retryable: false, BusinessStarted: true,
		},
		Action: &OperationAction{URL: "https://secret.example", Scopes: []string{"im:message"}},
	})
	require.NoError(t, err)
	require.JSONEq(t, `{
		"ok":false,"state":"failed","operation_id":"operation-failed",
		"failure":{"code":"feishu_not_found","category":"not_found","retryable":false,"business_started":true}
	}`, string(failure))
	require.NotContains(t, string(failure), "secret")
	require.NotContains(t, string(failure), "im:message")
}

func TestDecodeLarkTerminalFailure_StrictDurableBoundary(t *testing.T) {
	const writeFenceKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	raw, err := MarshalLarkToolResult(&OperationResult{
		OperationID: "op-unknown", State: model.FeishuOperationUnknown,
		Failure: &OperationFailure{
			Code: PublicCodeUnknownResult, Category: "unknown_result", BusinessStarted: true,
			WriteFenceKey: writeFenceKey,
		},
	})
	require.NoError(t, err)
	failure, ok := DecodeLarkTerminalFailure(raw)
	require.True(t, ok)
	require.Equal(t, "unknown_result", failure.Category)
	require.True(t, failure.BusinessStarted)
	require.Equal(t, writeFenceKey, failure.WriteFenceKey)

	for _, invalid := range []string{
		`{"ok":false,"state":"unknown","operation_id":"op","failure":{"code":"feishu_unknown_result","category":"unknown_result","retryable":false,"business_started":true},"extra":true}`,
		`{"ok":false,"state":"unknown","operation_id":"op","failure":{"code":"feishu_unknown_result","category":"validation","retryable":false,"business_started":true}}`,
		`{"ok":true,"state":"unknown","operation_id":"op","failure":{"code":"feishu_unknown_result","category":"unknown_result","retryable":false,"business_started":true}}`,
		`{"ok":false,"state":"unknown","operation_id":"op","failure":{"code":"feishu_unknown_result","category":"unknown_result","retryable":false,"business_started":true,"write_fence_key":"not-a-digest"}}`,
		`{"ok":false,"state":"failed","operation_id":"op","failure":{"code":"feishu_failed","category":"failed","retryable":false,"business_started":true,"write_fence_key":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}`,
		string(raw) + `{}`,
	} {
		got, accepted := DecodeLarkTerminalFailure(json.RawMessage(invalid))
		require.False(t, accepted)
		require.Nil(t, got)
	}
}

func TestMarshalLarkToolResult_RejectsUnsafeOrAmbiguousResults(t *testing.T) {
	tests := []*OperationResult{
		nil,
		{OperationID: "", State: model.FeishuOperationFailed, Failure: &OperationFailure{Code: PublicCodeFailed, Category: "failed"}},
		{OperationID: "operation-wait", State: model.FeishuOperationWaitingUserAuth},
		{OperationID: "operation-success", State: model.FeishuOperationSucceeded, Data: json.RawMessage(`not-json`)},
		{OperationID: "operation-failure", State: model.FeishuOperationFailed},
		{OperationID: "operation-raw-code", State: model.FeishuOperationFailed, Failure: &OperationFailure{Code: "raw_cli_code", Category: "failed"}},
		{OperationID: "operation-bad-category", State: model.FeishuOperationFailed, Failure: &OperationFailure{Code: PublicCodeNotFound, Category: "temporary"}},
		{OperationID: "operation-bad-scope", State: model.FeishuOperationFailed, Failure: &OperationFailure{
			Code: PublicCodeScopeRequired, Category: "scope_required", RequiredScopes: []string{"im:message"},
		}},
		{OperationID: "operation-unsorted-scope", State: model.FeishuOperationFailed, Failure: &OperationFailure{
			Code: PublicCodeScopeRequired, Category: "scope_required",
			RequiredScopes: []string{"docx:document:write_only", "docx:document:readonly"},
		}},
		{OperationID: "operation-unknown", State: model.FeishuOperationUnknown, Failure: &OperationFailure{
			Code: PublicCodeTemporaryError, Category: "temporary", Retryable: true, BusinessStarted: true,
		}},
		{OperationID: "operation-unknown-bad-fence", State: model.FeishuOperationUnknown, Failure: &OperationFailure{
			Code: PublicCodeUnknownResult, Category: "unknown_result", BusinessStarted: true, WriteFenceKey: "UPPERCASE",
		}},
		{OperationID: "operation-failed-with-fence", State: model.FeishuOperationFailed, Failure: &OperationFailure{
			Code: PublicCodeFailed, Category: "failed", BusinessStarted: true,
			WriteFenceKey: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
	}
	for _, result := range tests {
		encoded, err := MarshalLarkToolResult(result)
		require.Error(t, err)
		require.Nil(t, encoded)
	}
}

func TestNewOperationFailure_RetryabilityIsRiskAware(t *testing.T) {
	read := newOperationFailure(PublicCodeTemporaryError, model.FeishuOperationFailed, true, RiskRead, nil)
	require.True(t, read.Retryable)
	write := newOperationFailure(PublicCodeTemporaryError, model.FeishuOperationFailed, true, RiskWrite, nil)
	require.False(t, write.Retryable)
	notStartedWrite := newOperationFailure(PublicCodeTemporaryError, model.FeishuOperationFailed, false, RiskWrite, nil)
	require.True(t, notStartedWrite.Retryable)
	unknown := newOperationFailure(PublicCodeUnknownResult, model.FeishuOperationUnknown, true, RiskWrite, nil)
	require.False(t, unknown.Retryable)
}
