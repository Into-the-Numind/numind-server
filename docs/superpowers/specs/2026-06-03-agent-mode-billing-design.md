# Agent Mode 真实积分扣费接通（BLK-2）— 技术设计 Spec

> Feature: `agent-mode-billing` · Stage S2 · 2026-06-03 · numind-server only
> 上游：requirements/agent-mode-billing.md（S0）、proposals/agent-mode-billing-proposal.md（S1）
> 关联 in-flight：`b2b2c-student-agent-access`（已 merge develop，同改 runner/biz/student_run_lifecycle）

---

## §1 目标与非目标

**目标**：agent 的**每一次大模型/AI 服务调用**真实 Reserve/Reconcile 扣减**发起 run 的 userID**（`agent_run.user_id`）的积分；
父账户 Builder 试聊扣 admin_test 池；内存 BudgetTracker 的 MaxCredits/MaxTurns 维度真正生效；
异常（crash/cancel/timeout/余额不足）下不泄漏 reservation。

**非目标**：不改既有 SOP/chatbot/salesrag 计费金额与行为；不重设计计费规则/路由/trace 拓扑；不碰 config_prod.yaml；
不修 BLK-1/3/4/5；前端不改（budget 弹窗依赖后端真实回传，已具备）。

---

## §2 架构决策（核心）

### D1：混合方案（chat 走方向 A，image_gen 显式 Reserve）—— FINAL

**关键事实**（S2 勘察确认）：平台真实积分扣减**只发生在** `ContextBudgetCredits` 中间件，且该中间件
`context_budget.go:398` 仅对 `ChatRequest` 且 `len(ContextFragments)>0` 生效；内层 `Billing` 中间件只写
`usage_record`（用量快照）**不扣三池**。⇒

- **chat-shaped 调用**（ReAct 主循环 + analyze_image + annotate_image + compaction，全部 `aiservice.Chat`）：
  **方向 A** —— 适配器设 `ContextFragments` + run 入口注入 billing operation/ref/pool ctx，
  由 `ContextBudgetCredits` 自动 per-call Reserve/Reconcile。**复用其久经考验的流式 cancel 退款 + sweeper 兜底**。
- **image_gen**（非 chat，文生图扁平计费，当前裸 HTTP）：方向 A 不适用（中间件 ChatRequest-only）。
  **显式 Reserve/Reconcile** + 收编 aiservice（新 `ImageGen` 入口，满足「唯一入口」硬规则）+ 新 pricing rule。

**否决的替代**：
- *纯方向 A*：无法覆盖非 chat 的 image_gen（中间件不处理 ImageGenRequest）。
- *纯方向 B（runner 全显式包 Reserve/Reconcile）*：要在 N 个 LLM 调用点重新实现预扣/对账/流式退款/cancel/幂等/sweeper，
  与中间件已有逻辑重复，高出错面（任务交付说明亦警示「方向 B 要自己处理多次调用 + 异常退款」）。

### D2：pool-selector 落在 credit biz 层（非 shared 中间件）—— FINAL

admin_test 池是平台级 B2B 概念。经 ctx 注入 pool hint（沿用 `WithReservationRef` 同款专用 ctx key 模式），
`doReserveBudget` 读取 → `BudgetReservationInput.Pool`，**分支逻辑放 `ReserveBudget`/`Reconcile`（credit biz 层）**，
中间件调用点保持统一（仍 `ReserveBudget`/`Reconcile`），不把 agent 概念塞进 shared 中间件。

### D3：image_gen 显式 Reserve 放在 tool 层（biz/agent），不耦合 aiservice ↔ credit

`aiservice.ImageGen` 只负责「收编生成 + Tracing + usage_record」（保持 aiservice 纯 AI 网关）；
`tool_image_gen.go` 用 agent biz 已有的 creditService 依赖**显式 Reserve→调用 ImageGen→FinalizeReservation**（扁平估算，pool-aware）。
（chat 的扣费在 aiservice 中间件是因其需要 token 估算机器；image 扁平，显式更简单，可接受这点不对称。）

---

## §3 详细设计

### 3.1 chat-shaped 计费（方向 A）

**(a) 适配器设 ContextFragments** — `biz/agent/adapter.go::convertToAiserviceRequest`
照搬 `buildSOPGatewayFragments`（sop_fragments.go:133）模式，从 `[]*schema.Message` 构造 fragments：
- system role → `NewImmutableSystemFragment`
- 最后一条 user → `NewCriticalUserFragment`
- 其余 user/assistant/tool → `NewDurableUserFragment`/`NewDurableAssistantFragment`
保留现有 `Messages`（fallback）+ `Tools`。这是**解短路的唯一关键改动**。

**(b) run 入口注入 billing ctx** — `runner.go::Run:396` 与 `runner_runstream.go::RunStream:80`
（紧跟现有 `middleware.NewContextWithUserID(ctx, req.UserID)`）追加：
```go
ctx = billing.WithBillingMeta(ctx, req.UserID, "agent_run",
        billing.Metadata("run_id", billing.FormatUint(runID)))
ctx = aismw.WithReservationRef(ctx, fmt.Sprintf("agent_run:%d", runID))
ctx = aismw.WithBillingPool(ctx, pool)   // pool = "admin_test" if req.IsTest else "" (默认三池)
```
该 ctx 经 `queryCtx → attemptCtx(callctx.WithCallID) → einoAgent.Generate/Stream → adapter → aiservice.Chat` 全程传播（已验证）。
工具内部 `aiservice.Chat`（analyze_image 等）继承同一 run ctx ⇒ 一并接通（**S4 须验证工具 ctx 确实派生自 billing-injected ctx，见 T-verify**）。

**(c) Operation 枚举扩展**（纯 Go 代码，无 migration）：
- `types.go`：`OpAgentRun Operation = "agent_run"`
- `credit_service.go budgetOperationMap`：`"agent_run": OpAgentRun`
- `estimate.go estimatedCredits`：`"agent_run": <baseline>`（fallback，正常走 pricing.CalculateCost token 计价，模型已有 pricing_rule）

### 3.2 pool-selector（三池 vs admin_test）

- **ctx helper**（aiservice/middleware）：`WithBillingPool(ctx, pool string)` + `BillingPoolFromCtx(ctx) string`（专用 struct key，不被 `billing.WithBilling` 覆盖——继承 credit-log-task-names S2-D4 教训）。
- **`doReserveBudget`（context_budget.go）**：读 `BillingPoolFromCtx(ctx)` → `BudgetReservationInput.Pool` / `BudgetPrecheckInput.Pool`（新字段）。
- **`CheckAndEstimateBudget`**：`Pool=="admin_test"` → 查 admin_test 池余额（`adminConsumer.Status`）；否则三池总额（现状）。
- **`ReserveBudget`**：`Pool=="admin_test"` → 走 `adminConsumer.Consume` + 写 `credit_reservation`（**`operation="agent_test"` 哨兵**，沿用 ReserveAgentTest 既有写法）；否则三池 `DeductCreditsTx`（现状）。
- **`Reconcile`（reservationID 版，中间件 facade 走这条）**：已 `loadReservationForUpdate` 拿到 row →
  `row.Operation=="agent_test"` 分支到 admin_test 退/补（复用 ReconcileAgentTest 逻辑）；否则三池 reconcile（现状）。
- **`Refund`**：同样按 `row.Operation` 分支（admin_test 退回 adminConsumer）。
- 默认（无 pool ctx）= 三池 ⇒ **SOP/chatbot/salesrag 零行为变更**（关键安全网）。

### 3.3 admin 试聊接线

- **信号**：`CreateRunRequest` 加 `IsTest bool json:"is_test"`；`RunRequest` 加 `IsTest bool`；
  Create/CreateStream 构造时透传（student_run_lifecycle.go:321/557）。前端 Builder 试聊置 `is_test=true`（前端改动极小，列为 web-v3 配套；若前端未就绪，dev 可用 API 直接验）。
- **运行时**：`req.IsTest` → pool="admin_test" → 3.2 路由到 admin_test 池。扣的是**父账户自己的 admin_test 额度**（`credit_admin_test_grant`，按月 5000 default）。
- **审计列（migration M2，可选但推荐）**：`agent_run.is_test BOOLEAN NOT NULL DEFAULT false`，admin 监控可区分试聊 run。
- **复用既有**：`ReserveAgentTest`/`ReconcileAgentTest`/`AdminTestConsumer`/source_type `admin_test`（migration 20260521 已就位）全部现成，仅缺 caller——本任务通过 3.2 的 pool 分支统一接通（不必单独调 ReserveAgentTest，由 ReserveBudget 内部按 pool 走 adminConsumer）。

### 3.4 内存 BudgetTracker 接通

- **R-shared usage store**：新增进程级 `CallUsageStore`（callID→Usage，含读后删/TTL，规避 §3.7 sync.Map 无 GC 增长），在 `biz.go` 构造一份，注入：
  1. `agentRunner`（构造 adapter 时用它做 `usageStore`，替代当前 struct 字面量的 nil）——修 runner.go:828 / runner_runstream.go:401。
  2. `budgetgate.WrapHooks(..., WithUsageLookup(callUsageStore))`——修 biz.go:343。
  callID 全局唯一 ⇒ 跨 run 不串；PostToolCall 读后删 key ⇒ 有界。
- **RecordStep 接通**：ReAct 主循环每步（每个 LoopEvent step）调 `budgetTracker.RecordStep(ctx, runID)` ⇒ MaxTurns 维度生效。
- **效果**：PostToolCall `LookupUsage(callID)` 拿到**真实 token** → `RecordUsage` → MaxCredits 维度非 0 生效；RecordStep → MaxTurns 生效。wall_time 已有。
- **单位说明（次要决策，交 reviewer）**：BudgetTracker 是**per-run 内存安全帽**，与三池真实扣减**互补**（前者防单 run 跑飞，后者是真钱）。MaxCredits 维度沿用 tracker 既有单位喂真实 token（保持 enum/语义不变，I 系列 invariant 不动）；token↔credit 精确换算为后续优化，不在本任务阻塞项。

### 3.5 image_gen 收编 + 计费

- **收编**：新增 `aiservice.ImageGen(ctx, taskID, req ImageGenRequest) (*ImageGenResponse, error)` + dmxapi image provider（把 tool_image_gen.go 的裸 HTTP 迁进 provider）。经 Tracing（新 generation 点）+ Billing（usage_record，per-call pricing）中间件。**满足 ai-service.md §0 唯一入口硬规则**。
- **计费**：`tool_image_gen.go` 显式 `creditService.Reserve(user, OpImageGen, estimate, 0, idemKey)` → `aiservice.ImageGen` → `FinalizeReservation(rsv, actualCost, opErr)`；pool-aware（读 `BillingPoolFromCtx`，test→admin_test）。失败 → opErr → 自动 Refund（不扣费）。
- **枚举/估算/价**：`OpImageGen="image_gen"` + budgetOperationMap + `estimatedCredits["image_gen"]`；
  **migration M1**：`pricing_rule` 新增 `('image_gen','dmxapi','gemini-2.5-flash-image','flat','call', price_per_call=<X>)`（沿用 OCR flat-per-call 计价机制）。价格 `<X>` 待用户/运营确认（占位，S4 用合理值 + 注释）。

### 3.6 异常 / 退款设计

- **per-call 独立**：每次 LLM 调用独立 Reserve/Reconcile ⇒ 已完成调用各自已对账，泄漏面仅限「进程死时正 in-flight 的单次调用」。
- **detached ctx（R4）**：run 用 `context.Background()`，HTTP cancel 不传播。
  - 流式中间件的 `ctx.Done()` 退款路径在 detached ctx 下**不触发** → 依赖 **reservation_sweeper**（reserved>1h 自动 Refund "expired_by_cron"，5min 扫）兜底。
  - **增强（推荐）**：给 run 建**可取消的 detached ctx**（`context.WithCancel(context.Background())` + 注册进既有 cancels 注册表），用户 cancel / run terminal 时 cancel 它 → in-flight 调用的流式退款及时触发，sweeper 仅作最后兜底。S4 评估是否纳入（与 b2b2c 的 ctx 改动协调，避免冲突）。
- **余额不足**：Reserve 失败（`ErrInsufficientCredits` / `ErrAdminTestExhausted`）→ 中间件返回 error → adapter.Generate 返回 error → ReAct loop 终止 → 映射到 `error_max_budget`（或等价终态）。run 优雅停，不再调大模型。
- **多退少补**：Reserve 预扣（token 估算）→ Reconcile 按实际 token 多退少补（中间件 + Reconcile 既有：delta<0 退、delta>0 补、underestimate 吸收审计行）。

### 3.7 估算策略

- chat：沿用 `ContextBudgetCredits` 的 `EstimateFragments`（token 估算）→ `pricing.CalculateCost(llm_chat, provider, model, prompt, completion)`（模型已有 pricing_rule）→ Reserve；Reconcile 按实际。多轮**per-call** 预扣（不一次性预扣整个 run），避免爆。
- image_gen：扁平 per-call 估算（`estimatedCredits["image_gen"]` / pricing_rule price_per_call）。

---

## §4 内部接口契约（新增/改动）

| 符号 | 文件 | 签名/变更 |
|------|------|----------|
| `WithBillingPool` / `BillingPoolFromCtx` | aiservice/middleware | `func WithBillingPool(ctx, pool string) context.Context` / `func BillingPoolFromCtx(ctx) string`（专用 key）|
| `BudgetPrecheckInput.Pool` / `BudgetReservationInput.Pool` | biz/credit/types.go | 新增 `Pool string`（""=三池, "admin_test"=试聊池）|
| `ReserveBudget`/`CheckAndEstimateBudget`/`Reconcile`/`Refund` | biz/credit/credit_service.go | 内部按 Pool / row.Operation 分支（签名不变）|
| `aiservice.ImageGen` | internal/pkg/aiservice/ai.go | `func ImageGen(ctx, taskID string, req ImageGenRequest) (*ImageGenResponse, error)` + provider |
| `OpAgentRun` / `OpImageGen` | biz/credit/types.go | 新 Operation 常量 |
| `CallUsageStore` | biz/agent（或 budget 包）| `interface{ Store(callID, Usage); LookupUsage(callID)(Usage,bool); Delete(callID) }` 进程级 |
| `RunRequest.IsTest` / `CreateRunRequest.IsTest` | biz/agent | 新 bool 字段 |

---

## §5 Trace 拓扑（AI 功能 gate 要求）

- **Trace 起点**：不变 —— `runner.go:446` / `runner_runstream.go:102` `langfuse.CreateTrace("agent-runtime-*", WithUserID(req.UserID), ...)`。
- **Generation 点**：不变 —— aiservice `Tracing` 中间件对每次 Chat 自动 `CreateGeneration`；**新增** image_gen 经 `aiservice.ImageGen` → Tracing 记 generation。
- **关键 metadata**：reservation ref `agent_run:<runID>` + operation(`agent_run`/`image_gen`) + userID + pool。
- **不破坏既有**：billing ctx 与 Langfuse ctx 正交（不同 key），billing 注入不动 trace/generation；Reconcile 写 `credit_transaction` 不触 trace。
- **S5 验证**：跑 run 后 Langfuse 仍含各 generation；dev DB `credit_transaction` 有对应行（source_type 区分池）。

---

## §6 Migrations

| ID | 文件（建议名）| 内容 | 必需 |
|----|------|------|:---:|
| M1 | `YYYYMMDD_pricing_rule_image_gen.sql` | `pricing_rule` 插入 image_gen/dmxapi/gemini-2.5-flash-image flat per-call（含 IF NOT EXISTS / 幂等 + rollback 注释）| 是 |
| M2 | `YYYYMMDD_agent_run_add_is_test.sql` | `agent_run` 加 `is_test BOOLEAN NOT NULL DEFAULT false`（审计；GORM default:true 坑不适用——default false）| 推荐 |
| — | source_type `admin_test` | **已存在**（20260521）—— 无需新 migration | — |

---

## §7 PRD 覆盖核对（S1 §4 验收标准 → 设计落点）

| 验收标准 | 设计落点 |
|---------|---------|
| 多轮 run 后三池余额下降 + credit_transaction | 3.1（方向 A）+ 3.5（image）|
| 每次 LLM 调用都计费（主循环/vision/compaction/image）| 3.1（共享 ctx 覆盖工具内 Chat）+ 3.5 |
| 多退少补正确 | 3.6 + 中间件既有 reconcile |
| 余额不足终止 | 3.6（Reserve 失败 → error_max_budget）|
| 异常无泄漏 | 3.6（per-call 独立 + sweeper + 可选 cancelable ctx）|
| MaxCredits/MaxTurns 熔断 | 3.4（usageStore + WithUsageLookup + RecordStep）|
| admin试聊记账 | 3.2 + 3.3（pool=admin_test）|
| 扣减对象正确（子账户扣自己）| 3.1（req.UserID=agent_run.user_id；对齐 b2b2c）|
| 不破坏既有 SOP/chatbot/salesrag | 3.2（默认三池零变更）+ §5 |
| 回归保护落 Go 单测 | §8 + S3 S5 策略 |

---

## §8 测试策略预览（细化进 S3 plan + S5 策略）

biz 层为主（mock store + mock aiservice）：
- 扣费成功（chat 一次 → Reserve+Reconcile，三池下降）
- 余额不足 → Reserve 失败 → run 终止（不调大模型）
- 多轮多扣（N 次 Chat → N 对 Reserve/Reconcile，无遗漏/无重复，幂等）
- crash/cancel/timeout → 无 stuck reserved（sweeper 介入归零）
- Reconcile 多退少补（delta<0 退 / delta>0 补 / underestimate 吸收）
- pool 路由（IsTest → admin_test 池 used_amount 增，三池不变；默认 → 三池）
- **回归安全网**：SOP/chatbot 路径计费金额不变（未注入 pool ⇒ 与今日逐字一致）
- image_gen：Reserve→生成→Reconcile；失败→Refund
- BudgetTracker：RecordStep→MaxTurns 熔断；RecordUsage(真实token)→MaxCredits 熔断
NDF Rule 10：支付高风险 → S5 必须 Go 持久回归（+ dev 真实扣费黑盒验证），非一次性 /qa。

---

## §9 待门禁确认的开放决策

1. **image_gen price_per_call 价格值**（M1）——需运营/用户给数，S4 先占位 + 注释。
2. **agent_run.is_test 审计列（M2）**——推荐加（admin 监控可分试聊）；可省（admin_test 池已自带账）。reviewer 判。
3. **cancelable detached ctx 增强**（3.6）——推荐做（退款及时）；最简版仅靠 sweeper 兜底亦可上线。与 b2b2c ctx 改动协调。
4. **前端 Builder 试聊 is_test=true**——后端先就绪；前端配套可并行/稍后（dev 可 API 直验）。

---

## §10 风险登记（承 S1 §3）

R1 非chat计费（image 独立路径，3.5）✅设计闭环 · R2 pool-selector（3.2 落 credit 层）✅ ·
R3 admin试聊无标识（3.3 IsTest）✅ · R4 detached ctx 退款（3.6 sweeper+可选cancelable）⚠需S4验 ·
R5 多次调用 per-call（3.1/3.7）✅ · R6 b2b2c 文件冲突（S4 先 merge develop，协调 ctx 改动点）⚠ ·
R7 估算偏差（3.7 多退少补）✅
