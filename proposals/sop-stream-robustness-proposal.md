# 提案 + PRD：SOP 流式执行健壮性批次

- **Feature ID**: sop-stream-robustness
- **日期**: 2026-06-05
- **关联需求**: requirements/sop-stream-robustness.md

## 1. 问题与价值

线上 SOP 流式生成在网络抖动、provider 卡死时体验崩坏：节点无故判失败、无限转圈。
这些都贴着 Reserve/Reconcile 退积分逻辑（高风险）。本批次提升流式链路鲁棒性，
让"瞬断/卡死"成为可恢复的正常情况，而非用户可见故障。

## 2. 技术可行性

全部可在现有架构内完成，无需新依赖、无需 DB schema 变更：
- **断连解耦**：Go 1.24 `context.WithoutCancel` 把 LLM 调用从 HTTP 请求生命周期剥离，
  保留 trace/billing/reservation 上下文值。
- **idle 超时**：流式读取循环里加"每 chunk 重置"的计时器；fallback 用 `time.AfterFunc`
  关闭 `resp.Body` 解除阻塞读，gateway 用 select-over-channel + ctx cancel 传导给 adapter。
- **整体超时**：`context.WithTimeout`（context deadline，不用 `http.Client.Timeout`，
  绝不砍正常长流）。可配置，代码兜底 30 分钟。
- **重连查询**：复用现成 `GET /v1/sop/runs/:id/status`（`GetRunStatus` 已返回节点 Output）。
- **事务清理**：`s.db.Transaction(...)` 包三个删除（仿现有 `DeleteRun`）。
- **重试**：biz 层 3 次指数退避包 `UpdateRun`。
- **心跳/done/重试**：纯重构与一致性收敛。

## 3. 工作量

8 个 task（见 plan），全后端，单仓库（numind-server）。预计 1 个开发 session。

## 4. PRD（用户故事 + 验收标准）

### US-1：网络瞬断不丢结果（P1）
> 作为用户，SOP 某步生成时我网络抖了一下，回来后这步应已生成好，而不是失败要重跑。

**验收**：客户端中途断开 → 节点不判 Failed、后台生成照常落库、重连经
`GET /sop/runs/:id/status` 能看到结果、前端无 `event: error` 弹窗。退积分逻辑按
正常 Reconcile（不触发 `user_cancelled` 退款）。

### US-2：AI 卡死有兜底（P1）
> 作为用户，provider 发完头就卡住时，系统应在合理时间内给我明确提示，而不是无限转圈。

**验收**：provider 不吐字 → 连续 4 分钟（可配置）后 idle 超时，干净中止，返回友好
错误（走 `errtranslate.FriendlyForSSE`）；整体兜底超时默认 30 分钟可配置，不误杀正常长流。
idle 超时归类 `provider_timeout`（退款分类正确）。

### US-3：内部健壮性收敛（P2）
> 作为维护者，心跳样板、done 事件、再生清理、状态重试应当一致、原子、自愈。

**验收**：心跳 helper 三处复用、done 只发一次（ChatAfterRun 保留 message_id）、
再生清理原子化、状态更新失败带重试且用户无感。

### 涉及 AI 调用的可观测性
本批次不新增 LLM 调用点，只改流式读取/超时/上下文。需确认现有 Langfuse trace/generation
（`callAli/callVolc` 的 generation、Gateway 的 trace）在 detach ctx 后仍正常记录
（trace 值由 `WithoutCancel` 保留）。

## 5. 涉及仓库

- `numind-server`（唯一）。无前端、无 admin 改动。

## 6. 风险

- **退积分逻辑相邻**：detach ctx 改变了 client-disconnect 的退款分类（从
  `user_cancelled` 变为正常 Reconcile）——这是**期望行为**（瞬断不应退款，应完成生成）。
  须在 spec 固化并单测覆盖。
- **goroutine 泄漏**：gateway idle 超时后须 drain channel 防 adapter goroutine 阻塞。
- **行为变更**：问题 6 把 fail-fast 改为 silent-retry-continue，须确保 run 状态最终
  由完成块或 zombie-reset 收敛。
