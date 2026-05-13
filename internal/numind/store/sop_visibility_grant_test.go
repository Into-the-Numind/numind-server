package store

import (
	"context"
	"testing"

	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newSopVisibilityGrantTestDB 创建最小 schema SQLite DB 用于 SopVisibilityGrant 测试.
//
// 注: 用 raw SQL 建表而非 AutoMigrate, 因为 SopVisibilityGrant 通过 foreignKey tag 关联
// User (含 MySQL 'enum' 字段不兼容 SQLite) 和 SopTemplate, AutoMigrate 会顺带尝试创建
// 关联表导致 syntax error. 与 sop_template_visibility_test.go 单表 AutoMigrate 思路一致.
func newSopVisibilityGrantTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/svg_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// 手写 SQLite-compatible DDL, 与 spec §2.2 + Task 1 migration 等价.
	require.NoError(t, db.Exec(`
		CREATE TABLE sop_visibility_grant (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id INTEGER NOT NULL,
			sub_user_id INTEGER NOT NULL,
			sop_template_id INTEGER NOT NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME,
			UNIQUE(sub_user_id, sop_template_id)
		)
	`).Error)
	require.NoError(t, db.Exec(`CREATE INDEX idx_svg_parent_sub ON sop_visibility_grant(parent_user_id, sub_user_id)`).Error)
	require.NoError(t, db.Exec(`CREATE INDEX idx_svg_template ON sop_visibility_grant(sop_template_id)`).Error)
	require.NoError(t, db.Exec(`CREATE INDEX idx_svg_deleted ON sop_visibility_grant(deleted_at)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// TestReplaceGrantsTx_PhysicalDeleteIncludesSoftDeleted 验证 ReplaceGrantsTx 物理删模式.
//
// 场景: 软删一条 grant → 再次插入同 (sub_user_id, sop_template_id) 组合.
// 若 ReplaceGrantsTx 未用 Unscoped() 物理删, 软删记录残留会与新插入冲突唯一索引.
// 这是 spec §4.1.6 P0-2 双路径删除模式的关键回归测试.
func TestReplaceGrantsTx_PhysicalDeleteIncludesSoftDeleted(t *testing.T) {
	db := newSopVisibilityGrantTestDB(t)
	s := NewSopVisibilityGrantStore(db)
	ctx := context.Background()

	// 1. 插入一条 active grant (parent=1, sub=10, sop=100)
	require.NoError(t, db.Create(&model.SopVisibilityGrant{
		ParentUserID: 1, SubUserID: 10, SopTemplateID: 100,
	}).Error)

	// 2. 软删 (模拟 CleanupBySubUser/CleanupByEntity 路径)
	require.NoError(t, db.Where("sop_template_id = ?", 100).Delete(&model.SopVisibilityGrant{}).Error)

	// 验证软删后, default scope 查询为 0, unscoped 查询为 1
	var defaultCount, unscopedCount int64
	db.Model(&model.SopVisibilityGrant{}).Where("sop_template_id = ?", 100).Count(&defaultCount)
	db.Unscoped().Model(&model.SopVisibilityGrant{}).Where("sop_template_id = ?", 100).Count(&unscopedCount)
	require.Equal(t, int64(0), defaultCount, "after soft delete, default scope should see 0 rows")
	require.Equal(t, int64(1), unscopedCount, "after soft delete, unscoped scope should see 1 row")

	// 3. 用 ReplaceGrantsTx 重新插入同 (sub_user_id=10, sop_template_id=100)
	// 若 ReplaceGrantsTx 不用 Unscoped() 物理删, 此处会因唯一索引冲突而失败
	err := db.Transaction(func(tx *gorm.DB) error {
		return s.ReplaceGrantsTx(ctx, tx, 100, 1, []uint{10})
	})
	require.NoError(t, err, "ReplaceGrantsTx should physically delete soft-deleted records first to avoid unique conflict")

	// 4. 验证: 表中应只有 1 条 active 记录, 0 条 soft-deleted (物理删已清理)
	db.Model(&model.SopVisibilityGrant{}).Where("sop_template_id = ?", 100).Count(&defaultCount)
	db.Unscoped().Model(&model.SopVisibilityGrant{}).Where("sop_template_id = ?", 100).Count(&unscopedCount)
	assert.Equal(t, int64(1), defaultCount, "1 active row after replace")
	assert.Equal(t, int64(1), unscopedCount, "physical delete should have purged the prior soft-deleted row")
}

// TestReplaceGrantsTx_EmptySubUserIDs 验证 restricted=true + sub_user_ids=[] 场景.
// 应仅删旧 grants, 不插入新记录 (白名单严格全拒语义).
func TestReplaceGrantsTx_EmptySubUserIDs(t *testing.T) {
	db := newSopVisibilityGrantTestDB(t)
	s := NewSopVisibilityGrantStore(db)
	ctx := context.Background()

	require.NoError(t, db.Create(&model.SopVisibilityGrant{
		ParentUserID: 1, SubUserID: 10, SopTemplateID: 100,
	}).Error)

	err := db.Transaction(func(tx *gorm.DB) error {
		return s.ReplaceGrantsTx(ctx, tx, 100, 1, nil)
	})
	require.NoError(t, err)

	var count int64
	db.Model(&model.SopVisibilityGrant{}).Where("sop_template_id = ?", 100).Count(&count)
	assert.Equal(t, int64(0), count, "no active records after replace with empty list")
}

// TestCountBySubUserAndSop_IgnoresSoftDeleted 验证 IsSopVisibleToUser 所依赖的 Count 函数不计入软删记录.
func TestCountBySubUserAndSop_IgnoresSoftDeleted(t *testing.T) {
	db := newSopVisibilityGrantTestDB(t)
	s := NewSopVisibilityGrantStore(db)
	ctx := context.Background()

	require.NoError(t, db.Create(&model.SopVisibilityGrant{
		ParentUserID: 1, SubUserID: 10, SopTemplateID: 100,
	}).Error)

	// 软删
	require.NoError(t, db.Where("sop_template_id = ?", 100).Delete(&model.SopVisibilityGrant{}).Error)

	count, err := s.CountBySubUserAndSop(ctx, 10, 100)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "soft-deleted grant should not count toward visibility")
}
