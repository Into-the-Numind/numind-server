# 父账户自助费用对账页面 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给用户端父账户提供一个按月的自助费用对账内页，复用现有 B2B 结算内核，作用域收窄到当前登录父账户。

**Architecture:** 后端方案 A——给现有 `computeBilling` 加可选 `granterUserID *uint` 过滤参数（nil=所有父账户，admin 不变；&id=单父账户），新增用户端 slim 端点 `GET /v1/users/me/billing-report`（user_token，父账户专属，非父账户 403）。前端新增子路由 `/customers/billing` 内页，从客户管理页页面级按钮进入。单一真相源，金额口径永不与 admin 结算版漂移。

**Tech Stack:** Go 1.24 / Gin / GORM（后端）；Vue 3 Composition API / Pinia / axios（前端）。

**上游工件：** spec `docs/superpowers/specs/2026-06-01-parent-billing-report-design.md`（API 契约见 spec §3）。

**仓库与 worktree：**
- numind-server: `/private/tmp/wt-parent-billing-report-numind-server`（branch `feature/parent-billing-report`）
- numind-web-v3: `/private/tmp/wt-parent-billing-report-numind-web-v3`（branch `feature/parent-billing-report`）

**任务顺序与依赖（无环）：** Task 1 → Task 2 → Task 3（后端，串行，同文件）；Task 4 → Task 5（前端，串行）。后端在前端之前（NDF S3 规则）。前端 Task 4/5 仅依赖 spec §3 API 契约（已锁），理论上可与后端 Tier 2 跨仓库并行，但本计划按串行编排。Task 6 = S5 验证策略（最后）。

---

## File Structure

**numind-server：**
- Modify `internal/numind/biz/b2b_billing/b2b_billing.go` — 加 granter 过滤参数、`ParentBillingReport` 结构、`ErrNotParentAccount` sentinel、`lookupUsernames` helper、`GetBillingReportForParent` 方法（Task 1+2）
- Modify `internal/numind/biz/b2b_billing/b2b_billing_test.go` — 父账户作用域测试（Task 2）
- Create `internal/numind/controller/v1/parent_billing/billing_report.go` — 用户端控制器（Task 3）
- Modify `internal/numind/router.go` — 注册路由（Task 3）

**numind-web-v3：**
- Modify `src/api/parent.ts` — `getParentBillingReport` + 类型（Task 4）
- Create `src/views/CustomersBillingView.vue` — 对账内页（Task 5）
- Modify `src/router/index.ts` — 子路由（Task 5）
- Modify `src/views/CustomersView.vue` — 页面级入口按钮（Task 5）

---

## Task 1: biz — 给 computeBilling 加可选 granter 过滤（纯重构，零行为变化）

**Files:**
- Modify: `internal/numind/biz/b2b_billing/b2b_billing.go`（`computeBilling` 签名 + 3 处 Where + 唯一调用方 `GetBillingReport`）
- Test: `internal/numind/biz/b2b_billing/b2b_billing_test.go`（无需新增，靠现有套件做回归）

这是纯重构 task：加一个 nil-safe 参数，admin 路径传 nil 后行为完全不变。验证手段 = 现有 b2b_billing 测试套件保持全绿。

- [ ] **Step 1: 修改 `computeBilling` 签名，加 `granterUserID *uint` 参数**

把 `func (b *b2bBillingBiz) computeBilling(ctx context.Context, start, end time.Time) ([]grantEvent, error)` 改为：

```go
// granterUserID scopes the report to a single parent account:
//
//	nil  → all parent accounts (admin settlement report; unchanged behaviour)
//	&id  → only grants made by that parent (parent self-service report)
//
// When set, an additional `granter_user_id = ?` predicate is ANDed onto the
// Rule A / Rule B / trial queries so cross-parent rows never load into memory.
func (b *b2bBillingBiz) computeBilling(ctx context.Context, start, end time.Time, granterUserID *uint) ([]grantEvent, error) {
```

- [ ] **Step 2: 在 Rule A 查询追加 granter 过滤**

把 Rule A 的查询块改为先构建 query 再条件追加 Where：

```go
	// ── Rule A: first-month subscribers ──────────────────────────────────────
	var subsA []membershipModel.Subscription
	qA := b.ds.DB().WithContext(ctx).
		Where("source = ? AND first_started_at >= ? AND first_started_at < ? AND granter_user_id IS NOT NULL",
			membershipModel.SourceB2BGrant, start, end)
	if granterUserID != nil {
		qA = qA.Where("granter_user_id = ?", *granterUserID)
	}
	if err := qA.Find(&subsA).Error; err != nil {
		return nil, fmt.Errorf("computeBilling: query subs rule A: %w", err)
	}
```

- [ ] **Step 3: 在 Rule B 查询追加 granter 过滤**

```go
	// ── Rule B: cross-month renewals ─────────────────────────────────────────
	var subsB []membershipModel.Subscription
	qB := b.ds.DB().WithContext(ctx).
		Where("source = ? AND first_started_at < ? AND updated_at >= ? AND updated_at < ? AND granter_user_id IS NOT NULL",
			membershipModel.SourceB2BGrant, start, start, end)
	if granterUserID != nil {
		qB = qB.Where("granter_user_id = ?", *granterUserID)
	}
	if err := qB.Find(&subsB).Error; err != nil {
		return nil, fmt.Errorf("computeBilling: query subs rule B: %w", err)
	}
```

- [ ] **Step 4: 在 trial 查询追加 granter 过滤**

```go
	// ── Trial path ───────────────────────────────────────────────────────────
	var trials []membershipModel.TrialGrant
	qT := b.ds.DB().WithContext(ctx).
		Where("source = ? AND granted_at >= ? AND granted_at < ? AND granter_user_id IS NOT NULL",
			membershipModel.SourceB2BGrant, start, end)
	if granterUserID != nil {
		qT = qT.Where("granter_user_id = ?", *granterUserID)
	}
	if err := qT.Find(&trials).Error; err != nil {
		return nil, fmt.Errorf("computeBilling: query trials: %w", err)
	}
```

- [ ] **Step 5: 更新唯一调用方 `GetBillingReport` 传 nil**

在 `GetBillingReport` 内把 `events, err := b.computeBilling(ctx, start, end)` 改为：

```go
	events, err := b.computeBilling(ctx, start, end, nil)
```

- [ ] **Step 6: 跑现有测试套件做回归**

Run: `cd /private/tmp/wt-parent-billing-report-numind-server && go test ./internal/numind/biz/b2b_billing/... -v`
Expected: PASS（所有现有 `TestRuleA_*` / `TestGetBillingReport_*` 全绿——证明 nil 路径行为不变）

- [ ] **Step 7: lint + commit**

```bash
cd /private/tmp/wt-parent-billing-report-numind-server
task lint
git add internal/numind/biz/b2b_billing/b2b_billing.go
git commit -m "refactor(b2b_billing): add optional granterUserID filter to computeBilling

nil = all parents (admin, unchanged); &id = single parent (parent self-service).
Pure refactor, existing suite green."
```

---

## Task 2: biz — ParentBillingReport + GetBillingReportForParent + 父账户作用域测试

**Files:**
- Modify: `internal/numind/biz/b2b_billing/b2b_billing.go`（加 sentinel error、`ParentBillingReport`、`lookupUsernames` helper、接口方法、实现）
- Test: `internal/numind/biz/b2b_billing/b2b_billing_test.go`（新增 6 个测试）

实现细节关键点：**父账户校验只查 `parent_user_id` 单列**（用 `b.ds.DB()` 显式 Select，匹配本包既有 `Select("id, username")` 风格），不用 `Users().GetByID`（`SELECT *` 会在测试 sqlite 的部分 `user` 表上失败）。

- [ ] **Step 1: 先写失败测试 — 作用域隔离（越权）**

在 `b2b_billing_test.go` 末尾追加。先加一个插入子账户（parent_user_id 非空）的小 helper：

```go
// insertChildUser inserts a user whose parent_user_id is set (a non-parent / child account).
func insertChildUser(t *testing.T, db *gorm.DB, username string, parentID uint) uint {
	t.Helper()
	res := db.Exec(
		`INSERT INTO user (created_at, updated_at, username, parent_user_id) VALUES (?, ?, ?, ?)`,
		time.Now(), time.Now(), username, parentID,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

func TestGetBillingReportForParent_ScopedToParent(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parentA := insertB2BUser(t, db, "parentA")
	parentB := insertB2BUser(t, db, "parentB")
	childA := insertB2BUser(t, db, "childA")
	childB := insertB2BUser(t, db, "childB")

	may := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	insertSubGrant(t, db, parentA, childA, 1, may) // ¥99 by parentA
	insertSubGrant(t, db, parentB, childB, 3, may) // ¥297 by parentB

	r, err := biz.GetBillingReportForParent(context.Background(), "2026-05", parentA)
	require.NoError(t, err)
	assert.EqualValues(t, parentA, r.ParentUserID)
	assert.Equal(t, 1, r.GrantsCount, "parentA only sees own 1 grant")
	assert.EqualValues(t, 9900, r.TotalAmountCents)
	require.Len(t, r.Details, 1)
	assert.EqualValues(t, childA, r.Details[0].ChildUserID, "must NOT see parentB's child")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd /private/tmp/wt-parent-billing-report-numind-server && go test ./internal/numind/biz/b2b_billing/ -run TestGetBillingReportForParent_ScopedToParent`
Expected: FAIL（编译错误：`GetBillingReportForParent` undefined）

- [ ] **Step 3: 加 sentinel error + ParentBillingReport 结构**

在 `b2b_billing.go` import 块加 `"errors"`，并在 `B2BBillingReport` 结构定义附近加：

```go
// ErrNotParentAccount is returned by GetBillingReportForParent when the caller
// is not a parent account (User.ParentUserID != nil). Controllers map this to 403.
var ErrNotParentAccount = errors.New("b2b_billing: not a parent account")

// ParentBillingReport is one parent's own monthly settlement view (self-service).
// Unlike B2BBillingReport it is flat (no by-parent grouping) since it is always
// scoped to a single parent.
type ParentBillingReport struct {
	Month            string        `json:"month"`
	ParentUserID     uint          `json:"parent_user_id"`
	GrantsCount      int           `json:"grants_count"`
	TotalAmountCents int64         `json:"total_amount_cents"`
	Details          []GrantDetail `json:"details"`
}
```

- [ ] **Step 4: 抽 `lookupUsernames` helper（DRY，buildReport 与新方法共用）**

在 `buildReport` 上方加 helper，并把 `buildReport` 内的 username 查询替换为调用它：

```go
// lookupUsernames returns id→username for the given user IDs (Select id,username only).
func (b *b2bBillingBiz) lookupUsernames(ctx context.Context, ids []uint) (map[uint]string, error) {
	out := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var users []model.User
	if err := b.ds.DB().WithContext(ctx).
		Select("id, username").Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, fmt.Errorf("lookupUsernames: %w", err)
	}
	for _, u := range users {
		out[u.ID] = u.Username
	}
	return out, nil
}
```

在 `buildReport` 内，把原来的 `var users []model.User ... usernameByID := ...` 整段替换为：

```go
	usernameByID, err := b.lookupUsernames(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("buildReport: %w", err)
	}
```

（注意：`buildReport` 已 `return nil, fmt.Errorf(...)` 用过 err 变量，确认 err 已声明；若未声明改用 `:=`。）

- [ ] **Step 5: 实现 `GetBillingReportForParent` + 接口声明**

在 `IB2BBillingBiz` 接口加方法：

```go
type IB2BBillingBiz interface {
	GetBillingReport(ctx context.Context, month string) (*B2BBillingReport, error)
	GetBillingReportForParent(ctx context.Context, month string, parentUserID uint) (*ParentBillingReport, error)
}
```

实现（放在 `GetBillingReport` 之后）：

```go
// GetBillingReportForParent assembles the self-service monthly settlement view
// for one parent account. Reuses computeBilling with a granter filter so the
// amounts are byte-for-byte consistent with the admin settlement report.
//
// Returns ErrNotParentAccount if parentUserID is not a parent account.
func (b *b2bBillingBiz) GetBillingReportForParent(ctx context.Context, month string, parentUserID uint) (*ParentBillingReport, error) {
	start, end, err := parseMonth(month)
	if err != nil {
		return nil, err
	}

	// Parent-account gate: query parent_user_id only (matches package query style;
	// avoids SELECT * coupling to the full user schema).
	var row struct {
		ParentUserID *uint `gorm:"column:parent_user_id"`
	}
	if err := b.ds.DB().WithContext(ctx).
		Table("user").Select("parent_user_id").
		Where("id = ?", parentUserID).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotParentAccount
		}
		return nil, fmt.Errorf("GetBillingReportForParent: lookup user %d: %w", parentUserID, err)
	}
	if row.ParentUserID != nil {
		return nil, ErrNotParentAccount
	}

	events, err := b.computeBilling(ctx, start, end, &parentUserID)
	if err != nil {
		return nil, fmt.Errorf("GetBillingReportForParent month=%s parent=%d: %w", month, parentUserID, err)
	}

	report := &ParentBillingReport{
		Month:        month,
		ParentUserID: parentUserID,
		Details:      []GrantDetail{},
	}
	if len(events) == 0 {
		return report, nil
	}

	childIDs := make([]uint, 0, len(events))
	for _, e := range events {
		childIDs = append(childIDs, e.childUserID)
	}
	usernameByID, err := b.lookupUsernames(ctx, childIDs)
	if err != nil {
		return nil, fmt.Errorf("GetBillingReportForParent: %w", err)
	}
	for _, e := range events {
		report.Details = append(report.Details, GrantDetail{
			ChildUserID:   e.childUserID,
			ChildUsername: usernameByID[e.childUserID],
			ProductType:   e.productType,
			Months:        e.months,
			AmountCents:   e.amountCents,
			GrantedAt:     e.grantedAt,
		})
		report.TotalAmountCents += e.amountCents
	}
	report.GrantsCount = len(report.Details)
	return report, nil
}
```

需要 import `"gorm.io/gorm"`（确认 import 块已有或追加）。

- [ ] **Step 6: 跑 Step 1 测试确认通过**

Run: `go test ./internal/numind/biz/b2b_billing/ -run TestGetBillingReportForParent_ScopedToParent -v`
Expected: PASS

- [ ] **Step 7: 写剩余 5 个测试**

```go
func TestGetBillingReportForParent_AmountMatchesAdmin(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "parent")
	c1 := insertB2BUser(t, db, "c1")
	c2 := insertB2BUser(t, db, "c2")
	may := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	insertSubGrant(t, db, parent, c1, 12, may)        // annual ¥949
	insertTrialGrantRow(t, db, parent, c2, may)       // trial ¥9.9

	admin, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	require.Len(t, admin.ByParent, 1)

	self, err := biz.GetBillingReportForParent(context.Background(), "2026-05", parent)
	require.NoError(t, err)
	assert.Equal(t, admin.ByParent[0].AmountCents, self.TotalAmountCents, "口径必须一致")
	assert.Equal(t, admin.ByParent[0].GrantsCount, self.GrantsCount)
}

func TestGetBillingReportForParent_EmptyMonth(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)
	parent := insertB2BUser(t, db, "parent")

	r, err := biz.GetBillingReportForParent(context.Background(), "2026-05", parent)
	require.NoError(t, err)
	assert.EqualValues(t, 0, r.TotalAmountCents)
	assert.Equal(t, 0, r.GrantsCount)
	assert.NotNil(t, r.Details)
	assert.Len(t, r.Details, 0)
}

func TestGetBillingReportForParent_TrialAndMonthly(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)
	parent := insertB2BUser(t, db, "parent")
	c1 := insertB2BUser(t, db, "c1")
	c2 := insertB2BUser(t, db, "c2")
	may := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	insertSubGrant(t, db, parent, c1, 2, may)   // 2 months ¥198
	insertTrialGrantRow(t, db, parent, c2, may.Add(time.Hour)) // trial ¥9.9

	r, err := biz.GetBillingReportForParent(context.Background(), "2026-05", parent)
	require.NoError(t, err)
	require.Len(t, r.Details, 2)
	assert.EqualValues(t, 198_00+9_90, r.TotalAmountCents)
	// trial detail has Months==0, monthly has Months>0
	var sawTrial, sawMonthly bool
	for _, d := range r.Details {
		if d.ProductType == membershipModel.ProductTypeTrial {
			sawTrial = true
			assert.Equal(t, 0, d.Months)
		}
		if d.ProductType == membershipModel.ProductTypeMonthly {
			sawMonthly = true
			assert.Equal(t, 2, d.Months)
		}
	}
	assert.True(t, sawTrial && sawMonthly)
}

func TestGetBillingReportForParent_NotParentAccount(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)
	parent := insertB2BUser(t, db, "parent")
	child := insertChildUser(t, db, "child", parent)

	_, err := biz.GetBillingReportForParent(context.Background(), "2026-05", child)
	assert.ErrorIs(t, err, ErrNotParentAccount)
}

func TestGetBillingReportForParent_InvalidMonth(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)
	parent := insertB2BUser(t, db, "parent")
	for _, bad := range []string{"2026-13", "2026-1", "2026/05", "bad", ""} {
		_, err := biz.GetBillingReportForParent(context.Background(), bad, parent)
		assert.Error(t, err, "bad month %q must error", bad)
	}
}
```

- [ ] **Step 8: 跑全部 b2b_billing 测试**

Run: `go test ./internal/numind/biz/b2b_billing/... -v`
Expected: PASS（新 6 个 + 全部现有）

- [ ] **Step 9: lint + commit**

```bash
cd /private/tmp/wt-parent-billing-report-numind-server
task lint
git add internal/numind/biz/b2b_billing/
git commit -m "feat(b2b_billing): GetBillingReportForParent — parent self-service monthly report

Reuses computeBilling(granter filter); flat ParentBillingReport; ErrNotParentAccount
sentinel for non-parent. Tests: scope isolation (越权), amount-matches-admin (口径),
empty month, trial+monthly, non-parent, invalid month."
```

---

## Task 3: controller + router — 用户端 GET /v1/users/me/billing-report

**Files:**
- Create: `internal/numind/controller/v1/parent_billing/billing_report.go`
- Modify: `internal/numind/router.go`（authGroup 注册）

控制器不做单测（项目惯例：controller 由 E2E 覆盖）；biz 已全测。验证靠编译 + lint + S5 E2E。

- [ ] **Step 1: 创建控制器**

```go
package parent_billing

import (
	"errors"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/b2b_billing"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// ParentBillingController serves the user-side (parent) self-service billing report.
//
//	GET /v1/users/me/billing-report?month=YYYY-MM   (user_token, parent accounts only)
type ParentBillingController struct {
	biz b2b_billing.IB2BBillingBiz
}

// New constructs a ParentBillingController wired to the given biz.
func New(biz b2b_billing.IB2BBillingBiz) *ParentBillingController {
	return &ParentBillingController{biz: biz}
}

// GetMyBillingReport handles GET /v1/users/me/billing-report.
//
// Parent id is taken from the auth context only (never a client param) to
// prevent cross-parent access. Non-parent callers get 403.
func (ctrl *ParentBillingController) GetMyBillingReport(c *gin.Context) {
	userID := c.GetUint("userID")
	if userID == 0 {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("未识别的用户"), nil)
		return
	}

	month := c.Query("month")
	if month == "" {
		core.WriteResponse(c, errno.ErrBind.SetMessage("month 参数必填，格式 YYYY-MM"), nil)
		return
	}

	report, err := ctrl.biz.GetBillingReportForParent(c, month, userID)
	if err != nil {
		if errors.Is(err, b2b_billing.ErrNotParentAccount) {
			core.WriteResponse(c, errno.ErrForbidden.SetMessage("仅父账户可查看费用对账"), nil)
			return
		}
		log.C(c).Errorw("Failed to get parent billing report", "month", month, "userID", userID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, report)
}
```

> 注：`month` 格式非法时 biz 的 `parseMonth` 返回 err，会落到上面 InternalServerError 分支并返回 500。若希望非法 month 返回 400，可在调用前加一个轻量正则预校验，或让 biz 暴露一个 `IsInvalidMonth` sentinel。**本计划采用预校验**——见 Step 2。

- [ ] **Step 2: 非法 month 返回 400（前置正则预校验）**

在 `GetMyBillingReport` 的 `month == ""` 判断之后、调 biz 之前插入：

```go
	if !monthFormatRe.MatchString(month) {
		core.WriteResponse(c, errno.ErrBind.SetMessage("month 格式错误，应为 YYYY-MM"), nil)
		return
	}
```

并在文件顶部（import 后）加包级变量：

```go
import "regexp"

var monthFormatRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
```

（与 biz `monthRegex` 同规则；前置校验保证非法 month → 400 而非 500。biz 内 `parseMonth` 仍是第二道防线。）

- [ ] **Step 3: 在 router.go 注册路由**

在 `internal/numind/router.go` 的 `authGroup` 块内，紧邻现有 `/users/children*` 路由（约 router.go:280-283）追加。先在构造区实例化控制器（参照 admin 端 `b2b_billing.New(store.S)` 用法）：

```go
	parentBillingCtrl := parent_billing.New(b2b_billing.New(store.S))
```

再注册：

```go
	authGroup.GET("/users/me/billing-report", parentBillingCtrl.GetMyBillingReport)
```

并在 router.go import 块加：

```go
	"numind-server/internal/numind/biz/b2b_billing"
	"numind-server/internal/numind/controller/v1/parent_billing"
```

（确认 `b2b_billing` 是否已被 router.go import；admin 路由在 admin_router.go 而非 router.go，故 router.go 很可能尚未 import，需新增。）

- [ ] **Step 4: 编译 + lint**

Run:
```bash
cd /private/tmp/wt-parent-billing-report-numind-server
go build ./...
go test ./internal/numind/... 2>&1 | tail -20
task lint
```
Expected: build 成功，测试无回归，lint exit 0

- [ ] **Step 5: commit**

```bash
git add internal/numind/controller/v1/parent_billing/ internal/numind/router.go
git commit -m "feat(api): GET /v1/users/me/billing-report — parent self-service billing endpoint

user_token, parent-only (non-parent→403 via ErrNotParentAccount). Month pre-validated
(bad month→400). Mirrors admin_b2b controller; parent id from auth context only."
```

---

## Task 4: 前端 API — getParentBillingReport + 类型

**Files:**
- Modify: `numind-web-v3/src/api/parent.ts`

- [ ] **Step 1: 加类型 + API 函数**

在 `src/api/parent.ts` 末尾追加（沿用文件现有 `request` import 与 `ApiResponse` 类型；若类型名不同，按文件实际命名对齐）：

```typescript
export interface ParentBillingDetail {
  child_user_id: number
  child_username: string
  product_type: 'trial' | 'monthly'
  months: number
  amount_cents: number
  granted_at: string
}

export interface ParentBillingReport {
  month: string
  parent_user_id: number
  grants_count: number
  total_amount_cents: number
  details: ParentBillingDetail[]
}

/** 父账户自助费用对账：按月查询当前登录父账户名下子账号的开通明细。 */
export const getParentBillingReport = (month: string) =>
  request.get<ApiResponse<ParentBillingReport>>('/v1/users/me/billing-report', {
    params: { month },
  })
```

- [ ] **Step 2: type-check**

Run: `cd /private/tmp/wt-parent-billing-report-numind-web-v3 && npm run type-check`
Expected: 通过（无类型错误）

- [ ] **Step 3: commit**

```bash
cd /private/tmp/wt-parent-billing-report-numind-web-v3
git add src/api/parent.ts
git commit -m "feat(api): getParentBillingReport + types for parent self-service billing"
```

---

## Task 5: 前端内页 + 路由 + 客户管理入口

**Files:**
- Create: `numind-web-v3/src/views/CustomersBillingView.vue`
- Modify: `numind-web-v3/src/router/index.ts`（子路由）
- Modify: `numind-web-v3/src/views/CustomersView.vue`（页面级入口按钮）

样式遵循 `DESIGN.md` 设计 token 与 `CustomersView.vue` 现有 DataTable 风格；禁用外部 UI 框架（ui-ux.md 硬规则 #5）。四状态齐全（硬规则 #2）。

- [ ] **Step 1: 创建 CustomersBillingView.vue**

```vue
<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { getParentBillingReport, type ParentBillingReport } from '@/api/parent'

const router = useRouter()

// 默认当月 YYYY-MM
function currentMonth(): string {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`
}
const maxMonth = currentMonth() // 不允许选未来月

const month = ref(currentMonth())
const report = ref<ParentBillingReport | null>(null)
const loading = ref(false)
const error = ref('')

function yuan(cents: number): string {
  return `¥${(cents / 100).toFixed(2)}`
}
function durationLabel(d: ParentBillingReport['details'][number]): string {
  return d.product_type === 'trial' ? '3 天' : `${d.months} 个月`
}
function productLabel(t: string): string {
  return t === 'trial' ? '体验包' : '月订阅'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const res = await getParentBillingReport(month.value)
    report.value = res.data.data
  } catch (e: any) {
    error.value = e?.response?.data?.message || '加载失败，请重试'
    report.value = null
  } finally {
    loading.value = false
  }
}

function onMonthChange() {
  load()
}

onMounted(load)
</script>

<template>
  <div class="billing-view">
    <header class="billing-header">
      <button class="back-btn" @click="router.push('/customers')">← 返回客户管理</button>
      <h1>费用对账</h1>
      <label class="month-picker">
        月份
        <input type="month" v-model="month" :max="maxMonth" @change="onMonthChange" />
      </label>
    </header>

    <!-- loading -->
    <div v-if="loading" class="state-loading">加载中…</div>

    <!-- error -->
    <div v-else-if="error" class="state-error">
      <p>{{ error }}</p>
      <button @click="load">重试</button>
    </div>

    <!-- empty -->
    <div v-else-if="report && report.details.length === 0" class="state-empty">
      <p>本月（{{ report.month }}）暂无开通记录</p>
      <p class="empty-total">本月合计 ¥0.00</p>
    </div>

    <!-- success -->
    <div v-else-if="report" class="state-success">
      <table class="billing-table">
        <thead>
          <tr>
            <th>子账号</th>
            <th>会员类型</th>
            <th>时长</th>
            <th>价格</th>
            <th>开通时间</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(d, i) in report.details" :key="i">
            <td>{{ d.child_username }}</td>
            <td>{{ productLabel(d.product_type) }}</td>
            <td>{{ durationLabel(d) }}</td>
            <td>{{ yuan(d.amount_cents) }}</td>
            <td>{{ new Date(d.granted_at).toLocaleDateString() }}</td>
          </tr>
        </tbody>
        <tfoot>
          <tr class="total-row">
            <td colspan="3">本月合计（{{ report.grants_count }} 笔）</td>
            <td colspan="2">{{ yuan(report.total_amount_cents) }}</td>
          </tr>
        </tfoot>
      </table>
    </div>
  </div>
</template>

<style scoped>
/* 遵循 DESIGN.md token 与 CustomersView 表格风格。实现时对齐项目设计变量，
   勿引入外部 UI 框架。以下为结构性占位样式，按 DESIGN.md 细化。 */
.billing-view { padding: 24px; max-width: 960px; margin: 0 auto; }
.billing-header { display: flex; align-items: center; gap: 16px; margin-bottom: 24px; }
.billing-header h1 { flex: 1; }
.month-picker input { margin-left: 8px; }
.billing-table { width: 100%; border-collapse: collapse; }
.billing-table th, .billing-table td { padding: 12px; text-align: left; border-bottom: 1px solid var(--border-color, #eee); }
.total-row { font-weight: 600; }
.state-loading, .state-error, .state-empty { padding: 48px; text-align: center; }
</style>
```

> 实现者注意：本组件的视觉细节（配色/间距/字体）必须按 `DESIGN.md` 与 `CustomersView.vue` 既有 DataTable 风格落地，上面 `<style>` 仅为结构骨架。需要时调用 impeccable 情境动词（如 `/layout`、`/typeset`）打磨。

- [ ] **Step 2: 注册子路由**

在 `src/router/index.ts` 的 routes 数组中、`/customers` 路由之后追加：

```typescript
  {
    path: '/customers/billing',
    name: 'customers-billing',
    component: () => import('@/views/CustomersBillingView.vue'),
    meta: { title: '费用对账', requiresAuth: true, parentOnly: true },
  },
```

（`parentOnly` 与 `/customers` 一致——前端 guard 仅 UX 层；后端 403 是真正屏障。）

- [ ] **Step 3: 在 CustomersView 加页面级入口按钮**

在 `src/views/CustomersView.vue` 的 hero/工具栏区域（标题附近，参照第 2-14 行 hero 区）加一个按钮：

```vue
<button class="billing-entry-btn" @click="$router.push('/customers/billing')">
  费用对账
</button>
```

（按钮样式对齐页面现有按钮风格；这是**页面级**入口，不是逐行 action 菜单项。）

- [ ] **Step 4: lint + type-check**

Run:
```bash
cd /private/tmp/wt-parent-billing-report-numind-web-v3
npm run lint
npm run type-check
```
Expected: 均通过

- [ ] **Step 5: commit**

```bash
git add src/views/CustomersBillingView.vue src/router/index.ts src/views/CustomersView.vue
git commit -m "feat(customers): 费用对账内页 /customers/billing + 客户管理页面级入口

按月展示父账户名下子账号开通明细+合计，四状态齐全，默认当月不选未来月。"
```

---

## Task 6: S5 验证策略（NDF 规则 10 — 必须独立成 task）

> 本 task 不写功能代码，定义 S5 阶段如何验收。由 S3 gate 的独立 reviewer 一并审查合理性。

**验证方式：Playwright E2E（持久回归） + 后端 go test。**

**理由：** 本功能属**会员/计费高风险域**（testing.md §5 / ndf-enforcement 规则 10 明确：支付/权限/会员等级高风险功能应写 Playwright E2E 留持久回归保护，而非一次性 gstack /qa）。越权隔离 + 金额口径一致是不可回归的安全/正确性属性。

**后端（已在 Task 2 落地，永久留存）：**
- `go test ./internal/numind/biz/b2b_billing/...` —— 含越权隔离（`TestGetBillingReportForParent_ScopedToParent`）+ 与 admin 口径一致（`TestGetBillingReportForParent_AmountMatchesAdmin`）回归测试。
- S5 跑完整版 `task test`（含 race + coverage）。

**前端关键用户路径（S5 写 Playwright E2E，访问 localhost:5173）：**
1. 父账户登录（`E2E_USERNAME` / `E2E_PASSWORD`）
2. 导航到 `/customers`（客户管理）→ 断言「费用对账」入口可见
3. 点击入口 → 跳转 `/customers/billing`
4. 默认展示当月；有数据时断言明细表渲染 + 合计金额 = 各行金额之和
5. 切换到一个无开通的历史月 → 断言空状态出现 + 合计 ¥0.00
6. （可选）越权前端验证：子账号登录无法访问 `/customers/billing`（路由 guard）

**E2E 文件：** `numind-web-v3/e2e/parent-billing-report.spec.ts`（永久留存做回归）。

**可观测性：** N/A（无 LLM 调用）。

- [ ] 本 task 仅为策略声明，无 commit（策略已写入本 plan + spec §10）。S5 阶段据此执行。

---

## Self-Review（writing-plans 自检）

**Spec 覆盖检查（对照 spec 各节）：**
- spec §3 API 契约 → Task 3（controller 实现请求/响应/错误表）✓
- spec §4.1 biz computeBilling 参数化 → Task 1 ✓；ParentBillingReport + GetBillingReportForParent + lookupUsernames → Task 2 ✓
- spec §4.2 controller 父账户校验 → Task 2（biz sentinel）+ Task 3（映射 403）✓
- spec §4.3 router 注册 → Task 3 ✓
- spec §4.4 后端 6 测试 → Task 2 Step 1/7（ScopedToParent/AmountMatchesAdmin/EmptyMonth/TrialAndMonthly/NotParentAccount/InvalidMonth）+ Task 1（NilFilterUnchanged 由现有套件回归覆盖）✓
- spec §5.1 路由 → Task 5 Step 2 ✓；§5.2 内页 → Task 5 Step 1 ✓；§5.3 入口 → Task 5 Step 3 ✓；§5.4 api → Task 4 ✓；§5.5 local state → Task 5（无 store）✓
- spec §6 越权/父账户限定 → Task 2（SQL granter 过滤 + 父账户 gate）+ Task 3（auth 上下文取 id）✓
- spec §7 边界 → Task 2 测试（空月/trial/invalid）+ Task 5（empty 状态）✓
- spec §9 验收映射 / §10 S5 策略 → Task 6 ✓

**Placeholder 扫描：** 无 TBD/TODO；所有代码步骤含完整代码。前端 `<style>` 显式标注为结构骨架 + 按 DESIGN.md 细化（非占位逻辑）。

**类型/签名一致性：** `computeBilling(ctx, start, end, granterUserID *uint)`（Task 1 定义，Task 2 以 `&parentUserID` 调用）✓；`ParentBillingReport` 字段（Task 2 定义）与前端 TS `ParentBillingReport`（Task 4）/ API 契约（spec §3.2）字段名一致 ✓；`ErrNotParentAccount`（Task 2 定义，Task 3 `errors.Is` 引用）✓；`GrantDetail` 复用现有结构 ✓；`lookupUsernames`（Task 2 定义，buildReport + GetBillingReportForParent 共用）✓。

**依赖无环：** 1→2→3（后端串行）；4→5（前端串行）；6 最后。无循环。
