# Agent Mode 真实积分扣费接通（BLK-2）— 提案 + PRD

> 关联：requirements/agent-mode-billing.md（S0）。上线红线 BLK-2。仅 numind-server。

## §1 方案概述 [客户可见]

当前「智能体」（agent mode）跑一次会做多轮大模型调用 + 工具调用，但**全程不扣任何积分**——
用户余额、试用包、加量包纹丝不动。本次改造让智能体的**每一次大模型/AI 服务调用都真实计费**：
按现有「预扣 → 实际用量对账（多退少补）」两阶段机制扣减**发起这次对话的那个账号**的积分池
（子账户使用就扣子账户自己的）。同时让「单次对话积分上限 / 轮数上限」的熔断真正生效，
余额不足时对话优雅终止并提示，而不是免费跑到底。

父账户在「智能体搭建台」里**试聊**自己的智能体，也会从该父账户的 **B2B 试用额度池**记账，
供月末对公结算（不动用户的付费积分）。

预期效果：上线后智能体计费与 SOP / 销售对话 / 智能对话保持一致口径，杜绝「白嫖大模型」漏洞。

## §2 工作量与周期 [内部]

- 性质：内部 go-live blocker 修复（无对外报价）。
- 预估工作量：**4–6 人日**（含 S2 设计 + S4 TDD + S5 dev 验证）。
  - 拆分参考：方向 A 接线（适配器 + run 入口）≈1d；BudgetTracker 接通 ≈0.5d；
    pool-selector（student 三池 vs admin_test）中间件改造 + admin试聊接线 ≈1.5d；
    image_gen 收编 aiservice + 计费 + pricing rule + migration ≈1.5d；测试 + dev 验证 ≈1.5d。
- 风险缓冲：中间件 pool-selector 与 image_gen 非 chat 计费是不确定性来源，可能 +1d。
- 交付：先 dev 验证扣费真实生效，**不碰 prod**（上线授权另算）。

## §3 技术可行性 [内部]

### 现有功能复用（强）
- **ContextBudgetCredits 中间件**（`aiservice/middleware/context_budget.go`）已实现完整 Reserve→调用→
  Reconcile/Refund，含**流式 cancel 自动退款**（`ctx.Done()` 优先 select）+ 多退少补 + underestimate 吸收。
  只要 agent 的 ChatRequest 带上 `ContextFragments` + billing operation ctx，即自动接入（方向 A 核心）。
- **credit_service**：`Reserve`/`FinalizeReservation`/`Refund` + `ReserveAgentTest`/`ReconcileAgentTest`（admin_test 池，已实现仅缺 caller）。
- **reservation_sweeper**：reserved 状态超 1h 自动 Refund "expired_by_cron"，5min 扫——是 detached ctx 下漏退款的兜底网。
- **黄金先例 credit-log-task-names（已上线）**：用 `aismw.WithReservationRef` 把业务引用穿过同一计费网关；
  其 S2-D4 已踩坑并文档化——`billing.WithBilling` 会覆盖 `BillingCtx`，必须用**专用 ctx key**（非 billing.Meta）。本任务直接继承该经验。
- **chat-shaped 工具**：analyze_image / annotate_image / compaction 全走 `aiservice.Chat` → 方向 A 一并覆盖，无需逐个特判。
- **非 chat 计费先例**：OCR/ASR/Embed 经 Billing 中间件按 per-call/per-second 计 `usage_record`（pricing_rule 驱动）——image_gen 收编可复用此 pricing 机制。

### 技术风险与缓解
1. **R1 余额扣减仅发生在 ContextBudgetCredits（ChatRequest-only）**。非 chat 的 OCR/Embed/image_gen 经 Billing 中间件只写 usage_record **不扣三池**。
   ⇒ image_gen 不能靠「设 ContextFragments」计费。**缓解**：image_gen 收编 aiservice（新增 ImageGen 入口，满足「唯一入口」硬规则）+ 显式 Reserve/Reconcile（扁平 per-image 估算）+ 新增 pricing rule + 可能 migration。S2 论证「显式 Reserve」vs「扩展中间件支持非 chat 扁平计费」。
2. **R2 pool-selector（student 三池 vs admin_test 池）**。方向 A 把扣费推进共享中间件，而中间件当前写死 `creditService.Reserve`（三池）。admin试聊须走 `ReserveAgentTest`（admin_test 池）。
   **缓解**：经 ctx 注入「billing pool 选择器」（沿用 WithReservationRef 同款专用 ctx key 模式），ContextBudgetCredits 读后分支 Reserve vs ReserveAgentTest。是受控的中间件增量，不改既有 SOP/chatbot 行为（默认三池）。
3. **R3 admin试聊无 test 标识**。父账户走与 student 相同的 `POST /v1/agent-runs`，DB 层无法区分。
   **缓解**：`RunRequest` 加 `IsTest`（试聊入口置真），可选 `agent_run.is_test` 列（审计/migration）；运行时据此选 admin_test 池。S2 定是否落 DB。
4. **R4 detached ctx 退款不传播**。run 用 `context.Background()`，HTTP cancel 不传到 LLM 调用 → 流式 cancel 退款路径可能不触发。
   **缓解**：依赖 reservation_sweeper 兜底（最坏 1h 后退）；S2 评估是否给 run 建可取消的 detached ctx（cancel 注册表已存在）让退款及时。每次 LLM 调用独立 Reserve/Reconcile：已完成的调用各自已对账，泄漏面仅限「进程死时正 in-flight 的单次调用」。
5. **R5 一 run 多次 LLM 调用**。per-call Reserve/Reconcile，确保每次都扣且不重复；多轮别一次预扣爆。方向 A 天然 per-call（每次 aiservice.Chat 独立走中间件），符合预期。
6. **R6 与 b2b2c-student-agent-access 文件冲突**。两 feature 同改 runner.go/runner_runstream.go/biz.go/student_run_lifecycle.go。
   **缓解**：各自 worktree，串行 merge develop，冲突 merge 时解；S2/S3 与对方对齐 RunRequest 字段增改点，尽量减少同函数交叉。
7. **R7 estimate 偏差致首扣过大/不足**：沿用 R2 估算（ContextBudgetCredits 内 EstimateFragments token 估算）+ Reconcile 多退少补；余额不足 Reserve 失败 → run 优雅终止（error_max_budget 终态已存在）。

### 涉及仓库
- [x] numind-server
- [ ] numind-web-v3（前端 currentRun/budget 弹窗已另修；本任务后端真实扣费后阈值自然回传，无需前端改动）
- [ ] numind-admin-web

### AI 可观测性
- [x] 涉及 LLM 调用：是
- Trace 起点：agent run 已有 trace（runner 创建）——本任务不新建 trace，而是确保**每次计费的 LLM 调用仍在既有 trace 下**，且 Reconcile 不破坏 generation 记录。
- Generation 点：ReAct 主循环每次 chat、analyze_image/annotate_image vision、compaction、image_gen（收编后新增 generation）。
- 关键元数据：reservation reference（reserve 时写 `agent_run:<runID>` 类引用，复用 WithReservationRef）+ userID + operation（新增 `agent_run` operation 枚举）。
- 验证：S5 跑 run 后查 Langfuse trace 仍含各 generation + dev DB 查 credit_transaction.source_type 区分池。

## §4 产品需求定义 — PRD [内部 — 不简化]

### 用户故事
- 作为**终端使用者（student/子账户）**，我跑一次智能体对话，系统按实际大模型用量扣减**我自己**的积分池（三池：trial→cycle→booster FIFO），余额不足时对话优雅终止并提示，不让我免费无限跑。
- 作为**配置者（父账户）**，我在搭建台**试聊**自己的智能体，消耗记到我的 **B2B 试用额度池**（admin_test），供月末对公结算，不动子账户/付费积分。
- 作为**平台/财务**，我能在 credit_transaction / membership_event 看到 agent run 的真实扣减（source_type 区分 trial/subscription/cycle/booster），杜绝漏计费。

### 验收标准（可度量）
- [ ] 终端使用者跑一个多轮 agent run（含 web_search + 一次 vision 工具 + 一次 image_gen + 触发压缩）后：
      `credit_cycle.credits_remaining`（或 booster/trial）**下降**，且 `credit_transaction` 出现对应行（source_type 正确）。
- [ ] **每一次** LLM/AI 服务调用都计费：ReAct 主循环 chat、analyze_image、annotate_image、compaction、image_gen 各产生 Reserve+Reconcile（dev DB credit_reservation 行数 ≈ LLM 调用次数）。
- [ ] **多退少补正确**：构造预扣 > 实际 → 退差额；实际 > 预扣 → 补扣（或 underestimate 吸收审计行）。
- [ ] **余额不足终止**：把用户余额置低于一次 Reserve 估算 → 该 run 在 Reserve 失败处优雅终止（error_max_budget 或等价终态），不继续调用大模型。
- [ ] **异常无泄漏**：run crash / cancel / timeout 后，无长期 stuck 的 reserved reservation（sweeper 介入后归零）；正常完成的 reservation 全为 reconciled/refunded。
- [ ] **MaxCredits/MaxTurns 熔断生效**：构造超 MaxTurns 的 run → 在轮数维度中断（RecordStep 接通后）；超 MaxCredits → 在积分维度中断（RecordUsage 拿到真实 token 后）。
- [ ] **admin试聊记账**：父账户试聊自己 agent → `credit_admin_test_grant.used_amount` 增长，**不动**该父账户三池余额。
- [ ] **扣减对象正确**：子账户跑父账户 agent → 扣子账户三池，父账户余额不变（对齐 b2b2c）。
- [ ] **回归保护**：扣费成功 / 余额不足 / 多轮多扣 / crash·cancel 退款 / 多退少补 各落 Go 单测（biz 层 mock store + mock aiservice），永久留库（NDF Rule 10 高风险）。
- [ ] **不破坏既有**：SOP / chatbot / salesrag 计费金额与行为**不变**（pool-selector 默认三池，未注入 selector 时与今日一致）；Langfuse trace 正常。

### 边界情况
- 一次 run 0 次工具调用（纯文本答）：至少 1 次主循环 chat 计费。
- image_gen 失败（DMXAPI 报错）：Reserve 后失败 → Refund（不扣费）。
- 压缩（compaction）中文长上下文触发：压缩这次 LLM 调用也计费。
- 并发多 run：各自 reservation 独立，pool/userID 不串（注意 NarrationRunID 池级共享是另一 bug，不在本任务）。
- admin试聊 admin_test 池耗尽：ReserveAgentTest 返回 ErrAdminTestExhausted → run 优雅终止。
- 幂等：同一 LLM 调用重试不重复扣（idempotencyKey；中间件已支持）。

### 权限/计费路由规则
- 扣减对象 = `agent_run.user_id`（发起者），经 `store.users.GetByID` 取 `*model.User`，**绝不**用 agent.ParentUserID。
- pool 路由：`IsTest`（父账户 Builder 试聊）→ admin_test 池（ReserveAgentTest）；否则 → 三池（Reserve）。
- 默认（无 selector）：三池，保证 SOP/chatbot/salesrag 既有路径零行为变更。

### UI 行为规格
- N/A（后端任务）。前端 budget 60%/100% 阈值弹窗依赖后端真实扣减后回传 Snapshot，本任务使其有真实数据；前端 currentRun 占位已另修。
