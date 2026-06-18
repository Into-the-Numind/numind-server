package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFinalizeRun_UserTurnExcludesSystemPromptAndCarriesAttachments reproduces the
// agent-output-ux-fixes 问题二 bug (User-reported, dev testing 2026-06-18): when a
// user uploads an attachment, buildAgentInput appends a "【系统提示】…file_read…" hint
// to the LLM-facing Input. That composed string was persisted VERBATIM into
// agent_run.messages as the user turn (finalizeRun), so the internal hint + raw
// attachment URLs leaked into the user's chat bubble on screen and reload.
//
// Drives the REAL ReAct loop (newReActRunner + mock chatFn) so persistence flows
// through finalizeRun → buildTranscriptTurns. Pre-fix this FAILS: the persisted user
// turn content == Input (with the hint) and there is no attachments key. Post-fix the
// user turn shows ONLY the human's DisplayInput + an attachments array, while the LLM
// still receives the full hint (Input unchanged — asserted separately).
func TestFinalizeRun_UserTurnExcludesSystemPromptAndCarriesAttachments(t *testing.T) {
	store := newMockStore()
	runner, toolName := newReActRunner(store)
	withMockChatFn(t, successChatFn("已读取附件，要点如下：……"))

	raw := "请总结这个附件"
	attURL := "https://x.cos.ap-guangzhou.myqcloud.com/agent-attachments/7/report.pdf"
	input := buildAgentInput(raw, []string{attURL})
	require.Contains(t, input, "【系统提示】", "buildAgentInput should carry the hint (leak source)")

	display := raw
	req := newReActRequest(toolName, input)
	req.SessionID = "leak-1"
	req.DisplayInput = &display
	req.DisplayAttachments = []displayAttachment{{URL: attURL, Filename: "report.pdf"}}

	result, err := runner.Run(context.Background(), req)
	require.NoError(t, err)

	run, err := store.Get(context.Background(), result.AgentRunID)
	require.NoError(t, err)
	require.NotEmpty(t, run.Messages)

	var msgs []map[string]any
	require.NoError(t, json.Unmarshal(run.Messages, &msgs))

	var userTurn map[string]any
	for _, m := range msgs {
		if role, _ := m["role"].(string); role == "user" {
			userTurn = m
			break
		}
	}
	require.NotNil(t, userTurn, "persisted transcript must contain a user turn")

	content, _ := userTurn["content"].(string)
	assert.Equal(t, raw, content, "persisted user turn must be the human's raw message")
	assert.NotContains(t, content, "【系统提示】", "system-prompt hint must NOT leak into the user bubble")
	assert.NotContains(t, content, "file_read", "file_read hint must NOT leak into the user bubble")
	assert.NotContains(t, content, attURL, "raw attachment URL must NOT leak into the user bubble text")

	atts, ok := userTurn["attachments"].([]any)
	require.True(t, ok, "user turn must carry an attachments array for chip rendering")
	require.Len(t, atts, 1)
	att0, _ := atts[0].(map[string]any)
	assert.Equal(t, "report.pdf", att0["filename"])
	assert.Equal(t, attURL, att0["url"])

	// The LLM still receives the full hint — Input is unchanged (no regression of the
	// file_read instruction the model relies on to read the attachment).
	einoMsgs := buildEinoMessages(RunRequest{Input: input})
	require.NotEmpty(t, einoMsgs)
	assert.Contains(t, einoMsgs[len(einoMsgs)-1].Content, "【系统提示】",
		"LLM-facing message must still carry the file_read hint")
}

// TestRunRequest_displayUserText covers the fallback semantics: nil DisplayInput
// falls back to Input (resume/test paths), a non-nil value (incl. empty) is honored.
func TestRunRequest_displayUserText(t *testing.T) {
	// nil → Input
	assert.Equal(t, "compiled-input", RunRequest{Input: "compiled-input"}.displayUserText())
	// non-nil non-empty → DisplayInput
	d := "raw msg"
	assert.Equal(t, "raw msg", RunRequest{Input: "compiled-input", DisplayInput: &d}.displayUserText())
	// non-nil empty → "" (attachment-only send shows empty text, not the LLM hint)
	empty := ""
	assert.Equal(t, "", RunRequest{Input: "compiled-input", DisplayInput: &empty}.displayUserText())
}

// TestTransformMessages_UserAttachmentsParsed verifies the read path reconstructs
// attachment chips from a persisted user turn's "attachments" key (问题二).
func TestTransformMessages_UserAttachmentsParsed(t *testing.T) {
	raw := []byte(`[{"role":"user","content":"请总结","attachments":[{"url":"https://x/a.pdf","filename":"a.pdf"}]},{"role":"assistant","content":"好的"}]`)
	msgs := transformMessages(raw, 1, time.Time{}, nil, "terminated", "completed")
	require.GreaterOrEqual(t, len(msgs), 1)
	var user *agentMessage
	for i := range msgs {
		if msgs[i].Type == "user" {
			user = &msgs[i]
			break
		}
	}
	require.NotNil(t, user)
	assert.Equal(t, "请总结", user.Text)
	require.Len(t, user.Attachments, 1)
	assert.Equal(t, "a.pdf", user.Attachments[0].Filename)
	assert.Equal(t, "https://x/a.pdf", user.Attachments[0].URL)
}
