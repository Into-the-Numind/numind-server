# Agent Run Survives Exit — S2 技术设计

> **Feature**: `agent-run-survives-exit` (Standard track)  
> **Date**: 2026-08-03  
> **Status**: Draft, awaiting S2 gate  
> **Inputs**: [requirement-card](../../../requirements/agent-run-survives-exit.md) · [proposal+PRD](../../../proposals/agent-run-survives-exit-proposal.md)

---

## §1 设计目标

用户关闭页面、刷新、路由离开或网络断开时，Agent 执行必须继续在服务端完成。页面 SSE 只是观察通道，不能再成为执行生命周期的 owner。显式 `POST /v1/agent-runs/:id/cancel` 仍是唯一用户取消入口。

本设计覆盖用户端 AgentChat 的普通流式创建、`ask_user_question` 回答后的流式 resume、已有外部授权/外部工具 detached continuation 的兼容观察。服务进程重启后恢复未完成执行不在本次范围。

---

## §2 当前问题

后端 `StudentRunController.streamEvents` 目前把 HTTP request ctx 派生为 `runCtx`，并在 client disconnect / write error 时返回。返回会触发 `defer runCancel()`，导致 `runner.RunStream` 收到 ctx cancel 并把 run 终止为 `aborted_streaming`。这把“用户不再观察”误判为“用户取消执行”。

不能只把 `runCtx` 改成 `context.Background()`：当前 `RunStream` 的事件 channel 由 HTTP handler drain 并 publish 到 `RunEventBroker`。如果 handler 因页面断开返回，runner 可能卡在向无人消费的 channel 发送事件，仍然无法保证跑完。

已有可复用模式在 `external_tool_resume.go`：detached continuation 使用后台 goroutine drain `RunExternalContinuationStream` 的事件 channel，并 best-effort publish 到 `RunEventBroker`；Redis 失败只影响观察，不影响执行和 DB 最终落库。本设计把这个模式泛化到普通 create/answer stream。

### §2.1 方案取舍

- **选定方案：supervised streaming + event reattach**。保留实时体验，把执行和观察解耦，并复用现有 Redis Stream replay 与 DB snapshot SOT。
- **拒绝方案：只换 background ctx**。它仍依赖 HTTP handler drain 事件 channel，页面断开后 runner 可能阻塞。
- **拒绝方案：全部退回非流式+轮询**。实现简单但会丢掉 token、thinking、工具进度的实时体验。
- **后续方案：durable worker/queue**。可覆盖进程重启恢复，但超过本次“页面退出继续跑完”的交付范围。

---

## §3 架构总览

```
Browser AgentChat
  POST /v1/agent-runs/stream  or  POST /v1/agent-runs/:id/answer-stream
        │
        ▼
StudentRunController
  validate / precreate or validate+persist answer
  start supervised execution
  subscribe to replayable run events
        │
        ├────────────── observer ctx ──────────────► SSE writer
        │                                           closes on page exit only
        ▼
StudentRunService stream supervisor
  process-local execution registry
  detached runner ctx + explicit cancel bridge
  drains RunStream event channel
  best-effort PublishRunEvent(background ctx)
        │
        ├────────────── events ────────────────────► Redis Stream + Pub/Sub
        ▼
agentRunner.RunStream
  existing trace, billing, tools, transcript persistence
  DB agent_run.status/messages remain final SOT
```

核心拆分：

- **Execution owner**: `StudentRunService` 的 supervised run。它负责启动 runner、持续 drain event channel、结束后释放 execution registry。
- **Observation owner**: controller SSE 或 `/events` subscriber。它可以断开、重连、多 tab 并行观察，不能取消执行。
- **Cancellation owner**: `Cancel` API。它同时调用 `runner.Cancel(runID)` 和 supervisor parent cancel，覆盖 runner cancel func 尚未注册的极短窗口。

---

## §4 后端设计

### §4.1 新增 process-local execution registry

新增小型 registry，建议放在 `internal/numind/biz/agent/stream/execution_registry.go` 或 `student_run_stream_supervisor.go`：

```go
type StreamExecutionRegistry struct {
    mu sync.Mutex
    active map[uint64]*StreamExecution
}

type StreamExecution struct {
    RunID uint64
    StartedAt time.Time
    Cancel context.CancelFunc
    Done <-chan struct{}
}

func (r *StreamExecutionRegistry) Start(runID uint64, cancel context.CancelFunc, done <-chan struct{}) (started bool)
func (r *StreamExecutionRegistry) Cancel(runID uint64) bool
func (r *StreamExecutionRegistry) Finish(runID uint64)
func (r *StreamExecutionRegistry) IsActive(runID uint64) bool
```

这个 registry 防止同一进程内重复启动同一个 run，并为 cancel API 提供 runner 注册前的兜底取消。它不替代 DB 状态；run 的最终状态仍以 DB 为准。

### §4.2 StudentRunService supervised streaming 方法

新增三个服务方法，controller 不再直接把 request ctx 传进 runner：

```go
type PreparedStreamRun struct {
    RunID uint64
    SessionID string
    UserID uint
    Request CreateRunRequest
}

func (s *StudentRunService) PrepareStreamRun(ctx context.Context, userID uint, req CreateRunRequest) (*PreparedStreamRun, error)
func (s *StudentRunService) StartPreparedStreamRun(prepared *PreparedStreamRun) (started bool)
func (s *StudentRunService) StartPreparedAnswerStream(ctx context.Context, userID uint, runID uint64, req AnswerRequest) (started bool, err error)
```

`PrepareStreamRun` 由现有 `AcquireStreamLock` 中的 pre-create 逻辑抽出：校验输入、附件 ready、agent definition 权限、生成/继承 session metadata、同步创建 `agent_run(status=running)`。它不再获取 SSE subscription lock。

`StartPreparedStreamRun` 启动后台 goroutine：

- 构造 `bgCtx := middleware.NewContextWithUserID(context.Background(), prepared.UserID)`。
- `runnerCtx, cancel := context.WithCancel(bgCtx)`，先注册到 execution registry。
- 创建 `events := make(chan stream.Event, 256)`。
- 启动 publisher goroutine drain `events`，每个事件调用 `PublishRunEvent(bgCtx, runID, event)`。
- 调用现有 `RunStream(runnerCtx, prepared.UserID, prepared.Request, prepared.RunID, events)`。
- `defer close(events)`，等待 publisher drain 完，`registry.Finish(runID)`。

`StartPreparedAnswerStream` 必须同步执行 `validateAndPersistAnswer`，因为无效答案、跨用户、非 waiting run 等错误应继续以 HTTP 错误返回，而不是变成后台 error event。校验+持久化成功后，再用返回的 `RunRequest` 启动同样的 supervised publisher，执行 `runner.RunStream(runnerCtx, runReq, runID, events)`。

`AnswerStream(ctx, ..., ch)` 可保留给内部/测试，但 controller 主路径应切到 `StartPreparedAnswerStream`，避免 request ctx 再拥有 runner 生命周期。

### §4.3 Event publisher contract

新增内部 helper：

```go
func (s *StudentRunService) publishDetachedRunEvents(ctx context.Context, runID uint64, events <-chan stream.Event)
```

规则：

- 必须 drain 到 channel close，不能因为 Redis publish 失败停止。
- publish ctx 使用 background/user ctx，不使用 HTTP request ctx。
- 记录是否见到 `EventTerminal` / `EventError`；如果 runner 返回 error 且未见 terminal/error，可 best-effort publish 一个 `error` event，让在线观察者有明确失败感知。DB 终态仍由 runner/finalize 负责。
- `PublishRunEvent` 继续 best-effort；Redis broker failure 不影响 runner completion。

### §4.4 Controller SSE 改造

`CreateStream` 新流程：

1. 鉴权、bind request。
2. `prepared, err := runSvc.PrepareStreamRun(c.Request.Context(), user.ID, req)`；失败按现有 JSON 错误返回。
3. 立即调用 `runSvc.StartPreparedStreamRun(prepared)`。如果 registry 已 active，仍继续观察该 run。
4. 切换 SSE header，立即写 `:ok\n\n` 并 flush。
5. 调用新的 `observeRunEvents(c, user.ID, prepared.RunID, after="")`，从 Redis Stream replay 全部窗口内事件，再跟 live。

硬约束：`PrepareStreamRun` 一旦成功创建 DB run，controller 必须在任何可能失败的 SSE write/flush/subscribe 之前调用 `StartPreparedStreamRun`。即使 SSE header flush、broker subscribe 或 client connection 已经失败，也不能留下一个 `running` 但没有后台 runner 的预创建行。

`AnswerStream` 新流程：

1. 鉴权、parse runID、bind answer。
2. `started, err := runSvc.StartPreparedAnswerStream(c.Request.Context(), user.ID, runID, req)`；校验失败按现有 JSON 错误返回。
3. 切换 SSE header，写 `:ok\n\n`。
4. `observeRunEvents(c, user.ID, runID, after="")`。如果该 run 的 answer 已被另一个请求抢先持久化，`StartPreparedAnswerStream` 会因 state 不再 waiting 返回现有 invalid input。

`observeRunEvents` 只负责观察：

- `SubscribeRunEvents(c.Request.Context(), userID, runID, after)`。
- 对每个 `PublishedEvent` 写 SSE。若有 cursor，使用 SSE `id: <cursor>`。
- client disconnect / write error 只停止当前 handler，并记录 observer span `disconnect_reason=observer_disconnect|write_error|run_complete`。
- 不调用 runner cancel。
- 如果 broker unavailable，先从 DB load run 取 `session_id`，发送一个无 cursor 的 synthetic `stream_start` frame `{session_id, run_id, observer_fallback:true}` 后关闭，让前端进入 polling；后台 execution 已经启动或已在运行。

### §4.5 RunEventBroker / SubscribeRunEvents contract

保留现有端点：

- `GET /v1/agent-runs/:id/events?after=<cursor>`
- `after=""`：从当前 Redis Stream 最早可用事件开始 replay，然后跟 live。
- `after="pause"`：保持现有 external continuation 语义，从最近 waiting terminal 后开始。
- terminal DB fallback：如果 DB run 已 terminal 且 broker missed close frame，返回 synthetic terminal event。
- 鉴权顺序不变：先 DB owner 校验，再访问 Redis。

### §4.6 Explicit cancel contract

`StudentRunService.Cancel` 保留 DB ownership + `run.Status=="running"` 校验，然后：

```go
supervisor.Cancel(runID) // best-effort, covers pre-runner-register window
s.runner.Cancel(runID)  // existing query cancel
```

二者都 best-effort；只要 run 仍 running，接口语义仍是“已发出取消”。最终状态由 runner/finalize 写入 DB。

### §4.7 SubscriptionLock 调整

现有 `stream.SubscriptionLock` 是“单 SSE 观察者锁”。新设计允许多观察者独立订阅 `/events`，因此 controller 主路径不再依赖它。该类型可保留给旧测试或后续清理，但不能再保护 execution lifecycle。

---

## §5 前端设计

### §5.1 普通 running run 恢复

`agentChat.loadSessionSnapshot` 当前只在 `waiting_for_user_choice` 或 queued external continuation 时恢复 `currentRun`。改为：

- 如果 `snap.run.status` 是 `running` 或 `pending`，也设置 `currentRun=snap.run`。
- waiting/external continuation 的特殊状态映射保留。
- terminal run 不设置 active `currentRun`，继续按 snapshot messages 展示历史。

### §5.2 统一 reattach helper

在 `useAgentStream.ts` 中新增或扩展 helper：

```ts
attachRunEvents(runId: number, opts?: { after?: string; baseline?: 'cursor' | 'from_start' | 'pause' })
```

规则：

- 普通 running run 默认使用 saved cursor；没有 cursor 时传 `after=""`，允许 Redis replay 窗口内从头补。
- external continuation 默认保留 `after="pause"` 语义。
- 若 `/events` 503/网络失败/流提前关闭且未 terminal，启动 `useAgentRun().startStatusPolling()`。
- terminal event 后清理 cursor，并调用现有 `reconcileFromDB`。

现有 `attachContinuation` 可变成 `attachRunEvents(runId, { baseline: 'pause' })` 的 thin wrapper，减少两套重连逻辑。

### §5.3 初始 stream / answer stream 早结束 fallback

`useAgentStream.start` 和 `startResume` 需要处理“HTTP stream 正常结束但没有 terminal”的情况。这可能发生在 broker 不可用、代理关闭观察连接或后端主动降级时。

规则：

- 如果 stream 返回时 `finalTerminalSeen=false` 且 `store.currentRun` 仍 active，调用 `attachRunEvents(currentRun.id)`。
- 如果 attach 失败，启动 status polling。
- `AbortError` 仍代表本地观察停止，不显示错误；如果页面还在且 run active，也允许后续 watcher/polling 接管。

### §5.4 AgentChatView watcher

新增 watcher：当 `store.currentRun` active、当前没有 `isStreaming`、没有 `fallbackPolling`、没有 status polling 时，自动调用普通 `attachRunEvents`。它覆盖：

- 用户打开历史 session 时 snapshot 返回 running run。
- 用户刷新页面后 currentRun 从 `sessionStorage.agentChat:currentRunId` 恢复。
- initial stream 因 observer fallback 关闭后，store 已拿到 `stream_start`。

现有 external continuation watcher 保留，但可改为调用同一个 helper 的 `pause` baseline。

### §5.5 Unmount / stop 语义

- `onUnmounted` 继续 `stopStream()`、停止 narration/status polling、reset store；它只 abort 本地 fetch，不调 cancel。
- `handleStop` 保持先停本地观察，再调用 `runCtrl.cancel()`。后端 cancel 扩展后，用户点击停止仍能终止后台 execution。

---

## §6 API 契约

### `POST /v1/agent-runs/stream`

请求体不变。成功响应仍为 `text/event-stream`。

行为变化：

- HTTP/SSE disconnect 不取消 run。
- frames 尽量带 SSE `id` cursor；无 broker 或 synthetic fallback frame 可无 id。
- 连接可能在 run terminal 前结束；前端必须把这视为 observer loss，并切 `/events` 或 polling，不能判 run failed。

最小首帧：

```json
{
  "type": "stream_start",
  "run_id": 123,
  "data": {
    "session_id": "uuid",
    "run_id": 123,
    "observer_fallback": true
  }
}
```

`observer_fallback` 为可选字段；旧前端忽略，新前端用它更快进入 polling。

### `POST /v1/agent-runs/:id/answer-stream`

请求体不变。校验失败仍在执行启动前返回 JSON 错误。校验成功后切 SSE，并启动 supervised resume。HTTP/SSE disconnect 不取消 resumed execution。

### `GET /v1/agent-runs/:id/events?after=...`

保持现有协议。新增前端使用方式：普通 running run 允许 `after=""` 从 Redis 窗口内最早事件 replay；external continuation 继续用 `after=pause`。

### `POST /v1/agent-runs/:id/cancel`

请求体/响应不变。语义强化：同时取消 supervisor parent ctx 与 runner query ctx。

---

## §7 Trace / 可观测性拓扑

本功能不新增 LLM generation 点。

- **Trace 起点**：继续由 `agentRunner.Run` / `agentRunner.RunStream` 创建 `agent-runtime-run` trace。
- **Generation 点**：沿用现有 Eino/provider adapter generation 和 billing middleware，不新增 generation name。
- **Answer span**：`validateAndPersistAnswer` 的 `tool.ask_user_question.resume` span 保持同步记录。
- **Observer span**：controller SSE span 改为观察维度，metadata 使用 `observer_disconnect`、`write_error`、`run_complete` 等，不再把 client disconnect 记录成执行 abort。
- **关键 metadata**：`agent_run_id`, `session_id`, `user_id`, `agent_definition_id`, `state_reason`, `terminal_reason`, `observer_disconnect_reason`, `broker_publish_error_count`。

Billing 上下文仍由 runner 注入。页面退出后的正常完成必须走正常 Reserve/Reconcile；显式 cancel 走现有取消终态。

---

## §8 失败模式

| 场景 | 预期行为 |
|---|---|
| 首帧前页面断开 | run 已 precreate/start 则继续；没有 precreate 则没有 run |
| terminal 前页面断开 | observer handler 返回，supervised runner 继续，DB 最终落库 |
| terminal 后 finalize 前页面断开 | publisher 继续 drain；runner 用现有 persist ctx 完成 DB 写入 |
| Redis publish 失败 | 记录 warn，runner 继续；前端用 polling/snapshot 收敛 |
| `/events` subscribe 失败 | 前端转 polling；不显示 run failed |
| 用户立即点击停止 | cancel API 调 supervisor + runner，覆盖 runner cancel 尚未注册窗口 |
| 两个 tab 观察同一 run | 都可 `/events` subscribe；cursor 独立，不互相消费 |
| 两个 tab 提交同一 answer | 第一个成功并清 pending；第二个收到现有 invalid input |
| 进程重启中断 in-memory run | 本次不恢复执行；snapshot/status 只展示真实 DB 状态，不伪造完成 |

---

## §9 测试策略

### 后端单测

- `CreateStream_ClientDisconnect_DoesNotCancelExecution`：request ctx cancel 后 handler 返回，但 supervised runner stub 继续发送 terminal 并完成。
- `StartPreparedStreamRun_DrainsEventsWithoutSubscribers`：没有 SSE observer 时，event channel 不阻塞，runner returns。
- `StartPreparedStreamRun_PublishUsesDetachedContext`：HTTP ctx canceled 后 publish 仍被调用；publish failure 不阻止 runner。
- `StartPreparedAnswerStream_ValidationIsSynchronous`：非 owner/非 waiting/空 answer 在启动前返回错误，不启动 supervisor。
- `Cancel_CancelsSupervisorAndRunner`：cancel 同时命中 execution registry cancel 和 existing runner cancel。
- `SubscribeRunEvents_AfterEmptyReplaysFromBeginning`：普通 reattach 使用空 cursor 能回放窗口内事件。

### 前端单测

- `loadSessionSnapshot` 对普通 `running/pending` run 恢复 `currentRun`。
- reloaded historical running session 自动调用 `attachRunEvents(runId, after="")`，失败后 polling。
- initial stream 正常结束但无 terminal 时 fallback attach/poll，不展示错误。
- `AbortError` 不展示错误；`handleStop` 仍调用 cancel API。
- terminal event 后清 cursor，调用 DB reconcile。

### 浏览器 QA

用 mocked Agent stream 或 Dev 可控长任务验证：

1. 发起 Agent run。
2. 首帧后关闭/刷新页面。
3. 等待后回到同一 session。
4. 看到 running 状态恢复或最终答案展示。
5. 重复一次点击“停止任务”，确认 run 取消而不是继续完成。

---

## §10 PRD 覆盖矩阵

| PRD 验收标准 | 设计覆盖 |
|---|---|
| 初始流式 run 断连后不进入 `aborted_streaming` | §4 supervised runner 不使用 HTTP ctx；§8 terminal 前断开 |
| answer-stream 断连后 resume leg 继续 | §4.2 `StartPreparedAnswerStream` |
| 点击停止仍取消 | §4.6、§5.5 |
| 回来后已完成显示最终结果 | §5.1、§5.3、DB snapshot SOT |
| 回来后仍 running 恢复观察 | §5.1、§5.2、§5.4 |
| 多 tab/刷新不重复启动/不抢事件 | §4.1 registry、§4.5 broker subscription、§8 |
| Redis 不可用不影响执行 | §4.3、§4.4 fallback、§8 |
| 非 owner 不能订阅 | §4.5 owner-before-broker |
| 断连正常完成计费不混淆 | §7 billing context and cancel distinction |

---

## §11 非目标与后续

- 不新增 DB schema 或 migration。
- 不引入持久任务队列。
- 不承诺进程/容器重启后恢复 in-flight execution。
- 不改变 Agent event JSON 主协议；只增加可选 `observer_fallback` 字段。
- 后续可单独立项：durable Agent worker、跨进程 execution claim、长 run watchdog、永久事件审计表。
