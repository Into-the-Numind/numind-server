# agent-mode-billing — S5 验证策略（T13, NDF Rule 10）

> 支付高风险 feature → 必须有持久回归（Go test），不能只一次性 /qa。本文档定义 S5 验证方式 + 理由 + 关键路径。

## 验证方式（三层）

### 1. Go 持久回归（已落地 S4，永久留库）
S4 每个 task 的 TDD 测试即回归套件：
- **credit 层**：`credit_service_pool_test.go`（admin_test vs 三池路由 / Reconcile/Refund 按 operation=agent_test 路由 / under-reserve topup / 幂等 / 三池回归不变）+ `operation_agent_test.go`（枚举/估算）+ 全量 `credit_service_*_test.go -race` 回归（三池逐字不变）。
- **aiservice 中间件**：`billing_pool_test.go`（pool ctx 专用 key 不被覆盖 + 跨包 const equality 守卫）+ `bill_only_test.go`（bill-only 保留 tool 结构 messages + Reserve）+ `bill_only_integration_test.go`（pool 线程 + per-call 计费 + 非 agent 短路回归 + cancelled-ctx 退款持久化）。
- **agent 层**：`billing_ctx_test.go`（injectAgentBillingCtx: operation/ref/pool/bill-only + IsTest→admin_test + queryCtx 保值可取消）+ `call_usage_store_test.go`（read-and-delete）+ `budgetgate/gate_record_step_test.go`（RecordStep→MaxTurns 熔断）+ `tool_image_gen_billing_test.go`（image 计费 nil-safe + 余额不足 soft error）。
- **model**：`agent_run_test.go`（is_test 持久化）。
- 运行：`go test ./... -race`（S5 gate）。

**理由**：纯后端计费逻辑，核心可度量项是 DB 余额/事务行 + 路由分支；Go 单测给永久回归保护（Rule 10 硬要求；/qa 一次性无回归保护不达标）。

### 2. dev 真实扣费黑盒（S5/S6 在 dev 执行）
部署 dev 后跑一个真实 agent run，前后查 DB：
- **student run（三池）**：跑含 web_search + 一次 vision 工具 + 一次 image_gen + 触发压缩的 run → 查 `credit_cycle.credits_remaining`（或 `user_booster_balance`/`trial_grant`）**下降** + `credit_transaction` 新增行（`source_type` ∈ trial/cycle/booster）+ 行数 ≈ LLM 调用次数（per-call）。
- **父账户试聊（admin_test）**：父账户 IsTest=true 跑自己的 agent → `credit_admin_test_grant.used_amount` 增、三池**不变**；reservation `operation='agent_test'`。
- **子账户**：子账户跑父 agent → 扣**子账户**三池，父账户余额不变（对齐 b2b2c）。
- **余额不足**：把用户余额置低 → run 在 Reserve 失败处优雅终止（不再调大模型）。
- **MaxTurns 熔断**：构造超 MaxTurns 的 run → 在轮数维度中断。
- dev MySQL 经 `$DEV_SSH_*` SSH 查询。

**理由**：方法论铁律——测真实运行时（不信单测，prod-shape 验证）。

### 3. Langfuse trace（dev）
跑 run 后查 Langfuse：各 generation 仍记录（runner CreateTrace 起点 + Tracing 中间件 generation 不被计费改动破坏）。

## 关键用户路径（S5 必验）
1. student 多轮 run → 三池余额下降 + per-call credit_transaction
2. 余额置低 → run 优雅终止（不白嫖）
3. 父账户 IsTest 试聊 → admin_test 池增、三池不变
4. 子账户跑父 agent → 扣子账户（对齐 b2b2c）
5. cancel run → reservation 及时 refund（WithoutCancel 持久化，不等 1h sweeper）
6. SOP/chatbot/salesrag 计费金额不变（回归）

## 已知限制（S5 诚实声明）
- **streaming MaxCredits 漏计 final-answer tokens**：`adapter.Stream`（生产 SSE final-answer turn）不 stash usage → 内存 MaxCredits 仅计 tool-deciding Generate turns。**真实三池扣减不受影响**（aiservice ChatStream reconcile）；MaxTurns 全路径生效。完善 = follow-up。
- **T8 未做**：image_gen 仍裸 HTTP 打 DMXAPI（无 Langfuse trace，违 ai-service.md §0 唯一入口）；**计费已生效**（显式 Reserve/Reconcile）。aiservice.ImageGen 收编 = follow-up。
- **image pricing rule 占位价**：M1 price_per_call=0.30 占位（T9 实际用扁平 credit=10，不读 pricing_rule）；T8 收编后才消费此价，需运营确认。

## Playwright（可选）
前端 budget 弹窗/余额 UI 可 Playwright 验，但 dev「从零建 agent」422 bug → 用 seed agent / API 造 agent 绕过；前端非本任务核心，列可选。

## 环境
仅 dev（9091），**禁碰 prod**。
