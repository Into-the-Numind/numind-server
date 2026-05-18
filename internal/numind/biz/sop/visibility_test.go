package sop_test

import (
	"context"
	"sync"
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

	createVisUserTable(t, db)
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

// TestUpdateSopVisibility_TurnOnEmpty 验证 spec §2.5 I-2 不变量:
// restricted=true 且 sub_user_ids=[] → 白名单严格全拒, grant 表 0 条记录, 全部子用户都看不到.
func TestUpdateSopVisibility_TurnOnEmpty(t *testing.T) {
	db := newVisibilityTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertVisUser(t, db, nil)
	sub := insertVisUser(t, db, &parent)
	sopID := insertVisSopTemplate(t, db, parent)

	// 启用 visibility, 但白名单为空 (语义: 仅指定子用户可见, 但没指定任何子用户)
	require.NoError(t, sop.UpdateSopVisibility(ctx, s, parent, sopID, true, []uint{}))

	// IsSopVisibleToUser: sub 看不到 (visibility_restricted=true + grant=0 → 严格全拒)
	visible, err := sop.IsSopVisibleToUser(ctx, s, sub, sopID)
	require.NoError(t, err)
	assert.False(t, visible, "I-2: visibility_restricted=true + grant=0 → strict deny-all for subs")

	// 父账户仍 bypass 看到自己
	visible, err = sop.IsSopVisibleToUser(ctx, s, parent, sopID)
	require.NoError(t, err)
	assert.True(t, visible, "parent always visible")

	// 回读: restricted=true + 空名单
	restricted, ids, err := sop.GetSopVisibility(ctx, s, parent, sopID)
	require.NoError(t, err)
	assert.True(t, restricted)
	assert.Empty(t, ids, "empty whitelist")
}

// TestUpdateSopVisibility_IdempotentReplay 验证 spec §3.3 幂等性 + P0-2 双路径删除回归.
// 同一 PUT 连续 2 次, 第二次不触发唯一索引冲突 (Unscoped 物理删覆盖前一次记录).
func TestUpdateSopVisibility_IdempotentReplay(t *testing.T) {
	db := newVisibilityTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertVisUser(t, db, nil)
	sub := insertVisUser(t, db, &parent)
	sopID := insertVisSopTemplate(t, db, parent)

	require.NoError(t, sop.UpdateSopVisibility(ctx, s, parent, sopID, true, []uint{sub}))
	// 第二次相同请求: 应成功 (Unscoped 物理删覆盖旧记录, 无唯一冲突)
	require.NoError(t, sop.UpdateSopVisibility(ctx, s, parent, sopID, true, []uint{sub}),
		"idempotent replay should succeed; ReplaceGrantsTx Unscoped delete avoids unique conflict")

	_, ids, err := sop.GetSopVisibility(ctx, s, parent, sopID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uint{sub}, ids)
}

// TestUpdateSopVisibility_CrossParentSubUser 验证父账户提交他人子用户 → ErrCrossParentSubUser.
// 业务逻辑 owner 校验 (在 biz 层, 不是 controller 层).
func TestUpdateSopVisibility_CrossParentSubUser(t *testing.T) {
	db := newVisibilityTestDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent1 := insertVisUser(t, db, nil)
	parent2 := insertVisUser(t, db, nil)
	sub2 := insertVisUser(t, db, &parent2) // sub2 属于 parent2
	sopID := insertVisSopTemplate(t, db, parent1)

	// parent1 尝试把自己的 SOP visibility 授权给 parent2 的子用户
	err := sop.UpdateSopVisibility(ctx, s, parent1, sopID, true, []uint{sub2})
	assert.ErrorIs(t, err, errno.ErrCrossParentSubUser)
}

// TestUpdateSopVisibility_ConcurrentPUT_LastWriteWins 验证 spec §10.2 并发测试:
// 两 goroutine 同时 PUT 不同 sub_user_ids, 验证不死锁 + last-write-wins (最终状态等于其中一次写入).
//
// 注: 使用文件 + shared cache SQLite 而非 :memory: (后者跨 goroutine 不可见同一 DB).
func TestUpdateSopVisibility_ConcurrentPUT_LastWriteWins(t *testing.T) {
	db := newSharedSQLiteVisDB(t)
	s := store.NewTestStore(db)
	ctx := context.Background()

	parent := insertVisUser(t, db, nil)
	sub1 := insertVisUser(t, db, &parent)
	sub2 := insertVisUser(t, db, &parent)
	sub3 := insertVisUser(t, db, &parent)
	sopID := insertVisSopTemplate(t, db, parent)

	// 两 goroutine 并发写不同名单
	var wg sync.WaitGroup
	wg.Add(2)
	var errA, errB error
	go func() {
		defer wg.Done()
		errA = sop.UpdateSopVisibility(ctx, s, parent, sopID, true, []uint{sub1, sub2})
	}()
	go func() {
		defer wg.Done()
		errB = sop.UpdateSopVisibility(ctx, s, parent, sopID, true, []uint{sub3})
	}()

	// 5 秒超时, 防止死锁
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ConcurrentPUT timed out — suspected deadlock")
	}
	// 两次 PUT 都不应失败 (SQLite 串行化执行, 两次都 success)
	require.NoError(t, errA)
	require.NoError(t, errB)

	// 最终状态: 等于其中一次写入 (last-write-wins, 不应混合)
	_, ids, err := sop.GetSopVisibility(ctx, s, parent, sopID)
	require.NoError(t, err)
	isA := len(ids) == 2 && containsAll(ids, []uint{sub1, sub2})
	isB := len(ids) == 1 && ids[0] == sub3
	assert.True(t, isA || isB, "final state should match one writer's set, got: %v", ids)
}

// newSharedSQLiteVisDB 用文件 + shared cache 模式建 SQLite, 支持跨 goroutine 并发访问.
// 与 newVisibilityTestDB schema 一致, 仅连接模式不同.
func newSharedSQLiteVisDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/concurrent.db?cache=shared&_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	createVisUserTable(t, db)
	require.NoError(t, db.AutoMigrate(
		&model.SopTemplate{},
		&model.SopVisibilityGrant{},
	))
	return db
}

// createVisUserTable 创建 visibility 测试用的 user 表 (raw SQL, 避免 MySQL enum 不兼容 SQLite).
// 与 newVisibilityTestDB / newSharedSQLiteVisDB 共用.
// Post-T4: legacy_tier / billing_mode columns dropped.
func createVisUserTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`
		CREATE TABLE user (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at      DATETIME,
			updated_at      DATETIME,
			deleted_at      DATETIME,
			username        TEXT,
			nickname        TEXT,
			parent_user_id  INTEGER
		)`).Error)
}

// containsAll: ids 是否包含 wanted 所有元素 (用于 ConcurrentPUT 断言, 顺序无关).
func containsAll(ids []uint, wanted []uint) bool {
	set := make(map[uint]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	for _, w := range wanted {
		if !set[w] {
			return false
		}
	}
	return true
}
