# Agent ReAct 流式化 — S3 任务规划

> **Feature**: `agent-react-streaming` (Standard track)
> **Spec**: [2026-05-27-agent-react-streaming-design.md](../specs/2026-05-27-agent-react-streaming-design.md)
> **Worktrees**:
> - `numind-server`: `/private/tmp/wt-agent-react-streaming-numind-server`
> - `numind-web-v3`: `/private/tmp/wt-agent-react-streaming-numind-web-v3`

---

## 任务依赖图（reviewer P1 修复后版本）

```
Batch 1（4 并行，Tier 2 跨包 disjoint）：
   T01 stream/events.go (types + payloads)
   T02 stream/lock.go (SubscriptionLock)
   T03 errno entry (ErrAgentStreamAlreadyAttached)
   T06 stream/langfuse.go (sse.connection span)  ← P2-4 提前到 Batch 1

BE 后续串行：
   T04 consumeEinoStream helper             [需 T01]
     │
     ▼
   T05 agentRunner.RunStream                 [需 T04]
     │   Commit-1: 抽 finalizeRun helper（不动语义）
     │   Commit-2: 扩展 AgentRunner interface + 新增 RunStream + 接 einoAgent.Stream
     ▼
   T07 controller CreateStream + router + service 层桥接   [需 T01 T02 T05]
     │   (扩展 student_run_lifecycle.go 加 AcquireStreamLock/ReleaseStreamLock/RunStream)
     ▼
   [BE smoke test pass]

FE 串行链（与 BE Batch 1 之后跨仓库并行 — Tier 2）：
   T08 FE types (TS mirror)               [需 T01 spec 锁定，不需要代码]
     │
     ▼
   T09 api/agent-stream.ts (fetchSSE)      [需 T08]
     │
     ▼
   T10 stores/agentChat.ts applyStreamEvent + applyError   [需 T08；P1 调整为 composable 之前]
     │
     ▼
   T11 composables/useAgentStream.ts       [需 T09 + T10；P1 调整位置在 store 之后]
     │
     ▼ Batch 2（2 并行 Tier 3 disjoint Vue 组件文件）：
   T12 AssistantTextMessage cursor
   T13 ToolGroupMessage status badge
     │
     ▼
   T14 AgentChatView wire + abort           [需 T11 T12 T13]

最后（不阻塞 ndf-done）：
   T15 S5 验证策略 task（独立文档，可与所有 T 并行）
   T16 S5 测试实现                          [需 T01-T14]
```

**Tier 划分（per §11）**：
- Tier 1（永久并行）：reviewer subagents、独立文档（T15）
- Tier 2（永久并行）：跨仓库 / 跨包 disjoint write — Batch 1（T01/T02/T03/T06）；FE 全部任务跟 BE T04+ 之后的任务跨仓库并行
- Tier 3（需文件归属验证）：同仓库 disjoint file write — Batch 2（T12/T13 同包不同 Vue 文件，主 session 跑 `ndf-check-disjoint`）
- Tier 4（禁止并行）：T04→T05→T07、T08→T09→T10→T11→T14 — 这些链有顺序依赖，串行

---

## T01 — `stream/events.go`：事件类型与 payload

**仓库**: numind-server
**文件**:
- `internal/numind/biz/agent/stream/events.go` (new, ~180 LOC)
- `internal/numind/biz/agent/stream/events_test.go` (new, ~120 LOC)

**范围**:
- 定义 `EventType` 枚举（14 个值，见 spec §3.1）
- 定义 `Event` 信封（Type/Seq/Timestamp/RunID/StepIndex/Data）
- 定义 **12 个 payload struct**：TokenDeltaPayload / ReasoningDeltaPayload / AssistantMessagePayload / **ToolCallStartPayload / ToolCallProgressPayload / ToolCallResultPayload / ToolCallErrorPayload** (4 个 ToolCall*) / StepDonePayload / StateChangePayload / QuestionPromptPayload / TerminalPayload / ErrorPayload
- `stream_start` / `ping` 事件**无 payload struct**——直接 nil 或 inline map（spec §3.2 SSE 帧示例使用 inline map）
- 提供 `Encode(t EventType, payload any, seq uint64, runID uint64, step int) (Event, error)` helper

**测试**:
- 每种 EventType 一个 round-trip test：Encode → JSON marshal → JSON unmarshal → 字段对等
- snake_case 字段命名校验（避免 marshal 后字段名漂移）
- nil payload 安全（应返回 EmptyData event 不 panic）

**验收**:
- [ ] 14 个 EventType 全部声明
- [ ] 12 个 payload struct 字段与 spec §3.1 一致
- [ ] `go test ./internal/numind/biz/agent/stream/ -run TestEvent` 全绿
- [ ] `task lint` 通过

**依赖**: 无
**Tier**: 独立（其他 T 都依赖它）

---

## T02 — `stream/lock.go`：单订阅锁

**仓库**: numind-server
**文件**:
- `internal/numind/biz/agent/stream/lock.go` (new, ~40 LOC)
- `internal/numind/biz/agent/stream/lock_test.go` (new, ~80 LOC)

**范围**:
- `SubscriptionLock` 结构（内含 `sync.Mutex` + `map[uint64]struct{}`）
- `Acquire(runID uint64) bool` — 已被占用返回 false
- `Release(runID uint64)` — 总是成功
- 进程内单例（包级别 var + sync.Once 或注入 wire）

**测试**:
- 并发 1000 个 goroutine Acquire 同一 runID，只有 1 个成功
- Acquire → Release → Acquire 串行 OK
- Acquire 不同 runID 互不影响

**验收**:
- [ ] `go test -race ./internal/numind/biz/agent/stream/ -run TestLock` 全绿（含 race detector）
- [ ] `task lint` 通过

**依赖**: 无（与 T01 并行）
**Tier**: 2 (disjoint file with T01/T03)

---

## T03 — errno 新增 `ErrAgentStreamAlreadyAttached`

**仓库**: numind-server
**文件**:
- `internal/pkg/errno/agent.go` 或 `internal/pkg/errno/code.go`（视现有结构，~10 LOC）
- 无新增测试（errno 是常量定义，按 codebase 惯例不单测）

**范围**:
- 新增 errno code 40901（确认不冲突——S3 实现时 `grep` errno 包内现有 code）
- 消息文案："agent stream already attached for this run"

**验收**:
- [ ] errno 在 `errno` 包导出
- [ ] `task lint` 通过
- [ ] code 40901 未被现有 errno 占用（grep 验证）

**依赖**: 无
**Tier**: 2 (disjoint with T01/T02)

---

## T04 — `consumeEinoStream` helper

**仓库**: numind-server
**文件**:
- `internal/numind/biz/agent/runner_stream.go` (new, ~250 LOC)
- `internal/numind/biz/agent/runner_stream_test.go` (new, ~300 LOC)

**范围**:
- 私有函数 `(r *agentRunner) consumeEinoStream(ctx, run *model.AgentRun, sr *schema.StreamReader[*schema.Message], ch chan<- stream.Event, st *LoopState, ...) (*RunResult, error)`
- 内部循环 `sr.Recv()` 直到 EOF / err / ctx done
- 分类 `schema.Message`：
  - `Role == Assistant` + 有 Content → emit token_delta
  - `Role == Assistant` + 有 ReasoningContent → emit reasoning_delta
  - `Role == Assistant` + 有 ToolCalls → emit tool_call_start（按 ToolCallID 去重）
  - `Role == Tool` → emit tool_call_result
  - `ResponseMeta.FinishReason != ""` → emit assistant_message + step_done
- 维护 `currentMessageID`（每个 step 一个 UUID）、`toolCallStartedAt` map（计算 duration_ms）
- ctx.Done() → 写完 terminal(aborted_streaming) 后退出
- err 时 → 写 error event + 走 Run() 等价的 TerminalReason 设置逻辑（model_error / image_error 等）
- 关键不变性：本函数**不**改 hook 链调用、**不**改 TerminalReason 状态机、**不**改 compactv2 接线（这些都在 RunStream 父方法里处理）

**测试**（mock einoAgent.Stream 返回固定 chunk 序列）:
- 单步纯文本：3 个 text chunk + 1 个 FinishReason chunk → 3 个 token_delta + 1 个 assistant_message + 1 个 step_done
- 单步含工具调用：text + tool_calls + Tool result → token_delta + tool_call_start + assistant_message + step_done + tool_call_result
- 多步：N 个 step boundary 正确切 currentMessageID
- 中途 ctx cancel：emit terminal(aborted_streaming)，goroutine 干净退出
- stream err：emit error event，TerminalReason 正确分类（model_error / image_error）
- Reasoning content：thinking 模型场景，reasoning_delta + token_delta 交错
- 并发 emit：channel 满时不死锁（select with ctx.Done()）

**验收**:
- [ ] 所有上述测试场景 PASS
- [ ] `go test -race -timeout 30s ./internal/numind/biz/agent/ -run TestConsumeEinoStream` 全绿
- [ ] goroutine leak check：每个 test 末尾 `goleak.VerifyNone(t)` 或等价手段
- [ ] `task lint` 通过

**依赖**: T01（用 stream.Event 类型）
**Tier**: 必须在 T01 之后

---

## T05 — `agentRunner.RunStream` 公开方法

**仓库**: numind-server
**文件**:
- `internal/numind/biz/agent/runner.go` (extend, +~120 LOC) — **接口扩展 + RunStream 方法**
- `internal/numind/biz/agent/runner_test.go` (extend, ~150 LOC)

**Commit 拆分（两步避免回归 + 便于 reviewer 看 diff）**:

**T05-Commit-1 (refactor only, ~80 LOC)**：把 Run() 末尾的持久化收尾逻辑（WriteTurn + MergeTerminalMetadata + UpdateState + memory SyncTurn + IndexAgentRun + memoryExtractor.Enqueue + 完成日志 + RunResult 构造）抽到私有 helper `(r *agentRunner) finalizeRun(ctx, run, st, startTime, finalText, output, ...) (*RunResult, error)`。Run() 在原位置调 finalizeRun。**RunStream 还未引入**，本 commit 单独验证 Run() 行为零变化（所有现有 runner_test.go PASS）。

**T05-Commit-2 (additive, ~80 LOC)**：
- 在 `AgentRunner` interface（runner.go 现行约 line 82-85）**显式新增** `RunStream` 方法签名 — **修复 reviewer 标出的 P1：interface 不扩展导致 T07 service adapter 编不过**
- 新方法 `(r *agentRunner) RunStream(ctx, req RunRequest, runID uint64, ch chan<- stream.Event) (*RunResult, error)`
- 复用 Run 中 launch agent 之前的所有逻辑（compactv2 setup, agentmd loading, hook chain assembly, system prompt build, etc.）
- 用 `einoAgent.Stream(ctx, einoMessages)` 替代 `einoAgent.Generate`
- 调用 T04 的 `consumeEinoStream`
- 收尾调 T05-Commit-1 抽出的 `finalizeRun`
- 编译保险：`var _ AgentRunner = (*agentRunner)(nil)` 在 runner.go 已有，interface 扩展后未实现会编译失败 → 强制同 commit 实现

**关键约束**:
- I2/I3/I5/I6/I7 invariants 全保（同样的 hook 链 + 同样的 19 个 TerminalReason）
- Run 行为不变（现有 SDK 调用方零感知）— T05-Commit-1 单独验证此条
- Langfuse trace 与 Run 完全等价（同一个 trace ID 起点，同样的 generation 数量）

**测试**:
- TestRunStream_HappyPath：模拟 Eino Stream 返回 2 个 step 的 chunk 序列，断言 channel 收到完整事件序列 + TerminalCompleted
- TestRunStream_HookActionStop：pre-seeded HookActionStop，断言 emit terminal(hook_stopped)
- TestRunStream_MaxStepsReached：模拟 30 个 step boundary，断言 emit terminal(max_turns)
- TestRunStream_LLMError：mock stream 返回 ErrAIProviderTimeout，断言 emit error + terminal(model_error) + terminal_metadata 持久化（验证之前的 hotfix 在流式路径下也工作）
- TestRunStream_AbortedByCtx：中途 ctx cancel，断言 emit terminal(aborted_streaming) + state_reason 落库

**验收**:
- [ ] 所有 TestRunStream_* PASS
- [ ] `task lint` 通过
- [ ] `task test` 完整跑全绿（无回归）
- [ ] 现有 Run() 测试无变更（行为不变）

**依赖**: T04
**Tier**: 必须在 T04 之后

---

## T06 — `stream/langfuse.go`：SSE span 集成

**仓库**: numind-server
**文件**:
- `internal/numind/biz/agent/stream/langfuse.go` (new, ~80 LOC)
- `internal/numind/biz/agent/stream/langfuse_test.go` (new, ~60 LOC)

**范围**:
- `StartSSESpan(ctx, traceID, runID) (spanID string, finalize func(eventCount int, disconnectReason string))`
- 内部用 `langfuse.CreateSpan` + 收集 first_byte 时间戳
- finalize 时一次性 `UpdateSpan` 写入 metadata（first_byte_ms / event_count / disconnect_reason）
- 优雅降级：tc nil 时空操作

**测试**:
- mock langfuse 全局 client（用 `langfuse.WithClient` 注入 mock）
- 验证 span 创建 + 元数据字段
- nil tc 不 panic

**验收**:
- [ ] 测试 PASS
- [ ] 按 `.claude/rules/ai-service.md` §1 模板
- [ ] `task lint` 通过

**依赖**: 无 — 修复 reviewer 标出的 P2-4：该任务只用 `langfuse` 包 + `uint64` 原语，不 import stream.Event 也不调 RunStream。可与 T01/T02/T03 同 Batch 1 并行。
**Tier**: 2（与 T01/T02/T03 跨包 disjoint，可并行）

---

## T07 — SSE controller + 路由注册 + service 层桥接

**仓库**: numind-server
**文件**:
- `internal/numind/controller/v1/agent/student_run_stream.go` (new, ~180 LOC)
- `internal/numind/controller/v1/agent/student_run_stream_test.go` (new, ~150 LOC)
- `internal/numind/router.go` (extend, ~3 LOC)
- `internal/numind/biz/agent/student_run_lifecycle.go` (extend, +~40 LOC) — **reviewer 标出的 P1 修复：service 层方法宿主在这里**

**范围**:
- `(h *StudentRunController) CreateStream(c *gin.Context)`：参考 spec §4.1 伪代码
  - 参数绑定 + auth
  - `SubscriptionLock.Acquire(runID)` → 失败返 409 + 现有 run snapshot
  - SSE headers（参考 [chatbot.go:296-304](numind-server/internal/numind/controller/v1/chatbot/chatbot.go:296)）
  - goroutine 启动 `runSvc.RunStream` + event channel
  - 主循环 `select` 三路：ctx.Done / ping.C (25s keepalive) / eventCh
  - terminal/error event 后退出 + Release lock
- Router 注册 `POST /v1/agent-runs/stream`（**单独路径，不复用现有 `POST /v1/agent-runs`**——S3 决策为简化路由匹配，避免 query param 分流的隐式行为）
- `student_run_lifecycle.go` 中 `StudentRunService` 新增三个公开方法：
  - `AcquireStreamLock(ctx, userID, req) (runID uint64, acquired bool, err error)` — 同时建 agent_run row + 取锁
  - `ReleaseStreamLock(runID uint64)` — 直接调 T02 的 SubscriptionLock.Release
  - `RunStream(ctx, userID, req, runID, ch) (*RunResult, error)` — 委托给 T05 的 agentRunner.RunStream，通过 service 持有的 `runner AgentRunner` interface 字段

**Tier**：4（BE chain 末尾，串行——T06 已移到 Batch 1，T07 此时单独跑，没有同包并行需求）

**测试**（用 `httptest.ResponseRecorder` + buffered writer）:
- 普通流式：mock RunStream 推 5 个事件 → 断言 SSE 帧格式正确（`data: {...}\n\n`）
- 409 路径：第二次连接 Acquire 失败 → 状态码 409 + JSON body 含 run_id
- 客户端断开：用 cancelable ctx，触发 ctx.Done → controller 退出且 goroutine 不泄露
- 25s keepalive：触发 timer → 写入 `:ping\n\n`
- 大事件不阻塞：channel buffer=256，flood 1000 个事件不 deadlock

**验收**:
- [ ] 所有上述测试 PASS
- [ ] 路由在 router.go 注册
- [ ] `task lint` 通过

**依赖**: T01（用事件类型）+ T02（用锁）+ T05（用 RunStream）
**Tier**: 4（BE chain 末尾，串行）

---

## T08 — 前端：TypeScript 类型 mirror

**仓库**: numind-web-v3
**文件**:
- `src/types/agent-stream.ts` (new, ~120 LOC)
- `src/types/agent-stream.test.ts`（如果项目用 vitest，否则 typecheck 即可）

**范围**:
- `AgentStreamEventType` union 类型（14 值，与后端 stream.EventType 等价）
- 各 payload interface（13 个，与后端 payload struct 字段对应）
- `AgentStreamEvent<T = unknown>` 信封
- `AgentStreamConflict` Error 子类（含 runId）

**测试 / 验证**:
- `npm run type-check` 通过
- 可选：JSON sample → parse → assert TypeScript 类型推断正确

**验收**:
- [ ] type-check 无错
- [ ] npm run lint 无新增 warning

**依赖**: T01（看后端 spec/事件 schema）；不需要 T01 代码 compile，只需要 spec 锁定字段名
**Tier**: 2（FE 仓库与 BE 仓库不交集，T01 spec 落地后即可并行）

---

## T09 — `api/agent-stream.ts`：SSE 消费器

**仓库**: numind-web-v3
**文件**:
- `src/api/agent-stream.ts` (new, ~100 LOC)
- `src/api/agent-stream.test.ts` (new, ~120 LOC)

**范围**:
- 复用现有 `src/api/sales.ts:readSSEStream` + `parseSseChunk`（如不可直接复用就抽到 shared util）
- `streamAgentRun(req, onEvent, signal?)`：发起 POST，409 抛 AgentStreamConflict，其他错误抛通用 Error
- 路径配置常量：`POST /v1/agent-runs/stream`

**测试**:
- mock `fetch` 返回 SSE 流（按帧 reader）→ 断言 onEvent 被以正确顺序调用
- 409 响应 → 抛 AgentStreamConflict 含 runId
- abort signal → fetch 中断 + onEvent 不再调用

**验收**:
- [ ] 测试 PASS
- [ ] `npm run lint` 通过

**依赖**: T08
**Tier**: 顺序

---

## T10 — `agentChat.ts` store `applyStreamEvent` + `applyError`

> **顺序调整**：reviewer 标出 P1 — 原 T10 (composable) 调 store.applyStreamEvent / applyError 这两个方法此时还不存在。本任务（新 T10，原 T11）必须在 composable (新 T11) 之前完成，否则 type-check 失败。

**仓库**: numind-web-v3
**文件**:
- `src/stores/agentChat.ts` (extend, +~200 LOC)
- `src/stores/agentChat.test.ts`（extend，+~150 LOC）

**范围**:
- 新 action `applyStreamEvent(e: AgentStreamEvent)`
- 新 action `applyError(err: Error | unknown)` — 把非 409 的 SSE 错误转成一条 `system` 类型的 message（subtype=`failed`，markdown 含错误摘要）
- 14 个 EventType case（spec §5.3 伪代码）
- 关键 sub-helper：
  - `ensureStreamingAssistantMessage(messageId)`：找不到则创建新 assistant message with isStreaming=true
  - `ensureToolGroupForStep(step)`：按 step_index 找 / 创建 ToolGroup
  - `updateToolCall(toolCallId, mutator)`：定位 + 调 mutator
  - `reconcileFromDB(runId)`：terminal 后拉 GET 校准 messages 列表
- 在现有 Message discriminated union 上扩展 `assistant` 类型加 `isStreaming?: boolean` 和 `reasoning?: string` 字段

**测试**:
- 每种事件类型单独 case：emit → 断言 store 状态
- 多 step 序列：token_delta → assistant_message → tool_call_start → tool_call_result → step_done → repeat → terminal
- 顺序乱（实际不会发生，但防御性）：tool_call_result 在没有 start 时不 crash
- reconcileFromDB mock fetch：terminal 后调 1 次 GET，最终 messages 来自 DB 而非流式累积
- `applyError` 推入一条 system message，markdown 含错误

**验收**:
- [ ] 全部测试 PASS
- [ ] `npm run lint` + type-check 通过

**依赖**: T08
**Tier**: 顺序（必须在 T11 composable 之前）

---

## T11 — `useAgentStream.ts` composable

**仓库**: numind-web-v3
**文件**:
- `src/composables/useAgentStream.ts` (new, ~120 LOC)
- `src/composables/useAgentStream.test.ts` (new, ~100 LOC)

**范围**:
- 暴露 `start(req)`、`stop()`、`isStreaming`、`fallbackPolling`
- `start` 内部调 `streamAgentRun` + 把事件转发到 store.applyStreamEvent（T10 已实现）
- 抓 AgentStreamConflict → 调 `useAgentRun().startStatusPolling()`（**修正 reviewer 标出的 P2-3：startStatusPolling 是 useAgentRun composable 上的方法，不是 store 方法**）。composable 之间 composition 模式：useAgentStream 内部 `const { startStatusPolling } = useAgentRun()` 然后在 409 case 里调
- 其他错误 → `store.applyError(err)`（T10 已实现）
- `stop` AbortController 中断

**测试**:
- happy path：start → 收事件 → store.applyStreamEvent 被以正确顺序调用
- 409 → fallbackPolling = true + startStatusPolling 启动
- 网络错误 → store.applyError 调用

**验收**:
- [ ] 测试 PASS
- [ ] `npm run lint` + type-check 通过

**依赖**: T09 (api) + T10 (store actions exist)
**Tier**: 顺序

---

## T12 — `AssistantTextMessage` 流式光标

**仓库**: numind-web-v3
**文件**:
- `src/components/messages/AssistantTextMessage.vue`（如不存在则新建，~80 LOC）
- 相关 stories / 测试

**范围**:
- 接受 `isStreaming?: boolean` prop
- 内容用现有 markdown renderer
- 末尾闪烁光标 `▎`：CSS keyframe animation，1s blink，`isStreaming=false` 时消失
- 参考 Claude Code `components/messages/AssistantTextMessage.tsx`（如有 RN 对应物）的实现

**测试**:
- 渲染 with isStreaming=true → 光标元素存在
- isStreaming=false → 光标消失
- markdown 块流式 append 不破坏 DOM（virtual list 友好）

**验收**:
- [ ] 测试 PASS
- [ ] type-check 通过

**依赖**: T10（消费 store 的 isStreaming 字段）+ T11（composable 驱动 event loop → store 更新）
**Tier**: Tier 3（与 T13 文件不交集，可 Batch 2 并行；主 session 跑 `ndf-check-disjoint AssistantTextMessage.vue ToolGroupMessage.vue`）

---

## T13 — `ToolGroupMessage` 状态徽章

**仓库**: numind-web-v3
**文件**:
- `src/components/messages/ToolGroupMessage.vue` (extend, +~50 LOC)

**范围**:
- 在每个 tool call 卡片右上角加 status dot：
  - queued: 灰
  - use: 蓝
  - progress: 蓝旋转动画
  - result: 绿
  - error: 红
- 200ms 过渡动画
- 沿用现有 `ToolCallAggregate.current_state` 字段（已有，T11 流式更新）

**测试**:
- 各 state → 正确 class 名
- state 变化 → 过渡触发

**验收**:
- [ ] 测试 PASS
- [ ] type-check 通过

**依赖**: T10 + T11（同 T12 — ToolCallAggregate.current_state 由 composable event loop 驱动）
**Tier**: Tier 3（Batch 2 — 与 T12 并行）

---

## T14 — `AgentChatView` 接线 + abort 按钮

**仓库**: numind-web-v3
**文件**:
- `src/views/agent/AgentChatView.vue` (extend, +~50 LOC)
- `src/components/agent/ChatComposer.vue`（如有该组件；否则改 view 直接，+~30 LOC）

**范围**:
- `handleSend` 改用 `useAgentStream().start(req)` 代替原 `useAgentRun().create + startStatusPolling`
- 输入框 disabled 状态绑 `useAgentStream().isStreaming`
- 加 "中止" 按钮，绑 `useAgentStream().stop()`
- 流式中断 / 完成后 UI 恢复

**测试**:
- 提交问题 → 输入框 disable + 中止按钮可见
- 中止点击 → stop 调用 + UI 恢复
- terminal 收到 → UI 恢复

**验收**:
- [ ] 测试 PASS
- [ ] type-check 通过

**依赖**: T11 (composable) + T12 + T13
**Tier**: 顺序（最后接线）

---

## T15 — S5 验证策略 task（per NDF Rule 10）

**仓库**: numind-server（文档放这里）
**文件**:
- `docs/superpowers/specs/2026-05-27-agent-react-streaming-s5-strategy.md` (new, ~50 LOC)

**范围**:
- 记录 S5 验证方式 = **Playwright E2E + 后端 Go test 双层**（已在 S2-D3 锁定，此 task 是把决策正式落地为可被 S3 reviewer 审计的工件）
- 列出 Playwright E2E 5 个关键场景（spec §8）
- 列出后端 Go test 6 类覆盖（spec §8）
- 说明 gstack /qa 不替代 e2e 的理由（agent 流式涉及时序敏感行为，需精确控制 fetch reader 的回归保护）

**验收**:
- [ ] 文档存在
- [ ] 5 个 Playwright 场景 + 6 类 Go test 全部列出
- [ ] reviewer 审 S3 plan 时一并审本 task（plan 原子性 reviewer 同 turn 审）

**依赖**: 无（与所有 T 并行可写）
**Tier**: 1（独立文档）

---

## T16 — S5 测试实现

**仓库**: numind-server + numind-web-v3
**文件**:
- `numind-web-v3/e2e/agent-streaming.spec.ts` (new, ~250 LOC)
- 后端 Go test 已散布在 T01-T07 各 task 的 *_test.go 文件里（T04 已覆盖 6 类中的 5 类；本 task 补漏 hook 顺序 + terminal_metadata 一致性等剩余覆盖）
- 可能 `internal/numind/biz/agent/runner_stream_e2e_test.go` (new, ~150 LOC)

**范围**: 实现 T15 列出的全部 11 个测试场景（5 Playwright + 6 后端）

后端 6 类覆盖中，T04 的 `runner_stream_test.go` 已覆盖 4 类（happy text/tool/multi-step/ctx-cancel/stream-err/reasoning 中的前 5 个场景，归入 4 个测试类别）。本任务新建 2 个文件覆盖剩余 2 类：
- `internal/numind/biz/agent/runner_stream_hookchain_test.go` (~150 LOC)：hook 触发顺序在流式路径下与非流式 Run() 一致（pre-seed 不同 HookAction 序列，断言相同的 LoopEvent 转移 + TerminalReason）
- `internal/numind/biz/agent/terminal_metadata_consistency_test.go` (~80 LOC)：stream 失败时 terminal_metadata 写入与 Hotfix [model-error-recovery](https://github.com/Into-the-Numind/numind-server/commit/648d16d4) 一致

**验收**:
- [ ] `npm run test:e2e -- e2e/agent-streaming.spec.ts` 退出码 0
- [ ] `go test -race -timeout 60s ./internal/numind/biz/agent/...` 退出码 0
- [ ] 5 个 Playwright 场景全 PASS（包括 failure-mode scenarios）
- [ ] 新增的两个 Go test 文件 PASS

**依赖**: 全部 T01-T14（最后一个 task，最终验证）
**Tier**: 顺序（最后）

---

## 全局约束

### S4 阶段 reviewer 制度（per NDF Rule 6）

每个 T0X 完成后，**必须并行 dispatch 双 Sonnet reviewer**（同一 turn 两个 Agent 调用）：
- **spec-compliance reviewer** — 用 `templates/ndf/review-spec-compliance.md` 对照 S2 spec 检查
- **code-quality reviewer** — 用 `templates/ndf/review-code-quality.md` 检查代码质量
- Reviewer 输出结构化：`severity: file:line — rule-id — problem — fix`
- P0 必修后才能进下一 task；P1/P2 同 turn 修

### NDF Rule 11 不适用

本 feature 是产品改造（不是 bug-from-customer），不需要"第一个 commit 是失败的复现测试"。

### 并行 Tier 表（per NDF Rule 12，reviewer P1 修复后）

| 并行批次 | Tasks | Tier | 文件归属 |
|---------|------|------|---------|
| Batch 1 (BE) | T01, T02, T03, T06 | 2 | T01→stream/events*.go, T02→stream/lock*.go, T03→errno/agent.go, T06→stream/langfuse*.go — 4 个独立文件 |
| Batch 1' (FE, 跨仓库与 Batch 1 并发) | T08 | 2 | FE 仓库独立 |
| Batch 2 (FE) | T12, T13 | 3 | 同包不同 Vue 文件 — 主 session 跑 `ndf-check-disjoint AssistantTextMessage.vue ToolGroupMessage.vue` 验证 |
| 串行链 BE | T04→T05→T07 | 4 | T05 抽 finalizeRun + 扩展 AgentRunner interface + 新增 RunStream 的两 commit；T07 controller + router + service 层桥接 |
| 串行链 FE | T09→T10→T11→T14 | 4 | api → store → composable → view，每步依赖上一步导出的符号 |
| Tier 1 独立 | T15 (S5 文档) | 1 | 与所有 T 并行可写 |
| 最后 | T16 | 4 | 等 T01-T14 全 done |

### Plan 完整性自审

- [x] 覆盖 spec §1 架构 — T04/T05/T07
- [x] 覆盖 spec §3 事件协议 — T01
- [x] 覆盖 spec §4 后端 — T01-T07
- [x] 覆盖 spec §5 前端 — T08-T14
- [x] 覆盖 spec §6 API 契约 — T07
- [x] 覆盖 spec §7 Trace topology — T06
- [x] 覆盖 spec §8 S5 验证策略 — T15+T16
- [x] 验收标准（PRD §4）每条都有对应 task — 全 mapped via spec §9

### 风险监控

S3 阶段的 reviewer 应特别核查：
1. T04 consumeEinoStream 是否真的不破坏 hook 链（用 mock einoAgent 测够不够？）
2. T05 finalizeRun helper 抽提是否引入 regression（Run 和 RunStream 共享时）
3. T07 controller 在 client 断开时 goroutine 是否真不泄露（goleak）
4. T11 store reconcileFromDB 的时机是否引入闪烁（terminal 收到 → GET 拉 → 期间 UI 状态）
