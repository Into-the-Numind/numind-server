package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

type fakeSkillReadExecutor struct {
	mu       sync.Mutex
	requests []feishu.SkillReadRequest
	result   *feishu.SkillReadPage
	err      error
}

func (f *fakeSkillReadExecutor) Read(_ context.Context, request feishu.SkillReadRequest) (*feishu.SkillReadPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, request)
	if f.result == nil {
		return nil, f.err
	}
	clone := *f.result
	clone.References = append([]string(nil), f.result.References...)
	return &clone, f.err
}

func (f *fakeSkillReadExecutor) snapshot() []feishu.SkillReadRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]feishu.SkillReadRequest(nil), f.requests...)
}

type fakeLarkExecutor struct {
	mu       sync.Mutex
	requests []feishu.ExecuteRequest
	result   *feishu.OperationResult
	err      error
}

func (f *fakeLarkExecutor) Execute(_ context.Context, request feishu.ExecuteRequest) (*feishu.OperationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request.Argv = append([]string(nil), request.Argv...)
	request.StdinJSON = append(json.RawMessage(nil), request.StdinJSON...)
	request.SkillReceipts = append([]string(nil), request.SkillReceipts...)
	f.requests = append(f.requests, request)
	if f.result == nil {
		return nil, f.err
	}
	clone := *f.result
	clone.Data = append(json.RawMessage(nil), f.result.Data...)
	if f.result.Action != nil {
		action := *f.result.Action
		action.Scopes = append([]string(nil), f.result.Action.Scopes...)
		clone.Action = &action
	}
	return &clone, f.err
}

func (f *fakeLarkExecutor) snapshot() []feishu.ExecuteRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]feishu.ExecuteRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func larkPersonalWorkspaceContext(userID uint, runID uint64, toolCallID string) context.Context {
	ctx := middleware.NewContextWithUserID(context.Background(), userID)
	ctx = WithRunID(ctx, runID)
	return WithToolCallID(ctx, toolCallID)
}

func requireSafeLarkSoftError(t *testing.T, result ToolResult, err error, forbidden ...string) {
	t.Helper()
	require.NoError(t, err)
	require.NotEmpty(t, result)
	assert.Contains(t, string(result), "ERROR")
	for _, value := range forbidden {
		if value != "" {
			assert.NotContains(t, string(result), value)
		}
	}
}

func TestLarkPersonalWorkspace_SkillReadSuccessUsesOnlyRunContext(t *testing.T) {
	executor := &fakeSkillReadExecutor{result: &feishu.SkillReadPage{
		Skill:      "lark-doc",
		Path:       "skills/lark-doc/SKILL.md",
		Content:    "line 1\nquoted: \"safe\"",
		References: []string{"references/docs.md"},
		Cursor:     "next-cursor",
		Receipt:    "opaque-receipt",
	}}
	tool := &larkSkillReadTool{executor: executor}

	result, err := tool.Execute(WithRunID(context.Background(), 41), ToolInput(`{"skill":"lark-doc","reference":"references/docs.md","cursor":"cursor-1"}`))
	require.NoError(t, err)
	requests := executor.snapshot()
	require.Len(t, requests, 1)
	assert.Equal(t, feishu.SkillReadRequest{
		AgentRunID: 41,
		Skill:      "lark-doc",
		Reference:  "references/docs.md",
		Cursor:     "cursor-1",
	}, requests[0], "skill reads must not require or derive a user connection")

	var output struct {
		OK         bool     `json:"ok"`
		Skill      string   `json:"skill"`
		Path       string   `json:"path"`
		Content    string   `json:"content"`
		References []string `json:"references"`
		Cursor     string   `json:"cursor"`
		Receipt    string   `json:"receipt"`
		CLIVersion string   `json:"cli_version"`
	}
	require.NoError(t, json.Unmarshal(result, &output))
	assert.True(t, output.OK)
	assert.Equal(t, executor.result.Skill, output.Skill)
	assert.Equal(t, executor.result.Path, output.Path)
	assert.Equal(t, executor.result.Content, output.Content, "raw content must JSON round-trip without corruption")
	assert.Equal(t, executor.result.References, output.References)
	assert.Equal(t, executor.result.Cursor, output.Cursor)
	assert.Equal(t, executor.result.Receipt, output.Receipt)
	assert.Equal(t, "1.0.68", output.CLIVersion)
	assert.True(t, tool.IsReadOnly())
	assert.True(t, tool.IsConcurrencySafe(nil))
	assert.Contains(t, tool.Description(), "lark-shared")
	assert.Contains(t, tool.Description(), "controlled reference")
}

func TestLarkPersonalWorkspace_SkillReadStrictInputAndSafeFailures(t *testing.T) {
	for name, input := range map[string]string{
		"user identity":     `{"skill":"lark-doc","user_id":9}`,
		"run identity":      `{"skill":"lark-doc","run_id":9}`,
		"tool identity":     `{"skill":"lark-doc","tool_call_id":"tc"}`,
		"raw path":          `{"skill":"lark-doc","path":"/tmp/secret"}`,
		"exact duplicate":   `{"skill":"lark-doc","skill":"lark-wiki"}`,
		"case variant":      `{"Skill":"lark-doc"}`,
		"trailing document": `{"skill":"lark-doc"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			executor := &fakeSkillReadExecutor{}
			tool := &larkSkillReadTool{executor: executor}
			result, err := tool.Execute(WithRunID(context.Background(), 11), ToolInput(input))
			requireSafeLarkSoftError(t, result, err, "/tmp/secret")
			assert.Empty(t, executor.snapshot())
		})
	}

	internalErr := errors.New("reader failed at /private/home with embedded secret-content")
	for name, testCase := range map[string]struct {
		ctx      context.Context
		executor SkillReadExecutor
	}{
		"nil executor": {ctx: WithRunID(context.Background(), 11)},
		"nil context":  {ctx: nil, executor: &fakeSkillReadExecutor{}},
		"missing run":  {ctx: context.Background(), executor: &fakeSkillReadExecutor{}},
		"reader error": {ctx: WithRunID(context.Background(), 11), executor: &fakeSkillReadExecutor{err: internalErr}},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := (&larkSkillReadTool{executor: testCase.executor}).Execute(testCase.ctx, ToolInput(`{"skill":"lark-doc"}`))
			requireSafeLarkSoftError(t, result, err, internalErr.Error(), "/private/home", "secret-content")
		})
	}
}

func TestLarkPersonalWorkspace_SkillReadOptionalStringsRejectNonStrings(t *testing.T) {
	invalidValues := map[string]string{
		"null":   "null",
		"object": `{}`,
		"array":  `[]`,
		"number": `42`,
		"bool":   `true`,
	}
	for _, field := range []string{"reference", "cursor"} {
		for valueName, value := range invalidValues {
			t.Run(field+"_"+valueName, func(t *testing.T) {
				executor := &fakeSkillReadExecutor{result: &feishu.SkillReadPage{Skill: "lark-doc"}}
				input := ToolInput(`{"skill":"lark-doc","` + field + `":` + value + `}`)
				result, err := (&larkSkillReadTool{executor: executor}).Execute(WithRunID(context.Background(), 12), input)
				requireSafeLarkSoftError(t, result, err)
				assert.Empty(t, executor.snapshot(), "non-string optional input must not reach the skill reader")
			})
		}
	}

	executor := &fakeSkillReadExecutor{result: &feishu.SkillReadPage{Skill: "lark-doc"}}
	result, err := (&larkSkillReadTool{executor: executor}).Execute(
		WithRunID(context.Background(), 13),
		ToolInput(`{"skill":"lark-doc","reference":"","cursor":""}`),
	)
	require.NoError(t, err)
	assert.NotContains(t, string(result), "ERROR")
	requests := executor.snapshot()
	require.Len(t, requests, 1)
	assert.Empty(t, requests[0].Reference)
	assert.Empty(t, requests[0].Cursor)
}

func TestLarkPersonalWorkspace_ExecuteDerivesTenantAndIdempotencyFromContext(t *testing.T) {
	executor := &fakeLarkExecutor{result: &feishu.OperationResult{
		OperationID: "op-success",
		State:       model.FeishuOperationSucceeded,
		Data:        json.RawMessage(`{"document":{"id":"doc-1"},"count":2}`),
	}}
	tool := &larkExecuteTool{executor: executor}
	input := ToolInput(`{"argv":["docs","+fetch","--doc","doc-1"],"stdin_json":{"value":"line\nquoted: \"yes\""},"skill_receipts":["receipt-doc","receipt-shared"]}`)

	first, err := tool.Execute(larkPersonalWorkspaceContext(21, 501, "synthetic-a"), input)
	require.NoError(t, err)
	second, err := tool.Execute(larkPersonalWorkspaceContext(22, 502, "synthetic-b"), ToolInput(`{"argv":["wiki","+get","--node","n1"],"stdin_json":null,"skill_receipts":["receipt-wiki"]}`))
	require.NoError(t, err)

	requests := executor.snapshot()
	require.Len(t, requests, 2)
	assert.Equal(t, uint(21), requests[0].UserID)
	assert.Equal(t, uint64(501), requests[0].AgentRunID)
	assert.Equal(t, "synthetic-a", requests[0].ToolCallID)
	assert.Equal(t, "501:synthetic-a", requests[0].IdempotencyKey)
	assert.Equal(t, []string{"docs", "+fetch", "--doc", "doc-1"}, requests[0].Argv)
	assert.JSONEq(t, `{"value":"line\nquoted: \"yes\""}`, string(requests[0].StdinJSON))
	assert.Equal(t, []string{"receipt-doc", "receipt-shared"}, requests[0].SkillReceipts)
	assert.Equal(t, uint(22), requests[1].UserID, "a second tenant must derive its own identity from context")
	assert.Equal(t, "502:synthetic-b", requests[1].IdempotencyKey)
	assert.Nil(t, requests[1].StdinJSON, "stdin_json:null must normalize to nil")

	var output struct {
		OK          bool            `json:"ok"`
		State       string          `json:"state"`
		OperationID string          `json:"operation_id"`
		Data        json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first, &output))
	assert.True(t, output.OK)
	assert.Equal(t, model.FeishuOperationSucceeded, output.State)
	assert.Equal(t, "op-success", output.OperationID)
	assert.JSONEq(t, `{"document":{"id":"doc-1"},"count":2}`, string(output.Data), "data must remain JSON, not base64")
	assert.NotContains(t, string(first), "eyJ", "data must not be base64 encoded")
	assert.False(t, tool.IsReadOnly())
	assert.False(t, tool.IsConcurrencySafe(nil))
	assert.Contains(t, tool.Description(), "Docs/Base/Wiki")
	assert.Contains(t, tool.Description(), "no shell")
	assert.Contains(t, tool.Description(), "no IM")
	_ = second
}

func TestLarkPersonalWorkspace_ExecuteRejectsUntrustedIdentityAndStrictJSON(t *testing.T) {
	for name, input := range map[string]string{
		"user id":           `{"argv":["docs"],"skill_receipts":["r"],"user_id":99}`,
		"run id":            `{"argv":["docs"],"skill_receipts":["r"],"run_id":99}`,
		"tool call id":      `{"argv":["docs"],"skill_receipts":["r"],"tool_call_id":"evil"}`,
		"idempotency":       `{"argv":["docs"],"skill_receipts":["r"],"idempotency_key":"evil"}`,
		"scopes":            `{"argv":["docs"],"skill_receipts":["r"],"scopes":["admin"]}`,
		"auth":              `{"argv":["docs"],"skill_receipts":["r"],"auth":{"token":"secret"}}`,
		"risk":              `{"argv":["docs"],"skill_receipts":["r"],"risk":"safe"}`,
		"domain":            `{"argv":["docs"],"skill_receipts":["r"],"domain":"im"}`,
		"home":              `{"argv":["docs"],"skill_receipts":["r"],"home":"/tmp/x"}`,
		"profile":           `{"argv":["docs"],"skill_receipts":["r"],"profile":"root"}`,
		"exact duplicate":   `{"argv":["docs"],"argv":["wiki"],"skill_receipts":["r"]}`,
		"case variant":      `{"Argv":["docs"],"skill_receipts":["r"]}`,
		"mixed case field":  `{"argv":["docs"],"Skill_Receipts":["r"]}`,
		"trailing document": `{"argv":["docs"],"skill_receipts":["r"]} {}`,
		"empty argv":        `{"argv":[],"skill_receipts":["r"]}`,
		"empty receipts":    `{"argv":["docs"],"skill_receipts":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			executor := &fakeLarkExecutor{}
			result, err := (&larkExecuteTool{executor: executor}).Execute(larkPersonalWorkspaceContext(7, 8, "tc-8"), ToolInput(input))
			requireSafeLarkSoftError(t, result, err, "evil", "secret", "/tmp/x")
			assert.Empty(t, executor.snapshot(), "rejected model input must never reach the operation executor")
		})
	}
}

func TestLarkPersonalWorkspace_ExecuteMissingContextFailsClosed(t *testing.T) {
	contexts := map[string]context.Context{
		"missing user": WithToolCallID(WithRunID(context.Background(), 8), "tc-8"),
		"missing run":  WithToolCallID(middleware.NewContextWithUserID(context.Background(), 7), "tc-8"),
		"missing call": WithRunID(middleware.NewContextWithUserID(context.Background(), 7), 8),
	}
	for name, ctx := range contexts {
		t.Run(name, func(t *testing.T) {
			executor := &fakeLarkExecutor{}
			result, err := (&larkExecuteTool{executor: executor}).Execute(ctx, ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`))
			requireSafeLarkSoftError(t, result, err, "receipt")
			assert.Empty(t, executor.snapshot())
		})
	}

	result, err := (&larkExecuteTool{}).Execute(larkPersonalWorkspaceContext(7, 8, "tc-8"), ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`))
	requireSafeLarkSoftError(t, result, err, "receipt")
}

func TestLarkPersonalWorkspace_ExecuteWaitingYieldsDurableExternalAction(t *testing.T) {
	expiresAt := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	states := []string{
		model.FeishuOperationWaitingConnection,
		model.FeishuOperationWaitingAppScope,
		model.FeishuOperationWaitingUserAuth,
		model.FeishuOperationWaitingConfirmation,
	}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			executor := &fakeLarkExecutor{result: &feishu.OperationResult{
				OperationID: "op-" + state,
				State:       state,
				ToolCallID:  "untrusted-result-tool-call",
				Action: &feishu.OperationAction{
					Provider:    "lark",
					OperationID: "op-" + state,
					SessionID:   "session-" + state,
					Phase:       "phase-" + state,
					URL:         "https://open.feishu.cn/action?state=live-only",
					ExpiresAt:   expiresAt,
				},
			}}
			result, err := (&larkExecuteTool{executor: executor}).Execute(
				larkPersonalWorkspaceContext(7, 600, "synthetic-context-call"),
				ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`),
			)
			assert.Nil(t, result)
			var yielded *yieldError
			require.ErrorAs(t, err, &yielded)
			require.NotNil(t, yielded.Payload.ExternalAction)
			action := yielded.Payload.ExternalAction
			assert.Equal(t, executor.result.Action.Provider, action.Provider)
			assert.Equal(t, executor.result.Action.OperationID, action.OperationID)
			assert.Equal(t, executor.result.Action.SessionID, action.SessionID)
			assert.Equal(t, "synthetic-context-call", action.ToolCallID, "external wait must use adapter context identity")
			assert.Equal(t, executor.result.Action.Phase, action.Phase)
			assert.Equal(t, executor.result.Action.URL, action.URL, "live action keeps its transient URL")
			assert.Equal(t, expiresAt, action.ExpiresAt)

			persistent, marshalErr := json.Marshal(action.Persistent())
			require.NoError(t, marshalErr)
			assert.NotContains(t, string(persistent), "url", "restart-safe persistence must strip the live URL")
			assert.Contains(t, string(persistent), "synthetic-context-call")
		})
	}
}

func TestLarkPersonalWorkspace_ExecuteReloadedActionWithoutURLStillYields(t *testing.T) {
	expiresAt := time.Date(2026, 7, 13, 12, 30, 0, 0, time.UTC)
	live := ExternalActionPayload{
		Provider:    "lark",
		OperationID: "op-reloaded-confirmation",
		SessionID:   "session-reloaded-confirmation",
		ToolCallID:  "original-tool-call",
		Phase:       "confirmation",
		URL:         "https://confirmation.example/live-only",
		ExpiresAt:   expiresAt,
	}
	persisted, err := json.Marshal(live.Persistent())
	require.NoError(t, err)
	reloaded, err := ParsePendingExternalAction(persisted)
	require.NoError(t, err)
	require.Empty(t, reloaded.URL, "cross-process reload must not recover the transient URL")

	executor := &fakeLarkExecutor{result: &feishu.OperationResult{
		OperationID: reloaded.OperationID,
		State:       model.FeishuOperationWaitingConfirmation,
		Action: &feishu.OperationAction{
			Provider:    reloaded.Provider,
			OperationID: reloaded.OperationID,
			SessionID:   reloaded.SessionID,
			Phase:       reloaded.Phase,
			ExpiresAt:   reloaded.ExpiresAt,
		},
	}}
	result, err := (&larkExecuteTool{executor: executor}).Execute(
		larkPersonalWorkspaceContext(7, 601, "reloaded-context-call"),
		ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`),
	)
	assert.Nil(t, result)
	var yielded *yieldError
	require.ErrorAs(t, err, &yielded)
	require.NotNil(t, yielded.Payload.ExternalAction)
	assert.Empty(t, yielded.Payload.ExternalAction.URL)
	assert.Equal(t, reloaded.Provider, yielded.Payload.ExternalAction.Provider)
	assert.Equal(t, reloaded.OperationID, yielded.Payload.ExternalAction.OperationID)
	assert.Equal(t, reloaded.SessionID, yielded.Payload.ExternalAction.SessionID)
	assert.Equal(t, reloaded.Phase, yielded.Payload.ExternalAction.Phase)
	assert.Equal(t, reloaded.ExpiresAt, yielded.Payload.ExternalAction.ExpiresAt)

	repersisted, err := json.Marshal(yielded.Payload.ExternalAction.Persistent())
	require.NoError(t, err)
	assert.NotContains(t, string(repersisted), "url")
}

func TestLarkPersonalWorkspace_ExecuteInvalidWaitingAndExecutorErrorsAreSafe(t *testing.T) {
	invalidWaiting := &fakeLarkExecutor{result: &feishu.OperationResult{
		OperationID: "op-sensitive",
		State:       model.FeishuOperationWaitingUserAuth,
		Action: &feishu.OperationAction{
			Provider: "lark",
			URL:      "https://secret.example/action",
		},
	}}
	result, err := (&larkExecuteTool{executor: invalidWaiting}).Execute(
		larkPersonalWorkspaceContext(7, 8, "tc-8"),
		ToolInput(`{"argv":["docs","+fetch"],"skill_receipts":["secret-receipt"]}`),
	)
	requireSafeLarkSoftError(t, result, err, "op-sensitive", "secret.example", "secret-receipt", "+fetch")
	var yielded *yieldError
	assert.False(t, errors.As(err, &yielded), "incomplete action must not create an unpersistable card")

	internalErr := errors.New("runner leaked argv docs +fetch receipt-raw at /private/home")
	failing := &fakeLarkExecutor{err: internalErr}
	result, err = (&larkExecuteTool{executor: failing}).Execute(
		larkPersonalWorkspaceContext(7, 8, "tc-8"),
		ToolInput(`{"argv":["docs","+fetch"],"skill_receipts":["receipt-raw"]}`),
	)
	requireSafeLarkSoftError(t, result, err, internalErr.Error(), "receipt-raw", "+fetch", "/private/home")
}

func TestLarkPersonalWorkspace_ExecuteTerminalStatesNeverFakeSuccess(t *testing.T) {
	for _, state := range []string{model.FeishuOperationFailed, model.FeishuOperationUnknown, model.FeishuOperationCancelled} {
		t.Run(state, func(t *testing.T) {
			executor := &fakeLarkExecutor{result: &feishu.OperationResult{OperationID: "op-1", State: state}}
			result, err := (&larkExecuteTool{executor: executor}).Execute(
				larkPersonalWorkspaceContext(7, 8, "tc-8"),
				ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`),
			)
			require.NoError(t, err)
			var output struct {
				OK    bool   `json:"ok"`
				State string `json:"state"`
			}
			require.NoError(t, json.Unmarshal(result, &output))
			assert.False(t, output.OK)
			assert.Equal(t, state, output.State)
		})
	}
}

func TestLarkPersonalWorkspace_ExecuteRejectsNonTerminalNonWaitingStates(t *testing.T) {
	for name, state := range map[string]string{
		"executing":      model.FeishuOperationExecuting,
		"not started":    model.FeishuOperationNotStarted,
		"unknown string": "provider_future_state_sensitive",
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			executor := &fakeLarkExecutor{result: &feishu.OperationResult{
				OperationID: "op-sensitive-active-state",
				State:       state,
				Data:        json.RawMessage(`{"secret":"active-lease-data"}`),
			}}
			result, err := (&larkExecuteTool{executor: executor}).Execute(
				larkPersonalWorkspaceContext(7, 9, "tc-active-state"),
				ToolInput(`{"argv":["docs"],"skill_receipts":["receipt"]}`),
			)
			requireSafeLarkSoftError(t, result, err,
				state, "op-sensitive-active-state", "active-lease-data")
			var yielded *yieldError
			assert.False(t, errors.As(err, &yielded), "active or unrecognized states must fail closed, not yield")
		})
	}
}

func TestPlatformToolFactory_LarkPersonalWorkspaceBothOrNoneAndNoLegacyTools(t *testing.T) {
	reader := &fakeSkillReadExecutor{}
	executor := &fakeLarkExecutor{}
	legacyNames := []string{"lark_create_doc", "lark_read_bitable", "lark_send_message", "feishu_connect"}

	for name, configure := range map[string]func(ToolFactory){
		"none":         func(ToolFactory) {},
		"reader only":  func(f ToolFactory) { SetFactoryLarkWorkspaceExecutors(f, reader, nil) },
		"execute only": func(f ToolFactory) { SetFactoryLarkWorkspaceExecutors(f, nil, executor) },
	} {
		t.Run(name, func(t *testing.T) {
			factory := NewPlatformToolFactory(nil, nil)
			configure(factory)
			tools, metadata, err := factory.LoadTools(context.Background())
			require.NoError(t, err)
			assert.Len(t, tools, 19)
			assert.Len(t, metadata, 19)
			for _, tool := range tools {
				assert.NotContains(t, append(legacyNames, "lark_skill_read", "lark_execute"), tool.Name())
			}
		})
	}
	assert.NotPanics(t, func() { SetFactoryLarkWorkspaceExecutors(nil, reader, executor) })

	factory := NewPlatformToolFactory(nil, nil)
	SetFactoryLarkWorkspaceExecutors(factory, reader, executor)
	pf := factory.(*platformToolFactory)
	pf.larkProviderOverride = &fakeLarkProvider{api: &fakeLarkAPI{}}
	pf.feishuConnectorOverride = &fakeConnector{step: &feishu.ConnectStep{Phase: feishu.ConnectPhaseDone}}
	tools, metadata, err := factory.LoadTools(context.Background())
	require.NoError(t, err)
	require.Len(t, tools, 21, "19 base tools plus exactly two controlled Lark tools")
	require.Len(t, metadata, 21)
	assert.Equal(t, "lark_skill_read", tools[19].Name())
	assert.Equal(t, "lark_execute", tools[20].Name())
	assert.Equal(t, "lark_skill_read", metadata[19].ToolName)
	assert.Equal(t, "safe", metadata[19].RiskLevel)
	assert.Equal(t, "飞书", metadata[19].Category)
	assert.Equal(t, "lark_execute", metadata[20].ToolName)
	assert.Equal(t, "moderate", metadata[20].RiskLevel)
	assert.Equal(t, "飞书", metadata[20].Category)
	for _, legacy := range legacyNames {
		for _, tool := range tools {
			assert.NotEqual(t, legacy, tool.Name(), "legacy factory registration must stay removed")
		}
	}

	registry := NewAgentToolRegistry(newMockDefStore(), newMockFacStore())
	require.NoError(t, registry.RegisterFactory(factory))
	require.NoError(t, registry.LoadAll(context.Background()))
	_, ok := registry.GetTool("lark_skill_read")
	assert.True(t, ok)
	_, ok = registry.GetTool("lark_execute")
	assert.True(t, ok)
	for _, legacy := range legacyNames {
		_, exists := registry.GetTool(legacy)
		assert.False(t, exists, "registry must not expose %s", legacy)
	}
}

func TestLarkPersonalWorkspace_BashExecRoutesFeishuToControlledTools(t *testing.T) {
	tool := &bashExecTool{}
	description := tool.Description()
	assert.Contains(t, description, "飞书 Docs/Base/Wiki 必须通过 `lark_skill_read` + `lark_execute`")

	result, err := tool.Execute(context.Background(), ToolInput(`{"command":"lark-cli docs +fetch"}`))
	require.NoError(t, err)
	assert.Contains(t, string(result), "LarkCLIRoute")
	assert.Contains(t, string(result), "lark_execute")
	assert.NotContains(t, string(result), "沙箱当前不可用", "route denial must happen before sandbox lookup")
}
