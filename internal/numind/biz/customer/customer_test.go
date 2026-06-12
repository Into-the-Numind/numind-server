package customer

import (
	"context"
	"errors"
	"testing"
	"time"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ============================================================================
// Chatbot 权限 biz 层单测（child-run-permission spec §3.5 / plan Task 3）
//
// 测试目标：父子校验 + chatbot 归属校验 + 委托 store 的幂等 grant/revoke。
//
// 测试 DB：纯 SQLite 最小 schema，映射 user + user_chatbot_permission +
//          chatbot_config 三张核心表；hand-roll user 是为了对齐 model.User
//          剔除 legacy_tier 列后的真实结构 (post-T4)。
// ============================================================================

// newBizTestDB 创建 biz 层 chatbot 权限测试所需的 SQLite DB。
func newBizTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/biz_customer_test.db?_busy_timeout=5000"), &gorm.Config{
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
        CREATE TABLE user_chatbot_permission (
            id          INTEGER PRIMARY KEY AUTOINCREMENT,
            sub_user_id INTEGER NOT NULL,
            chatbot_id  INTEGER NOT NULL,
            created_at  DATETIME,
            UNIQUE (sub_user_id, chatbot_id)
        )`).Error)

	// chatbot_config 对齐 model.ChatbotConfig 的最小字段（soft-delete via deleted_at）
	require.NoError(t, db.Exec(`
        CREATE TABLE chatbot_config (
            id              INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at      DATETIME,
            updated_at      DATETIME,
            deleted_at      DATETIME,
            user_id         INTEGER NOT NULL,
            name            TEXT NOT NULL DEFAULT '',
            description     TEXT DEFAULT '',
            system_prompt   TEXT NOT NULL DEFAULT '',
            status          TEXT NOT NULL DEFAULT 'draft',
            greeting_enabled INTEGER NOT NULL DEFAULT 0,
            greeting_message TEXT DEFAULT ''
        )`).Error)

	// sop_template —— ListVisibleTemplates(creator_user_id, status, publish_status) +
	// CountActiveAuthorizedSopTemplates(JOIN on template_id, status='active')。
	require.NoError(t, db.Exec(`
        CREATE TABLE sop_template (
            id              INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at      DATETIME,
            updated_at      DATETIME,
            deleted_at      DATETIME,
            creator_user_id INTEGER,
            name            TEXT NOT NULL DEFAULT '',
            description     TEXT DEFAULT '',
            status          TEXT NOT NULL DEFAULT 'active',
            publish_status  TEXT NOT NULL DEFAULT 'published'
        )`).Error)

	// user_template_permission —— 子账户 SOP 白名单（CountActiveAuthorizedSopTemplates）。
	require.NoError(t, db.Exec(`
        CREATE TABLE user_template_permission (
            id             INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at     DATETIME,
            updated_at     DATETIME,
            deleted_at     DATETIME,
            parent_user_id INTEGER,
            sub_user_id    INTEGER NOT NULL,
            template_id    INTEGER NOT NULL
        )`).Error)

	// user_feature_permission —— 子账户 sales_agent Layer 1（CheckSubUserFeatureGrant）。
	require.NoError(t, db.Exec(`
        CREATE TABLE user_feature_permission (
            id             INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at     DATETIME,
            updated_at     DATETIME,
            deleted_at     DATETIME,
            parent_user_id INTEGER,
            sub_user_id    INTEGER NOT NULL,
            feature_key    TEXT NOT NULL
        )`).Error)

	// sales_agent_owner —— sales_agent Layer 0（SalesAgentOwners().Exists）。
	require.NoError(t, db.Exec(`
        CREATE TABLE sales_agent_owner (
            parent_user_id INTEGER PRIMARY KEY,
            created_at     DATETIME
        )`).Error)

	// sop_run / chatbot_session —— GetSubUserRunCounts 聚合（缺表会 fallback，建出来避免噪音）。
	require.NoError(t, db.Exec(`
        CREATE TABLE sop_run (
            id         INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at DATETIME,
            deleted_at DATETIME,
            user_id    INTEGER NOT NULL
        )`).Error)
	require.NoError(t, db.Exec(`
        CREATE TABLE chatbot_session (
            id         INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at DATETIME,
            updated_at DATETIME,
            deleted_at DATETIME,
            user_id    INTEGER NOT NULL
        )`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// insertBizSopTemplate 插入 sop_template 行（返回 id）。
func insertBizSopTemplate(t *testing.T, db *gorm.DB, creatorID uint, name, status, publishStatus string) uint {
	t.Helper()
	now := time.Now()
	res := db.Exec(
		`INSERT INTO sop_template (created_at, updated_at, creator_user_id, name, status, publish_status)
         VALUES (?, ?, ?, ?, ?, ?)`,
		now, now, creatorID, name, status, publishStatus,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

// grantBizTemplate 给子账户写一条 SOP 白名单。
func grantBizTemplate(t *testing.T, db *gorm.DB, parentID, subID, templateID uint) {
	t.Helper()
	now := time.Now()
	require.NoError(t, db.Exec(
		`INSERT INTO user_template_permission (created_at, updated_at, parent_user_id, sub_user_id, template_id)
         VALUES (?, ?, ?, ?, ?)`,
		now, now, parentID, subID, templateID,
	).Error)
}

// insertBizUser 插入 user 行（返回 id）。parentID=nil 表示父账号。
func insertBizUser(t *testing.T, db *gorm.DB, parentID *uint) uint {
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

// insertBizChatbot 插入 chatbot_config 行（返回 id）。
func insertBizChatbot(t *testing.T, db *gorm.DB, userID uint, name, status string) uint {
	t.Helper()
	now := time.Now()
	res := db.Exec(
		`INSERT INTO chatbot_config (created_at, updated_at, user_id, name, status)
         VALUES (?, ?, ?, ?, ?)`,
		now, now, userID, name, status,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

// newBizUnderTest 构造被测 biz 实例（绑定 SQLite DB 的 IStore）。
func newBizUnderTest(t *testing.T) (*gorm.DB, ICustomerBiz) {
	t.Helper()
	db := newBizTestDB(t)
	ds := store.NewTestStore(db)
	return db, New(ds)
}

// ----------------------------------------------------------------------------
// CheckChatbotPermission —— 透传到 store，只需 smoke test
// ----------------------------------------------------------------------------

// TestCheckChatbotPermission_DelegatesToStore 验证 biz 层 CheckChatbotPermission
// 直接透传到 store.HasChatbotPermission；父账号 → true，子账号 0 记录 → false。
func TestCheckChatbotPermission_DelegatesToStore(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	child := insertBizUser(t, db, &parent)

	ok, err := biz.CheckChatbotPermission(context.Background(), parent, 999)
	require.NoError(t, err)
	assert.True(t, ok, "父账号应 bypass")

	ok, err = biz.CheckChatbotPermission(context.Background(), child, 1)
	require.NoError(t, err)
	assert.False(t, ok, "子账号 0 记录 → default-deny")
}

// ----------------------------------------------------------------------------
// GrantChatbots —— 父子校验 + 归属校验 + 幂等
// ----------------------------------------------------------------------------

// TestGrantChatbots_Success 正常 grant：父账号给自己的子账号授权自己拥有的 chatbot。
func TestGrantChatbots_Success(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	child := insertBizUser(t, db, &parent)
	bot1 := insertBizChatbot(t, db, parent, "bot1", "published")
	bot2 := insertBizChatbot(t, db, parent, "bot2", "published")

	err := biz.GrantChatbots(context.Background(), parent, child, []uint{bot1, bot2})
	require.NoError(t, err)

	// 验证写入：子账号白名单应含 bot1, bot2
	ok, err := biz.CheckChatbotPermission(context.Background(), child, bot1)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = biz.CheckChatbotPermission(context.Background(), child, bot2)
	require.NoError(t, err)
	assert.True(t, ok)

	// 再次 grant 同一 chatbot → 幂等，不报错
	err = biz.GrantChatbots(context.Background(), parent, child, []uint{bot1})
	require.NoError(t, err)
}

// TestGrantChatbots_CrossParentRejected 父账号 A 试图给父账号 B 的子账号授权
// → GetSubUser 返回 ErrRecordNotFound → biz 转为 ErrForbidden。
func TestGrantChatbots_CrossParentRejected(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parentA := insertBizUser(t, db, nil)
	parentB := insertBizUser(t, db, nil)
	childOfB := insertBizUser(t, db, &parentB)
	botA := insertBizChatbot(t, db, parentA, "botA", "published")

	err := biz.GrantChatbots(context.Background(), parentA, childOfB, []uint{botA})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrForbidden),
		"父 A 给父 B 的子账号授权必须返回 ErrForbidden，实际: %v", err)
}

// TestGrantChatbots_ChatbotNotOwned 传入非自己拥有的 chatbot_id → ErrChatbotNotFound。
func TestGrantChatbots_ChatbotNotOwned(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parentA := insertBizUser(t, db, nil)
	parentB := insertBizUser(t, db, nil)
	childOfA := insertBizUser(t, db, &parentA)

	botA := insertBizChatbot(t, db, parentA, "botA", "published")
	botB := insertBizChatbot(t, db, parentB, "botB", "published") // 属于父 B

	// 父 A 给自己的子账号授权，混入一个父 B 的 chatbot → 拒绝
	err := biz.GrantChatbots(context.Background(), parentA, childOfA, []uint{botA, botB})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrChatbotNotFound),
		"授权包含非自己 chatbot 必须返回 ErrChatbotNotFound，实际: %v", err)

	// 传入不存在的 chatbot_id 也拒绝
	err = biz.GrantChatbots(context.Background(), parentA, childOfA, []uint{botA, 9999})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrChatbotNotFound),
		"不存在的 chatbot_id 必须返回 ErrChatbotNotFound，实际: %v", err)
}

// TestGrantChatbots_SelfParentBypassed 【S3 Gate review 追加】
// 父账号对自己调 grant（parentID == subID）→ 应被拒绝，与既有 GrantTemplates
// 行为一致（父账号 row 的 parent_user_id 为 NULL，不匹配 parentID，GetSubUser
// 返回 ErrRecordNotFound）。
func TestGrantChatbots_SelfParentBypassed(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	bot := insertBizChatbot(t, db, parent, "bot", "published")

	err := biz.GrantChatbots(context.Background(), parent, parent, []uint{bot})
	require.Error(t, err, "父账号对自己调 grant 应失败（与 GrantTemplates 一致）")
	assert.True(t, errors.Is(err, errno.ErrForbidden),
		"self-grant 应返回 ErrForbidden，实际: %v", err)
}

// ----------------------------------------------------------------------------
// RevokeChatbots
// ----------------------------------------------------------------------------

// TestRevokeChatbots_Success 正常 revoke。
func TestRevokeChatbots_Success(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	child := insertBizUser(t, db, &parent)
	bot1 := insertBizChatbot(t, db, parent, "bot1", "published")
	bot2 := insertBizChatbot(t, db, parent, "bot2", "published")
	bot3 := insertBizChatbot(t, db, parent, "bot3", "published")

	require.NoError(t, biz.GrantChatbots(context.Background(), parent, child,
		[]uint{bot1, bot2, bot3}))

	// Revoke bot2
	require.NoError(t, biz.RevokeChatbots(context.Background(), parent, child, []uint{bot2}))

	ok, err := biz.CheckChatbotPermission(context.Background(), child, bot1)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = biz.CheckChatbotPermission(context.Background(), child, bot2)
	require.NoError(t, err)
	assert.False(t, ok, "bot2 撤销后应 deny")
	ok, err = biz.CheckChatbotPermission(context.Background(), child, bot3)
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestRevokeChatbots_CrossParentRejected 跨父账号 revoke → ErrForbidden。
func TestRevokeChatbots_CrossParentRejected(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parentA := insertBizUser(t, db, nil)
	parentB := insertBizUser(t, db, nil)
	childOfB := insertBizUser(t, db, &parentB)
	botA := insertBizChatbot(t, db, parentA, "botA", "published")

	err := biz.RevokeChatbots(context.Background(), parentA, childOfB, []uint{botA})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrForbidden), "实际: %v", err)
}

// ----------------------------------------------------------------------------
// ListSubUserChatbots
// ----------------------------------------------------------------------------

// TestListSubUserChatbots_ReturnsDetailsForOwnedIDs 已授权的 chatbot 应返回详情列表。
func TestListSubUserChatbots_ReturnsDetailsForOwnedIDs(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	child := insertBizUser(t, db, &parent)
	bot1 := insertBizChatbot(t, db, parent, "售前助手", "published")
	bot2 := insertBizChatbot(t, db, parent, "售后助手", "published")
	_ = insertBizChatbot(t, db, parent, "未授权助手", "published") // 不授权

	require.NoError(t, biz.GrantChatbots(context.Background(), parent, child, []uint{bot1, bot2}))

	configs, err := biz.ListSubUserChatbots(context.Background(), parent, child)
	require.NoError(t, err)
	require.Len(t, configs, 2)

	names := map[string]bool{}
	for _, cfg := range configs {
		names[cfg.Name] = true
	}
	assert.True(t, names["售前助手"])
	assert.True(t, names["售后助手"])
	assert.False(t, names["未授权助手"])
}

// TestListSubUserChatbots_CrossParentRejected 跨父账号列表查询 → ErrForbidden。
func TestListSubUserChatbots_CrossParentRejected(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parentA := insertBizUser(t, db, nil)
	parentB := insertBizUser(t, db, nil)
	childOfB := insertBizUser(t, db, &parentB)

	_, err := biz.ListSubUserChatbots(context.Background(), parentA, childOfB)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrForbidden), "实际: %v", err)
}

// TestListSubUserChatbots_EmptyWhenNoGrants 未授权 → 空列表（非 nil，非 error）。
func TestListSubUserChatbots_EmptyWhenNoGrants(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	child := insertBizUser(t, db, &parent)

	configs, err := biz.ListSubUserChatbots(context.Background(), parent, child)
	require.NoError(t, err)
	assert.NotNil(t, configs)
	assert.Len(t, configs, 0)
}

// ----------------------------------------------------------------------------
// BatchGrantChatbots / BatchRevokeChatbots
// ----------------------------------------------------------------------------

// TestBatchGrantChatbots_MixedValid 2 子账号 × 2 chatbot 全部合法 → 全部 grant 成功。
func TestBatchGrantChatbots_MixedValid(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	child1 := insertBizUser(t, db, &parent)
	child2 := insertBizUser(t, db, &parent)
	bot1 := insertBizChatbot(t, db, parent, "bot1", "published")
	bot2 := insertBizChatbot(t, db, parent, "bot2", "published")

	err := biz.BatchGrantChatbots(context.Background(), parent,
		[]uint{child1, child2}, []uint{bot1, bot2})
	require.NoError(t, err)

	for _, child := range []uint{child1, child2} {
		for _, bot := range []uint{bot1, bot2} {
			ok, cerr := biz.CheckChatbotPermission(context.Background(), child, bot)
			require.NoError(t, cerr)
			assert.True(t, ok, "child=%d bot=%d 应已授权", child, bot)
		}
	}
}

// TestBatchGrantChatbots_CrossParentSubUserRejected 批量中混入跨父账号子账号 → 拒绝，
// 且已处理的合法子账号权限已写入（接受部分成功语义，幂等重试安全）。
func TestBatchGrantChatbots_CrossParentSubUserRejected(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parentA := insertBizUser(t, db, nil)
	parentB := insertBizUser(t, db, nil)
	child1OfA := insertBizUser(t, db, &parentA)
	childOfB := insertBizUser(t, db, &parentB)
	botA := insertBizChatbot(t, db, parentA, "botA", "published")

	// 父 A 尝试给 [child1OfA, childOfB] 授权 botA → childOfB 处失败
	err := biz.BatchGrantChatbots(context.Background(), parentA,
		[]uint{child1OfA, childOfB}, []uint{botA})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrForbidden), "实际: %v", err)
}

// TestBatchRevokeChatbots_Success 批量撤销。
func TestBatchRevokeChatbots_Success(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	child1 := insertBizUser(t, db, &parent)
	child2 := insertBizUser(t, db, &parent)
	bot1 := insertBizChatbot(t, db, parent, "bot1", "published")
	bot2 := insertBizChatbot(t, db, parent, "bot2", "published")

	require.NoError(t, biz.BatchGrantChatbots(context.Background(), parent,
		[]uint{child1, child2}, []uint{bot1, bot2}))

	require.NoError(t, biz.BatchRevokeChatbots(context.Background(), parent,
		[]uint{child1, child2}, []uint{bot1}))

	for _, child := range []uint{child1, child2} {
		ok, err := biz.CheckChatbotPermission(context.Background(), child, bot1)
		require.NoError(t, err)
		assert.False(t, ok, "child=%d bot1 应被撤销", child)
		ok, err = biz.CheckChatbotPermission(context.Background(), child, bot2)
		require.NoError(t, err)
		assert.True(t, ok, "child=%d bot2 应保留", child)
	}
}

// TestBatchGrantChatbots_UnknownChatbotRejected 批量中混入非自己 chatbot → 全部拒绝。
func TestBatchGrantChatbots_UnknownChatbotRejected(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	child := insertBizUser(t, db, &parent)
	bot := insertBizChatbot(t, db, parent, "bot", "published")

	err := biz.BatchGrantChatbots(context.Background(), parent,
		[]uint{child}, []uint{bot, 9999})
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrChatbotNotFound), "实际: %v", err)

	// 验证未写入任何权限（前置校验失败，不应调用 store）
	ok, cerr := biz.CheckChatbotPermission(context.Background(), child, bot)
	require.NoError(t, cerr)
	assert.False(t, ok, "chatbot 归属校验失败不应写入任何权限")
}

// 保留：确保 model 包在非 Chatbot 场景仍被 import（防止 goimports 删除）
var _ = model.ChatbotConfig{}

// ============================================================================
// CheckFeaturePermission biz 矩阵测试 (spec §6.2 / spec D2)
//
// 覆盖 9 个场景：
//   1. SalesAgent_ParentOwnerExists → true
//   2. SalesAgent_ParentOwnerAbsent → false  (关键回归: admin 路径)
//   3. SalesAgent_SubUserBothLayers → true
//   4. SalesAgent_SubUserLayer1Only → false  (Layer 0 拦截)
//   5. SalesAgent_SubUserLayer0Only → false  (Layer 1 拦截)
//   6. ContentMonitor_ParentBypass → true    (关键回归: bypass 保留)
//   7. SelfServiceConfig_ParentBypass → true (关键回归: bypass 保留)
//   8. ContentMonitor_SubUserDeny → false
//   9. UserNotFound → (false, err)
// ============================================================================

// newCheckFeaturePermTestDB 创建含 user + user_feature_permission + sales_agent_owner
// 三张表的 SQLite DB，供 CheckFeaturePermission biz 矩阵测试使用。
func newCheckFeaturePermTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/check_feature_perm_test.db?_busy_timeout=5000"), &gorm.Config{
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

	require.NoError(t, db.Exec(`
        CREATE TABLE sales_agent_owner (
            parent_user_id INTEGER PRIMARY KEY,
            created_at     DATETIME
        )`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// insertBizUserRaw 通过 raw SQL 插入 user 行（parentID=nil 表示父账户）。
// 注意：AutoIncrement 管理 ID，不传入 ID。
func insertBizUserRaw(t *testing.T, db *gorm.DB, parentID *uint) uint {
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

// insertSalesAgentOwner 插入父账户到 sales_agent_owner 表（Layer 0）。
func insertSalesAgentOwner(t *testing.T, db *gorm.DB, parentUserID uint) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO sales_agent_owner (parent_user_id, created_at) VALUES (?, ?)`,
		parentUserID, time.Now(),
	).Error)
}

// insertSubUserFeatureGrant 直接写 user_feature_permission 行（Layer 1）。
func insertSubUserFeatureGrant(t *testing.T, db *gorm.DB, parentID, subUserID uint, featureKey string) {
	t.Helper()
	now := time.Now()
	require.NoError(t, db.Exec(
		`INSERT INTO user_feature_permission (created_at, updated_at, parent_user_id, sub_user_id, feature_key) VALUES (?, ?, ?, ?, ?)`,
		now, now, parentID, subUserID, featureKey,
	).Error)
}

// setupCheckFeaturePermBiz 构造绑定指定 DB 的 customerBiz 实例。
func setupCheckFeaturePermBiz(t *testing.T, db *gorm.DB) ICustomerBiz {
	t.Helper()
	ds := store.NewTestStore(db)
	return New(ds)
}

// --- 场景 1: SalesAgent，父账户，owner 存在 → true ---

func TestCheckFeaturePermission_SalesAgent_ParentOwnerExists(t *testing.T) {
	db := newCheckFeaturePermTestDB(t)
	biz := setupCheckFeaturePermBiz(t, db)
	ctx := context.Background()

	parent := insertBizUserRaw(t, db, nil)
	insertSalesAgentOwner(t, db, parent)

	ok, err := biz.CheckFeaturePermission(ctx, parent, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.True(t, ok, "父账户在 owner 表中 → true")
}

// --- 场景 2: SalesAgent，父账户，owner 不存在 → false (关键回归：admin 路径) ---

func TestCheckFeaturePermission_SalesAgent_ParentOwnerAbsent(t *testing.T) {
	db := newCheckFeaturePermTestDB(t)
	biz := setupCheckFeaturePermBiz(t, db)
	ctx := context.Background()

	parent := insertBizUserRaw(t, db, nil)
	// 不插入 sales_agent_owner 行

	ok, err := biz.CheckFeaturePermission(ctx, parent, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.False(t, ok, "父账户不在 owner 表 → false（无 bypass）")
}

// --- 场景 3: SalesAgent，子账户，Layer 0 + Layer 1 都有 → true ---

func TestCheckFeaturePermission_SalesAgent_SubUserBothLayers(t *testing.T) {
	db := newCheckFeaturePermTestDB(t)
	biz := setupCheckFeaturePermBiz(t, db)
	ctx := context.Background()

	parent := insertBizUserRaw(t, db, nil)
	child := insertBizUserRaw(t, db, &parent)
	insertSalesAgentOwner(t, db, parent)                                        // Layer 0
	insertSubUserFeatureGrant(t, db, parent, child, model.FeatureKeySalesAgent) // Layer 1

	ok, err := biz.CheckFeaturePermission(ctx, child, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.True(t, ok, "双层均满足 → true")
}

// --- 场景 4: SalesAgent，子账户，仅 Layer 1 有（Layer 0 无）→ false ---

func TestCheckFeaturePermission_SalesAgent_SubUserLayer1Only(t *testing.T) {
	db := newCheckFeaturePermTestDB(t)
	biz := setupCheckFeaturePermBiz(t, db)
	ctx := context.Background()

	parent := insertBizUserRaw(t, db, nil)
	child := insertBizUserRaw(t, db, &parent)
	// 不插入 sales_agent_owner（Layer 0 缺失）
	insertSubUserFeatureGrant(t, db, parent, child, model.FeatureKeySalesAgent) // Layer 1 有

	ok, err := biz.CheckFeaturePermission(ctx, child, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.False(t, ok, "Layer 0 缺失 → false（AND 逻辑）")
}

// --- 场景 5: SalesAgent，子账户，仅 Layer 0 有（Layer 1 无）→ false ---

func TestCheckFeaturePermission_SalesAgent_SubUserLayer0Only(t *testing.T) {
	db := newCheckFeaturePermTestDB(t)
	biz := setupCheckFeaturePermBiz(t, db)
	ctx := context.Background()

	parent := insertBizUserRaw(t, db, nil)
	child := insertBizUserRaw(t, db, &parent)
	insertSalesAgentOwner(t, db, parent) // Layer 0 有
	// 不插入 user_feature_permission（Layer 1 缺失）

	ok, err := biz.CheckFeaturePermission(ctx, child, model.FeatureKeySalesAgent)
	require.NoError(t, err)
	assert.False(t, ok, "Layer 1 缺失 → false（AND 逻辑）")
}

// --- 场景 6: ContentMonitor，父账户 → true（bypass 保留）---

func TestCheckFeaturePermission_ContentMonitor_ParentBypass(t *testing.T) {
	db := newCheckFeaturePermTestDB(t)
	biz := setupCheckFeaturePermBiz(t, db)
	ctx := context.Background()

	parent := insertBizUserRaw(t, db, nil)
	// 不插入任何权限行，验证父账户在 content_monitor 路径走硬 bypass

	ok, err := biz.CheckFeaturePermission(ctx, parent, model.FeatureKeyContentMonitor)
	require.NoError(t, err)
	assert.True(t, ok, "content_monitor 父账户应硬 bypass → true")
}

// --- 场景 7: SelfServiceConfig，父账户 → true（bypass 保留）---

func TestCheckFeaturePermission_SelfServiceConfig_ParentBypass(t *testing.T) {
	db := newCheckFeaturePermTestDB(t)
	biz := setupCheckFeaturePermBiz(t, db)
	ctx := context.Background()

	parent := insertBizUserRaw(t, db, nil)
	// 不插入任何权限行，验证父账户在 self_service_config 路径走硬 bypass

	ok, err := biz.CheckFeaturePermission(ctx, parent, model.FeatureKeySelfServiceConfig)
	require.NoError(t, err)
	assert.True(t, ok, "self_service_config 父账户应硬 bypass → true")
}

// --- 场景 8: ContentMonitor，子账户，无授权行 → false ---

func TestCheckFeaturePermission_ContentMonitor_SubUserDeny(t *testing.T) {
	db := newCheckFeaturePermTestDB(t)
	biz := setupCheckFeaturePermBiz(t, db)
	ctx := context.Background()

	parent := insertBizUserRaw(t, db, nil)
	child := insertBizUserRaw(t, db, &parent)
	// 不给 child 授权 content_monitor

	ok, err := biz.CheckFeaturePermission(ctx, child, model.FeatureKeyContentMonitor)
	require.NoError(t, err)
	assert.False(t, ok, "子账户无 content_monitor 授权 → false")
}

// --- 场景 9: 用户不存在 → (false, err) ---

func TestCheckFeaturePermission_UserNotFound(t *testing.T) {
	db := newCheckFeaturePermTestDB(t)
	biz := setupCheckFeaturePermBiz(t, db)
	ctx := context.Background()

	ok, err := biz.CheckFeaturePermission(ctx, 99999, model.FeatureKeySalesAgent)
	assert.Error(t, err, "用户不存在时应返回错误")
	assert.False(t, ok)
}

// ----------------------------------------------------------------------------
// ListSubUsers —— 父账户行「已授权模板」计数（parent-self-template-count）
// ----------------------------------------------------------------------------

// TestListSubUsers_ParentRowCountsAllAvailable 复现并锁定：父账户那一行的「已授权
// 模板」应等于其全部可用资产数（自己创建的可见 SOP + 名下 published chatbot +
// sales_agent 0/1），而非白名单计数。父账户 bypass、无白名单行，修复前为
// 0(SOP) + 0(chatbot) + 1(sales_agent) = 1。
func TestListSubUsers_ParentRowCountsAllAvailable(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	_ = insertBizUser(t, db, &parent) // 一个子账户，验证 self 置顶

	// 父账户创建 3 个 active+published SOP；draft 的不计入
	insertBizSopTemplate(t, db, parent, "sop1", "active", "published")
	insertBizSopTemplate(t, db, parent, "sop2", "active", "published")
	insertBizSopTemplate(t, db, parent, "sop3", "active", "published")
	insertBizSopTemplate(t, db, parent, "sopDraft", "active", "draft")
	// 父账户名下 2 个 published chatbot；draft 的不计入
	insertBizChatbot(t, db, parent, "bot1", "published")
	insertBizChatbot(t, db, parent, "bot2", "published")
	insertBizChatbot(t, db, parent, "botDraft", "draft")
	// 父账户拥有 sales_agent（Layer 0）
	insertSalesAgentOwner(t, db, parent)

	resp, err := biz.ListSubUsers(context.Background(), parent, 0, 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(resp.SubUsers), 1)

	parentRow := resp.SubUsers[0] // store ORDER BY CASE WHEN id=parent THEN 0 → 父账户置顶
	require.Equal(t, parent, parentRow.UserID, "父账户应置顶为第一行")
	// 期望 3 SOP + 2 chatbot + 1 sales_agent = 6（修复前 0+0+1=1 → FAIL）
	assert.Equal(t, 6, parentRow.AuthorizedTemplates,
		"父账户行应显示全部可用资产数（全部可见 SOP + 名下 chatbot + sales_agent）")
}

// TestListSubUsers_ChildRowStaysWhitelist 守卫：子账户行仍按白名单计数，不受父账户
// 分支影响。子账户被授权 1 个 SOP，无 chatbot 白名单、无 sales_agent → 计数为 1。
func TestListSubUsers_ChildRowStaysWhitelist(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	child := insertBizUser(t, db, &parent)

	// 父账户创建 3 个 SOP（父全部可用，但子账户只被授权其中 1 个）
	tpl1 := insertBizSopTemplate(t, db, parent, "sop1", "active", "published")
	insertBizSopTemplate(t, db, parent, "sop2", "active", "published")
	insertBizSopTemplate(t, db, parent, "sop3", "active", "published")
	grantBizTemplate(t, db, parent, child, tpl1)

	resp, err := biz.ListSubUsers(context.Background(), parent, 0, 10)
	require.NoError(t, err)

	childIdx := -1
	for i := range resp.SubUsers {
		if resp.SubUsers[i].UserID == child {
			childIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, childIdx, 0, "应能在列表中找到子账户行")
	assert.Equal(t, 1, resp.SubUsers[childIdx].AuthorizedTemplates,
		"子账户行应按白名单计数（仅被授权的 1 个 SOP）")
}

// TestListSubUsers_ChildRowExcludesUnpublishedTemplate 复现客户上报 bug（prod 莫小派）：
// 「已授权模板」对子账号多计 1。根因——子账号被授权过一个模板，该模板后来被下架
// （status 仍 'active' 但 publish_status 变 'draft'），白名单权限行残留。计数查询
// CountActiveAuthorizedSopTemplates 只过滤 status='active'，漏了 publish_status='published'，
// 于是把这个已下架模板也计入，子账号数字超过「父账号可见模板宇宙」。
//
// 期望：已授权模板只计入「已发布(published)」模板，排除 active 但未发布的草稿。
// 修复前 = 2（FAIL）；修复后 = 1。
func TestListSubUsers_ChildRowExcludesUnpublishedTemplate(t *testing.T) {
	db, biz := newBizUnderTest(t)
	parent := insertBizUser(t, db, nil)
	child := insertBizUser(t, db, &parent)

	// 一个已发布模板 + 一个 active 但已下架(draft)的模板，两者都在子账号白名单里
	pub := insertBizSopTemplate(t, db, parent, "pubSop", "active", "published")
	draft := insertBizSopTemplate(t, db, parent, "draftSop", "active", "draft")
	grantBizTemplate(t, db, parent, child, pub)
	grantBizTemplate(t, db, parent, child, draft)

	resp, err := biz.ListSubUsers(context.Background(), parent, 0, 10)
	require.NoError(t, err)

	childIdx := -1
	for i := range resp.SubUsers {
		if resp.SubUsers[i].UserID == child {
			childIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, childIdx, 0, "应能在列表中找到子账户行")
	assert.Equal(t, 1, resp.SubUsers[childIdx].AuthorizedTemplates,
		"已授权模板应只计入已发布模板，排除 active 但未发布(draft)的草稿模板")
}
