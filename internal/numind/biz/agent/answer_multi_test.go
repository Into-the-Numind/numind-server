package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/datatypes"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/model"
)

// agent-multi-question T2: the answer endpoint accepts an `answers` map keyed by
// question text (Claude Code's model). These tests pin the multi-question answer
// protocol, per-question validation, and the resumed LLM message.

// seedAnswerRunWithPending seeds a waiting run with explicit pending JSON.
func seedAnswerRunWithPending(rs *answerRunStore, userID uint, pendingJSON string) uint64 {
	run := &model.AgentRun{
		UserID:              userID,
		SessionID:           "sess-multi",
		Status:              "terminated",
		StateReason:         string(TerminalWaitingForUserChoice),
		Messages:            datatypes.JSON(`[]`),
		PendingQuestionJSON: datatypes.JSON(pendingJSON),
		StartedAt:           time.Now(),
	}
	_ = rs.Create(context.Background(), run)
	return run.ID
}

const twoQuestionPending = `{"questions":[` +
	`{"question":"你们的陪跑周期多长？","options":[{"key":"a","label":"90天"},{"key":"b","label":"180天"}]},` +
	`{"question":"主要客群是谁？","multi_select":true,"options":[{"key":"a","label":"宝妈"},{"key":"b","label":"职场人"}]}` +
	`]}`

func TestAnswer_MultiQuestion_HappyPath(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRunWithPending(rs, userID, twoQuestionPending)

	req := AnswerRequest{Answers: map[string]AnswerItem{
		"你们的陪跑周期多长？": {Selected: []string{"90天"}},
		"主要客群是谁？":    {Selected: []string{"宝妈", "职场人"}, FreeText: "主要一二线城市"},
	}}
	resp, err := svc.Answer(context.Background(), userID, runID, req)

	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "resumed", resp.Status)
	assert.Contains(t, rs.answerAndClearCalls, runID)
}

// parsePending parses pending JSON into a YieldPayload for direct
// buildAnswerMessage tests (Answer() threads the already-parsed payload).
func parsePending(t *testing.T, s string) YieldPayload {
	t.Helper()
	p, err := ParsePendingQuestion([]byte(s))
	require.NoError(t, err)
	return p
}

func TestBuildAnswerMessage_MultiQuestion_RendersAllInOrder(t *testing.T) {
	answers := map[string]AnswerItem{
		"主要客群是谁？":    {Selected: []string{"宝妈", "职场人"}, FreeText: "主要一二线城市"},
		"你们的陪跑周期多长？": {Selected: []string{"90天"}},
	}
	msg := buildAnswerMessage(parsePending(t, twoQuestionPending), answers)

	assert.Contains(t, msg, "用户已回答")
	assert.Contains(t, msg, "「你们的陪跑周期多长？」")
	assert.Contains(t, msg, "90天")
	assert.Contains(t, msg, "「主要客群是谁？」")
	assert.Contains(t, msg, "宝妈、职场人")
	assert.Contains(t, msg, "主要一二线城市")
	// Order follows the pending questions, not the map iteration order.
	assert.Less(t, indexOf(msg, "陪跑周期"), indexOf(msg, "主要客群"),
		"questions render in pending order")
}

func TestBuildAnswerMessage_FreeTextOnlyQuestion(t *testing.T) {
	pending := `{"questions":[{"question":"请提供你们的价格区间","options":[]}]}`
	answers := map[string]AnswerItem{"请提供你们的价格区间": {FreeText: "3000-5000元"}}
	msg := buildAnswerMessage(parsePending(t, pending), answers)
	assert.Contains(t, msg, "「请提供你们的价格区间」")
	assert.Contains(t, msg, "3000-5000元")
}

func TestAnswer_PartialSkip_Allowed(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRunWithPending(rs, userID, twoQuestionPending)

	// Only answer the first question; the second is skipped (omitted from map).
	req := AnswerRequest{Answers: map[string]AnswerItem{
		"你们的陪跑周期多长？": {Selected: []string{"90天"}},
	}}
	resp, err := svc.Answer(context.Background(), userID, runID, req)
	require.NoError(t, err, "skipping a question (omitting its key) is allowed")
	require.NotNil(t, resp)
}

func TestAnswer_EmptyAnswers_Rejected(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRunWithPending(rs, userID, twoQuestionPending)

	_, err := svc.Answer(context.Background(), userID, runID, AnswerRequest{Answers: map[string]AnswerItem{}})
	require.Error(t, err, "answering zero questions must be rejected")
}

func TestAnswer_UnknownQuestionKey_Rejected(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRunWithPending(rs, userID, twoQuestionPending)

	req := AnswerRequest{Answers: map[string]AnswerItem{
		"这个问题根本没问过？": {Selected: []string{"x"}},
	}}
	_, err := svc.Answer(context.Background(), userID, runID, req)
	require.Error(t, err, "an answer keyed by a question that was never asked must be rejected")
}

func TestAnswer_PresentButEmptyAnswer_Rejected(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRunWithPending(rs, userID, twoQuestionPending)

	req := AnswerRequest{Answers: map[string]AnswerItem{
		"你们的陪跑周期多长？": {Selected: nil, FreeText: "   "},
	}}
	_, err := svc.Answer(context.Background(), userID, runID, req)
	require.Error(t, err, "a present answer with neither a selection nor free text must be rejected")
}

func TestAnswer_TooManySelected_Rejected(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRunWithPending(rs, userID, twoQuestionPending)

	req := AnswerRequest{Answers: map[string]AnswerItem{
		"主要客群是谁？": {Selected: []string{"a", "b", "c", "d", "e"}},
	}}
	_, err := svc.Answer(context.Background(), userID, runID, req)
	require.Error(t, err, "more than 4 selected options for a question must be rejected")
}

// A multi-select question accepts exactly 4 selections (the max-valid boundary).
func TestAnswer_FourSelected_Accepted(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	// 主要客群是谁？ is multi_select=true in twoQuestionPending.
	runID := seedAnswerRunWithPending(rs, userID, twoQuestionPending)

	req := AnswerRequest{Answers: map[string]AnswerItem{
		"主要客群是谁？": {Selected: []string{"宝妈", "职场人", "学生", "白领"}},
	}}
	resp, err := svc.Answer(context.Background(), userID, runID, req)
	require.NoError(t, err, "exactly 4 selections on a multi-select question is valid")
	require.NotNil(t, resp)
}

// A single-select question rejects more than one selected option.
func TestAnswer_SingleSelect_MultipleSelected_Rejected(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	// 你们的陪跑周期多长？ is single-select (no multi_select) in twoQuestionPending.
	runID := seedAnswerRunWithPending(rs, userID, twoQuestionPending)

	req := AnswerRequest{Answers: map[string]AnswerItem{
		"你们的陪跑周期多长？": {Selected: []string{"90天", "180天"}},
	}}
	_, err := svc.Answer(context.Background(), userID, runID, req)
	require.Error(t, err, "a single-select question must reject more than one selected option")
}

// Answer() rejects a run whose pending_question_json is corrupt (not a resume).
func TestAnswer_CorruptPendingJSON_Rejected(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	runID := seedAnswerRunWithPending(rs, userID, `{not valid json`)

	req := AnswerRequest{Answers: map[string]AnswerItem{"任意问题?": {Selected: []string{"x"}}}}
	_, err := svc.Answer(context.Background(), userID, runID, req)
	require.Error(t, err, "a corrupt pending_question_json must be rejected")
	assert.Contains(t, err.Error(), "corrupt")
}

// A legacy single-question pending row (pre-agent-multi-question) is still
// answerable via the answers map keyed by its question text.
func TestAnswer_LegacySingleQuestionPending_AnswerableViaMap(t *testing.T) {
	rs := newAnswerRunStore()
	svc := newAnswerService(rs)
	userID := uint(42)
	legacy := `{"question":"公司全称?","options":[{"key":"a","label":"我口述"},{"key":"b","label":"上传"}],"multi_select":false}`
	runID := seedAnswerRunWithPending(rs, userID, legacy)

	req := AnswerRequest{Answers: map[string]AnswerItem{
		"公司全称?": {Selected: []string{"我口述"}, FreeText: "莫小派科技"},
	}}
	resp, err := svc.Answer(context.Background(), userID, runID, req)
	require.NoError(t, err, "legacy single-question pending must be answerable via the answers map")
	require.NotNil(t, resp)
}

// indexOf is a tiny helper for order assertions.
func indexOf(haystack, needle string) int { return strings.Index(haystack, needle) }
