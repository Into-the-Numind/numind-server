# Agent Mode 真实积分扣费接通（BLK-2）— 实施规划 Plan

> Feature `agent-mode-billing` · Stage S3 · 2026-06-03 · numind-server only
> Spec: docs/superpowers/specs/2026-06-03-agent-mode-billing-design.md
> 全部 task 在 worktree `/private/tmp/wt-agent-mode-billing-numind-server`（feature/agent-mode-billing）。
> **S4 第一步**：worktree 内 `git merge develop`（拿 b2b2c-student-agent-access 已落地的 runner/biz/student_run_lifecycle 改动），再逐 task。

## 并行 Tier 评估

所有 task 同仓库、多处文件交叉（credit 层 / agent 层互依），依赖链明确 ⇒ **默认 Tier 4 串行**（不并行 implementer）。
Reviewer 永远 Tier 1 并行（每 task 后双 Sonnet reviewer）。

## 任务依赖 DAG（无环）

```
T1(enums) ─┐
T2(pool ctx+fields) ─┬─→ T3(credit pool 分支) ─┐
T4(中间件 bill-only 模式) ←T2 ──────────────────┤
T5(RunRequest.IsTest + runner ctx 注入+bill-only) ←T2,T4 ─┼─→ T12(集成/回归)
T6(BudgetTracker 接通) ─────────────────────────┤
T7(controller IsTest 透传, 仅 RunRequest) ←T5 ──┤
T8(aiservice.ImageGen 收编) ─┐                  │
T9(image 显式计费) ←T8,T1,T3 ─┴─────────────────┤
T10(migrations M1/M2 + model + preRun.IsTest) ──┤
T11(cancelable ctx) ←T5 ────────────────────────┘
T13(S5 验证策略) ← 全部
```

建议串行顺序：T1 → T2 → T3 → T4 → T5 → T7 → T6 → T10 → T8 → T9 → T11 → T12 → T13。
**总 task 数：13**（T1–T13；S4 设计修正后 T4=bill-only 中间件、T4b 取消）。

> **S3 独立 reviewer（Sonnet）VERDICT=PASS_WITH_FIXES**，已修 5 项。**S4 设计修正（用户确认）**：方向 A 的「设 ContextFragments」破 ReAct tool 结构 → 改 bill-only 网关模式（spec §3.0）；T4 重定义为中间件 bill-only，T4b 取消。

---

## T1 — Operation 枚举 + 估算 fallback + budgetOperationMap
- **描述**：新增 `OpAgentRun="agent_run"` / `OpImageGen="image_gen"` 两个 Operation 常量，接入 normalization + fallback 估算表。
- **涉及文件**：`biz/credit/types.go`（const）、`biz/credit/credit_service.go`（budgetOperationMap）、`biz/credit/estimate.go`（estimatedCredits map）、`biz/credit/*_test.go`。
- **验收**：`go test ./internal/numind/biz/credit/...` 绿；新增表驱动测试断言 `budgetOperationMap["agent_run"]==OpAgentRun`、`GetEstimatedCredits("agent_run")>0`、image_gen 同理。编译通过。
- **TDD**：RED 写枚举映射测试（缺常量编译失败）→ GREEN 加常量+map 项。

## T2 — pool ctx helper + BudgetInput.Pool 字段
- **描述**：新增 `WithBillingPool(ctx,string)`/`BillingPoolFromCtx(ctx)`（aiservice/middleware，**专用 struct key**，验证不被 `billing.WithBilling` 覆盖）；`BudgetPrecheckInput.Pool` + `BudgetReservationInput.Pool` 新字段（`""`=三池，`"admin_test"`=试聊）。
- **涉及文件**：`internal/pkg/aiservice/middleware/context_budget.go`（或同包新 helper 文件）、`biz/credit/types.go`、对应 `_test.go`。
- **验收**：测试断言 ctx round-trip 取回 pool；断言先 `WithReservationRef`+`WithBilling`+`WithBillingPool` 混合后三者各自值不互相覆盖（继承 credit-log-task-names S2-D4）。`go test` 绿。
- **TDD**：RED ctx 不覆盖测试 → GREEN 专用 key 实现。

## T3 — credit biz 层 pool 分支（核心高风险）
- **描述**：`doReserveBudget` 读 `BillingPoolFromCtx`→`BudgetReservationInput.Pool`。`CheckAndEstimateBudget`（pool=admin_test 查 adminConsumer.Status 余额）/`ReserveBudget`（admin_test 走 adminConsumer.Consume + 写 reservation `operation="agent_test"` 哨兵；否则三池 DeductCreditsTx）/`Reconcile`（已 load row → `row.Operation=="agent_test"` 路由 admin_test 退补，复用 ReconcileAgentTest 逻辑）/`Refund`（同按 row.Operation 分支）。
- **涉及文件**：`internal/pkg/aiservice/middleware/context_budget.go`（doReserveBudget 读 pool）、`biz/credit/credit_service.go`、`biz/credit/*_test.go`。
- **验收**：
  - 新测试：pool=admin_test → Reserve 走 mock adminConsumer.Consume，reservation.operation=="agent_test"，Reconcile 走 admin_test 退补；三池未动。
  - **Refund 路由测试（S3 reviewer P2）**：`Refund(agent_test reservation)` → `adminConsumer.Refund` 被调用，**三池 items 不被触碰**（现 `creditsImpl.Refund:843` 只懂三池 items，agent_test 必须分支，否则错退回三池）。
  - **回归安全网测试**：pool="" → 与现状逐字一致（三池 DeductCreditsTx 路径，金额不变）。
  - `go test ./internal/numind/biz/credit/... ./internal/pkg/aiservice/middleware/...` 绿（含 `-race`）。
- **TDD**：RED admin_test 路由测试 + Refund 路由测试 + 三池回归测试 → GREEN 分支实现。**禁止改三池金额算法**。

## T4 — 适配器设 ContextFragments（解短路关键）
- **描述**：`convertToAiserviceRequest` 从 `[]*schema.Message` 构造 `ContextFragments`（system→NewImmutableSystemFragment；最后 user→NewCriticalUserFragment；其余→NewDurable*Fragment），照搬 `buildSOPGatewayFragments` 模式；保留 Messages（fallback）+ Tools。
> **⚠ T4/T4b 重做（S4 设计修正，见 spec §3.0）**：原「适配器/工具设 ContextFragments」会让中间件渲染替换 Messages
> → 丢 ReAct tool 结构 → HTTP 400。用户确认改 **bill-only 网关模式**。T4 重定义为中间件 bill-only 模式；**T4b 取消**
> （bill-only 标志经 ctx 继承自动覆盖 vision/compaction，无需各自建 fragments）。total_tasks 14→13。

## T4 — 中间件 bill-only 网关模式（取代原 adapter/T4b fragments）
- **描述**：`ContextBudgetCredits` 加 ctx 标志 `WithGatewayBillingOnly`/`gatewayBillingOnlyFromCtx`（middleware）。bill-only 时：在 Step1 短路之前判定 → **跳过 Prepare（不压缩/不渲染/不替换 Messages）** → 从 `chatReq.Messages` rune 数估算 prompt token → 合成 `PrepareResult{Messages:nil, Policy{ChargeUser:true, ReservedOutputTokens:route.MaxOut/2}, EstimatedAfter:估算, NormalizedOp:operation}` → 复用既有 Step4-7（Messages=nil ⇒ 保留 agent 原 messages；Plan 空 ⇒ "ok"）→ Reserve/Reconcile（含 T3 pool 路由）。估算函数 `billOnlyPromptEstimate(messages)`（Σrune/2，保守，Reconcile 校正）。
- **涉及文件**：`internal/pkg/aiservice/middleware/context_budget.go`、`internal/pkg/aiservice/middleware/billing_pool.go`（加 flag helper）、`*_test.go`。
- **验收**：测试：bill-only ctx + 带 tool_calls 的 messages → 中间件**不改 Messages**（tool 结构保留）+ Reserve 被调用（mock CreditService 捕获）+ Reconcile 用实际 token；非 bill-only 路径走 Prepare 不变（回归）。`go test ./internal/pkg/aiservice/middleware/ -race` 绿。
- **TDD**：RED bill-only 下 Messages 被改/未 Reserve → GREEN bill-only 分支。**默认无标志 = Prepare 路径逐字不变（SOP/chatbot 回归）**。

## T5 — RunRequest.IsTest + runner billing ctx 注入（含 bill-only 标志）
- **描述**：`RunRequest` 加 `IsTest bool`；`runner.go::Run`（~:396/398，post-b2b2c）与 `runner_runstream.go::RunStream`（~:80）在 `NewContextWithUserID` 后注入 `billing.WithBillingMeta(ctx, req.UserID, "agent_run", Metadata("run_id",...))` + `aismw.WithReservationRef(ctx,"agent_run:<runID>")` + `aismw.WithBillingPool(ctx, poolFromIsTest(req.IsTest))` + **`aismw.WithGatewayBillingOnly(ctx)`**（关键：使 agent 走 bill-only）。标志经 run ctx → attemptCtx → 工具 ctx 继承 ⇒ 主循环 + vision + compaction 统一计费。
- **涉及文件**：`biz/agent/runner.go`、`biz/agent/runner_runstream.go`、`biz/agent/*_test.go`。
- **验收**：测试用 mock aiservice（捕获 ctx）断言一次 run 内的 Chat 调用 ctx 携带 operation=agent_run + ref=agent_run:<id> + pool（IsTest 时 admin_test）+ bill-only 标志。`go test` 绿。**与 b2b2c 改动同函数 → merge 后核对注入点未被冲突破坏**。
- **TDD**：RED ctx 缺 billing 字段/bill-only 标志断言 FAIL → GREEN 注入。
- **验证子项（原 T-verify）**：S5 须验工具内 aiservice.Chat（vision/compaction）的 ctx 确实派生自 billing+bill-only ctx（否则漏计费）。

## T6 — BudgetTracker 接通（MaxCredits/MaxTurns）
- **描述**：新增进程级 `CallUsageStore`（callID→Usage，读后删/TTL）。**接线（S3 reviewer P2 显式化）**：`biz.go` 构造**一份** CallUsageStore 实例，同时传给 (1) `budgetgate.WrapHooks(..., WithUsageLookup(store))`（biz.go:343）和 (2) `agent.NewAgentRunner(..., WithCallUsageStore(store))`——需**新增 `WithCallUsageStore(CallUsageStore) RunnerOption` + `agentRunner.callUsageStore` 字段**；`runner.go`（~:849 adapter 构造）与 `runner_runstream.go`（~:401）改用 `r.callUsageStore` 替代当前 nil `usageStore`。ReAct 主循环每步调 `budgetTracker.RecordStep(ctx, runID)`。callID 全局唯一 ⇒ 进程级单例 store 跨 run 不串；PostToolCall 读后删 key ⇒ 有界。
- **涉及文件**：`biz/agent/`（CallUsageStore 新文件 + `WithCallUsageStore` RunnerOption + adapter usageStore 接线 + runner loop RecordStep）、`biz/biz.go`、`biz/agent/budgetgate/`（如需 option）、`biz/budget/`、`*_test.go`。
- **验收**：测试：构造超 MaxTurns 的循环 → RecordStep 触发 DimMaxTurns 熔断；mock usageLookup 返真实 token → RecordUsage → 超 MaxCredits 触发 DimMaxCredits 熔断。CallUsageStore 读后删（无增长）。`go test -race` 绿。
- **TDD**：RED 熔断不触发（维度 0）→ GREEN 接线。**不改 HookAction/TerminalReason/LoopEvent enum（I 系列 invariant）**。

## T7 — controller IsTest 透传（仅 RunRequest，不碰 model）
- **描述**：`CreateRunRequest` 加 `IsTest bool json:"is_test"`；`student_run_lifecycle.go` Create/CreateStream 构造 RunRequest 时透传 `IsTest: req.IsTest`。**仅透传到 `RunRequest`（不写 `model.AgentRun`，避免依赖 T10 的 model 字段 → T7 可独立编译）**；persist 到 DB 的 `preRun.IsTest` 由 T10 负责（model 字段就绪后）。（Estimate 路径如需估算 admin_test 余额可一并透传，否则不动。）
- **涉及文件**：`biz/agent/student_run_lifecycle.go`、`controller/v1/agent/student_run.go`（若 DTO 在 controller）、`*_test.go`。
- **验收**：测试断言 IsTest=true 经 Create → RunRequest.IsTest=true → 下游 pool=admin_test。`go test` 绿。**与 b2b2c 同文件 → merge 后核对**。
- **TDD**：RED 透传断言 → GREEN 加字段透传。

## T8 — image_gen 收编 aiservice.ImageGen
- **描述**：新增 `aiservice.ImageGen(ctx, taskID, req ImageGenRequest)(*ImageGenResponse,error)` + dmxapi image provider（把 tool_image_gen.go 裸 HTTP 迁进 provider），经 Tracing（generation）+ Billing（usage_record）中间件。`ImageGenRequest/Response` 类型定义。
- **涉及文件**：`internal/pkg/aiservice/ai.go`、`internal/pkg/aiservice/types.go`、aiservice gateway/provider 包（新 image provider）、`*_test.go`。
- **验收**：测试 mock provider → `aiservice.ImageGen` 返回 image 字节/URL；经中间件链（Tracing/Billing）。`go test ./internal/pkg/aiservice/...` 绿。满足 ai-service.md §0 唯一入口。
- **TDD**：RED 调 ImageGen 编译失败/无实现 → GREEN 入口+provider。

## T9 — image_gen 显式计费 + pool-aware
- **描述**：`tool_image_gen.go` 改为：`creditService.Reserve(user, OpImageGen, estimate, 0, idemKey)`（pool 读 `BillingPoolFromCtx`）→ `aiservice.ImageGen(...)` → `FinalizeReservation(rsv, actualCost, opErr)`；失败 opErr→自动 Refund。删除裸 HTTP。
  **接线（S3 reviewer P1）**：`imageGenTool` 当前只有 `ds store.IStore`，无 creditService；`platformToolFactory`（`factory_platform.go:103` 构造 `&imageGenTool{ds:f.ds}`）也无 creditService 字段。须给 `platformToolFactory` 加 `creditService credit.ICreditService` 字段，从 `biz.go` 调 `NewPlatformToolFactory`/`...WithSkills` 处穿入，再注入 `imageGenTool`。
- **涉及文件**：`biz/agent/tool_image_gen.go`、`biz/agent/factory_platform.go`、`biz/biz.go`、`biz/agent/tool_image_gen_test.go`。
- **验收**：测试：成功路径 Reserve→ImageGen→Reconcile（积分扣）；失败路径→Refund（不扣）；test run→admin_test 池。编译通过（factory 接线完整）。`go test` 绿。
- **TDD**：RED 断言 Reserve 被调用（当前无）→ GREEN 接线。依赖 T1/T3/T8。

## T10 — migrations + GORM model
- **描述**：M1 `pricing_rule` 插入 `('image_gen','dmxapi','gemini-2.5-flash-image','flat','call', price_per_call=<占位+注释 TODO 运营定>)`（幂等 + rollback）；M2 `agent_run` 加 `is_test BOOLEAN NOT NULL DEFAULT false`（rollback）；`model.AgentRun` 加 `IsTest bool gorm:"default:false"` 字段（default false ⇒ GORM default:true 坑不适用；但 Create 显式置值避免歧义）。**并 populate（S3 reviewer P2）**：`student_run_lifecycle.go` 的 `preRun := &model.AgentRun{...}`（~:305/475）显式 `IsTest: req.IsTest`，让试聊 run 在 DB 落审计标记（T7 已把 IsTest 带到 RunRequest，T10 model 就绪后此处 populate 才能编译）。
- **涉及文件**：`migrations/<M1>.sql` + `migrations/<M1>_rollback.sql`、`migrations/<M2>.sql` + rollback、`internal/pkg/model/agent_run.go`（或所在 model 文件）、`biz/agent/student_run_lifecycle.go`（populate preRun.IsTest）。
- **验收**：migration 在 dev 跑通幂等；`agent_run.is_test` 列存在；pricing_rule 行存在。model 字段 AutoMigrate 兼容；试聊 run 的 `agent_run.is_test=true` 落库。（注：S5 在 dev 应用 migration。）
- **TDD**：model 字段加最小读写测试（in-memory sqlite AutoMigrate）+ IsTest=true persist 断言。

## T11 — cancelable detached ctx 增强
- **描述**：run 的 detached ctx 从 `context.Background()` 改为 `context.WithCancel(context.Background())`，cancel func 注册进既有 cancels 注册表；用户 cancel / run terminal 时 cancel → in-flight LLM 调用流式退款及时触发（sweeper 仅最后兜底）。
- **涉及文件**：`biz/agent/student_run_lifecycle.go`、`biz/agent/runner*.go`（cancels 注册表）、`*_test.go`。
- **验收**：测试：run 进行中 cancel → reservation 在合理时间内 Refund（不依赖 1h sweeper）。`go test` 绿。**与 b2b2c ctx 改动协调，merge 后核对**。
- **TDD**：RED cancel 后 reservation 仍 reserved → GREEN cancelable ctx + 触发退款。

## T12 — 集成 + 回归（biz 层端到端）
- **描述**：跨组件集成测试（mock store + mock aiservice）：(a) 多轮 run → N 次 Chat → N 对 Reserve/Reconcile 无遗漏/无重复/幂等；(b) 余额不足 → Reserve 失败 → run 优雅终止（error_max_budget）不再调大模型；(c) crash/cancel/timeout → 无 stuck reserved（sweeper 介入归零）；(d) **回归**：SOP/chatbot 路径计费金额与行为不变。
- **涉及文件**：`biz/agent/*_integration_test.go`、`biz/credit/*_test.go`、可能 `biz/sop`/`chatbot` 回归断言。
- **验收**：全部上述测试 + `go test ./... -race` 绿；`task lint` 退出 0。
- **TDD**：以测试驱动，缺一补一。

## T13 — S5 验证策略（NDF Rule 10，独立 task）
- **验证方式**：**(1) Go 持久回归**（biz 层 race 单测 T1–T12，永久留库——支付高风险按 Rule 10 必需）+ **(2) dev 真实扣费黑盒**（dev 跑 agent run，前后查 `credit_cycle`/`user_booster_balance`/`trial_grant` 余额下降 + `credit_transaction` source_type 区分池 + admin试聊查 `credit_admin_test_grant.used_amount`）+ **(3) Langfuse trace** 验各 generation 仍记录。
- **理由**：纯后端计费逻辑，核心可度量项是 DB 余额/事务行，Go 单测给持久回归保护（Rule 10 支付高风险硬要求，gstack /qa 一次性无回归保护不达标）；dev 黑盒验真实运行时（方法论铁律：不信单测，prod-shape 验证）。
- **关键路径**：① student 多轮 run（含 web_search+vision+image_gen+压缩）→ 余额下降 ② 余额置低 → run 优雅终止 ③ 父账户 IsTest 试聊 → admin_test 池增、三池不变 ④ 子账户跑父 agent → 扣子账户（对齐 b2b2c）⑤ cancel run → reservation 及时 refund。
- **Playwright（可选）**：前端 budget 弹窗/余额 UI 可 Playwright 验，但 dev「从零建 agent」422 bug → 用 seed agent / API 造 agent 绕过；前端非本任务核心，列可选。
- **环境**：仅 dev（9091），**禁碰 prod**；dev MySQL 经 `$DEV_SSH_*`。

---

## Gate 自检（S3）
- [x] 每 task 有 编号/标题/描述/涉及文件/验收条件
- [x] 覆盖 spec 全部需求（§7 PRD 核对逐条有 task 落点）
- [x] AI 功能条件：T5/T8 保留 trace 拓扑；T13 验证 Langfuse generation（引用 ai-service.md）
- [x] 依赖无环（上方 DAG）
- [x] Rule 10：T13 S5 验证策略独立 task，含方式/理由/关键路径
- [x] 原子性审查（独立 Sonnet reviewer）— VERDICT=PASS_WITH_FIXES，5 项已修（见 DAG 节注）
