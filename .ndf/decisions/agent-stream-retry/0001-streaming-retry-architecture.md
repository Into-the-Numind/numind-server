# ADR 0001 — 流式 LLM 重试/降级架构选型

- 日期：2026-06-12
- 状态：已采纳（Accepted）
- feature：agent-stream-retry
- 决策者：AI（用户授权技术决策自主）+ 独立架构 reviewer subagent 压测（置信度 8/10）

## 背景

流式 LLM 调用的 idle timeout 错误（`errno.ErrAIProviderTimeout`）是**异步**地作为 `ChatChunk.Err` 出现在 channel 上的，而同步中间件链（含现有 Retry / Fallback）在「建流成功」时就带着 channel 同步返回了（gateway.go:336-355 同步 err=nil）——所以现有 Retry（retry.go）和 Fallback（fallback.go）**看不到**这个异步错误，不会触发重试/降级。这是核心难点。

中间件链顺序（外→内）：Tracing → Fallback → **ContextBudgetCredits(Reserve)** → Billing → **Retry** → Adapter。

关键事实：**Reserve 在 ContextBudgetCredits 内只发生一次**（在 next() 之前，context_budget.go:511/842）。流式时 `wrapStreamForContextBudget` 包裹 channel，终止错误 chunk 且 `Usage==nil` → 自动 Refund（context_budget.go:1007-1013）。

## 候选方案

- **Option 1（网关层包一层）**：在 Gateway.ChatStream 内重试，重调整条 chain → 第二次 Reserve（虽 primary 自动 refund 净额正确，但多一条 reservation + 多一条 UsageRecord，脏账）。
- **Option 2（agent 适配器层）**：在 aiserviceAdapter.Stream 包，比 O1 更外层，每次重试完整 Reserve；且 agent 适配器层拿不到 route/provider 抽象，需求 #2「同模型换供应商」几乎做不到。
- **Option 3（既有 Retry + Fallback middleware 流式化）**：✅ 采纳。

## 决策：Option 3

理由（计费正确性是第一驱动）：唯一把**同供应商重试放在 Reserve/Billing 之内**（Retry 层，重调 next()=Adapter，零二次 Reserve/UsageRecord）、把**跨供应商降级放在 Reserve 之上**（Fallback 层，fresh Reserve 是不同供应商的正确扣费，primary 自动 refund）的方案。Option 1/2 都把计费正确性外包给「primary refund 兜底」，留脏 reservation/UsageRecord 行。

### 三个分层的职责

1. **Retry middleware 流式化**（retry.go）：当 next() 返回 `<-chan ChatChunk` 时，用共享 reattempt 助手消费它；首个内容 chunk（含 ReasoningDelta）之前若收到 retryable error 终止 chunk → 重调 next()（同 route 同供应商）≤1 次；对外返回拼接好的单一 channel。在 Reserve/Billing 之下 → 零重复计费。
2. **Fallback middleware 流式化**（fallback.go）：同 peek 逻辑；首内容 chunk 前可重试错误 → cascade 到「同模型不同供应商」备用路由（fresh Reserve，skip_retry）。
3. **配套 C**（gateway.go）：ChatStream 改 per-route lookupProvider（对齐 Embed/Chat），删 NOTE(rerank-routing T1) 注释。对当前 agent.run 非硬阻塞（aihubmix/dmxapi 共用 dmxapi 适配器 + per-route 读凭据），但作纵深防御 + 防未来异构 provider。
4. **配套 D**（registry）：新增 `ResolveModelAlternates` 返回 primary 模型全部 active route（按 priority DESC，排除已试的 primary provider），surface deepseek-v4-pro 的 dmxapi 备用路由。数据已存在（is_active=1），无需新数据。

### 全链路计费追踪（最坏路径）

1. ContextBudget Reserve R1（primary aihubmix）。
2. Adapter attempt1（aihubmix）→ idle error 首 chunk 前。
3. Retry 吞掉错误 chunk，drain 旧 channel，重调 Adapter attempt2（aihubmix 同 route）→ 又 idle error。
4. Retry 重试已用尽 → 透传错误终止 chunk 向上。
5. Billing 写 UsageRecord（0-token, aihubmix）。ContextBudget refund R1。
6. Fallback 收到首 chunk 前错误 → cascade：重调 next(skip_retry, dmxapi route) → fresh Reserve R2（dmxapi）。
7. Adapter attempt3（dmxapi）→ 成功，内容流出，ContextBudget reconcile R2，Billing 记 dmxapi usage。

总 upstream 调用 = 3（aihubmix×2 + dmxapi×1），符合 ≤3 设计（WithSkipRetry, retry.go:79）。净扣费：R1 退、R2 对账（仅成功那次扣一次）。2 条 UsageRecord（aihubmix 0-token + dmxapi 真实），如实记录两次 upstream 事件，财务净额正确。

## MUST-HANDLE（reviewer 给出，实现时逐条满足）

- **P0-1**：Retry 必须吞掉失败 channel 的 error 终止 chunk，绝不冒泡到 Billing/ContextBudget（否则 0-token UsageRecord + refund 后又透传成功内容 = 免费 LLM）。对外 channel 上有且仅有一个终止 chunk。
- **P0-2**：重试前 drain 旧 Adapter channel（`for range oldCh {}`），让 runOAIStream `defer r.Close()` 执行，防 HTTP body / goroutine 泄漏。
- **P0-3**：content-dedup 用局部 `firstContentForwarded bool`；任何 `Delta!=""||ReasoningDelta!=""` 即置真并永久禁用重试；单 goroutine 顺序消费零竞态。ReasoningDelta（思考模型先吐 reasoning）必须计入。
- **P0-4**：skip_retry early-return 保留（retry.go:136-138）——fallback 候选不再被同供应商重试，保 upstream ≤3。
- **P1-5**：重试/降级判定只看 `chunk.Err` 是否 retryable + `!firstContentForwarded`，**不看 Usage**（idle 终止 chunk 带 lastUsage，可能非 nil）。Retry 吞掉中间错误 chunk 后 ContextBudget 只见最终终止 chunk。
- **P1-6**：Fallback 也要做 channel-peek（现有 Fallback 是同步的，流式根本不触发——bug 根源）。
- **P1-7**：registry 新方法返回同模型全部 active route；agent.run role=fallback 绑定为空，必须靠它拿 dmxapi。
- **P2-8**：Config C 照 Chat/Embed 模板，安全；无人依赖锁定行为。
- **P2-9**：可观测性——重试/fallback 多次 upstream 应每次一个 generation span。fallback 已有 ctxKeyFallbackFromServiceID；同供应商重试加 attempt 计数 ctx 标记供 Tracing/Langfuse 区分（非阻塞）。

## 零回归约束

流式感知逻辑对非 agent caller（chatbot/sop/salesrag）必须纯增量、行为兼容：成功流（首 chunk 即内容、无 error）路径退化为透明转发，字节级等同现状。spec 验收纳入；对 chatbot/sop/salesrag 各跑一条「正常流式不受影响」回归测试。
