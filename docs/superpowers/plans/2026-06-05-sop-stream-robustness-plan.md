# Plan：SOP 流式执行健壮性批次

- **Feature ID**: sop-stream-robustness
- **日期**: 2026-06-05
- **Spec**: docs/superpowers/specs/2026-06-05-sop-stream-robustness-design.md
- **Worktree**: /private/tmp/wt-sop-stream-robustness-numind-server

8 个原子 task，每个完成后可独立编译 + `go test`，并跑双 Sonnet reviewer（spec 合规 +
代码质量），P0/P1 修完再下一个。依赖：T3 → {T4, T5, T6}；其余独立。顺序执行。

---

## T1 — 再生清理事务化（问题 5）
- **文件**: `internal/numind/store/sop.go`（含 ISopStore 接口）、`internal/numind/biz/sop/sop.go`、
  `internal/numind/store/sop_regeneration_cleanup_test.go`（新）
- **做**: 加 `CleanupDownstreamForRegeneration(runID uint, afterSort int) error`（事务包 3 删，spec §5）；
  接口声明；biz :736-749 三删替换为单次调用（失败 return error 中止再生）。
- **测试**: in-memory SQLite，建上下游 node runs + notes + chat → 调用 → 断言下游/笔记/对话删除、
  upstream（sort≤afterSort）幸存。
- **验收**: `go test ./internal/numind/store/...` 绿；biz 编译通过。

## T2 — 状态更新退避重试（问题 6）
- **文件**: `internal/numind/biz/sop/sop.go`、`internal/numind/biz/sop/run_status_retry_test.go`（新）
- **做**: 加 `updateRunStatusWithRetry`（3 次指数退避，spec §6）；改 draft→running（:559-573，
  成功才翻内存）+ 二次兜底（:656-670，失败不 return、log 续行）。
- **测试**: stub ISopStore（前 N 次失败后成功 / 恒失败）→ 断言重试成功、最终一致、恒失败不 panic 返回 error。
- **验收**: `go test ./internal/numind/biz/sop/...` 绿。

## T3 — 超时配置 + helper + errtranslate（问题 2 基建）
- **文件**: `internal/numind/biz/sop/executor.go`、`config_local.yaml`、`config_dev.yaml`、
  `config_qa.yaml`（**不动 config_prod.yaml**）、`internal/numind/biz/errtranslate/translate.go`、
  `internal/numind/biz/sop/stream_timeout_test.go`（新）、errtranslate 测试。
- **做**: `sopIdleTimeout()`/`sopOverallTimeout()`（viper + 代码兜底 4m/30m，spec §2.1）；
  config 加 `sop.stream_idle_timeout/stream_overall_timeout`；`ToErrno` 加
  `DeadlineExceeded → ErrAIProviderTimeout`（确认/修友好文案，spec §2.5）。
- **测试**: 默认值 + viper override；errtranslate DeadlineExceeded → ErrAIProviderTimeout 友好文案。
- **验收**: `go test` 绿；`task lint`。

## T4 — idle 超时（兜底路：callAli/callVolc/plain reader，问题 2）
- **文件**: `internal/numind/biz/sop/executor.go`、`internal/numind/biz/sop/idle_timeout_test.go`（新）
- **做**: `idleWatcher` + `startIdleWatcher`（spec §2.3）；应用到 `callAliDeepThinkingStream`、
  `callVolcDeepThinkingStream`、plain `ExecuteNodeStream` reader；删循环顶冗余 select；
  idle 超时返回 wrap DeadlineExceeded 的错误。
- **测试**: `idleWatcher` 用 pipe（无数据 → body 关 + tripped）；`callVolcDeepThinkingStream`
  指向 httptest server（发头后挂起）→ 短 idle → 断言 idle 超时错误（errors.Is DeadlineExceeded）。
- **验收**: `go test` 绿。

## T5 — idle 超时（Gateway 主路，问题 2）
- **文件**: `internal/numind/biz/sop/executor.go`、`internal/numind/biz/sop/gateway_idle_test.go`（新，如可测）
- **做**: `executeViaGateway` 改 select-over-channel + idleTimer + ctx cancel 传导 + drain
  防泄漏（spec §2.4）；`aiservice.ChatStream` 传 streamCtx。
- **测试**: 注入一个慢 `<-chan ChatChunk`（直接测消费循环逻辑，或抽出可测函数）验证 idle 触发返回
  超时错误且不泄漏；若 Gateway 难以单测，最小化为消费循环纯函数测试 + 代码评审覆盖。
- **验收**: `go test` 绿。

## T6 — 断连解耦（问题 1）【含 Rule 11 复现测试】
- **文件**: `internal/numind/biz/sop/sop.go`、`internal/numind/controller/v1/sop/sop.go`、
  `internal/numind/biz/sop/detach_context_test.go`（新）
- **做**: `detachStreamContext`（spec §1.1）；biz `ExecuteNodeStream`+`ChatAfterRunStream` 注入
  detach+overall（defer 顺序在 FinalizeReservation 之前）；控制器 `ExecuteNodeStream`+
  `ChatAfterRunStream` handler 改 `clientGone` 非中止模式（spec §1.2）。
- **测试**: **首 commit** `test(qa): reproduce client-disconnect aborts SOP node generation`
  （RED：断言 detach ctx 不随父取消而 Done + 值保留 + overall 超时触发）→ 实现 → GREEN。
- **验收**: `go test` 绿；手工推理重连查询走 `GetRunStatus`（已存在）。

## T7 — done 只发一次（问题 4）
- **文件**: `internal/numind/biz/sop/executor.go`、`internal/numind/controller/v1/sop/sop.go`、
  `internal/numind/biz/sop/done_event_test.go`（新）
- **做**: 删 executor/edit-stream 的 `handler("done","")` + dead `case/branch`（spec §4）；
  ChatAfterRun 的 message_id done 保持。
- **测试**: httptest SSE 含 `[DONE]` → callVolc/callAli → 断言 handler 未收到 `done` 事件。
- **验收**: `go test` 绿。

## T8 — SSE 心跳 helper（问题 3）
- **文件**: `internal/numind/controller/v1/sop/sse.go`（新）、`internal/numind/controller/v1/sop/sop.go`、
  `internal/numind/controller/v1/sop/sse_test.go`（新，如可测）
- **做**: `startSSEHeartbeat`（spec §3）；三个 handler 替换样板、去冗余双 ctx 检查；心跳保留。
- **测试**: 可行则用 httptest ResponseRecorder 验证心跳写出 + stop 停止；否则代码评审覆盖。
- **验收**: `go test` 绿；`task lint`。

---

## S5 验证策略（Rule 10）
见 `.ndf/features/sop-stream-robustness/s5-strategy.md`。摘要：纯后端 → Go 持久单测
（每 task 自带回归测试）+ `task test`（race+coverage）+ `task lint`。无前端改动 → 无
Playwright/gstack。高风险退积分相邻逻辑由 detach/timeout 的退款分类单测覆盖（D1/D2）。

## DAG（无环）
T1, T2 独立 → 并 T3 → T3 解锁 T4, T5, T6 → T7, T8 独立。实际串行执行：
T1 → T2 → T3 → T4 → T5 → T6 → T7 → T8。
