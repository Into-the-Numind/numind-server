package store

import (
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

func insertSopTemplate(t *testing.T, db *gorm.DB, name, status, publish string, createdAt time.Time) uint {
	t.Helper()
	tpl := &model.SopTemplate{
		Name:          name,
		Status:        status,
		PublishStatus: publish,
	}
	require.NoError(t, db.Create(tpl).Error)
	// AutoMigrate 的 CreatedAt 用当前时间，这里显式覆盖以便排序断言。
	require.NoError(t, db.Model(&model.SopTemplate{}).Where("id = ?", tpl.ID).
		UpdateColumn("created_at", createdAt).Error)
	return tpl.ID
}

// TestListVisibleTemplates_FiltersInactiveAndDrafts 验证 DB 层过滤：
// 混入 draft/inactive 模板后，ListVisibleTemplates 只返回 active+published，
// total 字段等于可见数量（不是总数），分页不会把已发布记录挤出首页。
func TestListVisibleTemplates_FiltersInactiveAndDrafts(t *testing.T) {
	db := newSopTemplateTestDB(t)
	s := &sopStore{db: db}

	base := time.Now().Add(-time.Hour)
	// 插入顺序与 created_at 解耦，便于断言 DESC 排序
	pubOldID := insertSopTemplate(t, db, "pub-old", "active", model.SopPublishStatusPublished, base)
	insertSopTemplate(t, db, "draft-mid", "active", model.SopPublishStatusDraft, base.Add(10*time.Minute))
	insertSopTemplate(t, db, "inactive-mid", "inactive", model.SopPublishStatusPublished, base.Add(20*time.Minute))
	pubNewID := insertSopTemplate(t, db, "pub-new", "active", model.SopPublishStatusPublished, base.Add(30*time.Minute))

	// limit 很小（1）的分页情况下，已发布的新记录仍应出现在首页，而不是被 draft 挤出。
	page1, total, err := s.ListVisibleTemplates(0, 1)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total, "total 应为可见记录数（2 条 active+published），而非全部 4 条")
	require.Len(t, page1, 1)
	assert.Equal(t, pubNewID, page1[0].ID, "DESC 排序下最新 published 记录应在首页首位")

	// limit 充足时返回所有可见记录。
	all, total2, err := s.ListVisibleTemplates(0, 500)
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
