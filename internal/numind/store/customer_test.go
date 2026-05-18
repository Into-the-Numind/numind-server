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

// newCustomerTestDB 创建 ListSubUsers 测试用的 SQLite DB（仅 user 表最小 schema）
func newCustomerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/customer_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
        CREATE TABLE user (
            id              INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at      DATETIME,
            updated_at      DATETIME,
            deleted_at      DATETIME,
            nickname        TEXT,
            username        TEXT,
            parent_user_id  INTEGER
        )`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// newPermissionTestDB 为 HasTemplatePermission / HasChatbotPermission / Grant /
// Revoke 等权限测试创建 SQLite DB，包含 user + user_template_permission +
// user_chatbot_permission 三张表的最小 schema。
//
// 不用 AutoMigrate：UserTemplatePermission 嵌入 gorm.Model 需要 deleted_at
// 列以验证软删除语义。显式 DDL 最稳妥。Post-T4: legacy_tier 列已 DROP。
func newPermissionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/customer_perm_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
        CREATE TABLE user (
            id              INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at      DATETIME,
            updated_at      DATETIME,
            deleted_at      DATETIME,
            nickname        TEXT,
            username        TEXT,
            parent_user_id  INTEGER
        )`).Error)

	// UserTemplatePermission —— 嵌入 gorm.Model（id/created_at/updated_at/deleted_at）
	require.NoError(t, db.Exec(`
        CREATE TABLE user_template_permission (
            id             INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at     DATETIME,
            updated_at     DATETIME,
            deleted_at     DATETIME,
            parent_user_id INTEGER NOT NULL,
            sub_user_id    INTEGER NOT NULL,
            template_id    INTEGER NOT NULL,
            UNIQUE (sub_user_id, template_id)
        )`).Error)

	// UserChatbotPermission —— 无软删除，唯一约束 (sub_user_id, chatbot_id)
	require.NoError(t, db.Exec(`
        CREATE TABLE user_chatbot_permission (
            id          INTEGER PRIMARY KEY AUTOINCREMENT,
            sub_user_id INTEGER NOT NULL,
            chatbot_id  INTEGER NOT NULL,
            created_at  DATETIME,
            UNIQUE (sub_user_id, chatbot_id)
        )`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// insertUserForPerm 插入测试用户（返回 id）。parentID=nil 表示父账号。
func insertUserForPerm(t *testing.T, db *gorm.DB, parentID *uint) uint {
	t.Helper()
	now := time.Now()
	var parentVal interface{}
	if parentID != nil {
		parentVal = *parentID
	}
	res := db.Exec(
		`INSERT INTO user (created_at, updated_at, parent_user_id) VALUES (?, ?, ?)`,
		now, now, parentVal,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

// insertTemplatePermissionRaw 插入原始 user_template_permission 行（可带
// deleted_at 参数模拟软删除状态）。
func insertTemplatePermissionRaw(t *testing.T, db *gorm.DB, parentID, subUserID, templateID uint, deletedAt *time.Time) {
	t.Helper()
	now := time.Now()
	res := db.Exec(
		`INSERT INTO user_template_permission
             (created_at, updated_at, deleted_at, parent_user_id, sub_user_id, template_id)
         VALUES (?, ?, ?, ?, ?, ?)`,
		now, now, deletedAt, parentID, subUserID, templateID,
	)
	require.NoError(t, res.Error)
}

func insertCustomerTestUser(t *testing.T, db *gorm.DB, parentID *uint, createdAt time.Time, nickname string) uint {
	t.Helper()
	var parentVal interface{}
	if parentID != nil {
		parentVal = *parentID
	}
	res := db.Exec(
		`INSERT INTO user (created_at, updated_at, nickname, parent_user_id) VALUES (?, ?, ?, ?)`,
		createdAt, createdAt, nickname, parentVal,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

func TestListSubUsers_IncludesParentSelf_Ordered(t *testing.T) {
	db := newCustomerTestDB(t)
	cs := NewCustomerStore(db)

	now := time.Now()
	parent := insertCustomerTestUser(t, db, nil, now.Add(-30*24*time.Hour), "ParentSelf")
	older := insertCustomerTestUser(t, db, &parent, now.Add(-20*24*time.Hour), "ChildOlder")
	newer := insertCustomerTestUser(t, db, &parent, now.Add(-5*24*time.Hour), "ChildNewer")

	// Unrelated parent X and their child Y — must NOT appear in parent's list
	otherParent := insertCustomerTestUser(t, db, nil, now, "OtherParent")
	otherChild := insertCustomerTestUser(t, db, &otherParent, now, "OtherChild")

	users, total, err := cs.ListSubUsers(context.Background(), parent, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total, "total = parent + 2 children")
	require.Len(t, users, 3)
	assert.Equal(t, parent, users[0].ID, "parent self must be first")
	assert.Equal(t, newer, users[1].ID, "newer child second (created_at DESC)")
	assert.Equal(t, older, users[2].ID, "older child third")

	for _, u := range users {
		assert.NotEqual(t, otherParent, u.ID, "otherParent must not leak")
		assert.NotEqual(t, otherChild, u.ID, "otherChild must not leak")
	}
}

// ============================================================================
// HasTemplatePermission —— 翻转后 default-deny 语义验证（child-run-permission）
// ============================================================================

// TestHasTemplatePermission_ParentAlwaysAllowed 父账号永远放行，不查权限表。
func TestHasTemplatePermission_ParentAlwaysAllowed(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	ok, err := cs.HasTemplatePermission(context.Background(), parent, 999)
	require.NoError(t, err)
	assert.True(t, ok, "父账号对任何 templateID 都应返回 true")
}

// TestHasTemplatePermission_DefaultDeny 翻转后：子账号 0 活跃记录 → false（原 true）。
func TestHasTemplatePermission_DefaultDeny(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	ok, err := cs.HasTemplatePermission(context.Background(), child, 1)
	require.NoError(t, err)
	assert.False(t, ok, "child-run-permission 翻转后，子账号 0 活跃记录应 deny")
}

// TestHasTemplatePermission_WhitelistHit 有匹配记录 → true。
func TestHasTemplatePermission_WhitelistHit(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)
	insertTemplatePermissionRaw(t, db, parent, child, 42, nil)

	ok, err := cs.HasTemplatePermission(context.Background(), child, 42)
	require.NoError(t, err)
	assert.True(t, ok, "白名单命中 → true")
}

// TestHasTemplatePermission_WhitelistMiss 有活跃记录但不含目标 templateID → false。
func TestHasTemplatePermission_WhitelistMiss(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)
	insertTemplatePermissionRaw(t, db, parent, child, 10, nil)

	ok, err := cs.HasTemplatePermission(context.Background(), child, 99)
	require.NoError(t, err)
	assert.False(t, ok, "白名单不含目标 → false")
}

// TestHasTemplatePermission_WhitelistMissAfterSoftDelete 【S3 Gate review 追加 + Task 2 code-review P1 强化】
// 软删除后活跃记录 = 0（GORM 默认 scope 过滤掉 deleted_at 非空行），按翻转后的
// default-deny 语义应返回 false。验证：被撤权的账号不会被误判为"新账号"放开权限。
//
// Task 2 code-review P1 强化：原版用 raw INSERT + deleted_at=NOW 模拟软删除，
// 这只测试了 SQLite 列过滤行为，没测到 GORM 软删除语义本身。改为通过 GORM
// `db.Delete(&model.UserTemplatePermission{...})` 触发真正的 GORM DeletedAt
// 赋值路径，确保将来 GORM 升级改动 DeletedAt 类型时测试能跟着暴露问题。
func TestHasTemplatePermission_WhitelistMissAfterSoftDelete(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	// 用 GORM ORM 写入一条权限行（走 gorm.Model CreatedAt/UpdatedAt 自动填充路径）
	perm := model.UserTemplatePermission{
		ParentUserID: parent,
		SubUserID:    child,
		TemplateID:   7,
	}
	require.NoError(t, db.Create(&perm).Error)
	require.NotZero(t, perm.ID, "Create should assign PK")

	// 通过 GORM 触发真正的软删除（而非 raw SQL 写 deleted_at）
	// 这测试了 HasTemplatePermission 的 Count 是否正确使用 GORM DeletedAt scope
	require.NoError(t, db.Delete(&perm).Error)

	// GORM Count 默认 scope 过滤软删除行 → totalPermissions = 0 → default-deny
	ok, err := cs.HasTemplatePermission(context.Background(), child, 7)
	require.NoError(t, err)
	assert.False(t, ok,
		"所有权限软删除后活跃记录 = 0，应按 default-deny 返回 false；"+
			"否则被撤权的账号会被误认为新账号而放开权限")

	// Sanity check: Unscoped 能看到软删除前的行（证明 soft-delete 生效而非 hard delete）
	var rawCount int64
	require.NoError(t, db.Unscoped().Model(&model.UserTemplatePermission{}).
		Where("sub_user_id = ?", child).Count(&rawCount).Error)
	assert.EqualValues(t, 1, rawCount, "unscoped 应看到软删除的行")

	// 强化断言：Scoped Count 应为 0（即 GORM 软删除 scope 对 Count 生效）—— 这是 P0 修复的基础
	var scopedCount int64
	require.NoError(t, db.Model(&model.UserTemplatePermission{}).
		Where("sub_user_id = ?", child).Count(&scopedCount).Error)
	assert.EqualValues(t, 0, scopedCount,
		"GORM Count 默认 scope 应过滤软删除行，这是 HasTemplatePermission P0 修复的基础")
}

// ============================================================================
// HasChatbotPermission —— default-deny + 父账号 bypass
// ============================================================================

// TestHasChatbotPermission_ParentBypass 父账号对任何 chatbot_id 都放行。
func TestHasChatbotPermission_ParentBypass(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	ok, err := cs.HasChatbotPermission(context.Background(), parent, 123)
	require.NoError(t, err)
	assert.True(t, ok, "父账号对任何 chatbotID 都应 bypass")
}

// TestHasChatbotPermission_DefaultDeny 子账号 0 记录 → false。
func TestHasChatbotPermission_DefaultDeny(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	ok, err := cs.HasChatbotPermission(context.Background(), child, 1)
	require.NoError(t, err)
	assert.False(t, ok, "子账号 0 记录应 deny")
}

// TestHasChatbotPermission_WhitelistHit 子账号有白名单记录 → true。
func TestHasChatbotPermission_WhitelistHit(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	require.NoError(t, cs.GrantChatbotPermissions(context.Background(), child, []uint{5, 6}))

	ok, err := cs.HasChatbotPermission(context.Background(), child, 5)
	require.NoError(t, err)
	assert.True(t, ok, "白名单命中 → true")

	ok, err = cs.HasChatbotPermission(context.Background(), child, 99)
	require.NoError(t, err)
	assert.False(t, ok, "白名单不含目标 → false")
}

// ============================================================================
// GrantChatbotPermissions / RevokeChatbotPermissions
// ============================================================================

// TestGrantChatbotPermissions_Idempotent 重复 grant 不报错，行数不变。
// UNIQUE (sub_user_id, chatbot_id) + ON CONFLICT DO NOTHING 保证幂等。
func TestGrantChatbotPermissions_Idempotent(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	// 第一次 grant
	require.NoError(t, cs.GrantChatbotPermissions(context.Background(), child, []uint{1, 2, 3}))
	var count1 int64
	require.NoError(t, db.Model(&model.UserChatbotPermission{}).
		Where("sub_user_id = ?", child).Count(&count1).Error)
	assert.EqualValues(t, 3, count1, "首次 grant 后应有 3 行")

	// 第二次 grant 同参数 → 应无报错、行数不变
	require.NoError(t, cs.GrantChatbotPermissions(context.Background(), child, []uint{1, 2, 3}))
	var count2 int64
	require.NoError(t, db.Model(&model.UserChatbotPermission{}).
		Where("sub_user_id = ?", child).Count(&count2).Error)
	assert.EqualValues(t, 3, count2, "重复 grant 行数不变（UNIQUE + DO NOTHING 幂等）")

	// 部分重叠、部分新增
	require.NoError(t, cs.GrantChatbotPermissions(context.Background(), child, []uint{3, 4, 5}))
	var count3 int64
	require.NoError(t, db.Model(&model.UserChatbotPermission{}).
		Where("sub_user_id = ?", child).Count(&count3).Error)
	assert.EqualValues(t, 5, count3, "{1,2,3} ∪ {3,4,5} = 5 行")
}

// TestGrantChatbotPermissions_EmptyNoOp 空数组不报错、不写入。
func TestGrantChatbotPermissions_EmptyNoOp(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	require.NoError(t, cs.GrantChatbotPermissions(context.Background(), child, []uint{}))
	require.NoError(t, cs.GrantChatbotPermissions(context.Background(), child, nil))

	var count int64
	require.NoError(t, db.Model(&model.UserChatbotPermission{}).Count(&count).Error)
	assert.EqualValues(t, 0, count)
}

// TestRevokeChatbotPermissions_MissingNoError revoke 不存在的 chatbot_id 不报错。
func TestRevokeChatbotPermissions_MissingNoError(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	// 无记录时 revoke
	require.NoError(t, cs.RevokeChatbotPermissions(context.Background(), child, []uint{1, 2, 3}))

	// 授权 + 撤销混合不存在 ID（期望部分命中不报错）
	require.NoError(t, cs.GrantChatbotPermissions(context.Background(), child, []uint{10, 20}))
	require.NoError(t, cs.RevokeChatbotPermissions(context.Background(), child, []uint{10, 99, 100}))

	remaining, err := cs.ListSubUserChatbotIDs(context.Background(), child)
	require.NoError(t, err)
	assert.Equal(t, []uint{20}, remaining, "撤销 10 后应剩 20；不存在的 99/100 应安静忽略")
}

// TestRevokeChatbotPermissions_EmptyNoOp 空数组 revoke 不报错、不改数据。
func TestRevokeChatbotPermissions_EmptyNoOp(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	require.NoError(t, cs.GrantChatbotPermissions(context.Background(), child, []uint{7, 8}))
	require.NoError(t, cs.RevokeChatbotPermissions(context.Background(), child, []uint{}))
	require.NoError(t, cs.RevokeChatbotPermissions(context.Background(), child, nil))

	remaining, err := cs.ListSubUserChatbotIDs(context.Background(), child)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uint{7, 8}, remaining, "空数组 revoke 应不影响已有授权")
}

// TestListSubUserChatbotIDs_EmptyAndPopulated 空列表 + 有授权后的列表。
func TestListSubUserChatbotIDs_EmptyAndPopulated(t *testing.T) {
	db := newPermissionTestDB(t)
	cs := NewCustomerStore(db)

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	ids, err := cs.ListSubUserChatbotIDs(context.Background(), child)
	require.NoError(t, err)
	assert.Empty(t, ids, "0 授权应返回空 slice")

	require.NoError(t, cs.GrantChatbotPermissions(context.Background(), child, []uint{3, 1, 2}))
	ids, err = cs.ListSubUserChatbotIDs(context.Background(), child)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uint{1, 2, 3}, ids)
}

// ============================================================================
// CheckSubUserFeatureGrant store 层单测 (spec D2 — store 纯查询)
// ============================================================================

// newSubUserFeaturePermTestDB 创建含 user + user_feature_permission 的 SQLite DB，
// 用于 CheckSubUserFeatureGrant 单测。
func newSubUserFeaturePermTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/sub_user_feat_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
        CREATE TABLE user (
            id              INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at      DATETIME,
            updated_at      DATETIME,
            deleted_at      DATETIME,
            nickname        TEXT,
            username        TEXT,
            parent_user_id  INTEGER
        )`).Error)

	require.NoError(t, db.Exec(`
        CREATE TABLE user_feature_permission (
            id             INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at     DATETIME,
            updated_at     DATETIME,
            deleted_at     DATETIME,
            parent_user_id INTEGER NOT NULL,
            sub_user_id    INTEGER NOT NULL,
            feature_key    TEXT NOT NULL,
            UNIQUE (sub_user_id, feature_key)
        )`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// TestCheckSubUserFeatureGrant_True 子用户有对应 feature 行 → true。
func TestCheckSubUserFeatureGrant_True(t *testing.T) {
	db := newSubUserFeaturePermTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	// 直接写入授权行
	require.NoError(t, cs.GrantFeatures(ctx, parent, child, []string{model.FeatureKeySalesAgent}))

	ok, err := cs.CheckSubUserFeatureGrant(ctx, child, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.True(t, ok, "有授权行时应返回 true")
}

// TestCheckSubUserFeatureGrant_False 子用户无对应 feature 行 → false。
func TestCheckSubUserFeatureGrant_False(t *testing.T) {
	db := newSubUserFeaturePermTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	ok, err := cs.CheckSubUserFeatureGrant(ctx, child, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.False(t, ok, "无授权行时应返回 false")
}

// TestCheckSubUserFeatureGrant_FeatureKeyDoesNotMix 不同 feature_key 不混淆。
func TestCheckSubUserFeatureGrant_FeatureKeyDoesNotMix(t *testing.T) {
	db := newSubUserFeaturePermTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertUserForPerm(t, db, nil)
	child := insertUserForPerm(t, db, &parent)

	// 只授权 content_monitor
	require.NoError(t, cs.GrantFeatures(ctx, parent, child, []string{model.FeatureKeyContentMonitor}))

	ok, err := cs.CheckSubUserFeatureGrant(ctx, child, model.FeatureKeyContentMonitor)
	require.NoError(t, err)
	assert.True(t, ok, "已授权的 content_monitor 应 true")

	ok, err = cs.CheckSubUserFeatureGrant(ctx, child, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.False(t, ok, "未授权的 sales_agent 不应被 content_monitor 混淆")
}
