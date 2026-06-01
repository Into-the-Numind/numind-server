# 积分消耗记录显示具体任务名 — 技术设计 Spec（快车）

> NDF Standard S2 工件。对应 `requirements/credit-log-task-names.md` + `proposals/credit-log-task-names-proposal.md`。
> 日期 2026-06-01。仓库 numind-server + numind-web-v3。全覆盖（S1-D3）。

## 1. 概述
弹窗「动作」列→「任务」，每条显示具体实体名。两段：**(A) 写入端** 把业务引用经 ctx 穿过 aiservice 网关写进 `credit_reservation.reference_id`；**(B) 读取端** 富集时按 reference_id 批量查名。只增不改（不碰扣费金额/路由/trace）。历史空引用回退通用名。

## 2. 写入端（业务引用 → reference_id）

### 2.1 ctx 载体（关键决定）
新增**专用 ctx key**（不复用 billing.Meta，因 salesrag `RetrieveStream:1048` 的 `WithBilling` 会覆盖 BillingCtx；专用 key 不被覆盖）。放 aiservice 中间件包（与 `WithUserID`/`WithFeatureRef` 并列，`internal/pkg/aiservice/middleware/`）：
```go
// reservation_ref.go
type ctxKeyReservationRef struct{}
func WithReservationRef(ctx context.Context, refID string) context.Context
func ReservationRefFromCtx(ctx context.Context) string // "" if absent
```

### 2.2 reference_id 编码（自描述，沿用 legacy SOP 格式）
| operation | reference_id |
|---|---|
| sop_run | `sop_run:<runID>:<nodeID>` |
| sop_chat | `sop_chat:<runID>` |
| salesrag_chat | `sales_session:<sessionID>` |
| chatbot_chat | `chatbot_session:<sessionID>` |
（`reference_type` 仍由 `referenceFromOp` 按 op 设；本设计只补 `reference_id`。）

### 2.3 三处注入点（业务 id 在作用域内，设 ctx 后再走 aiservice.Chat）
- **SOP**：`biz/sop/sop.go` —— 在已有 `billing.WithBillingMeta`（节点执行 ~783：run_id/node_id；sop_chat ~1538：run_id）旁加 `ctx = aismw.WithReservationRef(ctx, fmt.Sprintf("sop_run:%d:%d", runID, nodeID))`（chat 同理 `sop_chat:<runID>`）。
- **salesrag**：`biz/salesrag/salesrag.go::ChatWithSession`（~2144，sessionID 在作用域）设 `ctx = aismw.WithReservationRef(ctx, fmt.Sprintf("sales_session:%d", sessionID))`，向下传给 `RetrieveStream`（专用 key 不被其 WithBilling 覆盖）。
- **chatbot**：`biz/chatbot/stream.go::ChatStream`（~310，sessionID 是入参）设 `ctx = aismw.WithReservationRef(ctx, fmt.Sprintf("chatbot_session:%d", sessionID))`。

### 2.4 网关读 + 落库（additive）
- `context_budget.go::doReserveBudget`（~694-742）：`refID := aismw.ReservationRefFromCtx(ctx)`，设进 `BudgetReservationInput.ReferenceID`。
- `biz/credit/types.go` `BudgetReservationInput`：加字段 `ReferenceID string`。
- `credit_service.go::reserveBudgetRow`（~1306）：`if input.ReferenceID != "" { rsvRow.ReferenceID = input.ReferenceID }`（保留 idempotencyKey 分支为 fallback；业务 ReferenceID 优先）。
- **不动**：扣费金额、`DeductCreditsTx`、路由解析、Langfuse span、`referenceFromOp` 的 refType。纯增一处 reference_id 写入。

> legacy 直扣路径（`creditsImpl.Reserve`）SOP 已用 idempotencyKey 编码了 `sop_run:<runID>:<nodeID>` 落进 reference_id，本身就能被读取端解析，无需改。本设计聚焦生产网关路径。

## 3. 读取端（富集查名）

### 3.1 store 批量查名（新增，防 N+1）
按需新增（`store/`，均只读、按 id 批量）：
```go
// SOP
GetTemplateNamesByIDs(ctx, ids []uint) (map[uint]string, error)   // sop_template.id -> name
GetNodesByIDs(ctx, ids []uint) (map[uint]struct{Name string; TemplateID uint}, error) // node -> name + templateID
GetRunTemplateIDsByIDs(ctx, ids []uint) (map[uint]uint, error)    // sop_run.id -> template_id
// sales（限当前用户）
GetSessionTitlesByIDs(ctx, userID uint, ids []uint) (map[uint]string, error)
// chatbot（session -> chatbot 名）
GetChatbotNamesBySessionIDs(ctx, ids []uint) (map[uint]string, error) // join session.chatbot_id -> config.name
```

### 3.2 biz 富集（`biz/credit/consumption_log.go::ListConsumptionLog`）
1. 取该页 reconciled reservations（现有）。
2. 解析每行 reference_id → (entity, ids)；按 entity 分组收集 id。
3. 各 entity 一次批量查名（§3.1）。
4. 组装 `detail_name`：
   - sop_run → `<template.name> · <node.name>`（node 不存 template 时经 run→template；node 名缺失则只 SOP 名）
   - sop_chat → `<template.name>`
   - sales_session → session.title（空则回退通用名）
   - chatbot_session → chatbot config.name
   - 解析失败/空 reference_id/实体已删 → `detail_name=""`（前端回退 action_label）

### 3.3 DTO/响应（加字段，向后兼容）
`ConsumptionLogItem` 加 `DetailName string \`json:"detail_name"\``（具体名；空=不可解析）。`action`/`action_label` 保留不变。

## 4. 前端（numind-web-v3）
- `CreditConsumptionLogModal`：表头「动作」→「任务」。
- 「任务」列文本 = `detail_name || action_label`（具体名优先，回退通用名）。长名 CSS 截断 + `title` 属性 tooltip。
- `api/credits.ts` `ConsumptionLogItem` 接口加 `detail_name: string`。

## 5. 边界 / 权限 / 可观测性
- 历史空 reference_id → detail_name 空 → 回退通用名（不报错）。
- 实体被删 / 查不到 → 回退通用名。
- 越权：ids 来自当前用户自己的 reconciled reservations（ListConsumptionLog 已按 user_id 过滤），天然是本用户实体；sales/chatbot 批量查名额外带 userID 约束（防御）。
- AI 可观测性：**不新增 LLM 调用**；改动 aiservice 网关仅多写 reference_id 元数据。S5 必须验证：reserve 后 reference_id 已写入 + **扣费金额不变** + Langfuse trace/generation 正常。

## 6. 测试计划（S5 策略 S3 定稿）
- 后端单测：reserveBudgetRow 写 input.ReferenceID（idempotency fallback 不破）；ListConsumptionLog 富集（各 entity → detail_name 正确、批量查询次数有界=每域≤1、空/未知回退、越权隔离）；referenceFromOp/编码解析对称。
- 网关：doReserveBudget 从 ctx 读 refID 写入；**对账不变**（扣费金额单测断言与改动前一致）。
- S5（高风险计费域）：Playwright E2E + 真实触发 SOP/销售/对话各一次，验证「任务」列显示具体名 + 扣费金额不变 + trace 正常 + reference_id 落库。复用并扩展 `e2e/credit-consumption-log.spec.ts`。

## 7. 决策记录
- S0-D1/D2、S1-D3（见 manifest）。
- S2-D4：ctx 用专用 key `WithReservationRef`（非 billing.Meta，避 salesrag WithBilling 覆盖）。
- S2-D5：reference_id 自描述编码（sop_run:<run>:<node> / sop_chat:<run> / sales_session:<id> / chatbot_session:<id>）。
- S2-D6：响应加 `detail_name`（保留 action_label）；前端 `detail_name || action_label`。
- S2-D7：只增不改 billing；写入仅 reserveBudgetRow 一处 + 三调用方注入；批量查名防 N+1；越权靠「id 来自本用户 reservation」+ session 查名带 userID。
