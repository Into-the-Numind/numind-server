package agent

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/numind/biz/feishu"
	"numind-server/internal/numind/store"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test double: feishuConnector seam --------------------------------------

// fakeConnector scripts the feishuConnector seam (the biz/feishu
// ConnectOrchestrator) so the tool's orchestration is unit-tested without a real
// lark-cli / store / signer.
type fakeConnector struct {
	step    *feishu.ConnectStep
	stepErr error

	persisted    bool
	persistErr   error
	pollCalls    int
	gotPollUser  uint
	gotStepUser  uint
	gotStepRun   uint64
	gotStepQText string
}

func (f *fakeConnector) PollAndPersistApp(_ context.Context, userID uint) (bool, error) {
	f.pollCalls++
	f.gotPollUser = userID
	return f.persisted, f.persistErr
}

func (f *fakeConnector) NextConnectStep(_ context.Context, userID uint, runID uint64, questionText string) (*feishu.ConnectStep, error) {
	f.gotStepUser, f.gotStepRun, f.gotStepQText = userID, runID, questionText
	return f.step, f.stepErr
}

// ctxWithUserAndRun builds a context carrying both the userID and run ID, exactly
// as the runner injects them before tool execution.
func ctxWithUserAndRun(uid uint, runID uint64) context.Context {
	return WithRunID(ctxWithUser(uid), runID)
}

// --- create_app phase: yields an auth pause with the device-code page URL ----

func TestFeishuConnect_CreateApp_YieldsAuthPause(t *testing.T) {
	conn := &fakeConnector{
		step: &feishu.ConnectStep{Phase: feishu.ConnectPhaseCreateApp, URL: "https://open.feishu.cn/page/cli?user_code=ABC"},
	}
	tool := &feishuConnectTool{connector: conn}

	_, err := tool.Execute(ctxWithUserAndRun(7, 99), ToolInput(`{}`))

	// A yield is a *yieldError sentinel (a pause, NOT a soft error / success).
	var ye *yieldError
	require.True(t, errors.As(err, &ye), "create_app must yield (pause), got err=%v", err)
	assert.Equal(t, PauseTypeAuth, ye.Payload.PauseType, "must be an auth-class pause")
	assert.Equal(t, "https://open.feishu.cn/page/cli?user_code=ABC", ye.Payload.AuthURL)
	require.Len(t, ye.Payload.Questions, 1, "auth pause carries exactly one fixed-prompt question (the resume key)")
	assert.Equal(t, feishuConnectPromptText, ye.Payload.Questions[0].Question, "question text must be the fixed resume key")
	assert.Empty(t, ye.Payload.Questions[0].Options, "auth pause has no options")

	// The tool polls first (idempotent app-create bridge), then asks for the step.
	assert.Equal(t, 1, conn.pollCalls)
	assert.Equal(t, uint(7), conn.gotPollUser)
	assert.Equal(t, uint(7), conn.gotStepUser)
	assert.Equal(t, uint64(99), conn.gotStepRun, "runID must reach NextConnectStep so the callback resumes the right run")
	assert.Equal(t, feishuConnectPromptText, conn.gotStepQText, "questionText (answer key) must match the yield question")
}

// --- authorize phase: yields an auth pause with the 飞书 authorize URL --------

func TestFeishuConnect_Authorize_YieldsAuthPause(t *testing.T) {
	authURL := "https://open.feishu.cn/open-apis/authen/v1/authorize?app_id=cli_x&state=signed"
	conn := &fakeConnector{step: &feishu.ConnectStep{Phase: feishu.ConnectPhaseAuthorize, URL: authURL}}
	tool := &feishuConnectTool{connector: conn}

	_, err := tool.Execute(ctxWithUserAndRun(8, 100), ToolInput(`{}`))

	var ye *yieldError
	require.True(t, errors.As(err, &ye), "authorize must yield (pause), got err=%v", err)
	assert.Equal(t, PauseTypeAuth, ye.Payload.PauseType)
	assert.Equal(t, authURL, ye.Payload.AuthURL)
	require.Len(t, ye.Payload.Questions, 1)
	assert.Equal(t, feishuConnectPromptText, ye.Payload.Questions[0].Question)
}

// --- done phase: returns a non-yield success result (no pause) ---------------

func TestFeishuConnect_Done_ReturnsConnectedSuccess(t *testing.T) {
	conn := &fakeConnector{step: &feishu.ConnectStep{Phase: feishu.ConnectPhaseDone}}
	tool := &feishuConnectTool{connector: conn}

	res, err := tool.Execute(ctxWithUserAndRun(9, 101), ToolInput(`{}`))
	require.NoError(t, err, "done must NOT yield (no pause) and must NOT be a Go error")

	// Success result is a plain JSON object the LLM reads; it must NOT be a soft
	// error and must NOT leak any secret/token.
	assert.Empty(t, decodeErr(t, res), "done is success, not a soft error")
	assert.Contains(t, string(res), "已连接")
	assertNoSensitive(t, string(res))
}

// --- already-connected fast path: a re-call returns done without yielding -----

func TestFeishuConnect_AlreadyConnected_NoYield(t *testing.T) {
	// persisted=false (nothing new), step=done (token already present).
	conn := &fakeConnector{step: &feishu.ConnectStep{Phase: feishu.ConnectPhaseDone}}
	tool := &feishuConnectTool{connector: conn}

	_, err := tool.Execute(ctxWithUserAndRun(9, 101), ToolInput(`{}`))
	require.NoError(t, err, "already-connected re-call must return done, not yield")
	var ye *yieldError
	assert.False(t, errors.As(err, &ye), "must not yield when already connected")
}

// --- failure policy: every failure is a SOFT result, never a Go error --------

func TestFeishuConnect_NilConnector_SoftError(t *testing.T) {
	tool := &feishuConnectTool{connector: nil} // integration off
	res, err := tool.Execute(ctxWithUserAndRun(7, 99), ToolInput(`{}`))
	require.NoError(t, err, "a nil connector must soft-error, never kill the run")
	assert.NotEmpty(t, decodeErr(t, res))
}

func TestFeishuConnect_NoUser_SoftError(t *testing.T) {
	conn := &fakeConnector{step: &feishu.ConnectStep{Phase: feishu.ConnectPhaseCreateApp, URL: "u"}}
	tool := &feishuConnectTool{connector: conn}
	// ctx without a userID.
	res, err := tool.Execute(WithRunID(context.Background(), 5), ToolInput(`{}`))
	require.NoError(t, err)
	assert.NotEmpty(t, decodeErr(t, res), "missing user identity must soft-error")
	assert.Equal(t, 0, conn.pollCalls, "must not touch the connector without a user")
}

func TestFeishuConnect_PollError_SoftError(t *testing.T) {
	conn := &fakeConnector{persistErr: errors.New("poll boom")}
	tool := &feishuConnectTool{connector: conn}
	res, err := tool.Execute(ctxWithUserAndRun(7, 99), ToolInput(`{}`))
	require.NoError(t, err, "a poll failure must soft-error, never kill the run")
	assert.NotEmpty(t, decodeErr(t, res))
	// The raw infra error must NOT leak into the LLM-facing tool result; it goes
	// to the log + trace only (sanitize at the tool boundary).
	assert.NotContains(t, string(res), "poll boom", "infra error detail must not leak to the LLM")
}

func TestFeishuConnect_StepError_SoftError(t *testing.T) {
	conn := &fakeConnector{stepErr: errors.New("start provision failed")}
	tool := &feishuConnectTool{connector: conn}
	res, err := tool.Execute(ctxWithUserAndRun(7, 99), ToolInput(`{}`))
	require.NoError(t, err, "a step failure must soft-error, never kill the run")
	assert.NotEmpty(t, decodeErr(t, res))
	assert.NotContains(t, string(res), "start provision failed", "infra error detail must not leak to the LLM")
}

// --- a create_app/authorize step with a blank URL must soft-error (never yield
//     a useless empty-URL auth card) ----------------------------------------

func TestFeishuConnect_EmptyURL_SoftError(t *testing.T) {
	conn := &fakeConnector{step: &feishu.ConnectStep{Phase: feishu.ConnectPhaseAuthorize, URL: ""}}
	tool := &feishuConnectTool{connector: conn}
	res, err := tool.Execute(ctxWithUserAndRun(7, 99), ToolInput(`{}`))
	require.NoError(t, err)
	assert.NotEmpty(t, decodeErr(t, res), "an empty URL step must soft-error, not yield an empty auth card")
}

// --- tool metadata: read-only? no — it provisions/persists; not concurrency-safe

func TestFeishuConnect_Metadata(t *testing.T) {
	tool := &feishuConnectTool{}
	assert.Equal(t, "feishu_connect", tool.Name())
	assert.NotEmpty(t, tool.Description())
	assert.NotEmpty(t, tool.UserFacingName())
	assert.False(t, tool.IsReadOnly(), "connecting writes credentials → not read-only")
	assert.False(t, tool.IsConcurrencySafe(ToolInput(`{}`)), "must not run concurrently with itself")
	var _ FullTool = tool
}

// --- factory registration ---------------------------------------------------

// TestPlatformFactory_DoesNotRegisterLegacyFeishuConnect verifies the old
// connector seam cannot re-enable the retired factory registration.
func TestPlatformFactory_DoesNotRegisterLegacyFeishuConnect(t *testing.T) {
	db := newFactoryTestDB(t)
	ds := store.NewTestStore(db)
	f := &platformToolFactory{
		ds:                      ds,
		larkProviderOverride:    &fakeLarkProvider{api: &fakeLarkAPI{}},
		feishuConnectorOverride: &fakeConnector{step: &feishu.ConnectStep{Phase: feishu.ConnectPhaseDone}},
	}

	tools, metadata, err := f.LoadTools(context.Background())
	require.NoError(t, err)
	assert.Len(t, tools, 22)
	assert.Len(t, metadata, 22)

	got := map[string]bool{}
	for _, tl := range tools {
		got[tl.Name()] = true
	}
	assert.False(t, got["feishu_connect"], "legacy feishu_connect must not be registered")
}

// TestPlatformFactory_NoFeishuConnect_WhenConnectorAbsent verifies feishu_connect
// is NOT registered when the connector cannot be built (flag off / no Redis),
// even if the old lark provider seam is present — count stays at 21.
func TestPlatformFactory_NoFeishuConnect_WhenConnectorAbsent(t *testing.T) {
	db := newFactoryTestDB(t)
	ds := store.NewTestStore(db)
	f := &platformToolFactory{ds: ds, larkProviderOverride: &fakeLarkProvider{api: &fakeLarkAPI{}}}

	tools, _, err := f.LoadTools(context.Background())
	require.NoError(t, err)
	assert.Len(t, tools, 22, "legacy provider must not register old lark tools")
	for _, tl := range tools {
		assert.NotEqual(t, "feishu_connect", tl.Name())
	}
}

// assertNoSensitive is a defensive guard that a tool result the LLM sees carries
// no obvious secret material. The fixture connector never produces secrets, so
// this just locks in that the tool itself never echoes one.
func assertNoSensitive(t *testing.T, s string) {
	t.Helper()
	for _, bad := range []string{"appSecret", "app_secret", "access_token", "refresh_token"} {
		assert.NotContains(t, s, bad, "tool result must not leak %q to the LLM", bad)
	}
}
