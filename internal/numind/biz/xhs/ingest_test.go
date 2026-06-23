package xhs

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// mockXhsStore 是一个内存版 IXhsTopicStore，复刻真实 UpsertByUserNote 的
// content_hash 比对语义：新增 / hash 变化 → 覆盖并返回 true；hash 未变 → 保留
// 已有记录（含富化结果）并返回 false。用于隔离 biz 层逻辑（防重复扣分回归）。
type mockXhsStore struct {
	rows   map[string]*model.XhsTopicNote // key = userID/xhs_note_id
	nextID uint64
}

func newMockXhsStore() *mockXhsStore {
	return &mockXhsStore{rows: map[string]*model.XhsTopicNote{}, nextID: 0}
}

func key(userID uint, noteID string) string {
	return strconv.FormatUint(uint64(userID), 10) + "/" + noteID
}

func (m *mockXhsStore) UpsertByUserNote(_ context.Context, n *model.XhsTopicNote) (bool, error) {
	k := key(n.UserID, n.XhsNoteID)
	existing, ok := m.rows[k]
	if !ok {
		m.nextID++
		n.ID = m.nextID
		cp := *n
		m.rows[k] = &cp
		return true, nil
	}
	if existing.ContentHash == n.ContentHash {
		// 未变化：保留已有记录（含富化结果），回填主键。
		n.ID = existing.ID
		return false, nil
	}
	// 变化：覆盖全字段（含重置后的 enrich_status / 清空的 AI 字段）。
	n.ID = existing.ID
	cp := *n
	m.rows[k] = &cp
	return true, nil
}

func (m *mockXhsStore) ListNotes(context.Context, uint, store.XhsNoteFilter, int, int) ([]model.XhsTopicNote, int64, error) {
	return nil, 0, nil
}
func (m *mockXhsStore) ListPendingEnrich(context.Context, int) ([]model.XhsTopicNote, error) {
	return nil, nil
}
func (m *mockXhsStore) GetNote(context.Context, uint, uint64) (*model.XhsTopicNote, error) {
	return nil, nil
}
func (m *mockXhsStore) DeleteNote(context.Context, uint, uint64) error           { return nil }
func (m *mockXhsStore) UpdateEnrichStatus(context.Context, uint64, string) error { return nil }
func (m *mockXhsStore) UpdateEnrichResult(context.Context, *model.XhsTopicNote) error {
	return nil
}
func (m *mockXhsStore) GetByIDs(context.Context, uint, []uint64) ([]model.XhsTopicNote, error) {
	return nil, nil
}

var _ store.IXhsTopicStore = (*mockXhsStore)(nil)

const testUserID = uint(7)

func basePayload() NotePayload {
	return NotePayload{
		XhsNoteID: "note-1",
		Title:     "原标题",
		Content:   "原正文",
		VideoURL:  "https://v/1",
	}
}

// TestIngest_DuplicateSameHash_DoesNotResetEnrich 验证重复摄入同笔记（内容不变）：
// content_hash 不变，store 返回 false，已有富化结果不被重置（防重复扣分回归核心用例）。
func TestIngest_DuplicateSameHash_DoesNotResetEnrich(t *testing.T) {
	m := newMockXhsStore()
	b := NewXhsBiz(m)
	ctx := context.Background()

	// 首次摄入。
	n, ids, err := b.Ingest(ctx, testUserID, []NotePayload{basePayload()})
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	require.Len(t, ids, 1)

	// 模拟富化流水线已完成：标记 done + 写入 AI 字段。
	row := m.rows[key(testUserID, "note-1")]
	require.NotNil(t, row)
	row.EnrichStatus = model.XhsEnrichDone
	row.AITopicAngle = "已富化角度"

	// 再次摄入完全相同内容。
	n2, _, err := b.Ingest(ctx, testUserID, []NotePayload{basePayload()})
	require.NoError(t, err)
	assert.Equal(t, 1, n2)

	after := m.rows[key(testUserID, "note-1")]
	assert.Equal(t, model.XhsEnrichDone, after.EnrichStatus, "hash 未变不应重置 enrich_status")
	assert.Equal(t, "已富化角度", after.AITopicAngle, "hash 未变不应清空已有富化结果")
}

// TestIngest_HashChange_ResetsEnrich 验证内容变化（标题改）：content_hash 变化，
// store 覆盖记录，enrich_status 重置为 pending、AI 字段清空，触发重新富化。
func TestIngest_HashChange_ResetsEnrich(t *testing.T) {
	m := newMockXhsStore()
	b := NewXhsBiz(m)
	ctx := context.Background()

	_, _, err := b.Ingest(ctx, testUserID, []NotePayload{basePayload()})
	require.NoError(t, err)
	row := m.rows[key(testUserID, "note-1")]
	row.EnrichStatus = model.XhsEnrichDone
	// 预置全部 6 个 AI 富化字段，验证 hash 变化时一并被清空（回归覆盖缺口修复）。
	row.AITopicAngle = "旧角度"
	row.AIViralReason = "旧爆款原因"
	row.AIBorrowable = "旧可借鉴点"
	row.AITargetAudience = "旧目标人群"
	row.AITitleFormula = "旧标题公式"
	row.AIOneLine = "旧一句话总结"
	oldHash := row.ContentHash

	changed := basePayload()
	changed.Title = "新标题"
	_, _, err = b.Ingest(ctx, testUserID, []NotePayload{changed})
	require.NoError(t, err)

	after := m.rows[key(testUserID, "note-1")]
	assert.NotEqual(t, oldHash, after.ContentHash, "标题变化应改变 content_hash")
	assert.Equal(t, model.XhsEnrichPending, after.EnrichStatus, "hash 变化应重置 enrich_status=pending")
	assert.Empty(t, after.AITopicAngle, "hash 变化应清空 ai_topic_angle")
	assert.Empty(t, after.AIViralReason, "hash 变化应清空 ai_viral_reason")
	assert.Empty(t, after.AIBorrowable, "hash 变化应清空 ai_borrowable")
	assert.Empty(t, after.AITargetAudience, "hash 变化应清空 ai_target_audience")
	assert.Empty(t, after.AITitleFormula, "hash 变化应清空 ai_title_formula")
	assert.Empty(t, after.AIOneLine, "hash 变化应清空 ai_one_line")
}

// TestIngest_InvalidNoteType_Rejected 验证非枚举 note_type（如 'IMAGE'）整批拒绝、不落库，
// 避免 T4 富化流水线按 note_type 误路由。
func TestIngest_InvalidNoteType_Rejected(t *testing.T) {
	m := newMockXhsStore()
	b := NewXhsBiz(m)

	for _, bad := range []string{"IMAGE", "reel", "INVALID", "Normal"} {
		p := basePayload()
		p.NoteType = bad
		_, _, err := b.Ingest(context.Background(), testUserID, []NotePayload{p})
		require.Error(t, err, "note_type=%q 应被拒绝", bad)
		assert.True(t, errors.Is(err, errno.ErrBind), "非法 note_type 应返回 ErrBind: %q", bad)
	}
	assert.Empty(t, m.rows, "校验失败不应落库")
}

// TestIngest_ValidNoteType_Accepted 验证合法 note_type（normal/video/空）被接受。
func TestIngest_ValidNoteType_Accepted(t *testing.T) {
	for _, good := range []string{"", model.XhsNoteTypeNormal, model.XhsNoteTypeVideo} {
		m := newMockXhsStore()
		b := NewXhsBiz(m)
		p := basePayload()
		p.NoteType = good
		n, _, err := b.Ingest(context.Background(), testUserID, []NotePayload{p})
		require.NoError(t, err, "note_type=%q 应被接受", good)
		assert.Equal(t, 1, n)
	}
}

// TestIngest_PartialValidationFailure_NoCommit 验证两阶段语义：批中任一条校验失败时
// 整批拒绝、一行都不落库（即便失败条排在合法条之后）。
func TestIngest_PartialValidationFailure_NoCommit(t *testing.T) {
	m := newMockXhsStore()
	b := NewXhsBiz(m)

	good := basePayload()
	good.XhsNoteID = "note-good"
	bad := basePayload()
	bad.XhsNoteID = "note-bad"
	bad.NoteType = "ILLEGAL"

	ingested, ids, err := b.Ingest(context.Background(), testUserID, []NotePayload{good, bad})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrBind))
	assert.Equal(t, 0, ingested, "校验阶段失败应返回 0")
	assert.Empty(t, ids)
	assert.Empty(t, m.rows, "校验失败应一行都不落库（含失败条之前的合法条）")
}

// TestIngest_TextTooLarge_Rejected 验证正文超过 64KB 返回 ErrBind，且不落库。
func TestIngest_TextTooLarge_Rejected(t *testing.T) {
	m := newMockXhsStore()
	b := NewXhsBiz(m)

	p := basePayload()
	p.Content = strings.Repeat("x", maxTextBytes+1)

	_, _, err := b.Ingest(context.Background(), testUserID, []NotePayload{p})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrBind), "超长文本应返回 ErrBind")
	assert.Empty(t, m.rows, "校验失败不应落库")
}

// TestIngest_VideoTranscriptTooLarge_Rejected 验证 video_transcript 超过 64KB 返回 ErrBind。
func TestIngest_VideoTranscriptTooLarge_Rejected(t *testing.T) {
	m := newMockXhsStore()
	b := NewXhsBiz(m)

	big := strings.Repeat("y", maxTextBytes+1)
	p := basePayload()
	p.VideoTranscript = &big

	_, _, err := b.Ingest(context.Background(), testUserID, []NotePayload{p})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrBind))
}

// TestIngest_CommentsTruncated 验证评论 >10 条被截到 10 条、单条 text >200 字节被截断。
func TestIngest_CommentsTruncated(t *testing.T) {
	m := newMockXhsStore()
	b := NewXhsBiz(m)

	p := basePayload()
	for i := 0; i < 15; i++ {
		p.Comments = append(p.Comments, CommentPayload{
			Author: "u", Text: strings.Repeat("z", maxCommentBytes+50),
		})
	}

	_, _, err := b.Ingest(context.Background(), testUserID, []NotePayload{p})
	require.NoError(t, err)

	row := m.rows[key(testUserID, "note-1")]
	require.NotNil(t, row)
	require.NotNil(t, row.Comments)

	stored := decodeComments(t, []byte(row.Comments))
	assert.Len(t, stored, maxComments, "评论应截到 10 条")
	for _, c := range stored {
		assert.LessOrEqual(t, len(c.Text), maxCommentBytes, "单条评论 text 应截到 200 字节内")
	}
}

// TestIngest_TooManyNotes_Rejected 验证单次 >50 条返回 ErrBind。
func TestIngest_TooManyNotes_Rejected(t *testing.T) {
	m := newMockXhsStore()
	b := NewXhsBiz(m)

	payloads := make([]NotePayload, maxNotesPerIngest+1)
	for i := range payloads {
		p := basePayload()
		p.XhsNoteID = "note-" + strconv.Itoa(i)
		payloads[i] = p
	}

	_, _, err := b.Ingest(context.Background(), testUserID, payloads)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrBind))
}

// TestIngest_EmptyNoteID_Rejected 验证 xhs_note_id 必填。
func TestIngest_EmptyNoteID_Rejected(t *testing.T) {
	m := newMockXhsStore()
	b := NewXhsBiz(m)

	p := basePayload()
	p.XhsNoteID = "   "

	_, _, err := b.Ingest(context.Background(), testUserID, []NotePayload{p})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrBind))
}

// TestIngest_TruncateUTF8_NoBrokenRune 验证截断在多字节 rune 边界回退，不切断中文。
func TestIngest_TruncateUTF8_NoBrokenRune(t *testing.T) {
	// 「中」= 3 字节；limit=200 落在某个中文中间，应回退到完整 rune 边界。
	s := strings.Repeat("中", 100) // 300 字节
	out := truncateUTF8(s, maxCommentBytes)
	assert.LessOrEqual(t, len(out), maxCommentBytes)
	assert.True(t, utf8.ValidString(out), "截断结果应为合法 UTF-8（无半个字符）")
}

func decodeComments(t *testing.T, raw []byte) []CommentPayload {
	t.Helper()
	var cs []CommentPayload
	require.NoError(t, json.Unmarshal(raw, &cs))
	return cs
}
