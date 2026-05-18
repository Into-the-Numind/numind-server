package store

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newSopTemplateTestDB 创建最小 schema 的 SQLite DB 用于 SopTemplate 查询测试。
func newSopTemplateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/sop_template_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.AutoMigrate(&model.SopTemplate{}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// insertSopTemplateWithOwner 插入带 owner 的 SopTemplate，并覆盖 created_at 以便排序断言。
func insertSopTemplateWithOwner(t *testing.T, db *gorm.DB, name, status, publish string, createdAt time.Time, ownerID *uint) uint {
	t.Helper()
	tpl := &model.SopTemplate{
		Name:          name,
		Status:        status,
		PublishStatus: publish,
		CreatorUserID: ownerID,
	}
	require.NoError(t, db.Create(tpl).Error)
	require.NoError(t, db.Model(&model.SopTemplate{}).Where("id = ?", tpl.ID).
		UpdateColumn("created_at", createdAt).Error)
	return tpl.ID
}

// setupSopStoreTest 创建 ISopStore + 底层 *gorm.DB 供测试使用。
func setupSopStoreTest(t *testing.T) (ISopStore, *gorm.DB) {
	t.Helper()
	db := newSopTemplateTestDB(t)
	return &sopStore{db: db}, db
}

// TestListVisibleTemplates_FiltersInactiveAndDrafts 验证 DB 层过滤：
// 混入 draft/inactive 模板后，ListVisibleTemplates 只返回 active+published，
// total 字段等于可见数量（不是总数），分页不会把已发布记录挤出首页。
func TestListVisibleTemplates_FiltersInactiveAndDrafts(t *testing.T) {
	s, db := setupSopStoreTest(t)
	ctx := context.Background()

	ownerID := uint(30)

	base := time.Now().Add(-time.Hour)
	// 插入顺序与 created_at 解耦，便于断言 DESC 排序
	pubOldID := insertSopTemplateWithOwner(t, db, "pub-old", "active", model.SopPublishStatusPublished, base, &ownerID)
	insertSopTemplateWithOwner(t, db, "draft-mid", "active", model.SopPublishStatusDraft, base.Add(10*time.Minute), &ownerID)
	insertSopTemplateWithOwner(t, db, "inactive-mid", "inactive", model.SopPublishStatusPublished, base.Add(20*time.Minute), &ownerID)
	pubNewID := insertSopTemplateWithOwner(t, db, "pub-new", "active", model.SopPublishStatusPublished, base.Add(30*time.Minute), &ownerID)

	// limit 很小（1）的分页情况下，已发布的新记录仍应出现在首页，而不是被 draft 挤出。
	page1, total, err := s.ListVisibleTemplates(ctx, ownerID, 0, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total, "total 应为可见记录数（2 条 active+published），而非全部 4 条")
	require.Len(t, page1, 1)
	assert.Equal(t, pubNewID, page1[0].ID, "DESC 排序下最新 published 记录应在首页首位")

	// limit 充足时返回所有可见记录。
	all, total2, err := s.ListVisibleTemplates(ctx, ownerID, 0, 500)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total2)
	require.Len(t, all, 2)
	assert.Equal(t, pubNewID, all[0].ID)
	assert.Equal(t, pubOldID, all[1].ID)
	for _, tpl := range all {
		assert.Equal(t, "active", tpl.Status)
		assert.Equal(t, model.SopPublishStatusPublished, tpl.PublishStatus)
	}
}

// TestListVisibleTemplates_FilterByOwner 验证多租户隔离：
// parentA 只看到自己的模板，parentB 数据不泄露。
func TestListVisibleTemplates_FilterByOwner(t *testing.T) {
	s, db := setupSopStoreTest(t)
	ctx := context.Background()

	parentA := uint(30)
	parentB := uint(31)

	// Seed: parentA(30) 2 published + parentB(31) 1 published + parentA 1 draft + 1 NULL
	require.NoError(t, db.Create(&model.SopTemplate{Name: "A1", CreatorUserID: &parentA, Status: "active", PublishStatus: "published"}).Error)
	require.NoError(t, db.Create(&model.SopTemplate{Name: "A2", CreatorUserID: &parentA, Status: "active", PublishStatus: "published"}).Error)
	require.NoError(t, db.Create(&model.SopTemplate{Name: "B1", CreatorUserID: &parentB, Status: "active", PublishStatus: "published"}).Error)
	require.NoError(t, db.Create(&model.SopTemplate{Name: "AD", CreatorUserID: &parentA, Status: "active", PublishStatus: "draft"}).Error)
	require.NoError(t, db.Create(&model.SopTemplate{Name: "NULL", CreatorUserID: nil, Status: "active", PublishStatus: "published"}).Error)

	// Query parentA: 仅 2 行（active+published 属于 parentA）
	itemsA, totalA, err := s.ListVisibleTemplates(ctx, parentA, 0, 100)
	require.NoError(t, err)
	require.Equal(t, int64(2), totalA)
	require.Len(t, itemsA, 2)
	for _, tpl := range itemsA {
		require.NotNil(t, tpl.CreatorUserID)
		require.Equal(t, parentA, *tpl.CreatorUserID, "parentA 视角不应出现非 parentA 的模板")
	}

	// Query parentB: 期望 1 行，且不含 parentA 的任何模板（多租户对称隔离）
	itemsB, totalB, err := s.ListVisibleTemplates(ctx, parentB, 0, 100)
	require.NoError(t, err)
	require.Equal(t, int64(1), totalB)
	require.Len(t, itemsB, 1)
	for _, tpl := range itemsB {
		require.NotNil(t, tpl.CreatorUserID)
		require.Equal(t, parentB, *tpl.CreatorUserID, "parentB 视角不应泄露 parentA 的模板")
	}
}

// TestListVisibleTemplates_DefensiveNullFilter 验证防御性 IS NOT NULL 过滤：
// creator_user_id=NULL 的历史行即使 status=active+published 也不返回。
func TestListVisibleTemplates_DefensiveNullFilter(t *testing.T) {
	s, db := setupSopStoreTest(t)
	ctx := context.Background()

	require.NoError(t, db.Create(&model.SopTemplate{Name: "NULL", CreatorUserID: nil, Status: "active", PublishStatus: "published"}).Error)

	items, total, err := s.ListVisibleTemplates(ctx, 30, 0, 100)
	require.NoError(t, err)
	require.Equal(t, int64(0), total)
	require.Empty(t, items)
}

// TestListVisibleTemplates_EmptyForNonExistentParent 验证不存在的父账户返回空列表。
func TestListVisibleTemplates_EmptyForNonExistentParent(t *testing.T) {
	s, _ := setupSopStoreTest(t)
	ctx := context.Background()

	items, total, err := s.ListVisibleTemplates(ctx, 999, 0, 100)
	require.NoError(t, err)
	require.Equal(t, int64(0), total)
	require.Empty(t, items)
}
