# agent-stream-retry — agent mode 流式 LLM 自动重试 + 同模型跨供应商降级

## 来源
- 提出人：用户（产品负责人）
- 提出日期：2026-06-12
- 关联前序工作：Part A「流式空闲超时」（已 landed develop + 部署 dev）。Part A 让 `runOAIStream` 挂了 idleWatcher，LLM 流 60s 无新字节即快速失败，往 channel 发 terminal chunk，错误 `errno.ErrAIProviderTimeout`（故意不用 `context.DeadlineExceeded`，后者 retryableError 判不可重试）。

## 需求描述
当 agent mode（莫小派第三模态；dev 上「定位调研助手」agent 100008）的流式 LLM 调用在**吐出首个内容 chunk 之前**发生 idle timeout，让它**自动重试**而不是直接判整个 run 失败。

用户已拍板硬参数（不再讨论）：
1. 空闲超时阈值 = 60s（Part A 已是此值，沿用）。
2. 重试次数 = 总共 2 次尝试 = 原始 1 次 + 重试 1 次（对**同一供应商同一模型**）。
3. 同供应商重试仍失败 → fallback 到**同一模型的另一个供应商**（同模型跨供应商）。
4. **只在「首个内容 chunk 之前」重试**；一旦已吐内容才卡住（中途断流），这次**先不做**，维持快速失败。
5. 重试/降级对用户无感、无需用户干预（agent 自动重试）。

## 业务目标
上线前可靠性硬化的最后一环。现状缺口：Part A 卡 60s 后只是「更快地失败」，仍会把整个 agent run 判失败、用户看到空泡/报错。本需求让瞬时供应商抖动（首 chunk 前 stall）对用户透明，提升 agent mode 上线可用性。

## 优先级
高（上线前阻塞项）。

## 现状调查结论（动手前已复核 A/B/C/D 四锚点）

**A — idle 超时错误（确认）**：`runOAIStream`（adapter/stream.go:199-211）watchdog tripped 时发 `IsFinal=true` + `Err=fmt.Errorf(...%w, errno.ErrAIProviderTimeout)` 的终止 chunk；`retryableError`（middleware/retry.go:63）判它可重试。

**B — WithFirstChunkSent 半接通（确认）**：全库 grep——`WithFirstChunkSent`/`ctxKeyFirstChunkSent` 只被 billing.go（行 140/493，当前 dormant：无人 set 故估算分支从不触发）与 sync Retry middleware（行 146，同步路径看不到异步流错误）**读取**，**无任何流式消费处 set 它**。"设计了一半没接通"属实。

**C — 跨供应商流式限制（确认+精确化）**：`ChatStream`（gateway.go:318-323）构造时锁定 primary 适配器实例，handler 闭包用固定 `chat`；而非流式 `resolveAndRun`/Embed（gateway.go:225/369）是 per-route `lookupProvider`。但适配器 base_url/api_key 来自 `route.Provider.*`（dmxapi.go:365/373/396/404），不是实例字段——锁定的只是"用哪套 wire 构造逻辑"。又因 `findAdapterByPrefix`（gateway.go:174-177）"all OpenAI-compatible providers can use dmxapi adapter" + alias，aihubmix/dmxapi/dmxapi-ssvip 共用同一 dmxapi/OAI 适配器，对 agent.run 该限制实际为哑。正确修法仍是让 ChatStream per-route lookup（对齐 Embed），防未来真换适配器类型（如 volc-ark）。

**D — fallback 路由（确认，好消息）**：agent.run（task_profile id=15）primary 模型 = `deepseek-v4-pro`（ai_service id=24）。该模型**已有 2 条 ai_service_route——同模型两供应商**：`aihubmix`/`deepseek-v4-pro`/priority 10（当前 primary）+ `dmxapi`/`deepseek-v4-pro-guan`/priority 5。"同模型换供应商"目标数据已存在，**无需新配路由数据**。缺口在解析层：`GetResolvedRoute`（store.go:336）`ORDER BY priority DESC LIMIT 1` 只取 aihubmix，dmxapi 备用路由 dormant；task 级 role=fallback 绑定为空（10 条均 role=allowed 给 ModelSelector）。需在 registry 增「列出 primary 模型全部路由按 priority」能力把备用路由 surface。

**关键计费洞察**：Reserve 在 `ContextBudgetCredits`（Retry/Adapter 之外）只发生一次；首 chunk 前 idle 失败时终止错误 chunk 经 `wrapStreamForContextBudget`（context_budget.go:1007-1013）自动 Refund。→ 同供应商重试若放在 **Retry middleware 内部**（Reserve/Billing 之外），重建流只重调 Adapter，不产生第二条 Reserve、不产生第二条 UsageRecord（Billing/ContextBudget 只包裹 Retry 返回的单一 channel）。这是满足「绝不重复计费」硬约束的关键。

## 需要做的两块
1. **流式重试包装器**：消费 gateway 返回的 channel，在首个内容 chunk 之前若收到可重试错误（retryableError==true，主要是 `errno.ErrAIProviderTimeout`）就重新建流：先同供应商同模型重试 1 次；仍失败再降级同模型另一供应商。一旦透传过任何内容 chunk，即 set WithFirstChunkSent 语义、不再重试（中途断流不处理）。对下游消费者透明（对外仍是一个 `<-chan ChatChunk`）。
2. **fallback 路由配置/surface**：确保 agent.run 有「同模型不同供应商」降级路由可用（数据已存在=deepseek-v4-pro on dmxapi；需解析层 surface，不需新数据）。

## Triage
- 推荐轨道：**Standard**
- 分类理由（5 条标准自核）：
  1. 数据库 schema 变更：**否**（备用路由数据已存在，无 DDL；仅 registry 查询层改动）
  2. 新增 API 端点：**否**
  3. 新外部服务集成：**否**（dmxapi / deepseek-v4-pro 已在用）
  4. 影响文件数：**>3**（gateway.go + retry.go + fallback.go + registry + 测试）→ FAIL
  5. 高风险业务逻辑（支付/权限）：**是**——直接是 Reserve/Reconcile 计费正确性 → FAIL
- 人类决定：**确认 Standard**（2026-06-12，经 AskUserQuestion）

## 关键风险（S2/S4 review 重点盯）
- **重复计费**：重试=第二次建流，绝不能产生第二条 Reserve 预扣或重复 Reconcile（架构倾向把同供应商重试放 Reserve 层之下规避）。支付/计费高风险。
- **重复内容 chunk**：包装器吞掉失败那次流时，必须保证没有半截内容已透传给下游又重发（故限定「首个内容 chunk 之前」才重试）。
- **跨供应商限制（C）**：降级若实际没切供应商等于白做，必须实测验证真的打到另一供应商 endpoint。

## 备注
- TDD 强制（task 要求）：先写失败测试（fake stream：首 chunk 前发 `errno.ErrAIProviderTimeout`→断言触发重试；重试再失败→断言降级到另一供应商；已吐内容后再失败→断言不重试、直接透传）。Part A 的 stallingReader 测试范式可参考（adapter/stream_idle_test.go）。虽属内部可靠性硬化而非纯客户 bug，但 run 138 是真实 dev 观测——首 commit 采用 `test(qa): reproduce ...` 复现测试前缀，满足 Rule 11。
- 范围：**仅 numind-server**，develop 分支，**绝不部署 prod**（除非用户明确指示）。
- 验证账号：admin / admin123456，dev 站点用「定位调研助手」跑端到端。
- 架构选型（Option 1 网关包一层 / Option 2 agent 适配器层 / 既有 Retry+Fallback middleware 流式化）在 S2 由独立 reviewer subagent 压测后拍板，理由写清。
