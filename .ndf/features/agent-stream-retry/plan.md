# agent-stream-retry — 实施计划 Plan

> 输入：spec.md + ADR 0001。全部 numind-server，单 worktree 串行实现（共享 middleware 包 = Tier 4，禁并行 implementer）。每 task RED→GREEN + 双 Sonnet reviewer 并行（Tier 1）。

## 依赖图
T0(RED) → T1(helper+Retry, repro 转 GREEN) → {T2(registry) , T4(gateway C)} → T3(Fallback, 需 helper+alternates) → T5(集成+验证)

T2、T4 互不依赖（可任意先后，但同 worktree 串行做）。T3 依赖 T1(helper) + T2(alternates)。

---

## T0 — RED 复现测试（Rule 11 首 commit）
- **文件**：`internal/pkg/aiservice/middleware/stream_reattempt_test.go`（新）
- **内容**：构造 fake handler 返回 channel，首 chunk 前发 `IsFinal+Err=ErrAIProviderTimeout` 终止 chunk。用现有 `Retry(deps)` 包裹，断言 handler 被调用 **2 次**（原始+重试）。
- **当前预期**：FAIL（现状 Retry 对流式 channel 直接透传，handler 只调 1 次）。
- **commit**：`test(qa): reproduce streaming idle-timeout not retried before first chunk`
- **验收**：测试存在且 FAIL；commit message 带 `test(qa):` 前缀。

## T1 — 共享 reattempt 助手 + 流式 Retry
- **文件**：`internal/pkg/aiservice/middleware/stream_reattempt.go`（新）+ `retry.go`（改 retryWithPolicy）
- **内容**：实现 §2 `wrapStreamWithReattempt`（含 P0-1/2/3、P1-5 不变量）；retryWithPolicy 流式分支用它（reattempt 计数≤1，backoff=policy.retryDelay）；保留 skip_retry early-return（P0-4）+ 非流式同步路径不动。
- **验收**：T0 复现测试转 GREEN；新增单测——重试成功透传后续内容；重试再失败透传单一 error 终止；已吐内容(含 ReasoningDelta)后 error 不重试；旧 channel 被 drain（用计数 reader 验证）；skip_retry 时不重试。
- **review 重点**：P0-1 错误 chunk 不冒泡 / P0-2 drain / P0-3 firstContentForwarded 含 ReasoningDelta / P1-5 不看 Usage。

## T2 — registry 同模型 alternates
- **文件**：`registry/store.go`（新 `ListResolvedRoutesByModel`）+ `registry/registry.go`（接口 + impl `ResolveModelAlternates`）+ 4 个 test stub 加 `return nil,nil`（fallback_test.go / gateway_test.go / gateway_rerank_repro_test.go / biz/aiservice_admin/biz_test.go）
- **内容**：见 spec §6。store 方法复制 GetResolvedRoute JOIN 去 LIMIT 1；impl 过滤 excludeProviderID。
- **验收**：单测——给 deepseek-v4-pro 式 fixture（2 route：pri10 providerA / pri5 providerB），`ResolveModelAlternates(taskID, serviceID, providerA.id)` 返回 [providerB route]；无备用模型返回空 slice；编译通过（接口实现齐全）。
- **review 重点**：raw SQL 与 GetResolvedRoute 一致性 / is_active+deprecated 过滤 / 排除逻辑。

## T3 — 流式 Fallback（跨供应商降级）
- **文件**：`middleware/fallback.go`
- **内容**：见 §4。流式分支用 `wrapStreamWithReattempt`（reattempt 串行取 alternates，注入 skip_retry+fallbackFromServiceID，backoff=nil）；alternates 经 `deps.Resolver.(modelAlternatesResolver)` type-assert（graceful）；非流式 cascade 不动。
- **验收**：单测——首 chunk 前 error（已无同供应商重试，模拟 skip_retry 或 Retry 已耗尽）→ 断言 cascade 到 alt route（next 收到不同 Provider.Name/ServiceID 的 route）→ 成功透传；计费 mock 断言 primary refund + alt fresh reserve+reconcile；无 alternates 时透传原错误。
- **review 重点**：P1-6 流式 peek / fresh Reserve 正确性 / ≤3 upstream（P0-4 配合）。

## T4 — 配套 C：gateway.ChatStream per-route lookup
- **文件**：`gateway.go`（ChatStream handler 闭包）
- **内容**：见 §5。handler 内 `lookupProvider(r.Provider.Name)` + capability 检查；构造期 primary lookup 降为 fail-fast；删 NOTE(rerank-routing T1)。
- **验收**：单测——注册 2 个 mock ChatProvider（primary + 另一 provider），handler 收到 fb route 时调用 fb provider 的 ChatStream（断言 fb adapter.Name 被调）；现有 gateway 流式测试不回归。
- **review 重点**：与 Embed 模式一致 / 无人依赖锁定行为 / capability 错误 %w ErrAICapabilityMismatch。

## T5 — 集成 + 验证策略（对应 ndf-enforcement 规则 10）
- **集成测试**：`middleware/chain_test.go` 或新文件——全链路（Tracing→Fallback→ContextBudget→Billing→Retry→Adapter mock）：首 chunk 前 idle error ×2(同供应商重试也失败) → 降级 alt → 成功；断言 Reserve 计数（R1 refund + R2 reconcile）、UsageRecord 计数、内容不重复、upstream=3。
- **验证方式**：后端 Go TDD（`go test ./...` + `-race` 关键子树）+ dev 实跑（admin/admin123456 + 定位调研助手 agent 100008）。**理由**：纯后端流式/计费逻辑，无 UI 改动 → 不做 Playwright；计费高风险 → 必须 Go 单测覆盖 Reserve 不重复。
- **关键用户路径**（S5 验证）：见 spec §9（agent 正常流零回归 / agent 首 chunk 前卡顿自动恢复 / chatbot·SOP 流式零回归）。
- **回归保护诚实声明**：选 Go TDD 产生持久化回归测试（永久留库），非一次性；跨供应商「真切 endpoint」无法在单测完全证明（mock provider），靠 dev 注册表配置核对 + per-route 读凭据代码路径证明。

## S5 验证策略小结（供 S3 gate reviewer 审）
- 验证方式：Go TDD（biz/middleware 单测 + race）+ dev 端到端 sanity。
- 不做 Playwright 理由：零前端改动，能力纯后端透明。
- 关键路径：3 条（见上）。计费正确性由 T1/T3/T5 单测的 Reserve/UsageRecord 计数断言覆盖（高风险 → 不可只靠 dev 手测）。
