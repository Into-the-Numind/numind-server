package artifact

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

func TestVersioning_ListHistory_ChildAccountDenied(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)

	_, err := svc.versioning.ListHistory(context.Background(), 0, 1)
	require.ErrorIs(t, err, errno.ErrPermissionDenied)
}

func TestVersioning_ListHistory_V1IsFirstPublish(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "v1", BodyMd: "body"})
	require.NoError(t, err)

	items, err := svc.versioning.ListHistory(ctx, 100, sk.ID)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, uint(1), items[0].Version)
	assert.Equal(t, "首次发布", items[0].DiffSummary)
}

func TestVersioning_ListHistory_DiffSummaryNonEmptyAfterUpdate(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "v1", BodyMd: "line 1\nline 2"})
	require.NoError(t, err)

	_, err = svc.Update(ctx, 100, sk.ID, CreateRequest{
		Name:        "v2",
		Description: "new desc",
		BodyMd:      "line 1\nline 2\nline 3 added\nline 4 added",
	})
	require.NoError(t, err)

	items, err := svc.versioning.ListHistory(ctx, 100, sk.ID)
	require.NoError(t, err)
	require.Len(t, items, 2)

	// 排序：version DESC，所以 items[0] = v2, items[1] = v1
	assert.Equal(t, uint(2), items[0].Version)
	assert.Contains(t, items[0].DiffSummary, "修改了")
	assert.Contains(t, items[0].DiffSummary, "name")
	assert.Contains(t, items[0].DiffSummary, "description")
	assert.Contains(t, items[0].DiffSummary, "body_md")
	assert.Contains(t, items[0].DiffSummary, "+")
	// v1 永远 "首次发布"
	assert.Equal(t, "首次发布", items[1].DiffSummary)
}

func TestVersioning_ListHistory_DiffSummaryTruncated(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "v1", BodyMd: "x"})
	require.NoError(t, err)

	// 极长 body 改动 → +N -M 数字可能很大但格式可控；测主要测 200 字符截断
	hugeBody := strings.Repeat("new line\n", 10000)
	_, err = svc.Update(ctx, 100, sk.ID, CreateRequest{Name: "v2", BodyMd: hugeBody})
	require.NoError(t, err)

	items, err := svc.versioning.ListHistory(ctx, 100, sk.ID)
	require.NoError(t, err)
	// 长度按 rune 计 ≤ 200
	for _, it := range items {
		assert.LessOrEqual(t, len([]rune(it.DiffSummary)), historyDiffMaxLen)
	}
}

func TestVersioning_Restore_NewVersionFromSnapshot(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "v1", BodyMd: "original"})
	require.NoError(t, err)

	_, err = svc.Update(ctx, 100, sk.ID, CreateRequest{Name: "v2", BodyMd: "modified"})
	require.NoError(t, err)

	// 恢复到 v1（应该 body=original，version=3）
	restored, err := svc.versioning.Restore(ctx, 100, sk.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, "v1", restored.Name)
	assert.Equal(t, "original", restored.BodyMd)
	assert.Equal(t, uint(3), restored.Version, "restore creates new version")
	assert.True(t, restored.IsActive)

	// history 行数 = 3（v1 创建 + v2 更新 + v3 恢复）
	var histCount int64
	require.NoError(t, db.Model(&model.SkillHistory{}).Where("skill_id = ?", sk.ID).Count(&histCount).Error)
	assert.Equal(t, int64(3), histCount)
}

func TestVersioning_Restore_VersionNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "v1", BodyMd: "x"})
	require.NoError(t, err)

	_, err = svc.versioning.Restore(ctx, 100, sk.ID, 999)
	require.ErrorIs(t, err, errno.ErrSkillArtifactVersionNotFound)
}

func TestVersioning_Restore_RevivesInactiveSkill(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "v1", BodyMd: "x"})
	require.NoError(t, err)

	// 软删
	_, err = svc.Delete(ctx, 100, sk.ID)
	require.NoError(t, err)

	// 验证已 inactive
	var row model.Skill
	require.NoError(t, db.First(&row, sk.ID).Error)
	require.False(t, row.IsActive)

	// 恢复
	restored, err := svc.versioning.Restore(ctx, 100, sk.ID, 1)
	require.NoError(t, err)
	assert.True(t, restored.IsActive, "Restore should revive is_active=0 skill")

	// DB 实际行也应 is_active=1
	require.NoError(t, db.First(&row, sk.ID).Error)
	assert.True(t, row.IsActive)
}

func TestVersioning_Restore_ChildAccountDenied(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)

	_, err := svc.versioning.Restore(context.Background(), 0, 1, 1)
	require.ErrorIs(t, err, errno.ErrPermissionDenied)
}

func TestVersioning_Restore_CrossTenantReturnsNotFound(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{Name: "v1", BodyMd: "x"})
	require.NoError(t, err)

	_, err = svc.versioning.Restore(ctx, 200, sk.ID, 1)
	require.ErrorIs(t, err, errno.ErrSkillArtifactNotFound)
}

func TestVersioning_WriteSnapshot_PersistsFullJSON(t *testing.T) {
	db := newTestDB(t)
	svc := NewService(db)
	ctx := context.Background()

	sk, err := svc.Create(ctx, 100, 100, CreateRequest{
		Name:         "verify-snapshot",
		Description:  "desc",
		WhenToUse:    "when",
		AllowedTools: []string{"a", "b"},
		BodyMd:       "body",
	})
	require.NoError(t, err)

	var h model.SkillHistory
	require.NoError(t, db.Where("skill_id = ? AND version = ?", sk.ID, 1).First(&h).Error)

	// snapshot JSON 应包含所有 5 个业务字段
	snapStr := string(h.Snapshot)
	assert.Contains(t, snapStr, "verify-snapshot")
	assert.Contains(t, snapStr, "desc")
	assert.Contains(t, snapStr, "when")
	assert.Contains(t, snapStr, "body")
}

func TestComputeDiffSummary_NoChange(t *testing.T) {
	// 两个相同的 snapshot 应返回 "更新"
	snap := []byte(`{"name":"a","description":"b","body_md":"c","allowed_tools":[]}`)
	summary := computeDiffSummary(snap, snap)
	assert.Equal(t, "更新", summary)
}

func TestComputeDiffSummary_MultiFieldsAndLineDiff(t *testing.T) {
	prev := []byte(`{"name":"old","description":"d1","body_md":"line1\nline2\n","allowed_tools":[]}`)
	curr := []byte(`{"name":"new","description":"d1","body_md":"line1\nline2 modified\nline3\n","allowed_tools":[]}`)
	summary := computeDiffSummary(prev, curr)
	assert.Contains(t, summary, "name")
	assert.Contains(t, summary, "body_md")
	assert.Contains(t, summary, "+")
	assert.Contains(t, summary, "-")
}

func TestComputeDiffSummary_MalformedReturnsUpdate(t *testing.T) {
	// 解析失败 fallback "更新"
	summary := computeDiffSummary([]byte("not-json"), []byte("not-json"))
	assert.Equal(t, "更新", summary)
}
