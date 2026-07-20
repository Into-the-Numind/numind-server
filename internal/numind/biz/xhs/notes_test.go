package xhs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// crudMockStore 是一个带 user 隔离语义的内存 IXhsTopicStore，用于隔离 biz CRUD 逻辑
// （分页归一化、user 隔离、model→NoteItem 映射）。它复刻真实 store 的关键约定：
// ListNotes/GetNote/DeleteNote 都按 user_id 过滤，他人记录不可见。
type crudMockStore struct {
	rows []model.XhsTopicNote
}

func (m *crudMockStore) UpsertByUserNote(context.Context, *model.XhsTopicNote) (bool, error) {
	return true, nil
}

// ListNotes 复刻真实 store 的 user 隔离 + 过滤 + 偏移分页（crawled_at 不在 mock 比较，
// 按插入顺序返回，足以验证 biz 的 offset/limit 计算与映射）。
func (m *crudMockStore) ListNotes(_ context.Context, userID uint, filter store.XhsNoteFilter, offset, limit int) ([]model.XhsTopicNote, int64, error) {
	var matched []model.XhsTopicNote
	for i := range m.rows {
		r := m.rows[i]
		if r.UserID != userID {
			continue
		}
		if filter.NoteType != "" && r.NoteType != filter.NoteType {
			continue
		}
		if filter.EnrichStatus != "" && r.EnrichStatus != filter.EnrichStatus {
			continue
		}
		matched = append(matched, r)
	}
	total := int64(len(matched))
	if offset >= len(matched) {
		return []model.XhsTopicNote{}, total, nil
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}
	return matched[offset:end], total, nil
}

func (m *crudMockStore) ListSnapshot(context.Context, uint, store.XhsSnapshotQuery) (*store.XhsSnapshotPage, error) {
	return nil, nil
}

func (m *crudMockStore) ListPendingEnrich(context.Context, int) ([]model.XhsTopicNote, error) {
	return nil, nil
}

func (m *crudMockStore) GetNote(_ context.Context, userID uint, id uint64) (*model.XhsTopicNote, error) {
	for i := range m.rows {
		if m.rows[i].UserID == userID && m.rows[i].ID == id {
			r := m.rows[i]
			return &r, nil
		}
	}
	return nil, errno.ErrXhsNoteNotFound
}

func (m *crudMockStore) DeleteNote(_ context.Context, userID uint, id uint64) error {
	for i := range m.rows {
		if m.rows[i].UserID == userID && m.rows[i].ID == id {
			m.rows = append(m.rows[:i], m.rows[i+1:]...)
			return nil
		}
	}
	// 与真实 store 一致：删不到（含他人记录）不报错，幂等。
	return nil
}

func (m *crudMockStore) UpdateEnrichStatus(context.Context, uint64, string) error { return nil }
func (m *crudMockStore) ClaimForEnrich(context.Context, uint64) (bool, error)     { return false, nil }
func (m *crudMockStore) UpdateEnrichResult(context.Context, *model.XhsTopicNote) error {
	return nil
}
func (m *crudMockStore) GetByIDs(context.Context, uint, []uint64) ([]model.XhsTopicNote, error) {
	return nil, nil
}

var _ store.IXhsTopicStore = (*crudMockStore)(nil)

func seedRows() *crudMockStore {
	return &crudMockStore{rows: []model.XhsTopicNote{
		{ID: 1, UserID: 100, XhsNoteID: "a", NoteType: model.XhsNoteTypeNormal, Title: "用户100-1", EnrichStatus: model.XhsEnrichDone},
		{ID: 2, UserID: 100, XhsNoteID: "b", NoteType: model.XhsNoteTypeVideo, Title: "用户100-2", EnrichStatus: model.XhsEnrichPending},
		{ID: 3, UserID: 100, XhsNoteID: "c", NoteType: model.XhsNoteTypeNormal, Title: "用户100-3", EnrichStatus: model.XhsEnrichDone},
		{ID: 4, UserID: 200, XhsNoteID: "d", NoteType: model.XhsNoteTypeNormal, Title: "用户200-1", EnrichStatus: model.XhsEnrichDone},
	}}
}

// TestListNotes_Pagination 验证 biz 层 offset/limit 计算正确（page 2 / size 2 取第 3 条）。
func TestListNotes_Pagination(t *testing.T) {
	b := NewXhsBiz(seedRows())
	ctx := context.Background()

	// page 1, size 2 → 前两条；total 反映用户 100 的全部 3 条。
	items, total, err := b.ListNotes(ctx, 100, ListFilter{}, 1, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total, "total 应为用户 100 的全部笔记数")
	require.Len(t, items, 2)
	assert.Equal(t, uint64(1), items[0].ID)
	assert.Equal(t, uint64(2), items[1].ID)

	// page 2, size 2 → 第 3 条（offset=(2-1)*2=2）。
	items, total, err = b.ListNotes(ctx, 100, ListFilter{}, 2, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	require.Len(t, items, 1)
	assert.Equal(t, uint64(3), items[0].ID)
}

// TestListNotes_PageSizeClamp 验证 page_size 超过 100 被钳到 100、非正值回退默认。
func TestListNotes_PageSizeClamp(t *testing.T) {
	b := NewXhsBiz(seedRows())
	ctx := context.Background()

	// page_size=500 → 钳到 100，仍返回用户全部 3 条（不报错）。
	items, _, err := b.ListNotes(ctx, 100, ListFilter{}, 1, 500)
	require.NoError(t, err)
	assert.Len(t, items, 3)

	// page<1 / page_size<1 归一化为 1 / 20，不 panic、不负偏移。
	items, _, err = b.ListNotes(ctx, 100, ListFilter{}, 0, 0)
	require.NoError(t, err)
	assert.Len(t, items, 3)
}

// TestNormalizePagination 锁定 controller List 回显契约：page=0/page_size=500 必须归一化为
// 1/100（否则响应回显非法原值，前端分页状态错位）；合法值原样保留；幂等（再归一化不变）。
func TestNormalizePagination(t *testing.T) {
	cases := []struct {
		name               string
		inPage, inSize     int
		wantPage, wantSize int
	}{
		{"zero page clamps to 1", 0, 20, 1, 20},
		{"negative page clamps to 1", -5, 20, 1, 20},
		{"oversize page_size clamps to 100", 1, 500, 1, 100},
		{"zero page_size falls back to default", 1, 0, 1, defaultPageSize},
		{"valid values preserved", 3, 50, 3, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, s := NormalizePagination(tc.inPage, tc.inSize)
			assert.Equal(t, tc.wantPage, p)
			assert.Equal(t, tc.wantSize, s)
			// 幂等：对归一化结果再归一化，值不变（controller 预归一化 + biz 内部再归一化纵深防御）。
			p2, s2 := NormalizePagination(p, s)
			assert.Equal(t, p, p2)
			assert.Equal(t, s, s2)
		})
	}
}

// TestListNotes_UserIsolation 验证只返回查询用户自己的笔记（不串户）。
func TestListNotes_UserIsolation(t *testing.T) {
	b := NewXhsBiz(seedRows())
	ctx := context.Background()

	items, total, err := b.ListNotes(ctx, 200, ListFilter{}, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	assert.Equal(t, uint64(4), items[0].ID, "用户 200 只应读到自己的笔记 4，绝不串读用户 100 的 1/2/3")
}

// TestListNotes_FilterByType 验证 note_type 过滤透传到 store。
func TestListNotes_FilterByType(t *testing.T) {
	b := NewXhsBiz(seedRows())
	ctx := context.Background()

	items, total, err := b.ListNotes(ctx, 100, ListFilter{NoteType: model.XhsNoteTypeVideo}, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	assert.Equal(t, model.XhsNoteTypeVideo, items[0].NoteType)
}

// TestGetNote_UserIsolation 验证跨用户取不到（视为不存在，防越权）。
func TestGetNote_UserIsolation(t *testing.T) {
	b := NewXhsBiz(seedRows())
	ctx := context.Background()

	// 用户 100 取自己的笔记 1 → 成功。
	item, err := b.GetNote(ctx, 100, 1)
	require.NoError(t, err)
	require.NotNil(t, item)
	assert.Equal(t, uint64(1), item.ID)

	// 用户 200 取用户 100 的笔记 1 → ErrXhsNoteNotFound（越权防护）。
	_, err = b.GetNote(ctx, 200, 1)
	assert.ErrorIs(t, err, errno.ErrXhsNoteNotFound)
}

// TestDeleteNote_UserIsolation 验证跨用户删不到对方记录（对方记录仍在）。
func TestDeleteNote_UserIsolation(t *testing.T) {
	m := seedRows()
	b := NewXhsBiz(m)
	ctx := context.Background()

	// 用户 200 试图删用户 100 的笔记 1 → 幂等成功但不影响用户 100 的记录。
	err := b.DeleteNote(ctx, 200, 1)
	require.NoError(t, err)
	got, err := b.GetNote(ctx, 100, 1)
	require.NoError(t, err, "他人删除不应影响用户 100 的笔记 1")
	assert.Equal(t, uint64(1), got.ID)

	// 用户 100 删自己的笔记 1 → 成功，之后取不到。
	err = b.DeleteNote(ctx, 100, 1)
	require.NoError(t, err)
	_, err = b.GetNote(ctx, 100, 1)
	assert.ErrorIs(t, err, errno.ErrXhsNoteNotFound)
}

// TestToNoteItem_JSONFields 验证 tags/comments 的 datatypes.JSON 被正确解析为切片，
// 空值返回 [] 而非 nil（前端稳定性）。
func TestToNoteItem_JSONFields(t *testing.T) {
	n := &model.XhsTopicNote{
		ID:       9,
		Tags:     datatypes.JSON([]byte(`["美妆","护肤"]`)),
		Comments: datatypes.JSON([]byte(`[{"author":"u1","text":"好","like_count":3}]`)),
	}
	item := toNoteItem(n)
	require.Len(t, item.Tags, 2)
	assert.Equal(t, "美妆", item.Tags[0])
	require.Len(t, item.Comments, 1)
	assert.Equal(t, "u1", item.Comments[0].Author)
	assert.Equal(t, 3, item.Comments[0].LikeCount)

	// 空 JSON → 空切片，非 nil。
	empty := toNoteItem(&model.XhsTopicNote{ID: 10})
	assert.NotNil(t, empty.Tags)
	assert.Len(t, empty.Tags, 0)
	assert.NotNil(t, empty.Comments)
	assert.Len(t, empty.Comments, 0)
}
