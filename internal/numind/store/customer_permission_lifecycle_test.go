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

// newFullPermissionTestDB 创建包含所有权限相关表的 SQLite DB：
// user + sop_template + user_template_permission + user_chatbot_permission + user_feature_permission
func newFullPermissionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/perm_lifecycle_test.db?_busy_timeout=5000"), &gorm.Config{
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
		CREATE TABLE sop_template (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at      DATETIME,
			updated_at      DATETIME,
			deleted_at      DATETIME,
			name            TEXT NOT NULL,
			description     TEXT,
			status          TEXT DEFAULT 'active',
			creator_user_id INTEGER,
			publish_status  TEXT DEFAULT 'published',
			visibility_restricted INTEGER NOT NULL DEFAULT 0
		)`).Error)

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

	require.NoError(t, db.Exec(`
		CREATE TABLE user_chatbot_permission (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			sub_user_id INTEGER NOT NULL,
			chatbot_id  INTEGER NOT NULL,
			created_at  DATETIME,
			UNIQUE (sub_user_id, chatbot_id)
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

func insertTestUser(t *testing.T, db *gorm.DB, parentID *uint) uint {
	t.Helper()
	now := time.Now()
	var parentVal interface{}
	if parentID != nil {
		parentVal = *parentID
	}
	res := db.Exec(`INSERT INTO user (created_at, updated_at, parent_user_id) VALUES (?, ?, ?)`, now, now, parentVal)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

func insertTestTemplate(t *testing.T, db *gorm.DB, name string, creatorID uint) uint {
	t.Helper()
	now := time.Now()
	res := db.Exec(
		`INSERT INTO sop_template (created_at, updated_at, name, creator_user_id, status, publish_status)
		 VALUES (?, ?, ?, ?, 'active', 'published')`,
		now, now, name, creatorID,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

// countActiveTemplatePerms 统计子用户活跃模板权限行数（GORM 默认 scope 过滤软删除）
func countActiveTemplatePerms(t *testing.T, db *gorm.DB, subUserID uint) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(&model.UserTemplatePermission{}).
		Where("sub_user_id = ?", subUserID).Count(&count).Error)
	return count
}

// ============================================================================
// GrantTemplates — 授权 + 持久化验证
// ============================================================================

func TestGrantTemplates_PersistsAndVerifiable(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{10, 20, 30}))

	ok, err := cs.HasTemplatePermission(ctx, child, 10)
	require.NoError(t, err)
	assert.True(t, ok, "grant 后应有权限 templateID=10")

	ok, err = cs.HasTemplatePermission(ctx, child, 20)
	require.NoError(t, err)
	assert.True(t, ok, "grant 后应有权限 templateID=20")

	ok, err = cs.HasTemplatePermission(ctx, child, 30)
	require.NoError(t, err)
	assert.True(t, ok, "grant 后应有权限 templateID=30")

	ok, err = cs.HasTemplatePermission(ctx, child, 99)
	require.NoError(t, err)
	assert.False(t, ok, "未授权的 templateID=99 应 deny")

	assert.EqualValues(t, 3, countActiveTemplatePerms(t, db, child))
}

func TestGrantTemplates_Idempotent(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{5, 6}))
	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{5, 6}))

	assert.EqualValues(t, 2, countActiveTemplatePerms(t, db, child), "重复 grant 行数不变")
}

// ============================================================================
// RevokeTemplates — 撤权 + 持久化验证
// ============================================================================

func TestRevokeTemplates_RemovesPermission(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{10, 20, 30}))
	require.NoError(t, cs.RevokeTemplates(ctx, parent, child, []uint{20}))

	ok, err := cs.HasTemplatePermission(ctx, child, 10)
	require.NoError(t, err)
	assert.True(t, ok, "templateID=10 未被 revoke，应仍有权限")

	ok, err = cs.HasTemplatePermission(ctx, child, 20)
	require.NoError(t, err)
	assert.False(t, ok, "templateID=20 已被 revoke，应 deny")

	ok, err = cs.HasTemplatePermission(ctx, child, 30)
	require.NoError(t, err)
	assert.True(t, ok, "templateID=30 未被 revoke，应仍有权限")
}

func TestRevokeTemplates_AllRevoked_DefaultDeny(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{10, 20}))
	require.NoError(t, cs.RevokeTemplates(ctx, parent, child, []uint{10, 20}))

	ok, err := cs.HasTemplatePermission(ctx, child, 10)
	require.NoError(t, err)
	assert.False(t, ok, "全部 revoke 后，default-deny 应生效")

	assert.EqualValues(t, 0, countActiveTemplatePerms(t, db, child),
		"全部 revoke 后活跃权限行数应为 0")
}

// ============================================================================
// RevokeTemplates → re-GrantTemplates 回归测试
// 验证 RevokeTemplates 物理删除后，GrantTemplates 能重新插入相同 (sub_user_id, template_id)
// ============================================================================

func TestRevokeTemplates_ThenReGrant_NoUniqueConflict(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{10, 20}))

	ok, err := cs.HasTemplatePermission(ctx, child, 10)
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, cs.RevokeTemplates(ctx, parent, child, []uint{10}))

	ok, err = cs.HasTemplatePermission(ctx, child, 10)
	require.NoError(t, err)
	assert.False(t, ok, "revoke 后应 deny")

	// Re-grant — 如果 RevokeTemplates 用软删除，这里会因 UNIQUE 冲突失败
	err = cs.GrantTemplates(ctx, parent, child, []uint{10})
	require.NoError(t, err, "revoke 后 re-grant 不应触发 UNIQUE 约束冲突")

	ok, err = cs.HasTemplatePermission(ctx, child, 10)
	require.NoError(t, err)
	assert.True(t, ok, "re-grant 后应恢复权限")
}

func TestRevokeTemplates_PhysicalDelete_NoGhostRows(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{1, 2, 3}))
	require.NoError(t, cs.RevokeTemplates(ctx, parent, child, []uint{1, 2, 3}))

	var totalRows int64
	require.NoError(t, db.Unscoped().Model(&model.UserTemplatePermission{}).
		Where("sub_user_id = ?", child).Count(&totalRows).Error)
	assert.EqualValues(t, 0, totalRows,
		"RevokeTemplates 应物理删除，不留软删除残留行")
}

// ============================================================================
// SetTemplates — 覆盖模式（先清后写）+ 持久化验证
// ============================================================================

func TestSetTemplates_ReplacesAll(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{1, 2, 3, 4, 5}))
	assert.EqualValues(t, 5, countActiveTemplatePerms(t, db, child))

	require.NoError(t, cs.SetTemplates(ctx, parent, child, []uint{10, 20}))
	assert.EqualValues(t, 2, countActiveTemplatePerms(t, db, child),
		"SetTemplates 应替换为新集合")

	ok, err := cs.HasTemplatePermission(ctx, child, 1)
	require.NoError(t, err)
	assert.False(t, ok, "旧权限 templateID=1 应被清除")

	ok, err = cs.HasTemplatePermission(ctx, child, 10)
	require.NoError(t, err)
	assert.True(t, ok, "新权限 templateID=10 应存在")

	ok, err = cs.HasTemplatePermission(ctx, child, 20)
	require.NoError(t, err)
	assert.True(t, ok, "新权限 templateID=20 应存在")
}

func TestSetTemplates_EmptyList_DefaultDeny(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{1, 2}))
	require.NoError(t, cs.SetTemplates(ctx, parent, child, []uint{}))

	ok, err := cs.HasTemplatePermission(ctx, child, 1)
	require.NoError(t, err)
	assert.False(t, ok, "SetTemplates 空列表后，所有权限应被清除")

	assert.EqualValues(t, 0, countActiveTemplatePerms(t, db, child))
}

func TestSetTemplates_PersistsAcrossMultipleReads(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.SetTemplates(ctx, parent, child, []uint{100, 200, 300}))

	cs2 := NewCustomerStore(db)

	ok, err := cs2.HasTemplatePermission(ctx, child, 100)
	require.NoError(t, err)
	assert.True(t, ok, "新 store 实例读取应能看到已持久化的权限")

	ok, err = cs2.HasTemplatePermission(ctx, child, 200)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = cs2.HasTemplatePermission(ctx, child, 300)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = cs2.HasTemplatePermission(ctx, child, 999)
	require.NoError(t, err)
	assert.False(t, ok, "未授权模板仍应 deny")
}

// ============================================================================
// BulkGrantAllTemplates — 新子用户创建时批量授权
// ============================================================================

// BulkGrantAllTemplates 使用 raw SQL 中的 NOW()（MySQL 函数），SQLite 不支持。
// 这些测试只验证 MySQL 环境下的行为，SQLite 下跳过。
func TestBulkGrantAllTemplates_GrantsAllExisting(t *testing.T) {
	t.Skip("BulkGrantAllTemplates uses MySQL NOW() — skipped under SQLite test DB")
}

func TestBulkGrantAllTemplates_DoesNotIncludeSoftDeletedTemplates(t *testing.T) {
	t.Skip("BulkGrantAllTemplates uses MySQL NOW() — skipped under SQLite test DB")
}

// ============================================================================
// GrantTemplateToAllSubUsers — 新模板发布时自动授权给所有子用户
// ============================================================================

func TestGrantTemplateToAllSubUsers_GrantsToAllChildren(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child1 := insertTestUser(t, db, &parent)
	child2 := insertTestUser(t, db, &parent)
	child3 := insertTestUser(t, db, &parent)

	newTpl := insertTestTemplate(t, db, "NewSOP", parent)
	require.NoError(t, cs.GrantTemplateToAllSubUsers(ctx, parent, newTpl))

	for _, childID := range []uint{child1, child2, child3} {
		ok, err := cs.HasTemplatePermission(ctx, childID, newTpl)
		require.NoError(t, err)
		assert.True(t, ok, "子用户 %d 应获得新模板权限", childID)
	}
}

func TestGrantTemplateToAllSubUsers_SkipsDuplicates(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)
	tpl := insertTestTemplate(t, db, "SOP", parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{tpl}))
	require.NoError(t, cs.GrantTemplateToAllSubUsers(ctx, parent, tpl))

	assert.EqualValues(t, 1, countActiveTemplatePerms(t, db, child),
		"已有授权不应重复插入")
}

func TestGrantTemplateToAllSubUsers_DoesNotLeakToOtherParent(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parentA := insertTestUser(t, db, nil)
	parentB := insertTestUser(t, db, nil)
	childA := insertTestUser(t, db, &parentA)
	childB := insertTestUser(t, db, &parentB)

	tpl := insertTestTemplate(t, db, "SOP-A", parentA)
	require.NoError(t, cs.GrantTemplateToAllSubUsers(ctx, parentA, tpl))

	ok, err := cs.HasTemplatePermission(ctx, childA, tpl)
	require.NoError(t, err)
	assert.True(t, ok, "parentA 的子用户应被授权")

	assert.EqualValues(t, 0, countActiveTemplatePerms(t, db, childB),
		"parentB 的子用户不应被 parentA 的授权影响")
}

// ============================================================================
// Chatbot 权限全生命周期: grant → verify → revoke → verify → re-grant → verify
// ============================================================================

func TestChatbotPermission_FullLifecycle(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	// 1. 初始状态: default-deny
	ok, err := cs.HasChatbotPermission(ctx, child, 100)
	require.NoError(t, err)
	assert.False(t, ok, "初始状态应 deny")

	// 2. Grant chatbot 100, 200
	require.NoError(t, cs.GrantChatbotPermissions(ctx, child, []uint{100, 200}))

	ok, err = cs.HasChatbotPermission(ctx, child, 100)
	require.NoError(t, err)
	assert.True(t, ok, "grant 后 chatbot=100 应 allow")

	ok, err = cs.HasChatbotPermission(ctx, child, 200)
	require.NoError(t, err)
	assert.True(t, ok, "grant 后 chatbot=200 应 allow")

	ok, err = cs.HasChatbotPermission(ctx, child, 300)
	require.NoError(t, err)
	assert.False(t, ok, "未授权 chatbot=300 应 deny")

	// 3. Revoke chatbot 100
	require.NoError(t, cs.RevokeChatbotPermissions(ctx, child, []uint{100}))

	ok, err = cs.HasChatbotPermission(ctx, child, 100)
	require.NoError(t, err)
	assert.False(t, ok, "revoke 后 chatbot=100 应 deny")

	ok, err = cs.HasChatbotPermission(ctx, child, 200)
	require.NoError(t, err)
	assert.True(t, ok, "chatbot=200 未被 revoke，应仍 allow")

	// 4. Re-grant chatbot 100
	require.NoError(t, cs.GrantChatbotPermissions(ctx, child, []uint{100}))

	ok, err = cs.HasChatbotPermission(ctx, child, 100)
	require.NoError(t, err)
	assert.True(t, ok, "re-grant 后 chatbot=100 应恢复 allow")

	// 5. Revoke 全部
	require.NoError(t, cs.RevokeChatbotPermissions(ctx, child, []uint{100, 200}))

	ids, err := cs.ListSubUserChatbotIDs(ctx, child)
	require.NoError(t, err)
	assert.Empty(t, ids, "全部 revoke 后应无授权")

	ok, err = cs.HasChatbotPermission(ctx, child, 100)
	require.NoError(t, err)
	assert.False(t, ok, "全部 revoke 后 default-deny")
}

func TestChatbotPermission_PhysicalDelete_NoGhostRows(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantChatbotPermissions(ctx, child, []uint{1, 2, 3}))
	require.NoError(t, cs.RevokeChatbotPermissions(ctx, child, []uint{1, 2, 3}))

	var totalRows int64
	require.NoError(t, db.Unscoped().Table("user_chatbot_permission").
		Where("sub_user_id = ?", child).Count(&totalRows).Error)
	assert.EqualValues(t, 0, totalRows,
		"chatbot 权限用物理 DELETE，revoke 后应无残留行（不像 template 的软删除）")
}

// ============================================================================
// Feature 权限 (SalesRAG 重点关注)
// ============================================================================

func TestFeaturePermission_SalesRAG_GrantAndVerify(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	// 父账号的父账户 bypass 语义已上移至 biz 层 (spec D2)。
	// store 层 CheckSubUserFeatureGrant 纯查询：无 user_feature_permission 行 → false。
	ok, err := cs.CheckSubUserFeatureGrant(ctx, parent, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.False(t, ok, "store 层纯查询：父账号在 user_feature_permission 表无行 → false（bypass 在 biz 层）")

	// 子账号初始无权限
	ok, err = cs.CheckSubUserFeatureGrant(ctx, child, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.False(t, ok, "子账号初始应无 sales_agent 权限")

	// Grant
	require.NoError(t, cs.GrantFeatures(ctx, parent, child, []string{model.FeatureKeySalesAgent}))

	ok, err = cs.CheckSubUserFeatureGrant(ctx, child, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.True(t, ok, "grant 后子账号应有 sales_agent 权限")

	// 其他功能仍无权限
	ok, err = cs.CheckSubUserFeatureGrant(ctx, child, model.FeatureKeyContentMonitor)
	require.NoError(t, err)
	assert.False(t, ok, "未授权的 content_monitor 应 deny")
}

func TestFeaturePermission_SalesRAG_RevokeAndVerify(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantFeatures(ctx, parent, child,
		[]string{model.FeatureKeySalesAgent, model.FeatureKeyContentMonitor}))

	// Revoke sales_agent only
	require.NoError(t, cs.RevokeFeatures(ctx, parent, child, []string{model.FeatureKeySalesAgent}))

	ok, err := cs.CheckSubUserFeatureGrant(ctx, child, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.False(t, ok, "revoke 后 sales_agent 应 deny")

	ok, err = cs.CheckSubUserFeatureGrant(ctx, child, model.FeatureKeyContentMonitor)
	require.NoError(t, err)
	assert.True(t, ok, "content_monitor 未被 revoke，应仍 allow")
}

func TestFeaturePermission_SalesRAG_RevokeAndReGrant(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	// Grant → Revoke → Re-grant
	require.NoError(t, cs.GrantFeatures(ctx, parent, child, []string{model.FeatureKeySalesAgent}))
	require.NoError(t, cs.RevokeFeatures(ctx, parent, child, []string{model.FeatureKeySalesAgent}))

	ok, err := cs.CheckSubUserFeatureGrant(ctx, child, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.False(t, ok, "revoke 后应 deny")

	// Re-grant（应恢复软删除记录而非创建新行）
	require.NoError(t, cs.GrantFeatures(ctx, parent, child, []string{model.FeatureKeySalesAgent}))

	ok, err = cs.CheckSubUserFeatureGrant(ctx, child, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.True(t, ok, "re-grant 后应恢复 allow")

	// 验证无重复行（unscoped 行数应为 1，因为是恢复而非新建）
	var totalRows int64
	require.NoError(t, db.Unscoped().Model(&model.UserFeaturePermission{}).
		Where("sub_user_id = ? AND feature_key = ?", child, model.FeatureKeySalesAgent).
		Count(&totalRows).Error)
	assert.EqualValues(t, 1, totalRows,
		"re-grant 应恢复软删除记录，不应产生重复行")
}

func TestFeaturePermission_ListUserFeatures(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	features, err := cs.ListUserFeatures(ctx, child)
	require.NoError(t, err)
	assert.Empty(t, features, "初始应无功能权限")

	require.NoError(t, cs.GrantFeatures(ctx, parent, child,
		[]string{model.FeatureKeySalesAgent, model.FeatureKeyContentMonitor, model.FeatureKeySelfServiceConfig}))

	features, err = cs.ListUserFeatures(ctx, child)
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]string{model.FeatureKeySalesAgent, model.FeatureKeyContentMonitor, model.FeatureKeySelfServiceConfig},
		features, "应列出所有已授权的功能")

	require.NoError(t, cs.RevokeFeatures(ctx, parent, child, []string{model.FeatureKeyContentMonitor}))

	features, err = cs.ListUserFeatures(ctx, child)
	require.NoError(t, err)
	assert.ElementsMatch(t,
		[]string{model.FeatureKeySalesAgent, model.FeatureKeySelfServiceConfig},
		features, "revoke 后列表应更新")
}

func TestFeaturePermission_GrantIdempotent(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantFeatures(ctx, parent, child, []string{model.FeatureKeySalesAgent}))
	require.NoError(t, cs.GrantFeatures(ctx, parent, child, []string{model.FeatureKeySalesAgent}))

	var count int64
	require.NoError(t, db.Unscoped().Model(&model.UserFeaturePermission{}).
		Where("sub_user_id = ? AND feature_key = ?", child, model.FeatureKeySalesAgent).
		Count(&count).Error)
	assert.EqualValues(t, 1, count, "重复 grant 不应产生重复行")
}

// ============================================================================
// 跨子用户隔离性测试
// ============================================================================

func TestCrossChildIsolation_TemplatePermissions(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	childA := insertTestUser(t, db, &parent)
	childB := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, childA, []uint{10, 20}))
	require.NoError(t, cs.GrantTemplates(ctx, parent, childB, []uint{20, 30}))

	// childA
	ok, err := cs.HasTemplatePermission(ctx, childA, 10)
	require.NoError(t, err)
	assert.True(t, ok, "childA 应有 10")

	ok, err = cs.HasTemplatePermission(ctx, childA, 30)
	require.NoError(t, err)
	assert.False(t, ok, "childA 不应有 30（只授给了 childB）")

	// childB
	ok, err = cs.HasTemplatePermission(ctx, childB, 10)
	require.NoError(t, err)
	assert.False(t, ok, "childB 不应有 10（只授给了 childA）")

	ok, err = cs.HasTemplatePermission(ctx, childB, 30)
	require.NoError(t, err)
	assert.True(t, ok, "childB 应有 30")

	// Revoke childA's 20, childB's 20 should be unaffected
	require.NoError(t, cs.RevokeTemplates(ctx, parent, childA, []uint{20}))

	ok, err = cs.HasTemplatePermission(ctx, childA, 20)
	require.NoError(t, err)
	assert.False(t, ok, "childA 的 20 已 revoke")

	ok, err = cs.HasTemplatePermission(ctx, childB, 20)
	require.NoError(t, err)
	assert.True(t, ok, "childB 的 20 不应受 childA revoke 影响")
}

func TestCrossChildIsolation_ChatbotPermissions(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	childA := insertTestUser(t, db, &parent)
	childB := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantChatbotPermissions(ctx, childA, []uint{1, 2}))
	require.NoError(t, cs.GrantChatbotPermissions(ctx, childB, []uint{2, 3}))

	ok, err := cs.HasChatbotPermission(ctx, childA, 3)
	require.NoError(t, err)
	assert.False(t, ok, "childA 不应有 chatbot=3")

	ok, err = cs.HasChatbotPermission(ctx, childB, 1)
	require.NoError(t, err)
	assert.False(t, ok, "childB 不应有 chatbot=1")

	require.NoError(t, cs.RevokeChatbotPermissions(ctx, childA, []uint{2}))

	ok, err = cs.HasChatbotPermission(ctx, childB, 2)
	require.NoError(t, err)
	assert.True(t, ok, "childB 的 chatbot=2 不应受 childA revoke 影响")
}

func TestCrossChildIsolation_FeaturePermissions(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	childA := insertTestUser(t, db, &parent)
	childB := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantFeatures(ctx, parent, childA, []string{model.FeatureKeySalesAgent}))

	ok, err := cs.CheckSubUserFeatureGrant(ctx, childA, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.True(t, ok, "childA 应有 sales_agent")

	ok, err = cs.CheckSubUserFeatureGrant(ctx, childB, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.False(t, ok, "childB 未被授权 sales_agent，应 deny")

	require.NoError(t, cs.RevokeFeatures(ctx, parent, childA, []string{model.FeatureKeySalesAgent}))

	ok, err = cs.CheckSubUserFeatureGrant(ctx, childA, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.False(t, ok, "childA revoke 后应 deny")
}

// ============================================================================
// 跨父账户隔离性测试
// ============================================================================

func TestCrossParentIsolation_TemplatePermissions(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parentA := insertTestUser(t, db, nil)
	parentB := insertTestUser(t, db, nil)
	childA := insertTestUser(t, db, &parentA)
	childB := insertTestUser(t, db, &parentB)

	require.NoError(t, cs.GrantTemplates(ctx, parentA, childA, []uint{10, 20}))
	require.NoError(t, cs.GrantTemplates(ctx, parentB, childB, []uint{30, 40}))

	ok, err := cs.HasTemplatePermission(ctx, childA, 30)
	require.NoError(t, err)
	assert.False(t, ok, "childA 不应有 parentB 授权的模板")

	ok, err = cs.HasTemplatePermission(ctx, childB, 10)
	require.NoError(t, err)
	assert.False(t, ok, "childB 不应有 parentA 授权的模板")
}

// ============================================================================
// SetTemplates 软删除交互边界场景
// ============================================================================

func TestSetTemplates_SoftDeletedRowsNotBlockNewGrant(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	// Grant then Set to different set (old ones soft-deleted)
	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{1, 2, 3}))
	require.NoError(t, cs.SetTemplates(ctx, parent, child, []uint{4, 5}))

	// Now Set back to include some old IDs — this would fail if soft-deleted
	// rows block the UNIQUE constraint
	require.NoError(t, cs.SetTemplates(ctx, parent, child, []uint{1, 2, 4}))

	ok, err := cs.HasTemplatePermission(ctx, child, 1)
	require.NoError(t, err)
	assert.True(t, ok, "re-grant templateID=1 应成功")

	ok, err = cs.HasTemplatePermission(ctx, child, 3)
	require.NoError(t, err)
	assert.False(t, ok, "templateID=3 不在新集合中，应 deny")

	assert.EqualValues(t, 3, countActiveTemplatePerms(t, db, child))
}

// ============================================================================
// GetAuthorizedTemplates — 联表查询验证
// ============================================================================

func TestGetAuthorizedTemplates_OnlyReturnsGrantedActive(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	tpl1 := insertTestTemplate(t, db, "Granted-A", parent)
	tpl2 := insertTestTemplate(t, db, "Granted-B", parent)
	_ = insertTestTemplate(t, db, "NotGranted", parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{tpl1, tpl2}))

	templates, err := cs.GetAuthorizedTemplates(ctx, child)
	require.NoError(t, err)

	var ids []uint
	for _, tpl := range templates {
		ids = append(ids, tpl.ID)
	}
	assert.ElementsMatch(t, []uint{tpl1, tpl2}, ids,
		"只应返回已授权的模板")
}

func TestGetAuthorizedTemplates_ExcludesSoftDeletedPerms(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	tpl1 := insertTestTemplate(t, db, "WillRevoke", parent)
	tpl2 := insertTestTemplate(t, db, "StillGranted", parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{tpl1, tpl2}))
	require.NoError(t, cs.RevokeTemplates(ctx, parent, child, []uint{tpl1}))

	templates, err := cs.GetAuthorizedTemplates(ctx, child)
	require.NoError(t, err)

	var ids []uint
	for _, tpl := range templates {
		ids = append(ids, tpl.ID)
	}
	assert.Equal(t, []uint{tpl2}, ids,
		"revoke（软删除）后的模板不应出现在授权列表中")
}

// ============================================================================
// ListUserTemplatePermissions — 权限列表查询验证
// ============================================================================

func TestListUserTemplatePermissions_ReturnsActive(t *testing.T) {
	db := newFullPermissionTestDB(t)
	cs := NewCustomerStore(db)
	ctx := context.Background()

	parent := insertTestUser(t, db, nil)
	child := insertTestUser(t, db, &parent)

	require.NoError(t, cs.GrantTemplates(ctx, parent, child, []uint{10, 20, 30}))
	require.NoError(t, cs.RevokeTemplates(ctx, parent, child, []uint{20}))

	perms, err := cs.ListUserTemplatePermissions(ctx, child)
	require.NoError(t, err)

	var tplIDs []uint
	for _, p := range perms {
		tplIDs = append(tplIDs, p.TemplateID)
	}
	assert.ElementsMatch(t, []uint{10, 30}, tplIDs,
		"应只返回活跃（未软删除）的权限")
}
