package sop_test

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newVisibilityTestDB 创建 SOP visibility biz 测试用 SQLite DB.
// 用 raw SQL 建 user 表 (避免 MySQL enum 字段不兼容 SQLite),
// 其他 model 走 AutoMigrate (SopTemplate / SopVisibilityGrant 无 enum).
func newVisibilityTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.Exec(`
		CREATE TABLE user (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at      DATETIME,
			updated_at      DATETIME,
			deleted_at      DATETIME,
			username        TEXT,
			nickname        TEXT,
			parent_user_id  INTEGER,
			billing_mode    TEXT NOT NULL DEFAULT 'credits',
			user_tier       TEXT DEFAULT 'free'
		)`).Error)

	require.NoError(t, db.AutoMigrate(
		&model.SopTemplate{},
		&model.SopVisibilityGrant{},
	))
	return db
}

func insertVisUser(t *testing.T, db *gorm.DB, parentID *uint) uint {
	t.Helper()
	now := time.Now()
	var parentVal interface{}
	if parentID != nil {
		parentVal = *parentID
	}
	require.NoError(t, db.Exec(
		`INSERT INTO user (created_at, updated_at, parent_user_id) VALUES (?, ?, ?)`,
		now, now, parentVal,
	).Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

func insertVisSopTemplate(t *testing.T, db *gorm.DB, ownerID uint) uint {
	t.Helper()
	tpl := &model.SopTemplate{
		Name:          "visibility-test",
		CreatorUserID: &ownerID,
		Status:        "active",
		PublishStatus: "published",
	}
	require.NoError(t, db.Create(tpl).Error)
	return tpl.ID
}

// TestUpdateSopVisibility_Smoke 冒烟测试: 父账户配置 → GetSopVisibility 回读一致.
func TestUpdateSopVisibility_Smoke(t *testing.T) {
	db := newVisibilityTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertVisUser(t, db, nil)
	sub1 := insertVisUser(t, db, &parent)
	_ = insertVisUser(t, db, &parent) // sub2 未授权
	sopID := insertVisSopTemplate(t, db, parent)

	require.NoError(t, sop.UpdateSopVisibility(ctx, s, parent, sopID, true, []uint{sub1}))

	restricted, ids, err := sop.GetSopVisibility(ctx, s, parent, sopID)
	require.NoError(t, err)
	assert.True(t, restricted, "after enable, visibility_restricted should be true")
	assert.ElementsMatch(t, []uint{sub1}, ids)
}

// TestUpdateSopVisibility_TurnOffPreservesGrants 验证 D3 保留语义.
// restricted=false 路径不动 grant 表; 重新打开后名单恢复.
func TestUpdateSopVisibility_TurnOffPreservesGrants(t *testing.T) {
	db := newVisibilityTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertVisUser(t, db, nil)
	sub1 := insertVisUser(t, db, &parent)
	sub2 := insertVisUser(t, db, &parent)
	sopID := insertVisSopTemplate(t, db, parent)

	// 1. 打开 + 配 2 个子用户
	require.NoError(t, sop.UpdateSopVisibility(ctx, s, parent, sopID, true, []uint{sub1, sub2}))
	// 2. 关闭 (D3: 不动 grant)
	require.NoError(t, sop.UpdateSopVisibility(ctx, s, parent, sopID, false, nil))
	// 3. GetSopVisibility 仍返回历史名单
	restricted, ids, err := sop.GetSopVisibility(ctx, s, parent, sopID)
	require.NoError(t, err)
	assert.False(t, restricted, "after turn off, visibility_restricted should be false")
	assert.ElementsMatch(t, []uint{sub1, sub2}, ids, "D3: grants preserved after toggling off")
}

// TestUpdateSopVisibility_NonOwner 验证 owner 校验: 非 owner caller → 403.
func TestUpdateSopVisibility_NonOwner(t *testing.T) {
	db := newVisibilityTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent1 := insertVisUser(t, db, nil)
	parent2 := insertVisUser(t, db, nil)
	sopID := insertVisSopTemplate(t, db, parent1)

	err := sop.UpdateSopVisibility(ctx, s, parent2, sopID, true, nil)
	assert.ErrorIs(t, err, errno.ErrEntityNotOwnedByCaller)
}

// TestUpdateSopVisibility_SubUserCallerDenied 验证子账户调用 → 403.
func TestUpdateSopVisibility_SubUserCallerDenied(t *testing.T) {
	db := newVisibilityTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertVisUser(t, db, nil)
	sub := insertVisUser(t, db, &parent)
	sopID := insertVisSopTemplate(t, db, parent)

	err := sop.UpdateSopVisibility(ctx, s, sub, sopID, true, nil)
	assert.ErrorIs(t, err, errno.ErrVisibilityPermissionDenied)
}

// TestIsSopVisibleToUser_ShortCircuit 验证 visibility_restricted=false 短路.
// 无论 grant 表是否有记录, 子用户都可见.
func TestIsSopVisibleToUser_ShortCircuit(t *testing.T) {
	db := newVisibilityTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertVisUser(t, db, nil)
	sub := insertVisUser(t, db, &parent)
	sopID := insertVisSopTemplate(t, db, parent)

	// 默认 visibility_restricted=false, sub 应可见
	visible, err := sop.IsSopVisibleToUser(ctx, s, sub, sopID)
	require.NoError(t, err)
	assert.True(t, visible, "default false should return visible")

	// 父账户 bypass: 无论字段如何都 true
	visible, err = sop.IsSopVisibleToUser(ctx, s, parent, sopID)
	require.NoError(t, err)
	assert.True(t, visible, "parent should always be visible")
}
