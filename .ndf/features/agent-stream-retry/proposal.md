# agent-stream-retry — 提案

## §1 方案概述 [客户可见]
agent mode 的流式回答如果在「还没开始吐字」时遇到供应商瞬时卡顿（60s 无响应），系统会自动、无感地重试一次；同一家供应商还是不行，就自动换到同一个模型的另一家供应商继续。整个过程用户看不到任何中断，只会看到回答正常出现。一旦已经开始吐字才卡住，则维持现状快速失败（本期不处理中途断流）。

## §2 报价与周期 [客户可见]
- 预估工作量：1 天（后端）
- 内部可靠性硬化，无对外报价。
- 交付：landed develop + 部署 dev 验证。

## §3 技术可行性 [AI 内部]
### 现有功能复用
- 复用既有 Retry middleware（retry.go：retryableError / WithSkipRetry / 单次重试 + backoff）。
- 复用既有 Fallback middleware（fallback.go：多级 cascade + skip_retry + fallback provenance）。
- 复用既有计费两阶段（ContextBudgetCredits Reserve/Reconcile/Refund + wrapStreamForContextBudget；Billing wrapStreamForBilling）。
- 复用 Part A 的 idle watchdog（adapter/stream.go + stream_idle.go）产生的 `ErrAIProviderTimeout` 终止 chunk。
- 复用既有同模型多供应商路由数据（deepseek-v4-pro: aihubmix + dmxapi 已配置）。

### 技术风险
- **重复计费**（高）：缓解=同供应商重试放 Reserve 层之下（Retry middleware 内），结构性保证零二次 Reserve。详见 ADR 0001 + spec §AC。
- **重复内容透传**（中）：缓解=只在首个内容 chunk 之前重试 + 局部 firstContentForwarded 标记。
- **跨供应商实际未切 endpoint**（中）：缓解=配套修 C（per-route lookupProvider）+ dev 实测验证打到另一供应商。
- **共享 middleware 回归**（中）：缓解=成功流路径退化为透明转发字节级等同 + 对 chatbot/sop/salesrag 各跑回归测试。

### 涉及仓库
- [x] numind-server
- [ ] numind-web-v3
- [ ] numind-admin-web

### AI 可观测性（涉及 LLM 调用）
- [x] 涉及 LLM 调用：是
- Trace 起点：agent runner（已有 agent-runtime-run trace，runner.go:490）。
- Generation 点：每次 upstream LLM attempt（primary attempt1 / 同供应商 retry attempt2 / 跨供应商 fallback）理想各一个 generation span。fallback 已有 ctxKeyFallbackFromServiceID 区分；同供应商重试加 attempt 计数 ctx 标记（P2，非阻塞）。
- 关键元数据：user_id, agent_run_id, fallback_from_service_id, retry_attempt。

## §4 产品需求定义 — PRD [AI 内部]
### 用户故事
- 作为 agent mode 用户，我需要 LLM 流在开始吐字前的瞬时供应商卡顿被自动重试/换供应商，以便我不会因为一次抖动看到整个 run 失败/空泡。

### 验收标准
- AC1：流式调用在首个内容 chunk 之前收到 `ErrAIProviderTimeout`（retryable）→ 同供应商同模型自动重试 1 次。
- AC2：同供应商重试仍失败 → 自动降级到同模型另一供应商（deepseek-v4-pro: aihubmix→dmxapi）。
- AC3：所有尝试都失败 → 才向下游报错（终止 chunk 透传）。
- AC4：已透传过任何内容 chunk（含 ReasoningDelta）后再出错 → 不重试、原样透传（快速失败）。
- AC5：重试/降级**不产生**第二条 Reserve 预扣、不重复 Reconcile；同供应商重试路径上 UsageRecord 计费净额正确（重试不重复扣分）。
- AC6：重试/降级不重复透传内容 chunk（对下游消费者透明，对外仍是一个 `<-chan ChatChunk`）。
- AC7：总 upstream 调用数 ≤3（attempt1 + 同供应商 retry1 + 跨供应商 fallback）。
- AC8：跨供应商降级**真的**打到另一供应商 endpoint（dev 实测验证）。
- AC9：零回归——非 agent 流式 caller（chatbot/sop/salesrag）正常流路径行为不变。

### 边界情况
- 首 chunk 前先来 usage-only chunk 再卡顿（lastUsage 非 nil）：重试判定只看 Err+!firstContentForwarded，不看 Usage（Retry 吞掉中间错误 chunk，ContextBudget 只见最终终止 chunk）。
- ctx 被取消（用户主动停）：不重试（context.Canceled 非 retryable），走既有 refund。
- 无备用路由的模型：Fallback 拿到空 alternates → 无 fallback，透传原错误（与现状一致）。

### 权限规则
- 无新权限。计费走既有三池 + Reserve/Reconcile。

### UI 行为规格
- N/A（后端透明，前端无改动）。
