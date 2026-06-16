package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newDocumentTestDB 创建 document store 测试用的内存 SQLite DB。
// document 表不依赖 MySQL 特有类型（mediumtext → TEXT, varchar → TEXT 在 sqlite 下退化兼容），
// 可放心 AutoMigrate 建表。
func newDocumentTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Document{}))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1) // 同名内存库多连接会锁，限单连接
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestDocumentStore_CreateAndGet(t *testing.T) {
	db := newDocumentTestDB(t)
	s := newDocumentStore(db)
	ctx := context.Background()

	d := &model.Document{
		UserID:          7,
		SourceObjectKey: "agent-outputs/7/123-report.docx",
		SourceMime:      "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Title:           "report",
		ContentMD:       "# 标题\n正文",
		ParseMethod:     "markitdown",
	}
	require.NoError(t, s.Create(ctx, d))
	assert.NotZero(t, d.ID, "Create 应回填自增 ID")

	// 命中查询
	got, err := s.GetByUserAndSource(ctx, 7, "agent-outputs/7/123-report.docx")
	require.NoError(t, err)
	assert.Equal(t, d.ID, got.ID)
	assert.Equal(t, "# 标题\n正文", got.ContentMD)
	assert.Equal(t, "markitdown", got.ParseMethod)

	// GetByID 命中
	byID, err := s.GetByID(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, "report", byID.Title)
}

func TestDocumentStore_GetByUserAndSource_Miss(t *testing.T) {
	db := newDocumentTestDB(t)
	s := newDocumentStore(db)
	ctx := context.Background()

	_, err := s.GetByUserAndSource(ctx, 7, "agent-outputs/7/nonexistent.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "miss 应返回 gorm.ErrRecordNotFound 供 biz 走建档分支")
}

func TestDocumentStore_GetByID_Miss(t *testing.T) {
	db := newDocumentTestDB(t)
	s := newDocumentStore(db)
	ctx := context.Background()

	_, err := s.GetByID(ctx, 99999)
	require.Error(t, err)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "不存在的 id 应返回 gorm.ErrRecordNotFound")
}

func TestDocumentStore_UpdateContent_NotFound(t *testing.T) {
	db := newDocumentTestDB(t)
	s := newDocumentStore(db)
	ctx := context.Background()

	err := s.UpdateContent(ctx, 99999, "x", "y")
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "更新不存在的 id 应返回 ErrRecordNotFound 而非静默成功")
}

func TestDocumentStore_GetByUserAndSource_IsolatedByUser(t *testing.T) {
	db := newDocumentTestDB(t)
	s := newDocumentStore(db)
	ctx := context.Background()

	require.NoError(t, s.Create(ctx, &model.Document{
		UserID: 7, SourceObjectKey: "agent-outputs/7/a.md", Title: "a", ContentMD: "x", ParseMethod: "direct",
	}))

	// 同 key 不同 user 不应命中（隔离）
	_, err := s.GetByUserAndSource(ctx, 8, "agent-outputs/7/a.md")
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}

func TestDocumentStore_UpdateContent(t *testing.T) {
	db := newDocumentTestDB(t)
	s := newDocumentStore(db)
	ctx := context.Background()

	d := &model.Document{
		UserID: 7, SourceObjectKey: "agent-outputs/7/u.md", Title: "old", ContentMD: "old body", ParseMethod: "direct",
	}
	require.NoError(t, s.Create(ctx, d))

	require.NoError(t, s.UpdateContent(ctx, d.ID, "new body", "new title"))

	got, err := s.GetByID(ctx, d.ID)
	require.NoError(t, err)
	assert.Equal(t, "new body", got.ContentMD)
	assert.Equal(t, "new title", got.Title)
}
