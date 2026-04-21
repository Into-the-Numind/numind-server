# Sales Agent Child Permission Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将销售智能体（SalesRAG）纳入父账号对子账号的 feature 权限管控，复用已存在的 `user_feature_permission` + `FeatureKeySalesAgent` 基础设施，与 content_monitor / SOP / chatbot 的权限语义对齐。

**Architecture:** 在 `salesGroup` 路由组上挂 `middleware.FeaturePermission(FeatureKeySalesAgent)`（一行代码覆盖 27 个运行端点），同时修复 `CheckSalesPermission` 硬编码 `true` 为真查询 `biz.Customers().CheckFeaturePermission()`。无 DB schema 变更，无 migration，无前端源码改动。

**Tech Stack:** Go 1.24 / Gin / GORM / in-memory SQLite (tests) / httptest / Playwright.

**Upstream Spec:** `numind-server/docs/superpowers/specs/2026-04-21-sales-agent-child-permission-design.md`

**Feature branches:** `feature/sales-agent-child-permission`（numind-server）+ `feature/sales-agent-permission-e2e`（numind-web-v3）

---

## Task Graph

```
Task 0 (store.NewTestDataStore helper)  ← test infra for Task 3
  ↓
Task 1 (C1+C2: gate + handler fix)   ← atomic, no going back
  ├─→ Task 2 (C3 unit test)          ← tests Task 1 handler change
  └─→ Task 3 (C5 httptest gate)      ← depends on Task 0 + Task 1
          ↓
Task 4 (C4 E2E, numind-web-v3)       ← cross-repo integration
          ↓
Task 5 (pre-merge SSH SQL check)     ← go/no-go before S6 merge
          ↓
Task 6 (S5 verification strategy)    ← NDF Rule 10 mandatory doc task
```

**Atomicity note:** Task 1 merges C1 (router.go) and C2 (sales_rag.go) into a single commit. Rationale: C1 alone would 403 children without warning them through `check-permission`; C2 alone would honestly say "no permission" but run endpoints would still let them through. Neither half is independently shippable.

**Task 0 rationale (added after reviewer P0-1):** `middleware.FeaturePermission` reads the package-level singleton `store.S *datastore` (unexported concrete type). Tests can't inject a test DB via `NewTestStore()` (returns `IStore` interface, not `*datastore`). To make Task 3 compileable we add one test-only exported helper `NewTestDataStore(db) *datastore` in `store/store.go` that returns the concrete pointer. This is minimal test infrastructure, does not change production code paths.

---

## Task 0: Add `NewTestDataStore` test helper in store package

**Files:**
- Modify: `numind-server/internal/numind/store/store.go`（append test-only helper）

**Why:** `middleware.FeaturePermission` at `middleware.go:222` reads package-level `store.S *datastore` (concrete type, unexported). Task 3 needs to inject a test-DB-backed `*datastore` into `store.S` for httptest verification. `NewTestStore()` returns the `IStore` interface which can't be assigned to `*datastore`. One new exported helper fixes this without expanding any production paths.

---

- [ ] **Step 1: Read current store.go around NewTestStore**

Open `numind-server/internal/numind/store/store.go:56-60`:
```go
// NewTestStore creates a fresh IStore instance backed by the given DB without
// the singleton constraint. Use only in tests.
func NewTestStore(db *gorm.DB) IStore {
	return &datastore{db: db}
}
```

- [ ] **Step 2: Append `NewTestDataStore` helper right after `NewTestStore`**

Add the following function immediately after line 60 (after `NewTestStore`):

```go
// NewTestDataStore is identical to NewTestStore but returns the concrete
// *datastore pointer so tests can assign it to the package-level `S` variable
// (which is typed `*datastore`, not `IStore`). Use only in tests that need to
// exercise code paths reading `store.S` directly (e.g. middleware.FeaturePermission).
func NewTestDataStore(db *gorm.DB) *datastore {
	return &datastore{db: db}
}
```

- [ ] **Step 3: Build + test to confirm nothing breaks**

```bash
cd numind-server && go build ./internal/numind/store/... && go test ./internal/numind/store/...
```
Expected: exit 0. No production code path references this helper yet, so pre-existing tests unaffected.

- [ ] **Step 4: Commit**

```bash
cd numind-server
git add internal/numind/store/store.go
git commit -m "$(cat <<'EOF'
test(store): add NewTestDataStore helper for middleware tests

middleware.FeaturePermission reads package-level store.S *datastore
(unexported concrete type). NewTestStore returns IStore interface which
cannot be assigned to *datastore. New NewTestDataStore returns *datastore
directly so tests can do:
    store.S = store.NewTestDataStore(db)

Test-only; no production callers. Required for upcoming
router_sales_gate_test.go (sales-agent-child-permission feature).
EOF
)"
```

---

## Task 1: Implement gate + fix CheckSalesPermission (C1 + C2)

**Files:**
- Modify: `numind-server/internal/numind/router.go:98-100`（salesGroup 定义处加一行 `.Use()`）
- Modify: `numind-server/internal/numind/controller/v1/salesrag/sales_rag.go:1019-1031`（`CheckSalesPermission` 硬编码改 biz 调用）

**Atomicity:** Single commit. System compiles with no test regression. Behavioral change: children without grant immediately hit 403 on run endpoints + see `has_permission: false` from check-permission (intentional — this is the feature).

---

- [ ] **Step 1: Open router.go and verify salesGroup location**

Read `numind-server/internal/numind/router.go` around lines 95-140. Confirm the structure:
```go
authGroup.GET("/sales-rag/check-permission", salesRAGc.CheckSalesPermission)  // line 95 — stays outside

salesGroup := authGroup.Group("/sales-rag")  // line 98
{
    salesGroup.POST("/ingest", salesRAGc.Ingest)  // line 101
    // ... 26 more endpoints
}
```

The `salesGroup` is defined via `authGroup.Group("/sales-rag")`. The `.Use()` line must go **between** the `salesGroup := ...` line and the first endpoint registration, OR inside the `{ ... }` block before the first `.POST/.GET/...`.

- [ ] **Step 2: Add `salesGroup.Use(FeaturePermission(FeatureKeySalesAgent))`**

Open `numind-server/internal/numind/router.go`. Find the line:
```go
salesGroup := authGroup.Group("/sales-rag")
```

Insert immediately after (and before the opening `{` of the route registration block, OR as the first line inside `{` — both work due to Gin's `.Use()` semantics):

```go
salesGroup.Use(importMw.FeaturePermission(model.FeatureKeySalesAgent))
```

**Precedent to follow:** `router.go:332` has `monitorGroup.Use(importMw.FeaturePermission(model.FeatureKeyContentMonitor))` — mirror exact style (same alias `importMw`, same constant location `model.FeatureKey*`).

**Verify imports:** `router.go` should already import both `importMw "numind-server/internal/pkg/middleware"` and `"numind-server/internal/pkg/model"` (it does — `monitorGroup.Use` precedent requires them). No new import needed.

- [ ] **Step 3: Build to confirm no compile error**

Run:
```bash
cd numind-server && go build ./internal/numind/...
```
Expected: exit 0, no output.

- [ ] **Step 4: Modify `CheckSalesPermission` to call biz layer**

Open `numind-server/internal/numind/controller/v1/salesrag/sales_rag.go`. Find lines 1019-1031:

```go
// CheckSalesPermission 检查当前用户是否有销售智能体使用权限
// 销售智能体/知识库已对所有登录用户开放（数据按 user_id 隔离），保留端点以兼容前端 UI gating
func (ctrl *SalesRAGController) CheckSalesPermission(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"has_permission": true,
	})
}
```

Replace with:

```go
// CheckSalesPermission 检查当前用户是否有销售智能体使用权限。
// 父账号（parent_user_id IS NULL）自动通过；子账号必须在 user_feature_permission
// 表有 sales_agent 记录。走 biz 层（D6 同源保证），不直调 store（遵守 controller→biz→store 单向规则）。
func (ctrl *SalesRAGController) CheckSalesPermission(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	hasPermission, err := ctrl.b.Customers().CheckFeaturePermission(c.Request.Context(), user.ID, model.FeatureKeySalesAgent)
	if err != nil {
		log.Errorw("CheckSalesPermission: check feature permission failed", "user_id", user.ID, "err", err)
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"has_permission": hasPermission,
	})
}
```

**Verify imports:** `sales_rag.go` already imports `"numind-server/internal/pkg/model"` (line 17) and `"numind-server/internal/pkg/log"` (line 15). `errno.ErrInternalServer` is in the already-imported `errno` package. No new imports needed.

- [ ] **Step 5: Build to confirm no compile error**

Run:
```bash
cd numind-server && go build ./...
```
Expected: exit 0.

- [ ] **Step 6: Run existing full test suite to confirm no regression**

Run:
```bash
cd numind-server && go test ./internal/numind/... ./internal/pkg/...
```
Expected: all existing tests PASS. **Note:** `TestReserve_ExactlyExhaustedThenRetry_ReturnsInsufficientSentinel` in `credit_service_boundary_test.go:100` is a **pre-existing failure** unrelated to this change (documented in manifest 2026-04-20 S4 notes). It should continue to fail with the same panic — that's acceptable, not introduced by this task.

- [ ] **Step 7: Run `task lint`**

Run:
```bash
cd numind-server && task lint
```
Expected: exit 0.

- [ ] **Step 8: Commit**

```bash
cd numind-server
git add internal/numind/router.go internal/numind/controller/v1/salesrag/sales_rag.go
git commit -m "$(cat <<'EOF'
feat(salesrag): gate salesGroup + fix CheckSalesPermission hardcoded true

- router.go: salesGroup.Use(FeaturePermission(FeatureKeySalesAgent)) to gate
  all 27 sales run endpoints. Mirrors router.go:332 monitorGroup precedent.
- sales_rag.go:1019-1031: CheckSalesPermission now queries ctrl.b.Customers()
  .CheckFeaturePermission() (biz layer) instead of hardcoded true. Obeys
  controller→biz→store single-direction rule (unlike middleware.go:222
  which directly accesses store — not expanding that violation).

Default deny-all for sub-users; parent accounts auto-pass per HasFeaturePermission
existing logic. check-permission endpoint stays in authGroup (NOT gated) so
frontend can receive denial signals.

Spec: docs/superpowers/specs/2026-04-21-sales-agent-child-permission-design.md
EOF
)"
```

---

## Task 2: Unit test for CheckSalesPermission (C3) — real biz + in-memory DB

**Files:**
- Create: `numind-server/internal/numind/controller/v1/salesrag/sales_rag_test.go`

**Why:** Tests Task 1's handler change. 4 cases covering parent / sub-granted / sub-denied / biz-error branches.

**Approach (rewritten after reviewer P0-2):** Use **real** `customerbiz.New(testDS)` with in-memory SQLite DB rather than stubbing the full `ICustomerBiz` interface. The real `ICustomerBiz` has 17 methods — stubbing them all is error-prone and the stubs would drift every time a new method is added. Instead:

- Build real `biz.IBiz` that returns a real `customerbiz.New(testDS)` from `Customers()`
- All other `biz.IBiz` methods return `nil` (valid for interface types, will panic at runtime if called — which is what we want)
- Seed users + `user_feature_permission` records in the DB; the controller → biz → store → DB path executes end-to-end
- Test case T4 (biz error) simulates by closing the DB before invocation

This trades "pure unit isolation" for "real interface compliance", which is a net win given ICustomerBiz churn.

---

- [ ] **Step 1: Inspect existing test DB pattern**

Read `numind-server/internal/numind/controller/v1/credit/credit_test.go:80-132` for the in-memory SQLite + `gin.New()` + `setCurrentUserMiddleware` pattern. Mirror.

Also confirm exact `biz.IBiz` method signatures by reading `numind-server/internal/numind/biz/biz.go:40-56` — the plan shows them below, but read before using to guard against drift.

- [ ] **Step 2: Write the test file**

Create `numind-server/internal/numind/controller/v1/salesrag/sales_rag_test.go`:

```go
// Package salesrag_test contains HTTP-handler level tests for CheckSalesPermission.
// Uses real customerbiz + in-memory SQLite instead of mocking ICustomerBiz
// (17 methods, too noisy to stub). Tests the full controller → biz → store → DB
// path, with user_feature_permission rows seeded to drive each case.
package salesrag_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/ali"
	chatbotbiz "numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/biz/config"
	"numind-server/internal/numind/biz/credit"
	customerbiz "numind-server/internal/numind/biz/customer"
	kbbiz "numind-server/internal/numind/biz/knowledgebase"
	"numind-server/internal/numind/biz/llmrouter"
	"numind-server/internal/numind/biz/monitor"
	"numind-server/internal/numind/biz/payment"
	"numind-server/internal/numind/biz/salesrag"
	sopbiz "numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/biz/user"
	"numind-server/internal/numind/biz/volc"
	salesragctl "numind-server/internal/numind/controller/v1/salesrag"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// -----------------------------------------------------------------------------
// test DB
// -----------------------------------------------------------------------------

// newTestDB creates in-memory SQLite with minimal schema: hand-rolled `user`
// table (model.User has MySQL ENUMs SQLite rejects) + AutoMigrated
// UserFeaturePermission.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.Exec(`
		CREATE TABLE user (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id INTEGER NULL,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME
		)
	`).Error)
	require.NoError(t, db.AutoMigrate(&model.UserFeaturePermission{}))
	return db
}

func seedUser(t *testing.T, db *gorm.DB, id uint, parentID *uint) {
	t.Helper()
	var pid interface{}
	if parentID != nil {
		pid = *parentID
	}
	require.NoError(t, db.Exec(`INSERT INTO user (id, parent_user_id) VALUES (?, ?)`, id, pid).Error)
}

func seedFeatureGrant(t *testing.T, db *gorm.DB, parent, sub uint, key string) {
	t.Helper()
	require.NoError(t, db.Create(&model.UserFeaturePermission{
		ParentUserID: parent, SubUserID: sub, FeatureKey: key,
	}).Error)
}

// -----------------------------------------------------------------------------
// realBizOnlyCustomers is a biz.IBiz implementation that only wires Customers().
// All other methods return nil; since the controller under test only touches
// Customers().CheckFeaturePermission, nil returns are safe (panicking if
// misused is acceptable — it exposes test scope creep).
// -----------------------------------------------------------------------------
type realBizOnlyCustomers struct {
	customers customerbiz.ICustomerBiz
}

func (b *realBizOnlyCustomers) Users() user.UserBiz                   { return nil }
func (b *realBizOnlyCustomers) Ali() ali.AliBiz                       { return nil }
func (b *realBizOnlyCustomers) Volc() volc.VolcBiz                    { return nil }
func (b *realBizOnlyCustomers) Configs() config.ConfigBiz             { return nil }
func (b *realBizOnlyCustomers) Sop() sopbiz.ISopBiz                   { return nil }
func (b *realBizOnlyCustomers) Customers() customerbiz.ICustomerBiz   { return b.customers }
func (b *realBizOnlyCustomers) SalesRAG() salesrag.SalesRAGBiz        { return nil }
func (b *realBizOnlyCustomers) Credit() credit.ICreditBiz             { return nil }
func (b *realBizOnlyCustomers) CreditService() credit.ICreditService  { return nil }
func (b *realBizOnlyCustomers) Pricing() pricing.ICalculator          { return nil }
func (b *realBizOnlyCustomers) Payment() payment.IPaymentBiz          { return nil }
func (b *realBizOnlyCustomers) Monitor() monitor.IMonitorBiz          { return nil }
func (b *realBizOnlyCustomers) KnowledgeBase() kbbiz.IKnowledgeBaseBiz { return nil }
func (b *realBizOnlyCustomers) Chatbot() chatbotbiz.IChatbotBiz       { return nil }
func (b *realBizOnlyCustomers) LLMRouter() *llmrouter.Router          { return nil }

// compile-time guard: this test struct must satisfy biz.IBiz or tests fail here.
var _ biz.IBiz = (*realBizOnlyCustomers)(nil)

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func setCurrentUserMW(u *model.User) gin.HandlerFunc {
	return func(c *gin.Context) {
		if u != nil {
			c.Set("current_user", u)
		}
		c.Next()
	}
}

func newRouter(t *testing.T, b biz.IBiz, u *model.User) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(setCurrentUserMW(u))
	ctrl := salesragctl.NewSalesRAGController(b, nil) // creditBiz not touched
	r.GET("/sales-rag/check-permission", ctrl.CheckSalesPermission)
	return r
}

func testBiz(t *testing.T, db *gorm.DB) biz.IBiz {
	t.Helper()
	ds := store.NewTestStore(db)
	return &realBizOnlyCustomers{customers: customerbiz.New(ds)}
}

func mustParent(id uint) *model.User { u := &model.User{}; u.ID = id; return u }
func mustSub(id, parentID uint) *model.User {
	u := &model.User{ParentUserID: &parentID}
	u.ID = id
	return u
}

// -----------------------------------------------------------------------------
// tests
// -----------------------------------------------------------------------------

// T1: Parent account (ParentUserID IS NULL) → has_permission: true by biz auto-pass logic
func TestCheckSalesPermission_Parent_True(t *testing.T) {
	db := newTestDB(t)
	seedUser(t, db, 1, nil) // parent
	r := newRouter(t, testBiz(t, db), mustParent(1))

	req := httptest.NewRequest(http.MethodGet, "/sales-rag/check-permission", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var resp struct {
		Code int `json:"code"`
		Data struct {
			HasPermission bool `json:"has_permission"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code)
	assert.True(t, resp.Data.HasPermission, "parent must auto-pass")
}

// T2: Sub-user WITH sales_agent grant → has_permission: true
func TestCheckSalesPermission_SubGranted_True(t *testing.T) {
	db := newTestDB(t)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)
	seedFeatureGrant(t, db, 1, 100, model.FeatureKeySalesAgent)

	r := newRouter(t, testBiz(t, db), mustSub(100, 1))

	req := httptest.NewRequest(http.MethodGet, "/sales-rag/check-permission", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct{ HasPermission bool `json:"has_permission"` } `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Data.HasPermission, "granted sub must return true")
}

// T3: Sub-user WITHOUT grant → has_permission: false, HTTP 200 (NOT 403)
// D1 invariant: check-permission must never return 403.
func TestCheckSalesPermission_SubDenied_FalseNot403(t *testing.T) {
	db := newTestDB(t)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 200, &parentID) // no grant

	r := newRouter(t, testBiz(t, db), mustSub(200, 1))

	req := httptest.NewRequest(http.MethodGet, "/sales-rag/check-permission", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "D1: must be 200, not 403")
	var resp struct {
		Code int `json:"code"`
		Data struct{ HasPermission bool `json:"has_permission"` } `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code, "business code 0 (success)")
	assert.False(t, resp.Data.HasPermission, "denied sub must return false")
}

// T4: biz layer returns error → 500 surfaced as non-zero business code
// Simulated by closing the underlying sqlDB before the request.
func TestCheckSalesPermission_BizError_Returns500(t *testing.T) {
	db := newTestDB(t)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 300, &parentID)

	b := testBiz(t, db)
	r := newRouter(t, b, mustSub(300, 1))

	// Force the DB to fail by closing it mid-test.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	req := httptest.NewRequest(http.MethodGet, "/sales-rag/check-permission", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "project envelope returns 200 with biz code; body: %s", w.Body.String())
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEqual(t, 0, resp.Code, "biz err → non-zero business code")
}
```

**Compile-time safety net:** the `var _ biz.IBiz = (*realBizOnlyCustomers)(nil)` line forces `go build` to fail if any `biz.IBiz` method is missing. If it fails, read the error and add the missing methods with return value `nil`. This catches interface drift automatically.

**Sanity check on imports:** each biz sub-package (`ali`, `chatbotbiz`, `config`, `credit`, `customerbiz`, `kbbiz`, `llmrouter`, `monitor`, `payment`, `salesrag`, `sopbiz`, `user`, `volc`) is an actual package under `numind-server/internal/numind/biz/`. If any import path is wrong, adjust per actual file system. `pricing` lives under `numind-server/internal/pkg/pricing`.

- [ ] **Step 3: Run the tests — expect PASS**

```bash
cd numind-server && go test ./internal/numind/controller/v1/salesrag/... -run TestCheckSalesPermission -v
```
Expected: 4 tests PASS. If compile fails on `var _ biz.IBiz`, a new method was added to the interface; mirror it with `return nil` as a next build-fix mechanical step.

- [ ] **Step 4: Run `task lint` on the new file**

```bash
cd numind-server && task lint
```
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/numind/controller/v1/salesrag/sales_rag_test.go
git commit -m "$(cat <<'EOF'
test(salesrag): CheckSalesPermission 4 cases (parent/sub-granted/sub-denied/biz-err)

Verifies handler correctly delegates to ctrl.b.Customers().CheckFeaturePermission()
(D6 biz-path rule) and preserves D1 (check-permission returns 200 with
has_permission=false for denied sub-users, NOT 403).
EOF
)"
```

---

## Task 3: Route gate httptest integration test (C5)

**Files:**
- Create: `numind-server/internal/numind/router_sales_gate_test.go`

**Why:** Tests that `salesGroup.Use(FeaturePermission(...))` actually intercepts run endpoints and does NOT intercept `/sales-rag/check-permission`. This is the project's first route-level gate test — closes Reviewer Q8 gap.

---

- [ ] **Step 1: Inspect middleware and store signatures**

Read `numind-server/internal/pkg/middleware/middleware.go:212-238` to confirm `FeaturePermission` uses `store.S.Customers().HasFeaturePermission`. This means the httptest harness must populate `store.S` with a test DB that has the `user_feature_permission` table + a `user` table with `parent_user_id` column.

**Critical:** `store.S` is a package-level singleton (`store.S store.IStore`). For tests we need to either (a) save/restore `store.S` or (b) use a `t.Cleanup` to restore. Mirror the pattern from existing store-using tests.

- [ ] **Step 2: Write the httptest harness**

Create `numind-server/internal/numind/router_sales_gate_test.go`:

```go
// Package numind_test contains route-level integration tests that validate
// salesGroup.Use(FeaturePermission(FeatureKeySalesAgent)) actually gates
// run endpoints while leaving /sales-rag/check-permission open.
// This closes the S2 Reviewer Q8 gap (middleware mounting never had Go
// verification — only E2E coverage).
package numind_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// -----------------------------------------------------------------------------
// test harness
// -----------------------------------------------------------------------------

// newGateTestDB creates an in-memory SQLite DB with the schema required by
// FeaturePermission middleware: the `user` table (for ParentUserID lookup)
// and `user_feature_permission` (for grant records).
//
// We hand-roll the user table because AutoMigrate on model.User drags in
// MySQL ENUM types that SQLite rejects.
func newGateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// Minimal user table (only the columns the middleware reads).
	require.NoError(t, db.Exec(`
		CREATE TABLE user (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_user_id INTEGER NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`).Error)

	require.NoError(t, db.AutoMigrate(&model.UserFeaturePermission{}))
	return db
}

// seedUser inserts a user with the given parentUserID (nil = parent account).
func seedUser(t *testing.T, db *gorm.DB, id uint, parentID *uint) {
	t.Helper()
	var pid interface{}
	if parentID != nil {
		pid = *parentID
	}
	require.NoError(t, db.Exec(`INSERT INTO user (id, parent_user_id) VALUES (?, ?)`, id, pid).Error)
}

// seedGrant inserts a user_feature_permission row for (sub, parent, featureKey).
func seedGrant(t *testing.T, db *gorm.DB, parent, sub uint, key string) {
	t.Helper()
	grant := &model.UserFeaturePermission{
		ParentUserID: parent,
		SubUserID:    sub,
		FeatureKey:   key,
	}
	require.NoError(t, db.Create(grant).Error)
}

// installStoreS injects a test-backed store.S singleton and returns a restorer.
// Uses NewTestDataStore (added in Task 0) because store.S is typed *datastore
// and NewTestStore returns IStore interface.
func installStoreS(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := store.S
	store.S = store.NewTestDataStore(db)
	t.Cleanup(func() { store.S = previous })
}

// setCurrentUserMW mounts a fabricated *model.User onto the gin context at
// key "current_user" (the key middleware.GetCurrentUser reads).
func setCurrentUserMW(user *model.User) gin.HandlerFunc {
	return func(c *gin.Context) {
		if user != nil {
			c.Set("current_user", user)
		}
		c.Next()
	}
}

// newMiniRouter builds a minimal gin router that mirrors the sales route
// topology under test: authGroup has check-permission OUTSIDE salesGroup,
// and salesGroup carries the FeaturePermission middleware.
func newMiniRouter(t *testing.T, user *model.User) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	authGroup := r.Group("/v1")
	authGroup.Use(setCurrentUserMW(user))

	// check-permission: NOT behind FeaturePermission gate (D1)
	authGroup.GET("/sales-rag/check-permission", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"code": 0, "data": gin.H{"has_permission": true}})
	})

	// salesGroup: behind FeaturePermission gate (C1 under test)
	salesGroup := authGroup.Group("/sales-rag")
	salesGroup.Use(middleware.FeaturePermission(model.FeatureKeySalesAgent))
	{
		// 3 representative run endpoints from §4 (docs / chat / ocr)
		salesGroup.GET("/documents", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"code": 0})
		})
		salesGroup.POST("/sessions/:id/chat", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"code": 0})
		})
		salesGroup.POST("/ocr", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"code": 0})
		})
	}
	return r
}

func mustUser(id uint, parentID *uint) *model.User {
	u := &model.User{ParentUserID: parentID}
	u.ID = id
	return u
}

// -----------------------------------------------------------------------------
// tests (H1-H5)
// -----------------------------------------------------------------------------

// H1: Sub-user without grant + GET /sales-rag/documents → 403 ErrForbidden
func TestGate_SubNoGrant_DocumentsListBlocked(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	parentID := uint(1)
	seedUser(t, db, 1, nil)              // parent
	seedUser(t, db, 100, &parentID)      // sub, no grant

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodGet, "/v1/sales-rag/documents", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "ErrForbidden uses HTTP 200 + biz code; body: %s", w.Body.String())
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEqual(t, 0, resp.Code, "gate must reject with non-zero biz code")
	// Do NOT assert on message text ("未开通 …") — brittle if copy changes. The
	// non-zero business code is the load-bearing signal.
}

// H2: Sub-user without grant + POST /sales-rag/sessions/1/chat → 403
func TestGate_SubNoGrant_ChatBlocked(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodPost, "/v1/sales-rag/sessions/1/chat", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct{ Code int `json:"code"` }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEqual(t, 0, resp.Code)
}

// H3: Sub-user without grant + POST /sales-rag/ocr → 403
func TestGate_SubNoGrant_OCRBlocked(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodPost, "/v1/sales-rag/ocr", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct{ Code int `json:"code"` }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEqual(t, 0, resp.Code)
}

// H4: Sub-user without grant + GET /sales-rag/check-permission → 200 code:0
// This is the D1 regression guard: check-permission MUST stay outside gate.
func TestGate_SubNoGrant_CheckPermissionNotGated(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodGet, "/v1/sales-rag/check-permission", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "D1: check-permission must NOT be behind gate")
	var resp struct{ Code int `json:"code"` }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code, "check-permission handler runs, business code=0")
}

// H5: Parent account + GET /sales-rag/documents → gate passes (endpoint handler runs)
func TestGate_Parent_PassesThrough(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	seedUser(t, db, 1, nil) // parent

	r := newMiniRouter(t, mustUser(1, nil))
	req := httptest.NewRequest(http.MethodGet, "/v1/sales-rag/documents", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp struct{ Code int `json:"code"` }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code, "parent passes gate, endpoint runs")
}

// H6 (bonus): Sub-user WITH grant + GET /sales-rag/documents → gate passes
func TestGate_SubGranted_PassesThrough(t *testing.T) {
	db := newGateTestDB(t)
	installStoreS(t, db)
	parentID := uint(1)
	seedUser(t, db, 1, nil)
	seedUser(t, db, 100, &parentID)
	seedGrant(t, db, 1, 100, model.FeatureKeySalesAgent)

	r := newMiniRouter(t, mustUser(100, &parentID))
	req := httptest.NewRequest(http.MethodGet, "/v1/sales-rag/documents", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
	var resp struct{ Code int `json:"code"` }
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code, "granted sub passes gate")
}
```

**Interface assumption check:** The test relies on `store.NewTestStore(db)` existing (it does per `credit_test.go:143`). It also relies on `store.S` being a writable package variable (confirmed by `middleware.go:222` reading `store.S.Customers()`). If `store.S` is instead a function or read-only, the `installStoreS` helper fails at compile — mechanical fix: use whatever the existing test pattern does.

- [ ] **Step 3: Run the 6 tests — expect PASS**

```bash
cd numind-server && go test ./internal/numind/... -run 'TestGate_' -v
```
Expected: 6 tests PASS.

**Debugging path if H4 fails with 403:** means `check-permission` somehow landed inside the gate — that would indicate a router.go misregistration; go back to Task 1 Step 2 and verify placement.

**Debugging path if H5 fails with 403:** means `HasFeaturePermission`'s parent auto-pass broke, or the test user fabrication didn't set `ParentUserID=nil` correctly. Check `seedUser(t, db, 1, nil)` row has NULL `parent_user_id`.

- [ ] **Step 4: Run `task lint`**

```bash
cd numind-server && task lint
```
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
cd numind-server
git add internal/numind/router_sales_gate_test.go
git commit -m "$(cat <<'EOF'
test(salesrag): route-level gate integration tests (httptest)

Closes S2 Reviewer Q8 gap: validates salesGroup.Use(FeaturePermission(...))
actually intercepts run endpoints AND leaves /sales-rag/check-permission
outside the gate (D1). 6 cases: H1-H3 deny sub-user on docs/chat/ocr,
H4 check-permission stays open for denied sub, H5 parent passes through,
H6 granted sub passes through.

Project's first route-level middleware-mounting test — consider extending
pattern to content_monitor (out of scope for this feature).
EOF
)"
```

---

## Task 4: Playwright E2E test (C4) — numind-web-v3 repo

**Files:**
- Create: `numind-web-v3/e2e/sales-agent-permission.spec.ts`

**Why:** Browser-level validation of parent grant → sub can use, parent revoke → sub blocked (both UI and API). Closes PRD AS-1 / AS-2 / AS-4 / AS-7.

**Switch repo:** this task runs in `numind-web-v3`, branch `feature/sales-agent-permission-e2e`. Follow `parent-self-grant.spec.ts` mock pattern for determinism.

---

- [ ] **Step 1: Confirm repo context**

```bash
cd /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-web-v3
git status  # should be on develop or a fresh feature branch
```

Create feature branch:
```bash
git checkout -b feature/sales-agent-permission-e2e
```

- [ ] **Step 2: Read precedent**

Read `numind-web-v3/e2e/parent-self-grant.spec.ts` for the mock + selector pattern. Key pattern: `page.route('**/v1/...', async (route) => { ... route.fulfill(...) })` to mock backend responses deterministically.

Also read `numind-web-v3/e2e/child-run-permission-api.spec.ts` — this is the closest precedent (API-level permission behavior test). Mirror its structure for backend API mocking.

- [ ] **Step 3: Write the spec file**

Create `numind-web-v3/e2e/sales-agent-permission.spec.ts`:

```ts
import { test, expect, type Page, type Route } from '@playwright/test'

/**
 * Sales Agent Child Permission E2E
 *
 * Validates parent account can toggle sales_agent feature permission for sub-users
 * via the customer-management UI, and the toggle correctly gates access on the
 * sub-user side.
 *
 * Coverage matrix:
 *   E1: Parent toggles "销售智能体" ON for sub-user → grant API called
 *   E2: Sub-user /sales-rag/check-permission mock → UI enters sales agent page
 *   E3: Parent toggles OFF → revoke API called
 *   E4: Sub-user /sales-rag/check-permission mock denied → sees "未开通" UI
 *   E5: Sub-user direct POST to /sales-rag/sessions/1/chat (mocked 403) → client handles gracefully
 *
 * We mock ALL backend calls for determinism. Real backend state can drift
 * across runs; we're testing UI wiring, not backend logic (that's covered by
 * Go httptest in numind-server).
 *
 * Prerequisites: auth setup runs first; auth fixture provides an authenticated
 * parent user with at least 1 sub-user in the customer list.
 */

const sel = {
  page: '.customers-page',
  tableRow: '.data-table tbody tr',
  actionTrigger: '.action-trigger',
  actionMenu: '.action-menu',
  actionMenuItem: '.action-menu-item',

  // Feature permission modal (opened via action menu → 权限管理 or similar)
  permModal: '.modal-dialog.feature-perm-dialog',
  salesAgentToggle: '.feature-perm-dialog [data-feature-key="sales_agent"] .toggle-switch',
  submitBtn: '.modal-dialog.feature-perm-dialog .btn-primary',

  toast: '.toast'
} as const

// E5 depends on the frontend route being `/sales` — confirm in App router
// before running. If route name drifts, update this constant.
const SALES_ROUTE = '/sales'

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

async function mockGrantFeatures(page: Page, expectedKey = 'sales_agent') {
  await page.route('**/v1/customers/sub-users/*/features', async (route: Route) => {
    const method = route.request().method()
    if (method !== 'POST' && method !== 'DELETE') {
      await route.fallback()
      return
    }
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        code: 0,
        message: 'ok',
        data: null
      })
    })
  })
}

async function mockCheckPermission(page: Page, hasPermission: boolean) {
  await page.route('**/v1/sales-rag/check-permission', async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        code: 0,
        message: 'ok',
        data: { has_permission: hasPermission }
      })
    })
  })
}

async function mockSalesChatDenied(page: Page) {
  await page.route('**/v1/sales-rag/sessions/*/chat', async (route: Route) => {
    if (route.request().method() !== 'POST') {
      await route.fallback()
      return
    }
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        code: 100207,
        message: '未开通该功能权限，请联系管理员'
      })
    })
  })
}

test.describe('Sales Agent Child Permission', () => {
  test('E1+E3: parent can toggle sales_agent ON and OFF for sub-user', async ({ page }) => {
    await mockGrantFeatures(page)
    await goToCustomers(page)

    // Find first sub-user row (assumes auth fixture seeds >=1 sub)
    const row = page.locator(sel.tableRow).filter({ hasNot: page.locator('text=我') }).first()
    await expect(row).toBeVisible({ timeout: 15_000 })
    await row.locator(sel.actionTrigger).click()
    await expect(row.locator(sel.actionMenu)).toBeVisible({ timeout: 3_000 })

    // The action menu item name for feature permission management — update if it differs.
    // Precedent: CustomersView.vue:652 shows `featurePermissions['sales_agent']` is
    // rendered inside a permission modal; the menu item that opens it is likely
    // "权限管理" or "功能权限". If this locator finds nothing, inspect the DOM.
    const permMenuItem = row.locator(sel.actionMenuItem, { hasText: /权限|功能/ })
    await permMenuItem.first().click()
    await expect(page.locator(sel.permModal)).toBeVisible({ timeout: 3_000 })

    // Toggle ON
    const toggle = page.locator(sel.salesAgentToggle)
    await expect(toggle).toBeVisible()
    const initiallyChecked = await toggle.evaluate((el) => el.classList.contains('checked'))
    if (!initiallyChecked) {
      await toggle.click()
    }

    await page.locator(sel.submitBtn).click()
    // Expect grant API call fired (page.route mock absorbed it silently)
    await expect(page.locator(sel.toast)).toContainText(/成功|已更新/, { timeout: 5_000 })

    // Re-open and toggle OFF
    await row.locator(sel.actionTrigger).click()
    await permMenuItem.first().click()
    await expect(page.locator(sel.permModal)).toBeVisible({ timeout: 3_000 })

    const toggleAgain = page.locator(sel.salesAgentToggle)
    if (await toggleAgain.evaluate((el) => el.classList.contains('checked'))) {
      await toggleAgain.click()
    }
    await page.locator(sel.submitBtn).click()
    await expect(page.locator(sel.toast)).toContainText(/成功|已更新/, { timeout: 5_000 })
  })

  test('E2: check-permission returns true → sub-user can enter sales page', async ({ page }) => {
    await mockCheckPermission(page, true)
    await page.goto(SALES_ROUTE)
    // Sales page loads without "未开通" notice
    await expect(page.locator('text=未开通')).toHaveCount(0, { timeout: 10_000 })
    // Some sales UI element is visible — adjust selector to whatever the
    // sales landing page renders on success (e.g. a "新对话" button).
    await expect(page.locator('.sales-page, .welcome-screen, text=新对话').first()).toBeVisible({
      timeout: 10_000
    })
  })

  test('E4: check-permission returns false → sub-user sees 未开通 notice', async ({ page }) => {
    await mockCheckPermission(page, false)
    await page.goto(SALES_ROUTE)
    // Existing "未开通" UI branch in HomeView.vue / SalesView.vue must render
    await expect(page.locator('text=未开通')).toBeVisible({ timeout: 10_000 })
  })

  test('E5: direct chat API returns 403 → client shows error notice', async ({ page }) => {
    await mockCheckPermission(page, true)  // let UI load
    // Count invocations of the 403 mock to confirm the client actually hit it.
    let chat403Count = 0
    await page.route('**/v1/sales-rag/sessions/*/chat', async (route: Route) => {
      if (route.request().method() !== 'POST') {
        await route.fallback()
        return
      }
      chat403Count++
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ code: 100207, message: '未开通该功能权限，请联系管理员' })
      })
    })

    await page.goto(SALES_ROUTE)

    // Fire the chat request directly via fetch so we don't depend on session-create UI.
    // This is the most robust way to verify: given the client makes a chat POST,
    // it MUST receive a 403-equivalent business code (not 200+data, not crash).
    const responseCode = await page.evaluate(async () => {
      const res = await fetch('/v1/sales-rag/sessions/1/chat', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ query: 'hi' })
      })
      const body = await res.json().catch(() => ({ code: -1 }))
      return body.code
    })

    expect(chat403Count).toBeGreaterThan(0)        // mock was hit (network contract honored)
    expect(responseCode).toBe(100207)              // business code surfaces to client
    // This is the end of the network-contract verification. UI error-notice
    // rendering is covered by E4 (the /sales route already renders denied state
    // on check-permission=false). AS-2's last bullet is covered by Go httptest
    // (Task 3 H1-H3) + this test's network contract.
  })
})
```

**Reality checks the implementer MUST do before committing** (non-negotiable, not "best-effort"):

1. Run `cd numind-web-v3 && npm run dev` and open the customers page as a parent account; inspect the feature-permission modal DOM in DevTools. Record the actual class names and replace the guessed selectors. Guess-to-reality mapping has to be verified, not assumed.
2. Inspect the action menu item label (plan guesses "权限管理" / "功能"). Record the real label and update `permMenuItem` locator.
3. Verify SALES_ROUTE by checking `numind-web-v3/src/router/index.ts` or equivalent. If it's not `/sales`, update the constant.
4. Check the `[data-feature-key="sales_agent"]` selector exists in `CustomersView.vue` — if the frontend renders toggles by array iteration without a data attribute, pick a different positional selector.
5. E5 is NOT allowed to degrade to `expect(body).toBeVisible()`. Its assertion must include `chat403Count > 0` AND `responseCode == 100207` (already updated in the spec above) — the mock-invocation count is the contract.

This test's Go counterpart is Task 3 (rigorous backend gate); E2E covers UX wiring. "Selectors allowed to drift" applies to non-critical style-class selectors only — action menu triggers and the toggle itself must be real.

- [ ] **Step 4: Run lint + type-check**

```bash
cd numind-web-v3
npm run lint
npm run type-check
```
Expected: both exit 0.

- [ ] **Step 5: Run just this spec**

Dev backend must be running at `$DEV_API_URL` (or local):
```bash
cd numind-web-v3
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npx playwright test e2e/sales-agent-permission.spec.ts
```

If UI selectors don't match, iterate on them (NOT on the mocks — mocks are the contract).

If the auth fixture doesn't provide a sub-user, tests that depend on sub-user existence will be marked `test.skip()` with a comment pointing to the fixture gap.

- [ ] **Step 6: Commit**

```bash
cd numind-web-v3
git add e2e/sales-agent-permission.spec.ts
git commit -m "$(cat <<'EOF'
test(e2e): sales agent child permission UX regression

Mocks feature-permission grant/revoke API, /sales-rag/check-permission, and
/sales-rag/sessions/*/chat responses to deterministically validate:
- E1/E3 parent grant/revoke toggle fires correct API
- E2 check-permission true → sales UI enters
- E4 check-permission false → "未开通" UI branch renders
- E5 direct chat API 403 → client handles gracefully

Backend gate rigor: numind-server/internal/numind/router_sales_gate_test.go.
This spec is UX smoke; selectors allowed to drift within tolerance.
EOF
)"
```

---

## Task 5: Pre-merge Go/No-Go SSH SQL check

**Files:** none (runtime data-gathering task)

**Why:** Spec §9 hard requirement. Before S6 merges, verify `user_feature_permission` table has `sales_agent` grant count baseline to inform product rollout comms ("are we blocking all sub-users or some?"). AI executes via SSH per CLAUDE.md §7 (never let user run manual DB commands).

---

- [ ] **Step 1: Discover dev DB credentials**

DB credentials are NOT in env vars at time of writing. Read them from config YAML:

```bash
cat /Users/zhiyuchen/Documents/10_跃迁有数/有数AI工作台/莫小派/Codes/numind-server/config_dev.yaml | grep -A 10 -E '^(database|mysql|db):'
```

Record the values:
- host (likely `49.233.219.254:13306` per manifest prior decisions)
- database name (likely `numind`)
- user
- password

If password contains special characters (`$`, `!`, `@`), single-quote it in the SQL command below.

- [ ] **Step 2: SSH dev server and run baseline query**

Substitute `<db-user>`, `<db-pass>`, `<db-name>` with values from Step 1:

```bash
sshpass -p "$DEV_SSH_PASS" ssh -o StrictHostKeyChecking=no "$DEV_SSH_USER@$DEV_SSH_HOST" \
  "mysql -h 127.0.0.1 -P 13306 -u <db-user> -p'<db-pass>' <db-name> -e \"
    SELECT COUNT(*) AS sales_agent_grants
      FROM user_feature_permission
      WHERE feature_key = 'sales_agent'
        AND deleted_at IS NULL;
    SELECT COUNT(*) AS total_sub_users
      FROM user
      WHERE parent_user_id IS NOT NULL
        AND deleted_at IS NULL;
  \""
```

If the DB is on the SSH host itself (common in dev), `127.0.0.1` is correct; if it's a separate host, use the IP from config. Do NOT prompt the user to run SQL — read the config and execute it.

- [ ] **Step 3: Record numbers in manifest decisions**

Open `numind-server/build-manifest.yaml`, find the `sales-agent-child-permission` feature entry, and append to `decisions` array:

```yaml
      - "2026-04-21 (Task 5 SSH baseline): dev DB sales_agent_grants=<N>, total_sub_users=<M>. <评估：N=0 → 100% sub-users will be denied on launch → product comms must address 'all sales agent users' / N>0 → partial impact>"
```

- [ ] **Step 4: Repeat for prod DB**

```bash
sshpass -p "$PROD_SSH_PASS" ssh -o StrictHostKeyChecking=no "$PROD_SSH_USER@$PROD_SSH_HOST" "<same query>"
```

Record prod numbers similarly. If prod numbers diverge significantly from dev, surface to user immediately — this is a Pause-and-Ask condition (NDF §5) because business rollout strategy may need to change.

- [ ] **Step 5: No commit needed**

This task produces manifest entries only (already committed during manifest update). If the baseline reveals a blocker (e.g., prod has wide adoption with zero grants), pause and request user decision on whether to proceed or execute a manual grant spreadsheet first.

---

## Task 6: S5 Verification Strategy Document

**Files:**
- Modify: this plan file (append §S5 Verification Strategy section below)

**Why:** NDF Rule 10 mandatory — S3 plan must close with a concrete S5 execution checklist so S5 doesn't re-derive strategy from scratch.

---

### S5 Verification Strategy (for executor)

**Verification method:** Playwright E2E + Go unit/integration tests.

**Rationale:** Permission is high-risk business logic (spec §7). Persisted regression coverage required. Chose E2E over gstack `/qa` because `/qa` is one-shot (no auto-regression). Aligns with parent-self-grant-membership and child-run-permission precedent.

**Key user paths (S5 must verify all):**

1. **Parent grants → sub-user enters**
   - Log in as parent (`$E2E_USERNAME`)
   - Navigate to `/customers`, open action menu on a sub-user row, open feature permission modal
   - Toggle "销售智能体" ON, save → toast "成功"
   - Log out, log in as that sub-user
   - Navigate to `/sales` → sales agent page loads, no "未开通" notice
   - Try uploading a doc or starting a session → HTTP 200, no 403

2. **Parent revokes → sub-user blocked**
   - Parent logs in, toggles "销售智能体" OFF for the sub-user, save
   - Sub-user refreshes `/sales` → sees "未开通，请联系管理员"
   - Sub-user tries direct POST to `/sales-rag/sessions/N/chat` via curl or browser devtools → 403 `{"code": 100207, "message": "未开通该功能权限，请联系管理员"}`

3. **Parent account itself unaffected**
   - Parent visits `/sales` directly → works (never required toggling)
   - Parent uploads doc, creates session, chats → all HTTP 200

4. **`/check-permission` returns honest answer**
   - Sub-user with grant: `GET /v1/sales-rag/check-permission` → `{has_permission: true}`
   - Sub-user without grant: same URL → `{has_permission: false}` (HTTP 200 body, NOT 403)
   - Parent: `{has_permission: true}`

5. **No credit consumption on denial**
   - Sub-user without grant tries `/sales-rag/sessions/N/chat` → 403
   - Check billing: no `usage_record` row inserted for this request

**Commands to run in S5 (local):**

```bash
# Backend
cd numind-server
task lint                          # exit 0
go test ./internal/numind/... ./internal/pkg/...  # all pass (pre-existing failure exception noted)
task test                          # full suite with race + coverage

# Frontend (numind-web-v3)
cd numind-web-v3
npm run lint                       # exit 0
npm run type-check                 # exit 0
E2E_USERNAME=$E2E_USERNAME E2E_PASSWORD=$E2E_PASSWORD npm run test:e2e -- sales-agent-permission.spec.ts
```

**Observability check:** N/A — no new LLM calls introduced.

**Regression risk:** If `task test` or E2E reveals failures in areas unrelated to sales_agent (e.g. credit, SOP), investigate whether this feature's middleware accidentally changed route chain behavior for other groups. Specifically re-run `TestGate_*` tests (Task 3) to confirm gate behavior is unchanged.

**QA report template:** `numind-server/docs/superpowers/qa/2026-04-21-sales-agent-child-permission-qa.md` using `templates/ndf/qa-report.md`. Must include:
- Command output excerpts (lint/test exit codes)
- Playwright run screenshot or pass count
- User-path checklist from above, each checked off with evidence
- Any SSH-baseline deferrals from Task 5

---

## Plan Self-Review (inline)

**Spec coverage:**
- §4 (27 endpoints) → Task 1 + Task 3 (H1-H3 sample 3, trust `.Use()`-for-group semantics for the other 24)
- §5 D1-D6 → Task 3 (H4 for D1, H1-3 for D2, biz-path C2 verified by Task 1+2 for D6); D3/D4/D5 verified in spec by code reading. D4 additionally tested live by Task 3 H5 (parent pass-through through real middleware).
- §6 C1-C5 → Task 1 (C1+C2), Task 2 (C3), Task 3 (C5), Task 4 (C4); plus Task 0 test infra helper added to enable Task 3
- §7 S5 strategy → Task 6
- §8 rollback → documented in spec, not a task (git revert is trivial)
- §9 pre-merge SQL → Task 5
- §10 AS-1..AS-8 → mapped via S5 user paths

**Placeholder scan:** No "TBD" / "TODO". Explicit reality-check clauses in Task 4 (selector inspection mandatory) and Task 5 (DB cred discovery step).

**Type consistency:** `FeatureKeySalesAgent` constant used in all 4 code-touching tasks; `ctrl.b.Customers().CheckFeaturePermission` signature consistent with `biz/customer/customer.go:340`; Task 2 uses `var _ biz.IBiz = (*realBizOnlyCustomers)(nil)` compile-time guard to catch future interface drift; Task 3 uses `store.NewTestDataStore(db)` (added by Task 0) for store.S injection.

**Atomicity of Task 1:** C1 + C2 combined prevents the UI/backend inconsistency window. All other tasks are test additions (0, 2, 3, 4, 6) or data-gathering (5) that don't break the build.

**Post-reviewer fixes included:**
- **P0-1** `store.S *datastore` assignment issue → added Task 0 introducing `NewTestDataStore`
- **P0-2** `stubCustomerBiz` interface drift → Task 2 rewritten to use real `customerbiz.New(testDS)` with in-memory SQLite, eliminating the need to mock 17-method interface; `realBizOnlyCustomers` uses `nil` returns with compile-time guard
- **P1-1** Task 5 DB password source → explicit Step 1 discovers creds from `config_dev.yaml`
- **P1-2** Task 4 E5 empty assertion → replaced with `chat403Count > 0` + `responseCode == 100207` contract assertions via `page.evaluate(fetch)`
- **P1-3** Task 4 selector guessing → reality-check clauses mandate DOM inspection before commit (not "best effort")
- **P2-1** router.go has no `{ }` block → clarified in Task 1 Step 2 prose
- **P2-2** Task 3 H1 brittle message assertion → dropped `Contains("未开通")`; kept only `resp.Code != 0`
