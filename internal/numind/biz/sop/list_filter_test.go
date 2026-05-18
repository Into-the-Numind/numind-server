package sop_test

import (
	"context"
	"testing"
	"time"

	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newListFilterTestDB 创建 ListVisibleTemplatesWithPermission 4 象限矩阵测试用 SQLite DB.
// 含 user / sop_template / user_template_permission / sop_visibility_grant 四张表.
func newListFilterTestDB(t *testing.T) *gorm.DB {
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
			parent_user_id  INTEGER
		)`).Error)

	require.NoError(t, db.AutoMigrate(
		&model.SopTemplate{},
		&model.UserTemplatePermission{},
		&model.SopVisibilityGrant{},
	))
	return db
}

func insertFilterUser(t *testing.T, db *gorm.DB, parentID *uint) uint {
	t.Helper()
	var pv interface{}
	if parentID != nil {
		pv = *parentID
	}
	require.NoError(t, db.Exec(
		`INSERT INTO user (created_at, updated_at, parent_user_id) VALUES (?, ?, ?)`,
		time.Now(), time.Now(), pv,
	).Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

func insertFilterTpl(t *testing.T, db *gorm.DB, ownerID uint, name string, visibilityRestricted bool) uint {
	t.Helper()
	tpl := &model.SopTemplate{
		Name:                 name,
		CreatorUserID:        &ownerID,
		Status:               "active",
		PublishStatus:        model.SopPublishStatusPublished,
		VisibilityRestricted: visibilityRestricted,
	}
	require.NoError(t, db.Create(tpl).Error)
	return tpl.ID
}

// TestListVisibleTemplatesWithPermission_FourQuadrants 验证 spec §4.2.1 两层 gate 串行:
// visibility 过滤 (Layer 1, 物理移除) → run-permission 标志 (Layer 2, 标记 HasPermission).
//
//	象限                   | visibility | run-perm | 期望
//	-----------------------+------------+----------+----------
//	visible+allowed (V/A)  | 不限制     | grant    | 列表显示, HasPermission=true
//	visible+denied (V/D)   | 不限制     | no grant | 列表显示, HasPermission=false
//	hidden+allowed (H/A)   | 限制+不在  | grant    | 不在列表 (visibility 过滤)
//	hidden+denied (H/D)    | 限制+不在  | no grant | 不在列表 (visibility 过滤)
//	included+restricted    | 限制+在    | grant    | 列表显示, HasPermission=true
//
// 测试构造 5 个 SOP, 验证子用户最终列表包含正确 3 个 (V/A + V/D + included+restricted).
func TestListVisibleTemplatesWithPermission_FourQuadrants(t *testing.T) {
	db := newListFilterTestDB(t)
	ds := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertFilterUser(t, db, nil)
	sub := insertFilterUser(t, db, &parent)

	// 5 SOP:
	//   sop100 (V/A): visibility=false, sub 在 run-perm 白名单 → 列表显示 + HasPermission=true
	//   sop101 (V/D): visibility=false, sub 不在 run-perm 白名单 → 列表显示 + HasPermission=false
	//   sop102 (H/A): visibility=true, sub 不在 vis 白名单, 在 run-perm 白名单 → 不在列表 (visibility 拦)
	//   sop103 (H/D): visibility=true, sub 不在 vis 白名单, 不在 run-perm 白名单 → 不在列表 (visibility 拦)
	//   sop104 (I/R): visibility=true, sub 在 vis 白名单, 在 run-perm 白名单 → 列表显示 + HasPermission=true
	sop100 := insertFilterTpl(t, db, parent, "VA-no-restrict-allowed", false)
	sop101 := insertFilterTpl(t, db, parent, "VD-no-restrict-denied", false)
	sop102 := insertFilterTpl(t, db, parent, "HA-restrict-allowed", true)
	sop103 := insertFilterTpl(t, db, parent, "HD-restrict-denied", true)
	sop104 := insertFilterTpl(t, db, parent, "IR-restrict-in-whitelist", true)

	// run-perm 白名单: sop100, sop102, sop104
	for _, sopID := range []uint{sop100, sop102, sop104} {
		require.NoError(t, db.Create(&model.UserTemplatePermission{
			ParentUserID: parent,
			SubUserID:    sub,
			TemplateID:   sopID,
		}).Error)
	}

	// visibility 白名单: 仅 sop104 给 sub
	require.NoError(t, db.Create(&model.SopVisibilityGrant{
		ParentUserID:  parent,
		SubUserID:     sub,
		SopTemplateID: sop104,
	}).Error)

	// 调子用户身份 (传 user struct 含 ParentUserID)
	subUser := &model.User{ParentUserID: &parent}
	subUser.ID = sub

	b := sop.NewSopBiz(ds, nil, nil)
	items, total, err := b.ListVisibleTemplatesWithPermission(ctx, subUser, 0, 500)
	require.NoError(t, err)

	// 期望结果: 3 项 (sop100 V/A, sop101 V/D, sop104 I/R)
	// sop102 H/A 和 sop103 H/D 被 visibility 过滤移除
	gotIDs := make(map[uint]bool)
	gotPerms := make(map[uint]bool)
	for _, it := range items {
		gotIDs[it.ID] = true
		gotPerms[it.ID] = it.HasPermission
	}

	assert.Len(t, items, 3, "should have 3 visible items (V/A + V/D + I/R), sop102/sop103 visibility-hidden")
	assert.Equal(t, int64(3), total, "total reflects post-visibility-filter count")
	assert.True(t, gotIDs[sop100], "V/A sop100 should be visible")
	assert.True(t, gotIDs[sop101], "V/D sop101 should be visible (visibility=false, run-perm 仅标志)")
	assert.False(t, gotIDs[sop102], "H/A sop102 should be HIDDEN (visibility filter)")
	assert.False(t, gotIDs[sop103], "H/D sop103 should be HIDDEN (visibility filter)")
	assert.True(t, gotIDs[sop104], "I/R sop104 should be visible (restricted but in whitelist)")

	assert.True(t, gotPerms[sop100], "V/A HasPermission=true")
	assert.False(t, gotPerms[sop101], "V/D HasPermission=false (no run-perm)")
	assert.True(t, gotPerms[sop104], "I/R HasPermission=true")
}

// TestListVisibleTemplatesWithPermission_ParentBypass 验证父账户 bypass visibility 过滤.
func TestListVisibleTemplatesWithPermission_ParentBypass(t *testing.T) {
	db := newListFilterTestDB(t)
	ds := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertFilterUser(t, db, nil)
	// 父账户创建一个 visibility 受限的 SOP (无 vis grant)
	sopID := insertFilterTpl(t, db, parent, "restricted", true)

	parentUser := &model.User{}
	parentUser.ID = parent

	b := sop.NewSopBiz(ds, nil, nil)
	items, total, err := b.ListVisibleTemplatesWithPermission(ctx, parentUser, 0, 500)
	require.NoError(t, err)

	assert.Len(t, items, 1, "parent bypasses visibility filter")
	assert.Equal(t, int64(1), total)
	assert.Equal(t, sopID, items[0].ID)
	assert.True(t, items[0].HasPermission, "parent always HasPermission=true")
}
