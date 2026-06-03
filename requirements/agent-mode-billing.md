# Agent Mode 真实积分扣费接通（BLK-2）

## 来源
- 提出人：产品（agent mode 上线前 prod-readiness 评审，§0.1 红线 BLK-2）
- 提出日期：2026-06-03

## 需求描述

产品已拍板：**v1 上线 agent mode 必须真实扣费**。当前 agent run 跑完**完全不扣任何真实积分**，是上线红线（go-live blocker）。

第一手确认的根因有两处，都要修：

1. **真实扣费链路从未接通**。平台唯一真实扣减积分（Reserve/Reconcile 两阶段，写
   `credit_reservation` / `credit_cycle` / `user_booster_balance` / `trial_grant`）的入口是
   aiservice 网关的 `ContextBudgetCredits` 中间件
   （`internal/pkg/aiservice/middleware/context_budget.go:398`）。它在函数第一行
   `if len(chatReq.ContextFragments) == 0 { return next(...) }` 直接短路。而 agent 的 LLM 适配器
   `aiserviceAdapter.convertToAiserviceRequest`（`internal/numind/biz/agent/adapter.go:191-253`）
   **只设 Messages/Tools，从不设 ContextFragments**，run 入口也只 `middleware.NewContextWithUserID`、
   不注入 billing operation。⇒ agent 每次 LLM 调用都被透传，不 Reserve、不 Reconcile，
   用户三池余额纹丝不动。只有内层 `Billing` 中间件写 `usage_record`（纯用量，不扣费）。
   工具内部的 `aiservice.Chat`（analyze_image / annotate_image / compaction 等）同样缺 ContextFragments；
   `image_gen` 更是裸 HTTP 打 DMXAPI，完全绕过 aiservice。

2. **内存预算护栏也失效**。`biz/budget` 的 `BudgetTracker` 的 MaxCredits/MaxTurns 维度生产恒为 0：
   - `biz/agent/budgetgate.WrapHooks` 在 `biz.go:343` 调用时**没传 `WithUsageLookup`**，
     `usageLookup` 恒 nil，PostToolCall 回落到 `tokensFromOutput`（解析工具输出 JSON），real token 拿不到 → `RecordUsage(tokens=0)`。
   - 适配器在 `runner.go` / `runner_runstream.go` 用 struct 字面量构造（绕过 `NewAiserviceAdapter`），
     `usageStore` 恒 nil → 即便接了 lookup 也 stash 不进。
   - `BudgetTracker.RecordStep`（MaxTurns 维度）**全仓库无 caller**。
   - 只剩 wall_time（900s）兜底。

## 业务目标

让 agent 的**每一次真实 LLM 调用**（ReAct 主循环的 chat + 工具内部的 `aiservice.Chat`）走真实
Reserve/Reconcile 扣费，**扣减发起 run 的那个 userID 对应用户的积分池**（子账户跑就扣子账户自己的——
对齐并行任务 #4「子账户访问」：扣减对象 = 发起 run 的 `agent_run.user_id`，**不是** agent 的 `ParentUserID`）。
同时接通内存 BudgetTracker 的 MaxCredits/MaxTurns 维度，让预算熔断真正生效。

满足上线 Gate（§6）：「agent run 真实扣费」从红线转为已关闭。

## 优先级
**高**（go-live blocker，上线 Gate exit criteria 之一）

## Triage
- 推荐轨道：**Standard**
- 分类理由：
  1. 数据库 schema 变更：**可能**（Reserve 写 reference_id / 新 operation 枚举值；是否需新 migration 待 S2 定）
  2. 新增 API 端点：否（复用现有 run 入口；budget 阈值经现有 extend-budget/terminal 回传）
  3. 新外部服务集成：否（沿用 aiservice 既有链路；image_gen 若纳入是「从裸 HTTP 收编进 aiservice」而非新服务）
  4. 影响文件数：**>3**（adapter.go / runner.go / runner_runstream.go / biz.go / budgetgate / 工具若干 + 测试）
  5. 高风险业务逻辑（支付/权限）：**是**（直接扣减用户真实积分，信任边界跨 agent / aiservice 网关 / billing / credit 多域）
- 人类决定：**Standard**（任务交付说明已预判，与 triage 一致；高风险 + 跨多文件，边界情况亦默认 Standard）

## 范围边界（待 S1/S2 细化，此处先框定）

**必做（in scope）**
- 方向 A vs B 二选一并论证（S2）：
  - **方向 A（初判推荐）**：适配器在 `convertToAiserviceRequest` 设 ContextFragments + run 入口注入 billing operation context，
    让 `ContextBudgetCredits` 对每次 agent LLM 调用自动 Reserve/Reconcile；工具内部 `aiservice.Chat` 一并接通。
  - **方向 B**：runner 层显式包 `creditService.Reserve/Reconcile`（更可控，但要自己处理多次调用 + 异常退款）。
- 接通内存 BudgetTracker：`biz.go` 给 budgetgate 传 `WithUsageLookup`、修 adapter `usageStore` nil、runner 调 `RecordStep`，
  让 MaxCredits/MaxTurns 维度真正生效。
- 异常下不泄漏 reservation：run 用 detached `context.Background()`（`student_run_lifecycle.go`），HTTP cancel 不传播 →
  退款触发重新评估；依赖 `reservation_sweeper`（status=reserved 超 1h 自动 Refund）兜底。
- 余额不足：Reserve 失败时 run 优雅终止（已有 `error_max_budget` 终态 + 前端 budget 弹窗）。

**待决（S1 与用户确认 / S2 论证）**
- `image_gen` 裸 HTTP 打 DMXAPI 是否本任务顺带收编进 aiservice（已知 tech debt）。
- admin「试聊」`ReserveAgentTest`/`ReconcileAgentTest`（admin_test 池，目前无 caller）是否纳入——
  父账户在 Builder 试聊也应记账供 B2B 月末对公结算。

**不做（out of scope）**
- BLK-1（权限后门，姊妹 hotfix `remove-permission-backdoor` 在做）、BLK-3/4/5。
- 前端 currentRun 占位（已修）；budget 阈值前端弹窗仅需后端真实扣费后回传阈值。
- 不碰 config_prod.yaml；不改扣费金额算法/路由/trace 拓扑（只接通，不重设计计费规则）。

## 关键代码锚点（S0 勘察已确认，file:line）
- 短路点：`internal/pkg/aiservice/middleware/context_budget.go:398`
- 扣费 API：`biz/credit/credit_service.go:91`（Reserve）/ `:984`（FinalizeReservation）/ `:161,208`（Agent试聊）
- 中间件链：`internal/pkg/aiservice/middleware/chain.go:204`（ContextBudgetCredits → Billing → Retry）
- 适配器缺口：`biz/agent/adapter.go:191-253`（无 ContextFragments）
- run 入口：`runner.go` / `runner_runstream.go`（仅 NewContextWithUserID，无 billing op）
- detached ctx：`student_run_lifecycle.go`（context.Background）
- budgetgate 接线缺口：`biz.go:343`（WrapHooks 无 WithUsageLookup）
- sweeper 兜底：`biz/credit/reservation_sweeper.go`（5min 扫，reserved>1h Refund "expired_by_cron"）
- **现成同款先例**：`credit-log-task-names`（Standard, 已上线）用 `aismw.WithReservationRef` 把业务引用穿过同一计费网关；
  S2-D4 记录关键坑——`billing.WithBilling` 会覆盖 `BillingCtx`，必须用专用 ctx key（非 billing.Meta）。

## 备注
- 测真实运行时行为，不信文档/规格/现有单测（prod-readiness 方法论铁律；单测因 test.v 跑真管线全绿但 prod 行为相反）。
- 测试聚焦 biz 层（mock store + mock aiservice）：扣费成功 / 余额不足终止 / 多轮多扣正确 /
  crash·cancel 退款无泄漏 / Reconcile 多退少补。NDF Rule 10：支付高风险，S5 强验证（Go/Playwright 持久回归，非一次性 /qa）。
- 验证仅在 dev（9091），**禁碰 prod**。dev 父账户 admin/admin123456（user id=30），名下当前无 agent；
  dev「从零创建智能体」有 422 bug（问卷 q6/q7/q12 不渲染）→ 验证可能靠 seed agent 或直接 API 造。
