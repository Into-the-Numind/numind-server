# S5 验证策略：sop-stream-robustness

- **方式**: 仅后端 Go TDD（持久化回归保护）。无 Playwright E2E、无 gstack /qa。
- **理由**: 本批次为纯后端流式健壮性修复，**零前端/UI 改动**，重连复用既有
  `GET /v1/sop/runs/:id/status`。前端 UI 验证不适用。每个 task 自带 Go 单测永久留库
  → 满足 Rule 10「持久回归保护」（不是一次性 /qa）。
- **高风险声明**: 问题 1/2 相邻 Reserve/Reconcile 退积分逻辑。退款分类的行为变更
  （D1：断连不再 `user_cancelled` 退款；D2：超时归 `provider_timeout`）由
  `detachStreamContext` 单测 + `credit.classifyReason` 既有测试覆盖。**不涉及金额计算
  改动**（只改 ctx 生命周期与超时分类），故 Go 单测足够，无需 E2E。

## 关键验证路径
1. **断连不丢结果**（US-1）：`detachStreamContext` 单测——父 ctx 取消不传导到 LLM ctx +
   ctx 值（trace/billing）保留 + overall 超时触发。手工推理：detach 后 biz 同步跑完
   生成+落库（store 不接 ctx），重连 `GetRunStatus` 返回 Output。
2. **idle 超时**（US-2）：`idleWatcher`（pipe）+ `callVolcDeepThinkingStream`（httptest 挂起）
   单测，短 idle 触发，错误 `errors.Is(context.DeadlineExceeded)`。
3. **整体超时**：`sopOverallTimeout` 默认/override 单测；`detachStreamContext` overall 触发。
4. **done 一次**（US-3）：httptest SSE `[DONE]` → 断言 executor 不透传 `done`。
5. **再生原子清理**（US-3）：store SQLite 事务删除 + upstream 幸存。
6. **状态重试**（US-3）：stub store 重试成功/恒失败不 panic + 内存一致。

## Gate
- `go test ./...` exit 0
- `task lint` exit 0
- `task test`（race + coverage）exit 0
