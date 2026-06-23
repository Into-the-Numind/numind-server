package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/model"
)

// larkAuthPromptText is the fixed prompt text an auth-class yield must carry as
// its single Question, mirroring what a lark tool will hardcode (design §6).
// The OAuth callback rebuilds the Answer key from state.question_text, which
// equals this string, so the run can be resumed via biz.Answer.
const larkAuthPromptText = "飞书授权链接已发送，完成授权后自动恢复"

// authYieldPayload builds an auth-class YieldPayload as a lark tool would: a
// single fixed Question (the resume key) plus PauseType=auth + an AuthURL.
func authYieldPayload(authURL string) YieldPayload {
	return YieldPayload{
		PauseType: PauseTypeAuth,
		AuthURL:   authURL,
		Questions: []YieldQuestion{{
			Question: larkAuthPromptText,
			Options:  nil,
		}},
	}
}

// TestYieldPayload_AuthFields_SerializeAndRoundTrip verifies that an auth-class
// yield serializes PauseType/AuthURL into pending_question_json and that
// ParsePendingQuestion round-trips them (so the non-stream path and the answer
// validator both see the same shape). The fixed Question survives so the resume
// key remains valid.
func TestYieldPayload_AuthFields_SerializeAndRoundTrip(t *testing.T) {
	p := authYieldPayload("https://open.feishu.cn/oauth/authorize?x=1")

	raw, err := json.Marshal(p)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"pause_type":"auth"`)
	assert.Contains(t, string(raw), `"auth_url":"https://open.feishu.cn/oauth/authorize?x=1"`)

	got, err := ParsePendingQuestion(raw)
	require.NoError(t, err)
	assert.Equal(t, PauseTypeAuth, got.PauseType)
	assert.Equal(t, "https://open.feishu.cn/oauth/authorize?x=1", got.AuthURL)
	require.Len(t, got.Questions, 1)
	assert.Equal(t, larkAuthPromptText, got.Questions[0].Question)
}

// TestYieldPayload_QuestionDefault_OmitsAuthFields verifies a plain
// ask_user_question yield (PauseType empty) does not leak pause_type/auth_url
// into the wire JSON — backward compatibility for existing question cards.
func TestYieldPayload_QuestionDefault_OmitsAuthFields(t *testing.T) {
	p := YieldPayload{Questions: []YieldQuestion{{Question: "公司全称?", Options: nil}}}
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "pause_type")
	assert.NotContains(t, string(raw), "auth_url")
}

// authYieldStreamFn returns a chatStreamFn mock whose stream requests an
// ask_user_question tool call carrying the fixed auth prompt as its single
// question (mirroring how a lark tool would yield for authorization).
func authYieldStreamFn() func(context.Context, string, aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
	return func(_ context.Context, _ string, _ aiservice.ChatRequest) (<-chan aiservice.ChatChunk, error) {
		ch := make(chan aiservice.ChatChunk, 1)
		ch <- aiservice.ChatChunk{
			IsFinal:      true,
			FinishReason: "tool_calls",
			ToolCalls: []aiservice.ToolCall{{
				ID:   "call_auth_1",
				Type: "function",
				Function: aiservice.ToolCallFunction{
					Name:      "ask_user_question",
					Arguments: `{"questions":[{"question":"` + larkAuthPromptText + `"}]}`,
				},
			}},
		}
		close(ch)
		return ch, nil
	}
}

// authPauseQuestionTool is a minimal FullTool (BaseTool defaults) that yields an
// auth-class pause (PauseType=auth + AuthURL) so the streaming SSE propagation
// can be tested end-to-end without a real lark client. It registers under
// "ask_user_question" so the mock chat stream's tool call resolves to it.
type authPauseQuestionTool struct {
	BaseTool
	authURL string
}

func (t *authPauseQuestionTool) Name() string           { return "ask_user_question" }
func (t *authPauseQuestionTool) Description() string    { return "auth pause test tool" }
func (t *authPauseQuestionTool) UserFacingName() string { return "授权" }
func (t *authPauseQuestionTool) NarrationVerb() string  { return "授权" }
func (t *authPauseQuestionTool) Execute(_ context.Context, _ ToolInput) (ToolResult, error) {
	return nil, &yieldError{Payload: authYieldPayload(t.authURL)}
}

// TestRunStream_AuthPause_SSEHasPauseTypeAndAuthURL verifies the streaming
// path propagates PauseType/AuthURL onto the question_prompt SSE event. Without
// the fix the struct lacks the fields and the frontend (T13) can never read
// pause_type to render an authorization card.
func TestRunStream_AuthPause_SSEHasPauseTypeAndAuthURL(t *testing.T) {
	withMockChatStreamFn(t, authYieldStreamFn())

	ms := newMockStore()
	run := makeRunForStream(t, ms)
	authURL := "https://open.feishu.cn/oauth/authorize?app_id=cli_x&state=abc"
	reg := newStaticRegistry(&authPauseQuestionTool{authURL: authURL})
	runner := NewAgentRunner(ms, reg)

	ch := make(chan stream.Event, 256)
	result, err := runner.RunStream(context.Background(), RunRequest{
		UserID:    1,
		Input:     "帮我把这篇稿子发到飞书文档",
		ToolNames: []string{"ask_user_question"},
	}, run.ID, ch)
	close(ch)

	require.NoError(t, err, "an auth yield is a pause, not an error")
	require.NotNil(t, result)
	assert.Equal(t, TerminalWaitingForUserChoice, result.TerminalReason)

	// pending_question_json must carry pause_type/auth_url for the non-stream/reload path.
	stored, gErr := ms.Get(context.Background(), run.ID)
	require.NoError(t, gErr)
	require.NotEmpty(t, stored.PendingQuestionJSON)
	storedPayload, perr := ParsePendingQuestion(stored.PendingQuestionJSON)
	require.NoError(t, perr)
	assert.Equal(t, PauseTypeAuth, storedPayload.PauseType)
	assert.Equal(t, authURL, storedPayload.AuthURL)

	// The SSE question_prompt event MUST carry pause_type/auth_url.
	var found *stream.QuestionPromptPayload
	for ev := range ch {
		if ev.Type == stream.EventQuestionPrompt {
			var p stream.QuestionPromptPayload
			require.NoError(t, json.Unmarshal(ev.Data, &p))
			found = &p
		}
	}
	require.NotNil(t, found, "question_prompt event must reach the SSE channel")
	assert.Equal(t, PauseTypeAuth, found.PauseType, "SSE payload must carry pause_type=auth")
	assert.Equal(t, authURL, found.AuthURL, "SSE payload must carry auth_url")
}

// TestAnswer_AuthPause_ResumesViaQuestionText verifies the OAuth-callback resume
// path: an auth yield persisted with a fixed Question can be resumed by calling
// biz.Answer with that exact question text as the key (what the callback derives
// from state.question_text). This is the §6 recovery-key contract.
func TestAnswer_AuthPause_ResumesViaQuestionText(t *testing.T) {
	rs := newAnswerRunStore()
	runner := &answerRunner{}
	svc := newAnswerServiceWithRunner(rs, runner)
	userID := uint(55)

	// Seed a waiting run whose pending_question_json is an auth-class yield.
	authPayload := authYieldPayload("https://open.feishu.cn/oauth/authorize?state=zzz")
	rawPending, err := json.Marshal(authPayload)
	require.NoError(t, err)
	seed := &model.AgentRun{
		UserID:              userID,
		SessionID:           "sess-auth-resume",
		Status:              "terminated",
		StateReason:         string(TerminalWaitingForUserChoice),
		Messages:            datatypes.JSON(`[]`),
		PendingQuestionJSON: datatypes.JSON(rawPending),
		StartedAt:           time.Now(),
	}
	require.NoError(t, rs.Create(context.Background(), seed))
	runID := seed.ID

	// The callback rebuilds this exact key from state.question_text.
	resp, err := svc.Answer(context.Background(), userID, runID, AnswerRequest{
		Answers: map[string]AnswerItem{
			larkAuthPromptText: {FreeText: "已授权"},
		},
	})
	require.NoError(t, err, "auth yield must resume when answered with question_text key")
	require.NotNil(t, resp)
	assert.Equal(t, "resumed", resp.Status)
	assert.Contains(t, rs.answerAndClearCalls, runID)
}

// TestSynthesizeQuestionPrompt_AuthPause_CarriesPauseTypeAndAuthURL covers the
// session-RELOAD path (design §10 auto续显): a run paused on an auth yield, when
// reloaded/polled, must synthesize a question_prompt card carrying pause_type=auth
// + auth_url so the frontend renders an authorization card (not a question card).
func TestSynthesizeQuestionPrompt_AuthPause_CarriesPauseTypeAndAuthURL(t *testing.T) {
	authURL := "https://open.feishu.cn/oauth/authorize?app_id=cli_y&state=def"
	pending, err := json.Marshal(authYieldPayload(authURL))
	require.NoError(t, err)
	run := &model.AgentRun{
		ID:                  321,
		StateReason:         string(TerminalWaitingForUserChoice),
		PendingQuestionJSON: datatypes.JSON(pending),
		StartedAt:           time.Now(),
	}

	msg, ok := synthesizeQuestionPrompt(run)
	require.True(t, ok)
	assert.Equal(t, PauseTypeAuth, msg.PauseType)
	assert.Equal(t, authURL, msg.AuthURL)

	// And the marshalled snapshot card must round-trip both fields for the
	// frontend (decode rather than substring-match: json.Marshal HTML-escapes
	// the `&` in the URL to &, which a raw substring check would miss).
	b, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"pause_type":"auth"`)
	var decoded struct {
		PauseType string `json:"pause_type"`
		AuthURL   string `json:"auth_url"`
	}
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, PauseTypeAuth, decoded.PauseType)
	assert.Equal(t, authURL, decoded.AuthURL)
}

// TestSynthesizeQuestionPrompt_QuestionPause_OmitsAuthFields ensures an ordinary
// question pause reload does NOT leak pause_type/auth_url (backward compatible).
func TestSynthesizeQuestionPrompt_QuestionPause_OmitsAuthFields(t *testing.T) {
	run := &model.AgentRun{
		ID:                  322,
		StateReason:         string(TerminalWaitingForUserChoice),
		PendingQuestionJSON: datatypes.JSON(`{"questions":[{"question":"公司全称?","options":[],"multi_select":false}]}`),
		StartedAt:           time.Now(),
	}
	msg, ok := synthesizeQuestionPrompt(run)
	require.True(t, ok)
	assert.Empty(t, msg.PauseType)
	assert.Empty(t, msg.AuthURL)
	b, err := json.Marshal(msg)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "pause_type")
	assert.NotContains(t, string(b), "auth_url")
}
