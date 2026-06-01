# 积分消耗记录显示具体任务名 — Implementation Plan（快车）

> **For agentic workers:** 用 subagent-driven-development 逐 task 实现。Steps 用 `- [ ]`。
> 对应 spec `docs/superpowers/specs/2026-06-01-credit-log-task-names-design.md`。

**Goal:** 「积分消耗记录」每条显示具体任务名（哪个 SOP·步骤 / SOP名 / 销售会话名 / 智能体名），表头「动作」→「任务」。

**Architecture:** 写入端在 reserve 时经专用 ctx key 把业务引用穿过 aiservice 网关写进 `credit_reservation.reference_id`（纯增、不碰金额/路由/trace）；读取端 `ListConsumptionLog` 解析 reference_id 批量查名富集 `detail_name`。历史空引用回退通用名。

**Tech Stack:** Go（aiservice 网关中间件 + credit biz/store + sop/salesrag/chatbot biz）；Vue3（弹窗）。

worktree：后端 `/private/tmp/wt-credit-log-task-names-numind-server`，前端 `/private/tmp/wt-credit-log-task-names-numind-web-v3`。

---

## File Structure
- `internal/pkg/aiservice/middleware/reservation_ref.go`（新）— ctx key `WithReservationRef`/`ReservationRefFromCtx`
- `internal/pkg/aiservice/middleware/context_budget.go`（改）— doReserveBudget 读 ctx → reserveIn.ReferenceID
- `internal/numind/biz/credit/types.go`（改）— BudgetReservationInput +ReferenceID
- `internal/numind/biz/credit/credit_service.go`（改）— reserveBudgetRow 写 input.ReferenceID
- `internal/numind/biz/sop/sop.go` / `biz/salesrag/salesrag.go` / `biz/chatbot/stream.go`（改）— 注入 ctx ref
- `internal/numind/biz/credit/consumption_log.go`（改）— DetailName + 富集查名
- `numind-web-v3 src/api/credits.ts` + `src/components/credit/CreditConsumptionLogModal.vue`（改）— detail_name + 「任务」

---

## Task 1: 写入端 — 网关贯通 reference_id（高风险，只增不改）

**Files:** Create `internal/pkg/aiservice/middleware/reservation_ref.go`; Modify `context_budget.go`, `biz/credit/types.go`, `biz/credit/credit_service.go`; Test `biz/credit/*_test.go`.

- [ ] **Step 1: 失败测试（落库 + 金额不变）**
在 `internal/numind/biz/credit/` 加测试（用既有 `newCreditReserveTestDB`/`newCreditServiceWithMembership` 或 reserveBudgetRow 直测）：断言 `BudgetReservationInput{ReferenceID:"sop_run:5:9"}` 经 `ReserveBudget` 后，写出的 `credit_reservation.reference_id == "sop_run:5:9"`；且 `ReservedCredits`（金额）与未传 ReferenceID 时**完全一致**（金额不受影响）；`IdempotencyKey` 为空时不回退覆盖。
```go
func TestReserveBudget_WritesReferenceID(t *testing.T) {
  // setup creditService (newCreditReserveTestDB + ds + svc) + a credits user
  // call svc.ReserveBudget(ctx, user, credit.BudgetReservationInput{
  //   BudgetPrecheckInput: <minimal valid>, EstimatedCredits: N, ReferenceID: "sop_run:5:9"})
  // assert reservation row .ReferenceID == "sop_run:5:9"
  // assert .ReservedCredits == <same as a control reserve without ReferenceID>
}
```
（若 ReserveBudget 的 BudgetPrecheckInput 构造复杂，参考本包既有 budget reserve 测试的构造方式。）

- [ ] **Step 2: Run → FAIL**（`ReferenceID` 字段不存在 / 未写入）
`cd <server-wt> && go test ./internal/numind/biz/credit/ -run TestReserveBudget_WritesReferenceID -v`

- [ ] **Step 3: 加 ctx key**
Create `internal/pkg/aiservice/middleware/reservation_ref.go`:
```go
package middleware

import "context"

// ctxKeyReservationRef carries a business reference id (e.g. "sop_run:5:9")
// to be written into credit_reservation.reference_id at reserve time.
type ctxKeyReservationRef struct{}

// WithReservationRef injects a self-describing business reference id into ctx.
// Read by doReserveBudget; survives WithBilling (distinct key). "" = no ref.
func WithReservationRef(ctx context.Context, refID string) context.Context {
	if refID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyReservationRef{}, refID)
}

// ReservationRefFromCtx returns the injected reference id, or "" if absent.
func ReservationRefFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyReservationRef{}).(string); ok {
		return v
	}
	return ""
}
```

- [ ] **Step 4: doReserveBudget 读 ctx → reserveIn.ReferenceID**
`context_budget.go` doReserveBudget（~730），把 reserveIn 改为：
```go
	reserveIn := credit.BudgetReservationInput{
		BudgetPrecheckInput: precheckIn,
		EstimatedCredits:    precheck.EstimatedCredits,
		ReferenceID:         ReservationRefFromCtx(ctx), // 业务引用（空则不影响）
	}
```

- [ ] **Step 5: BudgetReservationInput +ReferenceID**
`biz/credit/types.go` 在 `BudgetReservationInput` 加：
```go
	// ReferenceID, when non-empty, is written verbatim into
	// credit_reservation.reference_id (self-describing, e.g. "sop_run:5:9").
	// Additive metadata only — does NOT affect amount/routing/trace.
	ReferenceID string
```

- [ ] **Step 6: reserveBudgetRow 写入（业务 ref 优先，idempotency fallback 保留）**
`credit_service.go` reserveBudgetRow（~1306），在现有 idempotencyKey 块**之后**加：
```go
	if idempotencyKey != nil {
		rsvRow.ReferenceID = *idempotencyKey
	}
	if input.ReferenceID != "" { // 业务引用优先于 idempotency key
		rsvRow.ReferenceID = input.ReferenceID
	}
```

- [ ] **Step 7: Run → PASS** + 全包测试无回归
`cd <server-wt> && go test ./internal/numind/biz/credit/ -run TestReserveBudget_WritesReferenceID -v && go test ./internal/numind/biz/credit/ ./internal/pkg/aiservice/... 2>&1 | tail -20`

- [ ] **Step 8: lint + commit**
`task lint`（exit 0）→ commit `feat(credit): plumb business reference_id through gateway reserve (additive)`（4 files + test）。

---

## Task 2: 写入端 — 三调用方注入 ctx 引用

**Files:** Modify `biz/sop/sop.go`, `biz/salesrag/salesrag.go`, `biz/chatbot/stream.go`. (依赖 Task 1 的 ctx key)

- [ ] **Step 1: SOP**
`biz/sop/sop.go`：节点执行处（已有 `billing.WithBillingMeta(...run_id/node_id...)` ~783）后加 `ctx = aismw.WithReservationRef(ctx, fmt.Sprintf("sop_run:%d:%d", runID, nodeID))`；sop_chat 处（~1538）加 `ctx = aismw.WithReservationRef(ctx, fmt.Sprintf("sop_chat:%d", runID))`。import alias `aismw "numind-server/internal/pkg/aiservice/middleware"`。确认 runID/nodeID 在作用域（已确认）。

- [ ] **Step 2: salesrag**
`biz/salesrag/salesrag.go::ChatWithSession`（~2124-2144，sessionID 在作用域）：在调用 `RetrieveStream` 前 `ctx = aismw.WithReservationRef(ctx, fmt.Sprintf("sales_session:%d", sessionID))`，并把该 ctx 传入 RetrieveStream（专用 key 不被 RetrieveStream 内 WithBilling 覆盖）。

- [ ] **Step 3: chatbot**
`biz/chatbot/stream.go::ChatStream`（~310，sessionID 入参）：`ctx = aismw.WithReservationRef(ctx, fmt.Sprintf("chatbot_session:%d", sessionID))`（在走 aiservice.ChatStream 之前）。

- [ ] **Step 4: build + lint + 无回归**
`cd <server-wt> && go build ./... && task lint && go test ./internal/numind/biz/sop/... ./internal/numind/biz/salesrag/... ./internal/numind/biz/chatbot/... 2>&1 | tail -15`（预存失败不归本 task；编译 + lint 必过）。注入处行为级验证放 S5（真实触发各路径，确认 reference_id 落库）。

- [ ] **Step 5: commit** `feat(credit): inject business reference into reserve ctx (sop/salesrag/chatbot)`（3 files）。

---

## Task 3: 读取端 — 富集 detail_name（批量查名）

**Files:** Modify `biz/credit/consumption_log.go`; Test `biz/credit/consumption_log_test.go`.

- [ ] **Step 1: 失败测试**
在 `consumption_log_test.go` 加：seed `credit_reservation`（reconciled, actual_cost_cents>0）行带 reference_id（`sop_run:<run>:<node>`/`sales_session:<id>`/`chatbot_session:<id>`/空），并 seed 对应 sop_template/sop_node/sop_run/sales_session/chatbot_session/chatbot_config 行；断言 `ListConsumptionLog` 返回的 item.DetailName：sop_run=「<模板名> · <节点名>」、sales=会话 title、chatbot=智能体名、空引用/未知=""（回退）；并断言查名 query 次数有界（每域≤1，可用 gorm 日志或计数器验证「无 N+1」——简化为：20 行同类型只触发 1 次 IN 查询，断言结果正确即可）。越权：另一个 user 的 session 不被解析（sales 查名带 user_id）。

- [ ] **Step 2: Run → FAIL**（DetailName 不存在）

- [ ] **Step 3: DTO +DetailName**
`consumption_log.go` `ConsumptionLogItem` 加 `DetailName string \`json:"detail_name"\``。

- [ ] **Step 4: 富集实现**
在 `ListConsumptionLog` 取到 `rows []model.CreditReservation` 后、组装 items 前，加富集（用 `s.store.DB()` GORM query builder 批量查，禁 raw SQL）：
```go
// 1. parse reference_id → 收集各 entity 的 id
// 2. 批量查（每域一次 WHERE id IN ?）：
//    sopTplNames := map[uint]string  (sop_template.id->name)
//    sopNodes    := map[uint]{Name,TemplateID}  (sop_node)
//    sopRunTpl   := map[uint]uint   (sop_run.id->template_id)
//    salesTitles := map[uint]string (sales_session WHERE id IN ? AND user_id=?)
//    chatbotNames:= map[uint]string (sales chain: chatbot_session.id->chatbot_id->chatbot_config.name；可两步 IN 或 join)
// 3. detailName(refID) → 拼装；缺失返回 ""
```
实现一个包内 helper `func (s *creditService) enrichDetailNames(ctx, userID uint, rows []model.CreditReservation) map[uint64]string`（reservationID->detailName），在 map items 时 `DetailName: detailMap[r.ID]`。解析失败/查不到 → ""（前端回退）。GORM 例：
```go
var tpls []struct{ ID uint; Name string }
s.store.DB().WithContext(ctx).Model(&model.SopTemplate{}).Where("id IN ?", tplIDs).Select("id","name").Scan(&tpls)
```
（sales 查名带 `AND user_id = ?`。）

- [ ] **Step 5: Run → PASS** + lint
`cd <server-wt> && go test ./internal/numind/biz/credit/ -run TestListConsumptionLog -v && task lint`

- [ ] **Step 6: commit** `feat(credit): enrich consumption log with specific task names (detail_name)`（2 files）。

---

## Task 4: 前端 — 「任务」列 + detail_name

**Files:** Modify `numind-web-v3 src/api/credits.ts`, `src/components/credit/CreditConsumptionLogModal.vue`.

- [ ] **Step 1: api 类型**
`src/api/credits.ts` `ConsumptionLogItem` 接口加 `detail_name: string`。

- [ ] **Step 2: 弹窗**
`CreditConsumptionLogModal.vue`：表头 `<th class="col-action">动作</th>` → `任务`；该列单元格文本由 `r.action_label` 改为 `r.detail_name || r.action_label`；加截断样式（`text-overflow: ellipsis; overflow:hidden; max-width`）+ `:title="r.detail_name || r.action_label"` tooltip。其余（居中/翻页/卡片样式）不变。

- [ ] **Step 3: lint + type-check**
`cd <web-wt> && npx eslint src/api/credits.ts src/components/credit/CreditConsumptionLogModal.vue && npm run type-check`（均 exit 0）。

- [ ] **Step 4: commit** `feat(credits): show specific task name (detail_name) in consumption-log modal`（2 files）。

---

## Task 5: S5 验证策略（独立 task — 规则 10）

**验证方式：Playwright E2E（持久回归）+ 后端 TDD（Task1/3 内）+ 真实触发对账验证。**
**理由**：高风险计费域 + 改 aiservice 计费网关 → 必须验「钱不算错 + trace 不破 + reference_id 落库 + 任务名显示」。E2E 留持久回归。
**S5 关键路径**：
1. 后端 `task test`（含 race）全绿；网关/credit 单测含「金额不变」断言。
2. 本地起后端（连 dev 库）+ 前端 → 登录。
3. **真实触发各路径各一次**：跑一个 SOP 节点、一次销售对话、一次智能对话 → 各自扣费成功。
4. 打开「积分消耗记录」弹窗 → 验证：表头「任务」；新产生的 3 条分别显示「SOP名·步骤名」「销售会话名」「智能体名」；历史行回退通用名。
5. **对账**：确认这几次扣费金额与改动前口径一致（reference_id 是额外元数据，不影响金额）；Langfuse trace 正常（如本地有栈）。
6. E2E：扩展 `e2e/credit-consumption-log.spec.ts`（或新增 spec）覆盖「任务」表头 + detail_name 渲染（数据无关：有 detail_name 显示具体名，无则回退）。
**S5 还需重跑**：`task lint`+`task test`（后端）+ `npm run lint`+`npm run type-check`（前端）。

- [ ] 本 task 无代码改动；策略随 plan 入库，S5 执行。

---

## Self-Review
- **Spec 覆盖**：spec §2 写入→Task1+2；§3 读取→Task3；§3.3 DTO→Task3；§4 前端→Task4；§5 边界/越权/可观测→Task3 测试+Task5；§6 测试→Task1/3+Task5。
- **占位符**：无 TBD；批量查名给了 GORM 例 + helper 签名；caller 注入给了精确行号 + 代码。
- **类型一致**：`WithReservationRef/ReservationRefFromCtx`、`BudgetReservationInput.ReferenceID`、`reserveBudgetRow` 写入、`ConsumptionLogItem.DetailName`(json detail_name) 前后端一致。
- **依赖**：Task2 依赖 Task1 的 ctx key；Task3 独立可测（seed reference_id）；Task4 依赖 Task3 的 detail_name 字段。后端(1→2→3)先于前端(4)。无环。
