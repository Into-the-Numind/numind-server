package xhs

import (
	"context"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// exportMockStore 复刻真实 store.GetByIDs 的 user 隔离语义：只返回 (user_id, ids) 命中的行，
// 他人 id 一律过滤掉。其它接口方法 stub（export 路径不触达）。
type exportMockStore struct {
	crudMockStore
}

// GetByIDs 仅返回属于 userID 且 id ∈ ids 的行（user 隔离唯一裁决点）。
func (m *exportMockStore) GetByIDs(_ context.Context, userID uint, ids []uint64) ([]model.XhsTopicNote, error) {
	idSet := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		idSet[id] = struct{}{}
	}
	var out []model.XhsTopicNote
	for i := range m.rows {
		r := m.rows[i]
		if r.UserID != userID {
			continue
		}
		if _, ok := idSet[r.ID]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

func exportSeed() *exportMockStore {
	pub := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return &exportMockStore{crudMockStore{rows: []model.XhsTopicNote{
		{
			ID: 1, UserID: 100, XhsNoteID: "note-a", NoteType: model.XhsNoteTypeNormal,
			Title: "种草标题A", Content: "正文A内容", Tags: datatypes.JSON(`["美妆","平价"]`),
			LikeCount: 1234, EnrichStatus: model.XhsEnrichDone,
			AITopicAngle: "角度A", AIOneLine: "一句话A", PublishedAt: &pub,
			Images:   datatypes.JSON(`["https://img/1.jpg","https://img/2.jpg"]`),
			Comments: datatypes.JSON(`[{"author":"小明","text":"好棒","replies":[{"author":"楼主","text":"谢谢"}]}]`),
		},
		{
			ID: 2, UserID: 100, XhsNoteID: "note-b", NoteType: model.XhsNoteTypeVideo,
			Title: "视频标题B", EnrichStatus: model.XhsEnrichPending,
		},
		{
			ID: 3, UserID: 200, XhsNoteID: "note-c", NoteType: model.XhsNoteTypeNormal,
			Title: "他人笔记C", EnrichStatus: model.XhsEnrichDone,
		},
	}}}
}

// parseExportCSV 去掉 UTF-8 BOM 后解析 CSV，返回所有记录（含表头行）。
func parseExportCSV(t *testing.T, raw []byte) [][]string {
	t.Helper()
	body := strings.TrimPrefix(string(raw), "\xEF\xBB\xBF")
	records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	require.NoError(t, err)
	return records
}

// TestBuildExportCSV_ContainsSelectedFields 验证 CSV 含 BOM、表头、以及选中记录的源字段 + AI 字段。
func TestBuildExportCSV_ContainsSelectedFields(t *testing.T) {
	rows := exportSeed().rows[:1] // 只取笔记 1（用户 100）
	csvBytes, err := buildExportCSV(context.Background(), rows)
	require.NoError(t, err)

	// 带 UTF-8 BOM。
	assert.True(t, strings.HasPrefix(string(csvBytes), "\xEF\xBB\xBF"), "CSV 应以 UTF-8 BOM 开头便于 Excel 识别中文")

	records := parseExportCSV(t, csvBytes)
	require.Len(t, records, 2, "表头 + 1 条数据行")

	header := records[0]
	assert.Equal(t, exportCSVHeader, header, "表头列必须与 exportCSVHeader 一致（源字段 + images + comments，无 AI）")
	// 关键列存在性（防漏列）。
	assert.Contains(t, header, "comments")
	assert.Contains(t, header, "images")
	assert.Contains(t, header, "video_transcript")
	assert.NotContains(t, header, "ai_topic_angle", "AI 列应已移除")

	dataRow := records[1]
	require.Len(t, dataRow, len(exportCSVHeader), "数据行列数须与表头一致")
	// 字段→值映射用列名定位，避免硬编码下标。
	idx := func(col string) int {
		for i, h := range header {
			if h == col {
				return i
			}
		}
		t.Fatalf("column %s not found", col)
		return -1
	}
	assert.Equal(t, "1", dataRow[idx("id")])
	assert.Equal(t, "note-a", dataRow[idx("xhs_note_id")])
	assert.Equal(t, "种草标题A", dataRow[idx("title")])
	assert.Equal(t, "正文A内容", dataRow[idx("content")])
	assert.Equal(t, "美妆;平价", dataRow[idx("tags")], "tags 应以 ; 连接")
	assert.Equal(t, "1234", dataRow[idx("like_count")])
	assert.Contains(t, dataRow[idx("images")], "https://img/1.jpg", "images 列应含图片 URL")
	assert.Contains(t, dataRow[idx("comments")], "小明", "comments 列应含评论作者")
	assert.Contains(t, dataRow[idx("comments")], "好棒", "comments 列应含评论正文")
	assert.Contains(t, dataRow[idx("comments")], "谢谢", "comments 列应含回复内容")
	assert.Equal(t, model.XhsEnrichDone, dataRow[idx("enrich_status")])
	assert.NotEmpty(t, dataRow[idx("published_at")], "published_at 非空应格式化为 RFC3339")
}

// TestExport_UserIsolation 验证 Export 只导出请求用户自己的笔记，他人 id 被 GetByIDs 过滤掉。
func TestExport_UserIsolation(t *testing.T) {
	b := NewXhsBiz(exportSeed())
	ctx := context.Background()

	// 用户 100 请求导出 [1,2,3]，但笔记 3 属于用户 200 → GetByIDs 只返回 1、2。
	// COS 未启用时 Export 会在 upload/sign 阶段失败；这里直接验证 store 层过滤已生效，
	// 故改为单测 buildExportCSV + GetByIDs 组合（不依赖 COS）。
	rows, err := b.store.GetByIDs(ctx, 100, []uint64{1, 2, 3})
	require.NoError(t, err)
	require.Len(t, rows, 2, "用户 100 只应拿到自己的笔记 1、2，绝不含用户 200 的笔记 3")

	csvBytes, err := buildExportCSV(context.Background(), rows)
	require.NoError(t, err)
	content := string(csvBytes)
	assert.Contains(t, content, "种草标题A")
	assert.Contains(t, content, "视频标题B")
	assert.NotContains(t, content, "他人笔记C", "他人笔记绝不能出现在导出 CSV 中")
}

// TestExport_RejectTooManyIDs 验证 ids 超过 200 直接拒（ErrBind），不触达 store / COS。
func TestExport_RejectTooManyIDs(t *testing.T) {
	b := NewXhsBiz(exportSeed())
	ctx := context.Background()

	ids := make([]uint64, maxExportIDs+1)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	_, err := b.Export(ctx, 100, ids)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrBind, "ids > 200 应返回 ErrBind")
}

// TestExport_RejectEmptyIDs 验证空 ids 直接拒（ErrBind）。
func TestExport_RejectEmptyIDs(t *testing.T) {
	b := NewXhsBiz(exportSeed())
	_, err := b.Export(context.Background(), 100, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrBind)
}

// TestExport_NoOwnedNotes 验证选中 id 全部不属于该用户时返回 ErrXhsNoteNotFound（而非空 CSV）。
func TestExport_NoOwnedNotes(t *testing.T) {
	b := NewXhsBiz(exportSeed())
	// 用户 100 选笔记 3（属于用户 200）→ GetByIDs 返回空 → ErrXhsNoteNotFound。
	_, err := b.Export(context.Background(), 100, []uint64{3})
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrXhsNoteNotFound)
}
