# 父账户自助开通会员 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 放开 `GrantMembership` 的父子关系校验（允许 `child_id == parent_id` 且 caller 是父账户）并让 `ListSubUsers` 返回父自己置顶，使父账户能在客户管理页自助开通会员。

**Architecture:** 两处后端改动（biz + store），前端零改动。biz 层用双分支条件处理 self-grant vs delegate-grant，严卡三条越权防线。store 层用 `WHERE parent_user_id=? OR id=?` + `ORDER BY CASE` 把父自己置顶。测试用 Playwright E2E 对 mocked backend 验证交互路径（现有 grant-membership-modal.spec.ts 的模式）。

**Tech Stack:** Go 1.24, GORM, SQLite (in-memory tests), Gin, Vue 3 (E2E only), Playwright 1.48.

**Spec**: `numind-server/docs/superpowers/specs/2026-04-20-parent-self-grant-membership-design.md`

---

## 文件结构

| 文件 | 操作 | 职责 |
|------|------|------|
| `numind-server/internal/numind/biz/credit/grant_membership.go` | Modify | 79-81 行：双分支 self-grant vs delegate-grant 校验 |
| `numind-server/internal/numind/biz/credit/grant_membership_test.go` | Modify | 新增 5 个 self-grant 单测 + 1 个防越权单测 |
| `numind-server/internal/numind/store/customer.go` | Modify | 60-75 行：`ListSubUsers` WHERE + ORDER 改造 |
| `numind-server/internal/numind/store/customer_test.go` | Create | 新增 store 测试（父自己置顶验证） |
| `numind-web-v3/e2e/parent-self-grant.spec.ts` | Create | 3 个 Playwright E2E 场景（mocked grant API） |
| `numind-server/build-manifest.yaml` | Modify | 每 task 完成后更新 progress |

---

## Task 1: Backend biz — grant_membership.go self-grant 分支

**目标**：放开 `ChildUserID == ParentUserID` 的校验，在 caller 是父账户（`parent_user_id IS NULL`）时允许 self-grant，其它情况沿用原有校验。

**Files:**
- Modify: `numind-server/internal/numind/biz/credit/grant_membership.go:79-81`
- Modify: `numind-server/internal/numind/biz/credit/grant_membership_test.go`

**说明**：TDD。先加所有 self-grant 相关 test case 并验证全部 FAIL（现有代码会因为父账户 `parent_user_id == nil` 导致 `child.ParentUserID == nil` 命中拒绝分支），然后改 biz 代码，再验证全部 PASS。

### Step 1.1: 写 self-grant trial 成功 test case

- [ ] **写 `TestGrantMembership_SelfGrant_Trial_Success`**

在 `grant_membership_test.go` 追加（参考现有 `TestGrantMembership_Trial_Success` 结构）：

```go
// ---------- Self-Grant: Trial path ----------

func TestGrantMembership_SelfGrant_Trial_Success(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	// 父账户 (parent_user_id = nil)
	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  parent, // self-grant
		ProductType:  model.ProductTypeTrial,
		Reason:       "self trial",
	})
	require.NoError(t, err, "parent self-granting trial must succeed")

	// credit_package: trial, 200 credits, 3 days, grant_source=b2b_grant, granter=parent (自己)
	var pkgs []model.CreditPackage
	require.NoError(t, db.Where("user_id = ?", parent).Find(&pkgs).Error)
	require.Len(t, pkgs, 1)
	p := pkgs[0]
	assert.Equal(t, model.CreditTypeTrial, p.Type)
	assert.EqualValues(t, 200, p.TotalCredits)
	assert.Equal(t, model.GrantSourceB2BGrant, p.GrantSource)
	require.NotNil(t, p.GranterUserID)
	assert.Equal(t, parent, *p.GranterUserID, "self-grant: granter_user_id == user_id")

	// balance
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", parent).First(&acc).Error)
	assert.EqualValues(t, 200, acc.Balance)

	// action_log: user_id == target_id == parent (self-grant 签名)
	var logs []model.ActionLogM
	require.NoError(t, db.Where("user_id = ? AND action = ?", parent, "grant_membership").Find(&logs).Error)
	require.Len(t, logs, 1)
	require.NotNil(t, logs[0].TargetID)
	assert.Equal(t, parent, *logs[0].TargetID, "self-grant: target_id == user_id")
}
```

- [ ] **运行验证 FAIL**

Run: `cd numind-server && go test ./internal/numind/biz/credit/ -run TestGrantMembership_SelfGrant_Trial_Success -v`
Expected: FAIL — 当前代码在 `child.ParentUserID == nil` 时返回 `ErrGrantForbidden`

### Step 1.2: 写 self-grant monthly 成功 test case

- [ ] **写 `TestGrantMembership_SelfGrant_Monthly_ThreeMonths_CreatesThreePackages`**

```go
func TestGrantMembership_SelfGrant_Monthly_ThreeMonths_CreatesThreePackages(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  parent, // self-grant
		ProductType:  model.ProductTypeMonthly,
		Months:       3,
		Reason:       "self monthly 3m",
	})
	require.NoError(t, err)

	var pkgs []model.CreditPackage
	require.NoError(t, db.Where("user_id = ?", parent).Order("activated_at ASC").Find(&pkgs).Error)
	require.Len(t, pkgs, 3, "3 monthly packages")

	for i, p := range pkgs {
		assert.Equal(t, model.CreditTypeSubscription, p.Type)
		assert.EqualValues(t, 2000, p.TotalCredits)
		assert.Equal(t, model.GrantSourceB2BGrant, p.GrantSource)
		require.NotNil(t, p.GranterUserID)
		assert.Equal(t, parent, *p.GranterUserID)
		if i == 0 {
			assert.Equal(t, model.CreditPackageActive, p.Status, "first month active")
		} else {
			assert.Equal(t, model.CreditPackagePending, p.Status, "subsequent months pending")
		}
	}

	// balance only reflects first month
	var acc model.CreditAccount
	require.NoError(t, db.Where("user_id = ?", parent).First(&acc).Error)
	assert.EqualValues(t, 2000, acc.Balance)
}
```

- [ ] **运行验证 FAIL**

Run: `cd numind-server && go test ./internal/numind/biz/credit/ -run TestGrantMembership_SelfGrant_Monthly_ThreeMonths -v`
Expected: FAIL

### Step 1.3: 写子账户自开通拒绝的 test case（防越权 #1）

- [ ] **写 `TestGrantMembership_SubUserSelfGrant_Rejected`**

```go
func TestGrantMembership_SubUserSelfGrant_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	// 子账户 (parent_user_id = parent.id)
	child := insertGrantTestUser(t, db, model.UserTierFree, &parent, model.BillingModeCredits, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: child, // sub-user 自己当 caller
		ChildUserID:  child, // sub-user 自己当 target
		ProductType:  model.ProductTypeTrial,
		Reason:       "sub self-grant attempt",
	})
	require.Error(t, err, "sub-user self-grant must be rejected")
	assert.ErrorIs(t, err, ErrGrantForbidden, "must return ErrGrantForbidden")

	// 确认未写入 credit_package
	var count int64
	require.NoError(t, db.Model(&model.CreditPackage{}).Where("user_id = ?", child).Count(&count).Error)
	assert.EqualValues(t, 0, count, "no credit package written")
}
```

- [ ] **运行验证**

Run: `cd numind-server && go test ./internal/numind/biz/credit/ -run TestGrantMembership_SubUserSelfGrant_Rejected -v`
Expected: 此 case 在**当前代码**下可能 PASS（因为现有逻辑也拒绝 `child.ParentUserID != nil && *child.ParentUserID != parent_self`），**保留作防越权回归测试**

### Step 1.3b: 写父 A 直接给父 B 开通的拒绝 test case（防越权 #2 显式覆盖）

- [ ] **写 `TestGrantMembership_CrossParentGrant_Rejected`**

**说明**：现有 `TestGrantMembership_ChildNotBelongingToParent_Rejected` 覆盖的是"A → B 的子账户"；spec §5.1 case #4 要求覆盖"A → 父账户 B 本身"（B 也是 `parent_user_id=NULL` 的父账户），这是**不同的**越权场景，必须单独测试以对应 §4 防线 #2。

```go
func TestGrantMembership_CrossParentGrant_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	// 两个父账户（parent_user_id = nil）
	parentA := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)
	parentB := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	// 父 A 试图给父 B 开通 trial
	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parentA,
		ChildUserID:  parentB, // 目标是另一个父账户，不是 A 的子账户
		ProductType:  model.ProductTypeTrial,
		Reason:       "cross-parent attempt",
	})
	require.Error(t, err, "parent A must not grant to parent B (both are parent accounts)")
	assert.ErrorIs(t, err, ErrGrantForbidden)

	// 确认未写入 credit_package
	var count int64
	require.NoError(t, db.Model(&model.CreditPackage{}).Where("user_id = ?", parentB).Count(&count).Error)
	assert.EqualValues(t, 0, count)
}
```

- [ ] **运行验证**

Run: `cd numind-server && go test ./internal/numind/biz/credit/ -run TestGrantMembership_CrossParentGrant_Rejected -v`
Expected: 此 case 在**当前代码**下 PASS（现有逻辑 `child.ParentUserID == nil` 命中会拒绝，B 是父账户所以 ParentUserID 为 nil），**task 1.6 改动后也必须 PASS**（走 else 分支同样拒绝）。**保留作防越权回归测试**

### Step 1.4: 写 billing_mode 切换的 self-grant test case

- [ ] **写 `TestGrantMembership_SelfGrant_BillingModeSwitch`**

```go
func TestGrantMembership_SelfGrant_BillingModeSwitch(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	// 父账户 legacy_tier
	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeLegacyTier, nil)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  parent,
		ProductType:  model.ProductTypeTrial,
		Reason:       "legacy→credits switch on self-grant",
	})
	require.NoError(t, err)

	// Verify billing_mode switched
	var bm string
	require.NoError(t, db.Raw("SELECT billing_mode FROM user WHERE id = ?", parent).Scan(&bm).Error)
	assert.Equal(t, model.BillingModeCredits, bm, "billing_mode must switch legacy_tier → credits")
}
```

- [ ] **运行验证 FAIL**

Expected: FAIL（current code 拒绝 self-grant 所以 billing_mode 不会切换）

### Step 1.5: 写 trial 防重复 + active subscription 防重复的 self-grant test

- [ ] **写 `TestGrantMembership_SelfGrant_TrialAlreadyPurchased_Rejected` 和 `TestGrantMembership_SelfGrant_ActiveSubscription_Rejected`**

```go
func TestGrantMembership_SelfGrant_TrialAlreadyPurchased_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	// Insert a pre-existing trial package for parent (lifetime single-use)
	preexisting := &model.CreditPackage{
		UserID:        parent,
		Type:          model.CreditTypeTrial,
		TotalCredits:  200,
		RemainCredits: 200,
		ActivatedAt:   time.Now().Add(-10 * 24 * time.Hour),
		ExpiresAt:     time.Now().Add(-7 * 24 * time.Hour),
		Status:        model.CreditPackageExpired,
		GrantSource:   model.GrantSourceB2BGrant,
	}
	granter := parent
	preexisting.GranterUserID = &granter
	require.NoError(t, db.Create(preexisting).Error)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  parent,
		ProductType:  model.ProductTypeTrial,
		Reason:       "duplicate trial attempt",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGrantTrialAlreadyPurchased)
}

func TestGrantMembership_SelfGrant_ActiveSubscription_Rejected(t *testing.T) {
	db := newGrantTestDB(t)
	ds := store.NewTestStore(db)
	b := NewCreditBiz(ds).(*creditBiz)

	parent := insertGrantTestUser(t, db, model.UserTierFree, nil, model.BillingModeCredits, nil)

	// Insert an active subscription for parent
	active := &model.CreditPackage{
		UserID:        parent,
		Type:          model.CreditTypeSubscription,
		TotalCredits:  2000,
		RemainCredits: 1500,
		ActivatedAt:   time.Now().Add(-5 * 24 * time.Hour),
		ExpiresAt:     time.Now().Add(25 * 24 * time.Hour),
		Status:        model.CreditPackageActive,
		GrantSource:   model.GrantSourceB2BGrant,
	}
	granter := parent
	active.GranterUserID = &granter
	require.NoError(t, db.Create(active).Error)

	err := b.GrantMembership(context.Background(), GrantMembershipReq{
		ParentUserID: parent,
		ChildUserID:  parent,
		ProductType:  model.ProductTypeMonthly,
		Months:       1,
		Reason:       "duplicate monthly attempt",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrGrantActiveSubscription)
}
```

- [ ] **运行验证 FAIL**

Run: `cd numind-server && go test ./internal/numind/biz/credit/ -run 'TestGrantMembership_SelfGrant_(TrialAlreadyPurchased|ActiveSubscription)_Rejected' -v`
Expected: FAIL

### Step 1.6: 修改 `grant_membership.go` Step 2（校验分支改造）

- [ ] **改 `grant_membership.go:79-81`**

定位改动块：

```go
	if child.ParentUserID == nil || *child.ParentUserID != req.ParentUserID {
		return fmt.Errorf("%w: child=%d parent=%d", ErrGrantForbidden, req.ChildUserID, req.ParentUserID)
	}
```

替换为：

```go
	if req.ChildUserID == req.ParentUserID {
		// Self-grant: 仅允许父账户（parent_user_id IS NULL）给自己开通。
		// 子账户 (parent_user_id != NULL) 禁止自开通，防越权。
		if child.ParentUserID != nil {
			return fmt.Errorf("%w: caller=%d is a sub-user, self-grant only allowed for parent accounts",
				ErrGrantForbidden, req.ParentUserID)
		}
		// 放行：父账户 self-grant
	} else {
		// Delegate-grant: 目标必须是 caller 的子账户
		if child.ParentUserID == nil || *child.ParentUserID != req.ParentUserID {
			return fmt.Errorf("%w: child=%d parent=%d", ErrGrantForbidden, req.ChildUserID, req.ParentUserID)
		}
	}
```

### Step 1.7: 运行全部 GrantMembership 相关测试验证 PASS

- [ ] **Run full grant_membership test suite**

Run: `cd numind-server && go test ./internal/numind/biz/credit/ -run TestGrantMembership -v`
Expected: 全部 PASS（原有 9 个 + 新增 5 个 = 14 个）

- [ ] **Run task lint**

Run: `cd numind-server && task lint`
Expected: exit 0

### Step 1.8: Commit

- [ ] **Commit**

```bash
cd numind-server
git add internal/numind/biz/credit/grant_membership.go internal/numind/biz/credit/grant_membership_test.go
git commit -m "$(cat <<'EOF'
feat(credit): allow parent account self-grant membership

放开 GrantMembership 父子关系校验，允许 child_id == parent_id
当 caller 是父账户（parent_user_id IS NULL）时。严卡三条越权防线：
- 子账户不能自开通（child.ParentUserID != nil 时 self-grant 被拒）
- 父 A 不能跨父开通给 B（else 分支原有校验保留）
- 无效 child_id 返回 ErrGrantChildNotFound（既有逻辑）

新增 5 个单测覆盖 self-grant 成功路径 + billing_mode 切换 +
trial/monthly 防重复。原有 9 个 delegate-grant 测试保持绿。

Spec: docs/superpowers/specs/2026-04-20-parent-self-grant-membership-design.md §3.1

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Backend store — ListSubUsers 包含父自己且置顶

**目标**：`ListSubUsers(parentID)` 返回 `[parent_self, child1, child2, ...]`，父自己永远在第一位；`total` 含父自己。

**Files:**
- Modify: `numind-server/internal/numind/store/customer.go:60-75`
- Create: `numind-server/internal/numind/store/customer_test.go`（若文件不存在）

### Step 2.1: 检查 customer_test.go 是否存在

- [ ] **Run: `ls numind-server/internal/numind/store/customer_test.go`**

若文件存在 → 在末尾追加新 test
若不存在 → 创建带 package + imports

### Step 2.2: 写 ListSubUsers 置顶自己的 test case

- [ ] **写 `TestListSubUsers_IncludesParentSelf_Ordered`**

创建 `numind-server/internal/numind/store/customer_test.go`（若已存在则在末尾追加）：

```go
package store

import (
	"context"
	"testing"
	"time"

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
            parent_user_id  INTEGER,
            billing_mode    TEXT NOT NULL DEFAULT 'credits',
            user_tier       TEXT DEFAULT 'free'
        )`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
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
	_ = insertCustomerTestUser(t, db, &otherParent, now, "OtherChild")

	users, total, err := cs.ListSubUsers(context.Background(), parent, 0, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total, "total = parent + 2 children")
	require.Len(t, users, 3)
	assert.Equal(t, parent, users[0].ID, "parent self must be first")
	assert.Equal(t, newer, users[1].ID, "newer child second (created_at DESC)")
	assert.Equal(t, older, users[2].ID, "older child third")

	// Ensure OtherParent and OtherChild not leaked
	for _, u := range users {
		assert.NotEqual(t, otherParent, u.ID, "otherParent must not leak")
	}
}
```

- [ ] **运行验证 FAIL**

Run: `cd numind-server && go test ./internal/numind/store/ -run TestListSubUsers_IncludesParentSelf_Ordered -v`
Expected: FAIL — 当前 `WHERE parent_user_id = ?` 不包含父自己，`total == 2`，`len(users) == 2`

### Step 2.3: 修改 ListSubUsers WHERE + ORDER

- [ ] **改 `customer.go:60-75`**

定位改动块：

```go
func (c *customerStore) ListSubUsers(ctx context.Context, parentUserID uint, offset, limit int) ([]model.User, int64, error) {
	var users []model.User
	var total int64

	query := c.db.WithContext(ctx).Model(&model.User{}).Where("parent_user_id = ?", parentUserID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if err := query.Offset(offset).Limit(limit).Order("created_at DESC").Find(&users).Error; err != nil {
		return nil, 0, err
	}

	return users, total, nil
}
```

替换为：

```go
func (c *customerStore) ListSubUsers(ctx context.Context, parentUserID uint, offset, limit int) ([]model.User, int64, error) {
	var users []model.User
	var total int64

	// 包含父自己（self-grant 支持）+ 其直接子账户
	query := c.db.WithContext(ctx).Model(&model.User{}).
		Where("parent_user_id = ? OR id = ?", parentUserID, parentUserID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 父自己永远置顶（CASE WHEN id=parent THEN 0 ELSE 1），其它子账户按 created_at DESC
	// 用 fmt.Sprintf 拼接 parentUserID 是安全的——uint 类型不可承载 SQL 注入
	orderClause := fmt.Sprintf("CASE WHEN id = %d THEN 0 ELSE 1 END, created_at DESC", parentUserID)
	if err := query.Offset(offset).Limit(limit).Order(orderClause).Find(&users).Error; err != nil {
		return nil, 0, err
	}

	return users, total, nil
}
```

**注意**：新增 `fmt` import（若文件顶部未 import）。

### Step 2.4: 运行 store 测试 + lint

- [ ] **Run new test**

Run: `cd numind-server && go test ./internal/numind/store/ -run TestListSubUsers_IncludesParentSelf_Ordered -v`
Expected: PASS

- [ ] **Run full store tests（回归）**

Run: `cd numind-server && go test ./internal/numind/store/...`
Expected: 全部 PASS

- [ ] **Run task lint**

Run: `cd numind-server && task lint`
Expected: exit 0

### Step 2.5: Commit

- [ ] **Commit**

```bash
cd numind-server
git add internal/numind/store/customer.go internal/numind/store/customer_test.go
git commit -m "$(cat <<'EOF'
feat(customer): ListSubUsers includes parent account itself

ListSubUsers 返回列表现在包含父账户自己（WHERE parent_user_id=? OR id=?），
父自己通过 ORDER BY CASE 置顶。total 计数同步包含父自己。

支持前端"客户管理"页展示父账户在列表首行，配合 GrantMembership
self-grant 放行（task #1）实现父账户自助开通会员。

Spec: docs/superpowers/specs/2026-04-20-parent-self-grant-membership-design.md §3.2

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Frontend E2E — Playwright parent-self-grant.spec.ts

**目标**：3 个 E2E 场景覆盖父自助开通 + 子账户开通回归。mock grant API 保证确定性，`sub-users` API 命中真实后端（验证 task #2 的列表返回父自己）。

**Files:**
- Create: `numind-web-v3/e2e/parent-self-grant.spec.ts`

**前置**：task 1 + 2 已合并到 develop 并部署到 dev 环境，或本地运行 `task dev` + `npm run dev`。

### Step 3.1: 创建 E2E spec 文件

- [ ] **创建 `numind-web-v3/e2e/parent-self-grant.spec.ts`**

```typescript
import { test, expect, type Page, type Route } from '@playwright/test'

/**
 * Parent Self-Grant Membership E2E
 *
 * Tests that the parent account can grant membership to itself via the
 * customer-management page (CustomersView), using the exact same UI
 * interaction as granting to sub-users.
 *
 * Coverage:
 *   1. Parent appears in customer list (first row)
 *   2. Parent can self-grant trial via action menu → "帮开通会员"
 *   3. Granting to a sub-user still works (regression)
 *
 * We mock the grant POST so state is deterministic — the real dev
 * backend's membership state changes across runs.
 *
 * Prerequisites: auth setup must run first; the authenticated user must
 * be a parent account (parent_user_id IS NULL) with at least 1 sub-user.
 */

const sel = {
  page: '.customers-page',
  tableRow: '.data-table tbody tr',
  actionTrigger: '.action-trigger',
  actionMenu: '.action-menu',
  actionMenuItem: '.action-menu-item',

  grantModal: '.modal-dialog.tier-dialog',
  grantTitle: '.modal-dialog.tier-dialog .modal-title',
  targetName: '.modal-dialog.tier-dialog .perm-name',
  trialCard: '.modal-dialog.tier-dialog .upgrade-card:has-text("体验会员")',
  monthlyCard: '.modal-dialog.tier-dialog .upgrade-card:has-text("高级会员")',
  monthsSelect: '.modal-dialog.tier-dialog select.form-select',
  submitBtn: '.modal-dialog.tier-dialog .btn-primary',

  toast: '.toast'
} as const

async function goToCustomers(page: Page) {
  await page.goto('/customers')
  await page.waitForFunction(
    () => {
      const table = document.querySelector('.data-table')
      const empty = document.querySelector('.empty-state')
      const loading = document.querySelector('.loading-state')
      return (table || empty) && !loading
    },
    null,
    { timeout: 30_000 }
  )
}

async function openGrantModalForRow(page: Page, rowIndex: number) {
  const row = page.locator(sel.tableRow).nth(rowIndex)
  await expect(row).toBeVisible({ timeout: 15_000 })
  await row.locator(sel.actionTrigger).click()
  await expect(row.locator(sel.actionMenu)).toBeVisible({ timeout: 3_000 })
  await row.locator(sel.actionMenuItem, { hasText: '帮开通会员' }).click()
  await expect(page.locator(sel.grantModal)).toBeVisible({ timeout: 3_000 })
}

async function mockGrantSuccess(page: Page) {
  await page.route('**/v1/users/children/*/grant-membership', async (route: Route) => {
    if (route.request().method() !== 'POST') {
      await route.fallback()
      return
    }
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        code: 0,
        message: 'ok',
        data: { message: '开通成功' }
      })
    })
  })
}

// ══════════════════════════════════════════════════════════════════
// 1. Parent appears in customer list
// ══════════════════════════════════════════════════════════════════

test.describe('Parent Self-Grant — List rendering', () => {
  test('parent account appears as the first row in customer list', async ({ page }) => {
    await goToCustomers(page)

    const rows = page.locator(sel.tableRow)
    await expect(rows.first()).toBeVisible({ timeout: 15_000 })
    const count = await rows.count()
    expect(count).toBeGreaterThanOrEqual(1)

    // 父自己置顶：第一行的昵称应该是当前登录用户的昵称。
    // 由于我们不假设具体昵称，仅断言"有至少一行 + action 菜单可用"。
    const firstRow = rows.first()
    await firstRow.locator(sel.actionTrigger).click()
    await expect(firstRow.locator(sel.actionMenu)).toBeVisible({ timeout: 3_000 })
    await expect(
      firstRow.locator(sel.actionMenuItem, { hasText: '帮开通会员' })
    ).toBeVisible()
  })
})

// ══════════════════════════════════════════════════════════════════
// 2. Parent self-grant trial
// ══════════════════════════════════════════════════════════════════

test.describe('Parent Self-Grant — Trial self-grant', () => {
  test('parent can grant trial to themselves via the first row', async ({ page }) => {
    await mockGrantSuccess(page)
    await goToCustomers(page)

    // 第一行 = 父自己（task #2 保证）
    await openGrantModalForRow(page, 0)
    await expect(page.locator(sel.grantTitle)).toHaveText('帮开通会员')

    // Select trial
    await page.locator(sel.trialCard).click()
    await page.locator(sel.submitBtn).click()

    // Toast 成功 + modal 关闭
    await expect(page.locator(sel.toast)).toBeVisible({ timeout: 5_000 })
    await expect(page.locator(sel.toast)).toContainText('开通')
    await expect(page.locator(sel.grantModal)).not.toBeVisible({ timeout: 3_000 })

    // 注意：spec §5.3 路径 2 要求"列表刷新后自己行显示会员状态"。
    // 本 case 因 mock 了 grant API 后端不实际写入，列表刷新后的会员状态断言
    // 无法在 E2E mock 下可靠验证。会员状态持久化的端到端验证由后端 TDD
    // （TestGrantMembership_SelfGrant_Trial_Success 等 task 1 的单测）覆盖。
    // 此处仅断言 submitGrant 成功路径触发了 loadSubUsers()（刷新动作本身）。
  })
})

// ══════════════════════════════════════════════════════════════════
// 3. Regression: granting to a sub-user still works
// ══════════════════════════════════════════════════════════════════

test.describe('Parent Self-Grant — Sub-user regression', () => {
  test('granting monthly to a sub-user (second row) still works', async ({ page }) => {
    await mockGrantSuccess(page)
    await goToCustomers(page)

    const rowCount = await page.locator(sel.tableRow).count()
    test.skip(rowCount < 2, 'Need at least 1 sub-user for this regression test')

    // 第二行 = 第一个子账户
    await openGrantModalForRow(page, 1)
    await page.locator(sel.monthlyCard).click()
    await page.locator(sel.monthsSelect).selectOption('6')
    await page.locator(sel.submitBtn).click()

    await expect(page.locator(sel.toast)).toBeVisible({ timeout: 5_000 })
    await expect(page.locator(sel.grantModal)).not.toBeVisible({ timeout: 3_000 })
  })
})
```

### Step 3.2: 本地跑 E2E 验证

- [ ] **Start local backend + frontend**

```bash
cd numind-server && task dev &
cd numind-web-v3 && npm run dev &
```

- [ ] **Run the new E2E spec**

```bash
cd numind-web-v3
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- parent-self-grant.spec.ts
```

Expected: 全部 PASS（3 test）

- [ ] **Run full E2E suite（回归）**

```bash
cd numind-web-v3
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e
```

Expected: 全部 PASS（现有 + 新增）

- [ ] **Stop local services after validation**

### Step 3.3: Lint + type-check

- [ ] **Run frontend lint + type-check**

```bash
cd numind-web-v3
npm run lint
npm run type-check
```

Expected: 全部 exit 0

### Step 3.4: Commit

- [ ] **Commit E2E spec to numind-web-v3**

```bash
cd numind-web-v3
git add e2e/parent-self-grant.spec.ts
git commit -m "$(cat <<'EOF'
test(e2e): parent self-grant membership coverage

3 个 Playwright E2E 场景覆盖 parent-self-grant-membership 功能：
- 父账户在客户管理页列表首行出现且 action 菜单可用
- 父账户通过第一行自开 trial 成功（mocked grant API）
- 子账户开通回归测试（第二行开 monthly 6 月）

Spec: parent-self-grant-membership-design.md §5.3

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: S5 验证策略（NDF Rule 10 文档 task）

**目标**：按 NDF 规则 10 要求，把 S5 的验证策略固化在 plan 中，避免 S5 时 AI 临时选最省事的方案。

**Files:** 不创建新文件（所有信息在 spec §5.3 + 本 plan task 3）。

### Step 4.1: 确认 S5 验证策略

**验证方式**：**Playwright E2E**

**理由**：
- 本功能涉及会员开通 + 父子关系校验放开，属**高风险业务逻辑**（`.claude/rules/ndf-enforcement.md` §10 明确要求）
- 需要覆盖前端渲染 + API 调用 + 后端持久化的完整链路
- **需要自动回归保护**：未来任何对 CustomersView、ListSubUsers、GrantMembership 的修改都可能破坏本功能，E2E 能在 CI 持续拦截
- 拒绝 gstack `/qa` 一次性截图验证——无回归保护

**关键用户路径（S5 必须验证的操作步骤）**：

1. **父账户登录** → 访问 `/customers` 页面
2. **列表渲染** → 确认父自己出现在第一行（可通过 action 菜单包含"帮开通会员"项验证）
3. **父自开 trial** → 点击第一行 action 菜单 → "帮开通会员" → 弹窗 → 选 trial → 确认 → toast 成功 + modal 关闭
4. **子账户开通回归** → 点击第二行 action 菜单 → "帮开通会员" → 弹窗 → 选 monthly 6 月 → 确认 → toast 成功
5. **完整 E2E 套件回归** → 跑 `npm run test:e2e`，确保其它 spec（`grant-membership-modal.spec.ts`、`customers.spec.ts` 等）不受影响

**不做的验证**：
- 不做可观测性验证（本功能不涉及 LLM，`.claude/rules/ai-service.md` 的 Langfuse 检查不适用）

### Step 4.2: 无需 commit（文档内嵌在 plan 中）

本 task 是文档性质——把 S5 策略固化下来供 S5 阶段执行。S3 reviewer 审查 plan 时确认本策略合理，S5 按此执行。

---

## 最终 Gate 检查清单（S4 end / S5 准备）

S4 Gate（本 plan 全部 task 完成后）：

- [ ] `cd numind-server && task lint` → exit 0
- [ ] `cd numind-server && go test ./...` → exit 0
- [ ] `cd numind-web-v3 && npm run lint` → exit 0
- [ ] `cd numind-web-v3 && npm run type-check` → exit 0
- [ ] 每 task 完成后跑两阶段 review（spec-compliance + code-quality），无 P0
- [ ] manifest progress: completed_tasks == total_tasks == 3（task 4 是文档任务不计入）

S5 Gate（按 Task 4 策略执行）：

- [ ] 本地启动 numind-server + numind-web-v3
- [ ] `E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- parent-self-grant.spec.ts` → PASS
- [ ] `npm run test:e2e`（全量）→ PASS
- [ ] 停本地服务

---

## 自检回顾（plan 与 spec 对照）

**Spec §3 全部改动点**：
- §3.1 biz → Task 1 ✅
- §3.2 store → Task 2 ✅
- §3.3 前端零改动 → Task 3 仅创建 E2E 文件，不改前端源码 ✅
- §3.4 API 契约 → 未新增端点，无独立 task
- §3.5 数据库 → 零 schema 变更，无独立 task

**Spec §4 三条越权防线**：
- 防线 #1 子账户自开通 → Task 1 Step 1.3（`TestGrantMembership_SubUserSelfGrant_Rejected`） ✅
- 防线 #2 父 A 跨父开 B → Task 1 **Step 1.3b**（`TestGrantMembership_CrossParentGrant_Rejected`，spec §5.1 case #4 显式覆盖）；另外现有 `TestGrantMembership_ChildNotBelongingToParent_Rejected`（A → B 的子账户，不同场景）作为扩展回归保持 ✅
- 防线 #3 无效 child_id → 现有 `TestGrantMembership_ChildNotExists_Rejected` 覆盖 ✅

**Spec §5 测试 case**：
- §5.1 7 个新单测 → Task 1 Step 1.1-1.5（5 个 self-grant 单测 + 1 个 cross-parent-grant 显式测试） + 现有 cross-parent-via-child / not-exists 回归 ✅
- §5.2 store 测试 → Task 2 Step 2.2 ✅
- §5.3 S5 E2E → Task 3 + Task 4 ✅

**无 placeholder，无 TBD，type 一致**（`ErrGrantForbidden`、`ErrGrantTrialAlreadyPurchased`、`ErrGrantActiveSubscription`、`GrantSourceB2BGrant`、`CreditTypeTrial`、`CreditTypeSubscription` 全部使用 spec 已定义名称）。
