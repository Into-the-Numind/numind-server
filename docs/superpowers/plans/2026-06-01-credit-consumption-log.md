# 额度消耗记录（Credit Consumption Log）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给所有登录用户在用户端设置页提供「积分消耗记录」入口（点击弹窗），按时间倒序展示自己每次消耗积分的 动作/时间/消耗数量，只含对账后（reconciled）真实记录。

**Architecture:** 数据源 `credit_reservation`（status=reconciled, actual_cost_cents>0，每动作一行）。后端新增只读 store 查询 → biz `ListConsumptionLog`（operation→中文名映射 + DTO）→ controller handler → router 注册 `GET /v1/credits/consumption-log`（user_token）。前端：`credits.ts` 加 API + 独立 Pinia store + 复用 `ConfirmModal`/`DataTable` 的弹窗组件 + 设置页入口。零 DB schema 变更，不涉及 LLM 调用。

**Tech Stack:** Go 1.24 / Gin / GORM / MySQL（后端，TDD with in-memory SQLite）；Vue 3 + Pinia + TypeScript（前端）。

对应 spec：`docs/superpowers/specs/2026-06-01-credit-consumption-log-design.md`。

> **S3-D7 (placement correction)**：spec §4.2 把 biz 方法放在 `biz/membership`，但经 S3 代码核实，`MembershipService` 用 `membershipstore.IMembershipStore`（无 reservation 访问），而 `credit_reservation` 查询与 `store.IStore` 访问都在 `biz/credit` 的 `creditService`（`Reconcile` 即在此，且 controller 通过 `b.CreditService()` 可达）。故 biz 方法落在 `biz/credit` 的 `creditService`（ICreditService）。API 契约/行为不变。

---

## File Structure

**numind-server（后端，先做）**
- Modify `internal/numind/store/credit.go` — `CreditStore` 接口 + `creditStore` 加只读方法 `ListReconciledReservationsByUser`
- Test `internal/numind/store/credit_consumption_log_test.go` — store 查询单测
- Create `internal/numind/biz/credit/consumption_log.go` — `ConsumptionLogItem` + `operationLabels` map + `operationLabel()` + `(*creditService).ListConsumptionLog`
- Modify `internal/numind/biz/credit/contracts.go` — `ICreditService` 加 `ListConsumptionLog`
- Test `internal/numind/biz/credit/consumption_log_test.go` — 映射/过滤/分页归一化 + 对账一致性
- Create `internal/numind/controller/v1/credit/consumption_log.go` — `(*CreditController).ListConsumptionLog` handler
- Modify `internal/numind/router.go` — 注册路由

**numind-web-v3（前端，后做）**
- Modify `src/api/credits.ts` — 类型 + `getConsumptionLog`
- Create `src/stores/consumptionLog.ts` — Pinia store
- Create `src/components/credit/CreditConsumptionLogModal.vue` — 弹窗
- Modify `src/views/SettingsView.vue` — 「积分与加量包」section 头右侧入口 + 挂弹窗

> 工作目录：后端 worktree `/private/tmp/wt-credit-consumption-log-numind-server`，前端 worktree `/private/tmp/wt-credit-consumption-log-numind-web-v3`。

---

## Task 1: 后端 store 只读查询 `ListReconciledReservationsByUser`

**Files:**
- Modify: `internal/numind/store/credit.go`（`CreditStore` 接口 + `creditStore` 实现）
- Test: `internal/numind/store/credit_consumption_log_test.go`

- [ ] **Step 1: Write the failing test**

创建 `internal/numind/store/credit_consumption_log_test.go`：
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

	"numind-server/internal/pkg/model"
)

// newReservationTestDB 建内存 SQLite 并 hand-roll credit_reservation 表
// （CreditReservation.Status/FinalizeReason 是 MySQL ENUM，SQLite 不解析 → 用 TEXT）。
func newReservationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec(`
CREATE TABLE IF NOT EXISTS credit_reservation (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    reference_type TEXT NOT NULL DEFAULT '',
    reference_id TEXT NOT NULL DEFAULT '',
    operation TEXT NOT NULL,
    reserved_credits INTEGER NOT NULL DEFAULT 0,
    coefficient_id INTEGER,
    status TEXT NOT NULL DEFAULT 'reserved',
    actual_cost_cents INTEGER,
    delta INTEGER,
    finalize_reason TEXT,
    idempotency_key TEXT,
    reconciled_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    estimation_source TEXT NOT NULL DEFAULT 'credit_coefficient',
    token_profile_id INTEGER,
    estimated_prompt_tokens INTEGER NOT NULL DEFAULT 0,
    estimated_completion_tokens INTEGER NOT NULL DEFAULT 0,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    context_budget_event_id INTEGER,
    user_type_multiplier REAL NOT NULL DEFAULT 1.0
);`).Error)
	return db
}

func ptrI64(v int64) *int64 { return &v }

func TestListReconciledReservationsByUser(t *testing.T) {
	ctx := context.Background()
	db := newReservationTestDB(t)
	s := &creditStore{db: db}

	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	rows := []model.CreditReservation{
		{UserID: 1, Operation: "sop_run", Status: "reconciled", ActualCostCents: ptrI64(18), CreatedAt: base},
		{UserID: 1, Operation: "salesrag_chat", Status: "reconciled", ActualCostCents: ptrI64(6), CreatedAt: base.Add(time.Hour)},
		{UserID: 1, Operation: "sop_run", Status: "reserved", ActualCostCents: nil, CreatedAt: base.Add(2 * time.Hour)},   // 未平账 → 排除
		{UserID: 1, Operation: "ocr", Status: "refunded", ActualCostCents: ptrI64(0), CreatedAt: base.Add(3 * time.Hour)}, // 全退 → 排除
		{UserID: 1, Operation: "file_parse", Status: "reconciled", ActualCostCents: ptrI64(0), CreatedAt: base.Add(4 * time.Hour)}, // 0 成本 → 排除
		{UserID: 2, Operation: "sop_run", Status: "reconciled", ActualCostCents: ptrI64(99), CreatedAt: base}, // 别的用户 → 隔离
	}
	for i := range rows {
		require.NoError(t, db.Create(&rows[i]).Error)
	}

	got, total, err := s.ListReconciledReservationsByUser(ctx, 1, 0, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total, "只数 user=1 的 reconciled 且 actual_cost_cents>0")
	require.Len(t, got, 2)
	// created_at DESC：salesrag_chat（晚）在前，sop_run 在后
	assert.Equal(t, "salesrag_chat", got[0].Operation)
	assert.Equal(t, "sop_run", got[1].Operation)
	assert.Equal(t, int64(6), *got[0].ActualCostCents)

	// 分页：limit=1 拿第一页
	page1, total2, err := s.ListReconciledReservationsByUser(ctx, 1, 0, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total2)
	require.Len(t, page1, 1)
	assert.Equal(t, "salesrag_chat", page1[0].Operation)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-server && go test ./internal/numind/store/ -run TestListReconciledReservationsByUser -v`
Expected: 编译失败 `s.ListReconciledReservationsByUser undefined`。

- [ ] **Step 3: Add method to interface**

`internal/numind/store/credit.go` 的 `CreditStore interface` 内，`ListTransactionsByUser` 行下方加：
```go
	// ListReconciledReservationsByUser 返回某用户已平账（status=reconciled 且
	// actual_cost_cents>0）的预扣记录，按 created_at DESC（次级 id DESC）分页，
	// 返回过滤下的总数。用户端「积分消耗记录」数据源。只读。
	ListReconciledReservationsByUser(ctx context.Context, userID uint, offset, limit int) ([]model.CreditReservation, int64, error)
```

- [ ] **Step 4: Implement the method**

`internal/numind/store/credit.go`，在 `ListTransactionsByUser` 方法后追加：
```go
// ListReconciledReservationsByUser 见接口注释。只读，使用 GORM query builder。
func (s *creditStore) ListReconciledReservationsByUser(ctx context.Context, userID uint, offset, limit int) ([]model.CreditReservation, int64, error) {
	var rows []model.CreditReservation
	var total int64

	const cond = "user_id = ? AND status = ? AND actual_cost_cents > ?"
	countDB := s.db.WithContext(ctx).Model(&model.CreditReservation{}).Where(cond, userID, "reconciled", 0)
	if err := countDB.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	findDB := s.db.WithContext(ctx).Where(cond, userID, "reconciled", 0)
	if err := findDB.Order("created_at DESC").Order("id DESC").
		Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-server && go test ./internal/numind/store/ -run TestListReconciledReservationsByUser -v`
Expected: PASS。

- [ ] **Step 6: Lint + commit**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-server && task lint`
Expected: exit 0。
```bash
git add internal/numind/store/credit.go internal/numind/store/credit_consumption_log_test.go
git commit -m "feat(credit): add ListReconciledReservationsByUser store query

Read-only credit_reservation query (status=reconciled, actual_cost_cents>0,
created_at DESC) backing the user consumption-log feature."
```

---

## Task 2: 后端 biz `ListConsumptionLog`（映射 + DTO）

**Files:**
- Create: `internal/numind/biz/credit/consumption_log.go`
- Modify: `internal/numind/biz/credit/contracts.go`（`ICreditService` 加方法）
- Test: `internal/numind/biz/credit/consumption_log_test.go`

- [ ] **Step 1: Write the failing test**

创建 `internal/numind/biz/credit/consumption_log_test.go`（package `credit_test`）：
```go
package credit_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)
// 注：本文件在 package credit_test，经 svc(ICreditService) + 既有 helper 间接用被测包，
// 不直接 import biz/credit（无 credit.X 裸引用，避免 unused import 编译错误）。

func i64p(v int64) *int64 { return &v }

// TestListConsumptionLog_MappingFilterPaging 用 newCreditReserveTestDB（已含
// credit_reservation 表）直接 seed 行，验证 biz 映射 / 过滤 / 分页归一化。
func TestListConsumptionLog_MappingFilterPaging(t *testing.T) {
	ctx := context.Background()
	db := newCreditReserveTestDB(t)
	ds := store.NewTestStore(db) // NewTestStore（非 NewStore 单例）保证测试隔离
	svc := newCreditServiceWithMembership(ds, db, nil)

	base := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	seed := []model.CreditReservation{
		{UserID: 7, Operation: "sop_run", Status: "reconciled", ReservedCredits: 20, Delta: i64p(-2), ActualCostCents: i64p(18), CreatedAt: base},
		{UserID: 7, Operation: "weird_new_op", Status: "reconciled", ActualCostCents: i64p(5), CreatedAt: base.Add(time.Hour)},
		{UserID: 7, Operation: "sop_run", Status: "reserved", ActualCostCents: nil, CreatedAt: base.Add(2 * time.Hour)},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}

	items, total, err := svc.ListConsumptionLog(ctx, 7, 1, 20)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total)
	require.Len(t, items, 2)
	// created_at DESC：weird_new_op 在前（未知 operation 回退裸值）
	assert.Equal(t, "weird_new_op", items[0].Action)
	assert.Equal(t, "weird_new_op", items[0].ActionLabel)
	assert.Equal(t, int64(5), items[0].Credits)
	// sop_run → 中文名，credits = actual_cost_cents
	assert.Equal(t, "sop_run", items[1].Action)
	assert.Equal(t, "SOP 执行", items[1].ActionLabel)
	assert.Equal(t, int64(18), items[1].Credits)

	// 分页归一化：page=0 / pageSize=0 → 视为 1 / 20，不报错
	_, _, err = svc.ListConsumptionLog(ctx, 7, 0, 0)
	require.NoError(t, err)
	// pageSize 上限 100：传 9999 不应 panic / 报错
	_, _, err = svc.ListConsumptionLog(ctx, 7, 1, 9999)
	require.NoError(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-server && go test ./internal/numind/biz/credit/ -run TestListConsumptionLog_MappingFilterPaging -v`
Expected: 编译失败 `svc.ListConsumptionLog undefined` + `credit.ConsumptionLogItem undefined`。

- [ ] **Step 3: Add method to ICreditService interface**

`internal/numind/biz/credit/contracts.go` 的 `ICreditService interface` 末尾（`ReconcileAgentTest` 之后、`}` 之前）加：
```go

	// ListConsumptionLog 返回用户「平账后真实消耗」流水（每动作一行，数据源
	// credit_reservation status=reconciled）。page 1-based、pageSize 默认 20 上限
	// 100（方法内归一化），返回总数。只读。
	ListConsumptionLog(ctx context.Context, userID uint, page, pageSize int) ([]ConsumptionLogItem, int64, error)
```

- [ ] **Step 4: Implement biz method + map**

创建 `internal/numind/biz/credit/consumption_log.go`：
```go
package credit

import (
	"context"
	"fmt"
	"time"
)

// ConsumptionLogItem 是「积分消耗记录」单行展示 DTO。
// json tag 即用户端 API 字段（见 spec §3）。
type ConsumptionLogItem struct {
	ID          uint64    `json:"id"`
	Action      string    `json:"action"`       // 机读 operation（如 sop_run）
	ActionLabel string    `json:"action_label"` // 中文展示名（未知回退裸 operation）
	Credits     int64     `json:"credits"`      // 本次真实消耗积分（= actual_cost_cents）
	CreatedAt   time.Time `json:"created_at"`
}

// operationLabels：机读 operation → 中文展示名。未命中由 operationLabel 回退裸值。
// operation 全集见 types.go（OpSopRun 等）+ agent_test。
var operationLabels = map[string]string{
	"sop_run":          "SOP 执行",
	"sop_chat":         "SOP 对话",
	"salesrag_chat":    "销售对话",
	"chatbot_chat":     "智能对话",
	"profile_analysis": "客户画像分析",
	"file_parse":       "文件解析",
	"style_analysis":   "风格分析",
	"ocr":              "文字识别",
	"agent_test":       "智能体运行",
}

func operationLabel(op string) string {
	if label, ok := operationLabels[op]; ok {
		return label
	}
	return op
}

// ListConsumptionLog 见 ICreditService 接口注释。数据源 credit_reservation
// （status=reconciled, actual_cost_cents>0）；展示 credits = actual_cost_cents
// （= reserved_credits + delta，对账后真实净扣减，见 spec §2.2）。
func (s *creditService) ListConsumptionLog(ctx context.Context, userID uint, page, pageSize int) ([]ConsumptionLogItem, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	rows, total, err := s.store.Credits().ListReconciledReservationsByUser(ctx, userID, offset, pageSize)
	if err != nil {
		return nil, 0, fmt.Errorf("ListConsumptionLog: %w", err)
	}

	items := make([]ConsumptionLogItem, 0, len(rows))
	for _, r := range rows {
		var credits int64
		if r.ActualCostCents != nil {
			credits = *r.ActualCostCents
		}
		items = append(items, ConsumptionLogItem{
			ID:          r.ID,
			Action:      r.Operation,
			ActionLabel: operationLabel(r.Operation),
			Credits:     credits,
			CreatedAt:   r.CreatedAt,
		})
	}
	return items, total, nil
}
```

- [ ] **Step 5: Run mapping test to verify it passes**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-server && go test ./internal/numind/biz/credit/ -run TestListConsumptionLog_MappingFilterPaging -v`
Expected: PASS。

- [ ] **Step 6: Add the 对账一致性 (ledger truth) test**

在 `consumption_log_test.go` 追加（走真实 Reserve→Reconcile，验证展示数字 = 账本真实扣减）。**复用本包既有 `setupReservation(t, userID, estimated, []seedPackage{...})`**（见 `credit_service_reconcile_test.go` happy-path：它 seed 用户+订阅池、构建 svc 并完成一次 Reserve，返回 `(svc, ds, rsv)`）——无需新造 helper：
```go
// TestListConsumptionLog_LedgerTruth 跑真实 Reserve→Reconcile，断言展示的 credits
// == reservation.actual_cost_cents == 该用户 credit_transaction 净扣减绝对值。
// 这是「数字必须真实可信」的核心保证（spec §9 测试 2）。
func TestListConsumptionLog_LedgerTruth(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	// setupReservation: userID=505, estimated(reserved)=120, 订阅池 1000 credits；
	// 内部已 Reserve，返回 svc(ICreditService) / ds(store.IStore) / rsv(*Reservation)。
	svc, ds, rsv := setupReservation(t, 505, 120, []seedPackage{
		{Type: model.CreditTypeSubscription, TotalCredits: 1000, RemainCredits: 1000,
			ActivatedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	})
	// Reconcile actual=95（< reserved 120 → 退 25；净真实消耗 = 95）。
	require.NoError(t, svc.Reconcile(ctx, rsv.ID, 95))

	items, total, err := svc.ListConsumptionLog(ctx, 505, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Len(t, items, 1)
	assert.Equal(t, int64(95), items[0].Credits, "展示额 = actual_cost_cents (= reserved+delta)")

	// 账本对账：该用户所有 credit_transaction.amount 之和（负）取反 == 展示额。
	var ledgerSum int64
	require.NoError(t, ds.DB().Model(&model.CreditTransaction{}).
		Where("user_id = ?", 505).
		Select("COALESCE(SUM(amount),0)").Scan(&ledgerSum).Error)
	assert.Equal(t, items[0].Credits, -ledgerSum, "展示额必须等于账本真实净扣减")
}
```
> 说明：`setupReservation` / `seedPackage` / `model.CreditTypeSubscription` 均为本测试包既有符号（`credit_service_reconcile_test.go` happy-path 同款），直接复用。`setupReservation` 内部已用 `newCreditReserveTestDB`（含 credit_reservation + credit_transaction 表）。

- [ ] **Step 7: Run full credit pkg tests**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-server && go test ./internal/numind/biz/credit/ -run TestListConsumptionLog -v`
Expected: 两个 test 均 PASS。

- [ ] **Step 8: Lint + commit**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-server && task lint`
Expected: exit 0。
```bash
git add internal/numind/biz/credit/consumption_log.go internal/numind/biz/credit/contracts.go internal/numind/biz/credit/consumption_log_test.go
git commit -m "feat(credit): ListConsumptionLog biz method + operation label map

Maps reconciled credit_reservation rows to display DTO (action/label/
credits/time). credits = actual_cost_cents (ledger-truth asserted).
Unknown operations fall back to the raw value. Pagination normalized."
```

---

## Task 3: 后端 controller handler + router 注册（打通 API 契约）

**Files:**
- Create: `internal/numind/controller/v1/credit/consumption_log.go`
- Modify: `internal/numind/router.go`

- [ ] **Step 1: Implement handler**

创建 `internal/numind/controller/v1/credit/consumption_log.go`：
```go
package credit

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// ListConsumptionLog GET /v1/credits/consumption-log — C 用户查看自己的积分消耗流水
// （平账后真实记录，每动作一行）。Query: page(默认1) / page_size(默认20,上限100)。
// user_id 仅取自 auth 上下文，绝不接受客户端传入 → 杜绝越权（spec §7）。
func (c *CreditController) ListConsumptionLog(ctx *gin.Context) {
	user := middleware.GetCurrentUser(ctx)
	if user == nil {
		core.WriteResponse(ctx, errno.ErrTokenInvalid, nil)
		return
	}

	page, _ := strconv.Atoi(ctx.Query("page"))           // 解析失败=0 → biz 归一化为 1
	pageSize, _ := strconv.Atoi(ctx.Query("page_size"))  // 解析失败=0 → biz 归一化为 20

	items, total, err := c.creditSvc.ListConsumptionLog(ctx, uint(user.ID), page, pageSize)
	if err != nil {
		log.C(ctx).Errorw("ListConsumptionLog failed", "user_id", user.ID, "err", err)
		core.WriteResponse(ctx, errno.ErrInternalServer, nil) // 不向 C 端泄露内部 err 细节
		return
	}
	core.WriteResponse(ctx, nil, gin.H{"list": items, "total": total})
}
```

- [ ] **Step 2: Register route**

`internal/numind/router.go`，在 `authGroup.GET("/credits/balance", creditCtrl.GetBalance)` 同一花括号块内加一行：
```go
		authGroup.GET("/credits/consumption-log", creditCtrl.ListConsumptionLog)
```

- [ ] **Step 3: Build + lint + vet**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-server && go build ./... && task lint && go test ./...`
Expected: 全部 exit 0（编译通过，既有测试不回归）。

- [ ] **Step 4: Smoke-check route registration**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-server && grep -n "consumption-log" internal/numind/router.go`
Expected: 输出注册行（确认已注册，规则 "新增 API 端点必须在 router.go 注册"）。

- [ ] **Step 5: Commit**

```bash
git add internal/numind/controller/v1/credit/consumption_log.go internal/numind/router.go
git commit -m "feat(credit): wire GET /v1/credits/consumption-log endpoint

user_token handler; user_id from auth context only (no client-supplied
id → no cross-user leakage). Returns {list,total}."
```

> 行为级验证（实际 HTTP + 越权）放 S5 Playwright E2E（见 Task 7）。后端到此 API 契约打通、可编译、既有测试无回归。

---

## Task 4: 前端 API + Pinia store

**Files:**
- Modify: `src/api/credits.ts`
- Create: `src/stores/consumptionLog.ts`

> 工作目录：`/private/tmp/wt-credit-consumption-log-numind-web-v3`。

- [ ] **Step 1: Add API types + function**

`src/api/credits.ts` 末尾追加（沿用文件顶部已 import 的 `request`）：
```ts
// ── 积分消耗记录（credit-consumption-log）──────────────────────────
export interface ConsumptionLogItem {
  id: number
  action: string // 机读 operation
  action_label: string // 中文展示名
  credits: number // 本次消耗积分
  created_at: string // ISO 时间
}

export interface ConsumptionLogResp {
  list: ConsumptionLogItem[]
  total: number
}

/** GET /v1/credits/consumption-log — 当前用户「平账后真实消耗」流水（分页） */
export const getConsumptionLog = (page = 1, pageSize = 20) =>
  request.get<ConsumptionLogResp>('/v1/credits/consumption-log', {
    params: { page, page_size: pageSize }
  })
```

- [ ] **Step 2: Create the store**

创建 `src/stores/consumptionLog.ts`（Pinia setup 语法；响应 envelope 经 request 拦截器后用 `(res as unknown as {data}).data` 取 payload，与 `stores/credits.ts` 一致）：
```ts
import { defineStore } from 'pinia'
import { ref } from 'vue'

import { getConsumptionLog, type ConsumptionLogItem, type ConsumptionLogResp } from '@/api/credits'
import { useNotificationsStore } from '@/stores/notifications'

export const useConsumptionLogStore = defineStore('consumptionLog', () => {
  const records = ref<ConsumptionLogItem[]>([])
  const total = ref(0)
  const page = ref(1)
  const pageSize = ref(20)
  const loading = ref(false)
  const error = ref(false)

  async function fetchPage(targetPage = 1): Promise<void> {
    loading.value = true
    error.value = false
    try {
      const res = await getConsumptionLog(targetPage, pageSize.value)
      const payload = (res as unknown as { data: ConsumptionLogResp }).data
      records.value = payload?.list ?? []
      total.value = payload?.total ?? 0
      page.value = targetPage
    } catch {
      error.value = true
      records.value = []
      useNotificationsStore().error('加载积分消耗记录失败，请重试') // spec §8.4/§8.5 四状态：error toast
    } finally {
      loading.value = false
    }
  }

  function reset(): void {
    records.value = []
    total.value = 0
    page.value = 1
    error.value = false
    loading.value = false
  }

  return { records, total, page, pageSize, loading, error, fetchPage, reset }
})
```

- [ ] **Step 3: Lint + type-check**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-web-v3 && npm run lint && npm run type-check`
Expected: 均 exit 0。

- [ ] **Step 4: Commit**

```bash
git add src/api/credits.ts src/stores/consumptionLog.ts
git commit -m "feat(credits): consumption-log API client + pinia store"
```

---

## Task 5: 前端弹窗组件 `CreditConsumptionLogModal.vue`

**Files:**
- Create: `src/components/credit/CreditConsumptionLogModal.vue`

- [ ] **Step 1: Implement the modal**

创建 `src/components/credit/CreditConsumptionLogModal.vue`（复用 `ConfirmModal.vue` 的 Teleport+Transition+ESC/遮罩关闭模式 + `DataTable.vue` 渲染列表与分页）：
```vue
<template>
  <Teleport to="body">
    <Transition name="overlay-fade">
      <div v-if="open" class="ccl-overlay" @click.self="close">
        <div class="ccl-dialog" role="dialog" aria-modal="true" aria-label="积分消耗记录">
          <header class="ccl-header">
            <h3 class="ccl-title">积分消耗记录</h3>
            <button class="ccl-close" aria-label="关闭" @click="close">×</button>
          </header>

          <div class="ccl-body">
            <p v-if="store.error" class="ccl-error">
              加载失败，<button class="ccl-retry" @click="store.fetchPage(store.page)">重试</button>
            </p>
            <DataTable
              :columns="columns"
              :data="rows"
              :loading="store.loading"
              :total="store.total"
              :page="store.page"
              :page-size="store.pageSize"
              empty-text="暂无积分消耗记录"
              @update:page="store.fetchPage"
            />
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, watch } from 'vue'

import DataTable from '@/components/common/DataTable.vue'
import type { ConsumptionLogItem } from '@/api/credits'
import { useConsumptionLogStore } from '@/stores/consumptionLog'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()

const store = useConsumptionLogStore()

const columns = [
  { key: 'created_at', title: '时间', width: '180px', align: 'left' as const },
  { key: 'action_label', title: '动作', align: 'left' as const },
  { key: 'credits', title: '消耗积分', width: '110px', align: 'right' as const }
]

// 渲染用行：时间格式化为 YYYY-MM-DD HH:mm
const rows = computed(() =>
  store.records.map((r: ConsumptionLogItem) => ({
    ...r,
    created_at: formatTime(r.created_at)
  }))
)

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function close(): void {
  emit('update:open', false)
}

function onKey(e: KeyboardEvent): void {
  if (e.key === 'Escape' && props.open) close()
}

watch(
  () => props.open,
  (isOpen) => {
    if (isOpen) {
      store.fetchPage(1)
      document.addEventListener('keydown', onKey)
    } else {
      document.removeEventListener('keydown', onKey)
    }
  }
)

onBeforeUnmount(() => document.removeEventListener('keydown', onKey))
</script>

<style scoped>
.ccl-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  padding: 24px;
}
.ccl-dialog {
  width: 100%;
  max-width: 640px;
  max-height: 80vh;
  display: flex;
  flex-direction: column;
  background: var(--color-surface, #fff);
  border-radius: 12px;
  box-shadow: 0 24px 64px rgba(0, 0, 0, 0.2);
  overflow: hidden;
}
.ccl-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px;
  border-bottom: 1px solid var(--color-border, #eee);
}
.ccl-title {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
}
.ccl-close {
  border: none;
  background: none;
  font-size: 22px;
  line-height: 1;
  cursor: pointer;
  color: var(--color-text-secondary, #888);
}
.ccl-body {
  padding: 16px 20px;
  overflow: auto;
}
.ccl-error {
  margin: 0 0 12px;
  color: var(--color-danger, #d33);
  font-size: 13px;
}
.ccl-retry {
  border: none;
  background: none;
  color: var(--color-primary, #2563eb);
  cursor: pointer;
  text-decoration: underline;
}
.overlay-fade-enter-active,
.overlay-fade-leave-active {
  transition: opacity 0.2s ease;
}
.overlay-fade-enter-from,
.overlay-fade-leave-to {
  opacity: 0;
}
</style>
```
> **实现者注意**：`DataTable` 的 columns/props/emits 以仓库内 `src/components/common/DataTable.vue` 的实际定义为准（align 取值、`update:page` 事件名、`empty-text` prop 名）。上面按调研到的接口书写；若该组件 prop 名有出入，以组件源码为准对齐，勿改 DataTable。

- [ ] **Step 2: Lint + type-check**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-web-v3 && npm run lint && npm run type-check`
Expected: 均 exit 0。

- [ ] **Step 3: Commit**

```bash
git add src/components/credit/CreditConsumptionLogModal.vue
git commit -m "feat(credits): CreditConsumptionLogModal (Teleport modal + DataTable)"
```

---

## Task 6: 前端设置页入口 + 挂载弹窗

**Files:**
- Modify: `src/views/SettingsView.vue`

- [ ] **Step 1: 改「积分与加量包」section 头为左标题 + 右入口**

把 `src/views/SettingsView.vue` 现有片段（约 13-19 行）：
```html
      <div class="settings-section">
        <div class="section-label">积分与加量包</div>
        <div class="credit-grid">
          <CreditBalanceCard />
          <BoosterPurchaseCard @purchase="handleBoosterPurchase" />
        </div>
      </div>
```
替换为：
```html
      <div class="settings-section">
        <div class="section-header">
          <div class="section-label">积分与加量包</div>
          <button type="button" class="section-action" @click="logOpen = true">
            积分消耗记录
            <svg
              width="14" height="14" viewBox="0 0 24 24" fill="none"
              stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"
            >
              <polyline points="9 18 15 12 9 6" />
            </svg>
          </button>
        </div>
        <div class="credit-grid">
          <CreditBalanceCard />
          <BoosterPurchaseCard @purchase="handleBoosterPurchase" />
        </div>
      </div>
```

- [ ] **Step 2: 挂载弹窗组件**

在模板里 `BoosterPurchaseDialog`（约 117 行）旁边再加一个弹窗挂载：
```html
    <CreditConsumptionLogModal v-model:open="logOpen" />
```

- [ ] **Step 3: script 里 import + ref**

`<script setup>` 内：
- 加 import：`import CreditConsumptionLogModal from '@/components/credit/CreditConsumptionLogModal.vue'`
- 确认 `ref` 已从 vue import（若没有则补到现有 `import { ... } from 'vue'`）
- 加状态：`const logOpen = ref(false)`

- [ ] **Step 4: 加 section-header / section-action 样式**

`<style scoped>` 内追加（与现有 `.section-label` 风格一致；颜色用现有 token）：
```css
.section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.section-action {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  border: none;
  background: none;
  padding: 0;
  font-size: 13px;
  color: var(--color-primary, #2563eb);
  cursor: pointer;
}
.section-action:hover {
  opacity: 0.8;
}
```

- [ ] **Step 5: Lint + type-check**

Run: `cd /private/tmp/wt-credit-consumption-log-numind-web-v3 && npm run lint && npm run type-check`
Expected: 均 exit 0。

- [ ] **Step 6: Commit**

```bash
git add src/views/SettingsView.vue
git commit -m "feat(settings): add 积分消耗记录 entry to credits section header"
```

---

## Task 7: S5 验证策略（独立 task — NDF 规则 10）

> 本 task 不产代码，定义 S5 自动验收怎么做、为什么这么选、要验哪些用户路径。由 S3 gate 的独立 reviewer 一并审查。

**验证方式：Playwright E2E（持久化回归）+ 后端 TDD（已在 Task 1/2 内）。**

**理由：** 本功能属**计费高风险域**（积分消耗展示，数字必须真实可信、越权绝不可发生）。NDF 规则 10 + ui-ux 要求：高风险计费/权限功能选 gstack `/qa` 这种一次性验证不够，需 Playwright E2E 留**持久回归保护**。后端「数字真实/越权隔离」已由 Task 1（store 过滤）+ Task 2（对账一致性 + 映射）的 Go 单测覆盖；前端走 E2E 补端到端 + 视觉。

**S5 关键用户路径（E2E + 必要时 gstack /qa 截图）：**
1. 登录（`$E2E_USERNAME`/`$E2E_PASSWORD`）→ 进设置页 `/settings`。
2. 「积分与加量包」section 头**右侧**可见「积分消耗记录」入口。
3. 点击 → 弹窗出现，标题「积分消耗记录」。
4. 列表渲染：每行 时间 / 动作（中文名）/ 消耗积分；按时间倒序。
5. 翻页（若 total > pageSize）→ 第 2 页数据刷新。
6. 空状态（用换一个无消耗记录的账号或后端造空）→ 「暂无积分消耗记录」文案，不报错。
7. ESC / 点遮罩 → 弹窗关闭。
8. （越权）后端单测已覆盖；E2E 额外确认接口只回当前登录用户数据。

**E2E 文件：** `numind-web-v3/e2e/credit-consumption-log.spec.ts`（新增，永久留库回归）。

**S5 还需重跑：** `task lint` + `task test`（后端完整版含 race）+ `npm run lint` + `npm run type-check`（前端）。

- [ ] **Step 1:** 本 task 无代码改动；S3 后此策略写入 manifest 并由 S5 执行。S3 阶段无需 commit（策略随 plan 文件入库）。

---

## Self-Review（plan 自查，已执行）

**1. Spec 覆盖：** spec §2 数据源/取值 → Task 1+2；§3 API 契约 → Task 2(DTO)+Task 3(route/handler)；§4 分层 → Task 1/2/3；§5 映射表 → Task 2；§6 边界（reserved/refunded/expired/0成本/未知 op/分页归一化）→ Task 1+2 测试；§7 越权 → Task 3 handler（user_id from token）+ Task 1 store 过滤 + Task 2 ledger test；§8 前端 → Task 4/5/6；§9 测试计划 → Task 1/2 单测 + Task 7 E2E。无遗漏。

**2. 占位符扫描：** 无 TBD/TODO。Task 2 Step 6 的 `seedUserWithCredits` 给了"照搬既有 reconcile happy-path seed"的明确复用指引（非占位）。Task 5/6 对 DataTable/SettingsView 既有定义的"以源码为准对齐"是防漂移说明，非占位。

**3. 类型一致性：** `ListReconciledReservationsByUser(ctx, userID uint, offset, limit int) ([]model.CreditReservation, int64, error)` 在 Task 1 接口/实现/测试一致；`ConsumptionLogItem{ID,Action,ActionLabel,Credits,CreatedAt}` 在 Task 2 定义、Task 3 经 gin.H 透出、Task 4 前端 interface 字段名（id/action/action_label/credits/created_at）逐一对齐；`ListConsumptionLog(ctx, userID uint, page, pageSize int)` 在 biz/contract/controller 一致；前端 store `fetchPage` 在 store 与 modal 调用一致。

**4. 顺序/依赖：** 后端（1→2→3）先于前端（4→5→6），无环；前端 6 依赖 5 依赖 4；7 为独立策略。符合 NDF 多仓库"后端先于前端" + task 依赖无环。
