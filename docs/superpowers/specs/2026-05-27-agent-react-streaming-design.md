# Agent ReAct 流式化 — S2 技术设计

> **Feature**: `agent-react-streaming` (Standard track)
> **Date**: 2026-05-27
> **Status**: Draft, awaiting S2 gate
> **Inputs**: [requirement-card](../../../requirements/agent-react-streaming.md) · [proposal+PRD](../../../proposals/agent-react-streaming-proposal.md)
> **Reference impl**: Claude Code at `/Users/zhiyuchen/Downloads/ClaudeCode/src` (async-generator ReAct loop in `query.ts`, consumer in `QueryEngine.ts`)

---

## §1 架构总览

```
┌────────────────────────── 浏览器 (numind-web-v3) ───────────────────────────┐
│                                                                               │
│   AgentChatView.vue                                                           │
│       │ submitAnswer / submitMessage                                          │
│       ▼                                                                       │
│   useAgentStream.ts ── fetchSSE() ─┐                                          │
│       │ events (onEvent callback)  │                                          │
│       ▼                            │                                          │
│   agentChat.ts (Pinia store)       │                                          │
│       │ incremental state updates  │                                          │
│       ▼                            │                                          │
│   <AssistantStreamingMessage>      │                                          │
│   <ToolGroupMessage>               │                                          │
│   <PlanMessage> / etc.             │                                          │
│                                    │                                          │
└────────────────────────────────────│──────────────────────────────────────────┘
                                     │  text/event-stream  data: {...}\n\n
                                     ▼
┌────────────────────────── 后端 (numind-server) ────────────────────────────┐
│                                                                              │
│   POST /v1/agent-runs?stream=1                                               │
│      │ controller/v1/agent/student_run_stream.go (new)                       │
│      │   - validate, headers, gin SSE writer                                 │
│      │   - acquire single-subscriber lock for runID                          │
│      │   - on already-locked → 409, frontend falls back to poll              │
│      ▼                                                                       │
│   agentRunner.RunStream(ctx, req, ch <-chan AgentEvent)  (new)               │
│      │ same hook chain, same TerminalReason logic                            │
│      │ same compactv2, same Langfuse trace                                   │
│      ▼                                                                       │
│   einoAgent.Stream(ctx, msgs) → *schema.StreamReader[*schema.Message]        │
│      │ (eino native — react.go:485, verified v0.8.13)                        │
│      ▼                                                                       │
│   aiserviceAdapter.Stream → aiservice.ChatStream                             │
│      │ middleware chain Tracing→Fallback→Budget→Billing→Retry→Adapter        │
│      ▼                                                                       │
│   DMXAPIAdapter.ChatStream / aliAdapter.ChatStream / volcAdapter.ChatStream  │
│      │ (existing, working)                                                   │
│      ▼                                                                       │
│   dmxapi.cn / aihubmix / ali-dashscope / volcengine                          │
└──────────────────────────────────────────────────────────────────────────────┘
```

核心思路：**`agentRunner.RunStream` 是 `Run` 的姊妹方法**。不改 ReAct 内核语义，只增加一条 event 通道把"已经发生的事"实时推给调用方。`Run` 保留供 SDK / 同步客户端用，behavior 不变。

---

## §2 风险逐一收敛（决策落定）

| # | 风险 | 决策 | 依据 |
|---|------|------|------|
| **R1** | Eino ReAct loop 不支持流式 | **解除**：Eino v0.8.13 `react.Agent.Stream()` 已实现（[react.go:485](https://github.com/cloudwego/eino/blob/v0.8.13/flow/agent/react/react.go#L485)），返回 `*schema.StreamReader[*schema.Message]`。走原生流式。 | 验证完成 |
| **R2** | Hook 链与流式兼容 | Hook 在 **message boundary** 触发（一个完整的 schema.Message 收齐后），不在 token 级。流式只影响"何时把累积的 message 出口给 hook"，不影响 hook 调用次数。S4 加 e2e 测试覆盖。 | 现状代码已是此语义（hook 在 PreToolCall/PostToolCall 两个 boundary，不在 chunk 级） |
| **R3** | 断流恢复 | 自动 fallback。SSE 失败 → 前端 retry 1 次 → 再失败转为 GET `/v1/agent-runs/:id` 5s 轮询直到 terminal。 | 复用现有 [agentChat.ts:234-261](numind-web-v3/src/stores/agentChat.ts) 的 refreshRunStatus |
| **R4** | 多标签共享一个 run | **后开者轮询降级**。后端用 `sync.Map[runID]bool` 单订阅锁，第二个连接进来返回 409 + 现有状态 snapshot。前端收到 409 → fallback poll。 | 用户已签收 |
| **R5** | 事件流 vs DB 一致性 | DB 是最终 SoT。流式事件渲染中间态；stream `terminal` 事件到达后，前端拉一次 GET `/v1/agent-runs/:id` 做最终校准（messages JSON + terminal_metadata 以 DB 为准）。 | 简化方案，避免双写一致性 |
| **R6** | aiservice 中间件流式行为 | 复用现有约定：`ctxKeyFirstChunkSent` 已实现，Retry 中间件在 first chunk 后不再 retry；Billing 在 `IsFinal=true` chunk 拿 usage；Fallback 在 stream 起步前才能切换 provider。无需新代码，但 S5 覆盖测试。 | [retry.go:87-93](numind-server/internal/pkg/aiservice/middleware/retry.go) |

---

## §3 事件协议

### §3.1 Go 类型（后端内部 + JSON wire）

新增包 `internal/numind/biz/agent/stream/`，定义事件 union：

```go
package stream

import (
    "encoding/json"
    "time"
)

type EventType string

const (
    EventStreamStart   EventType = "stream_start"   // SSE 握手后立刻发，含 run_id / session_id
    EventTokenDelta    EventType = "token_delta"    // LLM 文本增量（最热的事件，最高频）
    EventReasoningDelta EventType = "reasoning_delta" // thinking 模型的内部推理增量
    EventAssistantMessage EventType = "assistant_message" // 完整 assistant turn（每步结束时发）
    EventToolCallStart EventType = "tool_call_start" // tool 被调用
    EventToolCallProgress EventType = "tool_call_progress" // 工具内部 narration（已有的 narration "progress" 状态）
    EventToolCallResult EventType = "tool_call_result" // 工具完成（含 preview）
    EventToolCallError EventType = "tool_call_error"
    EventStepDone      EventType = "step_done"      // 一个 ReAct iteration 完整收尾
    EventStateChange   EventType = "state_change"   // 状态机转移（LoopEvent）
    EventQuestionPrompt EventType = "question_prompt" // ask_user_question yield
    EventTerminal      EventType = "terminal"       // 流式结束（含 TerminalReason）
    EventError         EventType = "error"          // 流式中途致命错误
    EventPing          EventType = "ping"           // 25s keepalive
)

// Event 是 SSE 出口的统一信封。
type Event struct {
    Type      EventType       `json:"type"`
    Seq       uint64          `json:"seq"`           // 单调递增序号（断流重连时用于补漏，暂不实现，先占位）
    Timestamp time.Time       `json:"ts"`
    RunID     uint64          `json:"run_id"`
    StepIndex int             `json:"step,omitempty"` // 当前 ReAct 步号
    Data      json.RawMessage `json:"data,omitempty"` // 类型相关 payload，下面分类
}

// 每种 EventType 对应一个 payload struct（写入 Event.Data 前 json.Marshal）：

type TokenDeltaPayload struct {
    MessageID string `json:"message_id"` // assistant message uuid（多 step 时区分气泡）
    Text      string `json:"text"`        // 增量文本片段
}

type ReasoningDeltaPayload struct {
    MessageID string `json:"message_id"`
    Text      string `json:"text"`
}

type AssistantMessagePayload struct {
    MessageID    string  `json:"message_id"`
    Content      string  `json:"content"`
    ReasoningContent string `json:"reasoning_content,omitempty"`
    HasToolCalls bool    `json:"has_tool_calls"`
}

type ToolCallStartPayload struct {
    ToolCallID string         `json:"tool_call_id"`
    ToolName   string         `json:"tool_name"`
    InputDigest string        `json:"input_digest"` // 哈希，前端展开时按 ID 拉
    InputPreview map[string]any `json:"input_preview,omitempty"` // 截断后的 input JSON（前 500 字符）
}

type ToolCallProgressPayload struct {
    ToolCallID string `json:"tool_call_id"`
    Message    string `json:"message"` // narration.Event.Message
    Verb       string `json:"verb,omitempty"`
}

type ToolCallResultPayload struct {
    ToolCallID  string `json:"tool_call_id"`
    Preview     string `json:"preview"` // 截断结果（前 500 字符）
    ArtifactURL string `json:"artifact_url,omitempty"` // 若有，拉完整结果用
    DurationMs  int64  `json:"duration_ms"`
}

type StepDonePayload struct {
    StepIndex int    `json:"step_index"`
    StopReason string `json:"stop_reason,omitempty"` // LLM 给的 finish_reason
}

type StateChangePayload struct {
    LoopEvent     string `json:"loop_event"`
    PreviousState string `json:"previous_state,omitempty"`
}

type QuestionPromptPayload struct {
    Question     string   `json:"question"`
    Options      []string `json:"options"`
    Header       string   `json:"header,omitempty"`
    MultiSelect  bool     `json:"multi_select"`
}

type TerminalPayload struct {
    Reason           string                 `json:"reason"`            // TerminalReason enum value
    Duration         int64                  `json:"duration_ms"`
    StepCount        int                    `json:"step_count"`
    FinalOutput      string                 `json:"final_output,omitempty"`
    TerminalMetadata map[string]any         `json:"terminal_metadata,omitempty"`
    PermissionDenial map[string]any         `json:"permission_denial,omitempty"`
}

type ErrorPayload struct {
    Code    string `json:"code"`    // "model_error" / "permission" / "internal"
    Message string `json:"message"` // human readable, may match terminal_metadata.error_message
}
```

### §3.2 SSE 帧格式

每个 Event JSON marshal 后作为一帧：

```
data: {"type":"token_delta","seq":42,"ts":"2026-05-27T13:42:01.123Z","run_id":99,"step":3,"data":{"message_id":"msg_a7b","text":" 接下来"}}

data: {"type":"tool_call_start","seq":43,"ts":"2026-05-27T13:42:01.456Z","run_id":99,"step":3,"data":{"tool_call_id":"tc_5","tool_name":"web_search","input_digest":"a9772f5ec...","input_preview":{"query":"教培 AI 应用案例"}}}

```

末尾 `\n\n` 强制 flush。注释行（`:`）作 keepalive（每 25s 发 `:ping\n\n`）。

### §3.3 TypeScript 类型（前端 mirror）

`numind-web-v3/src/types/agent-stream.ts`（新增）：

```typescript
export type AgentStreamEventType =
  | 'stream_start' | 'token_delta' | 'reasoning_delta' | 'assistant_message'
  | 'tool_call_start' | 'tool_call_progress' | 'tool_call_result' | 'tool_call_error'
  | 'step_done' | 'state_change' | 'question_prompt' | 'terminal' | 'error' | 'ping'

export interface AgentStreamEvent<T = unknown> {
  type: AgentStreamEventType
  seq: number
  ts: string
  run_id: number
  step?: number
  data?: T
}

// 各 payload 类型与后端字段一一对应（snake_case → camelCase 转换在 API 层完成）
```

---

## §4 后端设计

### §4.1 新增 SSE endpoint

**File**: `internal/numind/controller/v1/agent/student_run_stream.go` (new, ~150 LOC)

**Route**: `POST /v1/agent-runs?stream=1`
- 复用现有 `POST /v1/agent-runs` controller，根据 query param 分流
- 或：单独 `POST /v1/agent-runs/stream`（参考 chatbot 模式）—— S3 决定，倾向单独路由清晰

**Pattern**（照搬 [chatbot.go:262-371](numind-server/internal/numind/controller/v1/chatbot/chatbot.go) 的 SSE 写法）：

```go
func (h *StudentRunController) CreateStream(c *gin.Context) {
    user := middleware.GetCurrentUser(c)
    var req agent.CreateRunRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        core.WriteResponse(c, errno.ErrBind.SetMessage("..."), nil)
        return
    }

    // Single-subscriber lock per run (R4 fallback).
    runID, acquired, err := h.runSvc.AcquireStreamLock(c, user.ID, req)
    if err != nil { ... }
    if !acquired {
        c.Status(http.StatusConflict)
        c.JSON(http.StatusConflict, gin.H{
            "code": errno.ErrAgentStreamAlreadyAttached.Code,
            "message": "another client is streaming this run",
            "data": gin.H{"run_id": runID},
        })
        return
    }
    defer h.runSvc.ReleaseStreamLock(runID)

    // SSE headers
    c.Header("Content-Type", "text/event-stream")
    c.Header("Cache-Control", "no-cache")
    c.Header("Connection", "keep-alive")
    c.Header("X-Accel-Buffering", "no")
    w := c.Writer
    flusher, _ := w.(http.Flusher)

    // Wire the run + event consumer
    eventCh := make(chan stream.Event, 256)
    runCtx, cancel := context.WithCancel(c.Request.Context())
    defer cancel()

    go func() {
        defer close(eventCh)
        _, _ = h.runSvc.RunStream(runCtx, user.ID, req, runID, eventCh)
    }()

    // Keepalive ticker
    ping := time.NewTicker(25 * time.Second)
    defer ping.Stop()

    encode := func(ev stream.Event) {
        b, _ := json.Marshal(ev)
        _, _ = fmt.Fprintf(w, "data: %s\n\n", b)
        flusher.Flush()
    }

    for {
        select {
        case <-runCtx.Done():
            return
        case <-ping.C:
            _, _ = fmt.Fprint(w, ":ping\n\n")
            flusher.Flush()
        case ev, ok := <-eventCh:
            if !ok { return }
            encode(ev)
            if ev.Type == stream.EventTerminal || ev.Type == stream.EventError {
                return
            }
        }
    }
}
```

### §4.2 Runner 改造

**File**: `internal/numind/biz/agent/runner.go` — 新增 `RunStream` 方法

```go
// RunStream is the streaming sibling of Run. Identical contract (same hook chain,
// same TerminalReason, same compactv2, same Langfuse trace) except that events
// are emitted to ch as they happen instead of being returned in a final RunResult.
// ch is owned by the caller and MUST be drained until closed.
//
// Lifecycle: closes ch on terminal (success/error). Caller's ctx cancel
// propagates to abort the LLM call and tool execution.
func (r *agentRunner) RunStream(
    ctx context.Context,
    req RunRequest,
    runID uint64,           // pre-allocated by AcquireStreamLock
    ch chan<- stream.Event,
) (*RunResult, error) {
    // ... identical setup to Run() up through einoAgent construction ...

    // The single difference: einoAgent.Stream() instead of Generate().
    streamReader, err := einoAgent.Stream(queryCtx, einoMessages)
    if err != nil { ... }

    return r.consumeEinoStream(queryCtx, run, streamReader, ch, st, ...)
}

// consumeEinoStream reads schema.Message chunks off the eino StreamReader and
// emits stream.Event into ch. Maintains step boundary tracking, tool call
// state, and TerminalReason exactly like Run() does, just async.
func (r *agentRunner) consumeEinoStream(
    ctx context.Context,
    run *model.AgentRun,
    sr *schema.StreamReader[*schema.Message],
    ch chan<- stream.Event,
    st *LoopState, ...) (*RunResult, error) {

    defer sr.Close()

    var (
        currentMsgID  = uuid.NewString()
        currentText   strings.Builder
        currentReason strings.Builder
        toolCalls     map[string]*toolCallState // by ToolCallID
        stepIdx       = 0
        seq           uint64
    )

    emit := func(t stream.EventType, payload any) {
        seq++
        b, _ := json.Marshal(payload)
        ev := stream.Event{
            Type: t, Seq: seq, Timestamp: time.Now(),
            RunID: run.ID, StepIndex: stepIdx, Data: b,
        }
        select {
        case ch <- ev:
        case <-ctx.Done():
        }
    }

    emit(stream.EventStreamStart, map[string]any{"session_id": run.SessionID})

    for {
        msg, err := sr.Recv()
        if errors.Is(err, io.EOF) { break }
        if err != nil { ... } // emit error, set TerminalModelError

        // schema.Message classification:
        switch msg.Role {
        case schema.Assistant:
            // Text delta
            if msg.Content != "" {
                currentText.WriteString(msg.Content)
                emit(stream.EventTokenDelta, stream.TokenDeltaPayload{
                    MessageID: currentMsgID, Text: msg.Content,
                })
            }
            if msg.ReasoningContent != "" {
                currentReason.WriteString(msg.ReasoningContent)
                emit(stream.EventReasoningDelta, stream.ReasoningDeltaPayload{
                    MessageID: currentMsgID, Text: msg.ReasoningContent,
                })
            }
            // Tool calls (always at boundary, not stream)
            for _, tc := range msg.ToolCalls {
                if _, seen := toolCalls[tc.ID]; seen { continue }
                toolCalls[tc.ID] = &toolCallState{StartedAt: time.Now()}
                emit(stream.EventToolCallStart, stream.ToolCallStartPayload{...})
            }
        case schema.Tool:
            // Tool result message
            if state, ok := toolCalls[msg.ToolCallID]; ok {
                emit(stream.EventToolCallResult, stream.ToolCallResultPayload{
                    ToolCallID: msg.ToolCallID,
                    Preview: truncate(msg.Content, 500),
                    DurationMs: time.Since(state.StartedAt).Milliseconds(),
                })
            }
        }

        // Detect message boundary (FinishReason populated → assistant message complete)
        if msg.ResponseMeta != nil && msg.ResponseMeta.FinishReason != "" {
            emit(stream.EventAssistantMessage, stream.AssistantMessagePayload{
                MessageID: currentMsgID,
                Content: currentText.String(),
                ReasoningContent: currentReason.String(),
                HasToolCalls: len(msg.ToolCalls) > 0,
            })
            emit(stream.EventStepDone, stream.StepDonePayload{
                StepIndex: stepIdx, StopReason: msg.ResponseMeta.FinishReason,
            })
            stepIdx++
            currentMsgID = uuid.NewString()
            currentText.Reset()
            currentReason.Reset()
        }
    }

    // Identical terminal handling to Run() — set st.TerminalReason, persist
    // messages JSON, MergeTerminalMetadata, UpdateState. Then emit terminal.
    // ... [Run() ending block, factored out into a shared helper]

    emit(stream.EventTerminal, stream.TerminalPayload{
        Reason: string(st.TerminalReason),
        Duration: time.Since(startTime).Milliseconds(),
        StepCount: st.StepCount,
        FinalOutput: finalText,
        TerminalMetadata: lastTerminalMetadata,
    })

    return &RunResult{...}, nil
}
```

**关键不变性**：
- I2 TerminalReason 19 值不变 — 流式路径走完同样的 state machine
- I5 aiservice 唯一入口 — adapter.Stream() 内部仍调 aiservice.ChatStream
- I3 system prompt 6 段顺序不变
- I6 HookAction 5 值不变
- I7 LoopEvent 19 值不变

### §4.3 Adapter 改造

**File**: `internal/numind/biz/agent/adapter.go` — 现有 `Stream()` 方法 (line 177) 已存在但被忽略，本 feature 把它接入。

无需新代码，确认现有 `Stream()` 正确即可。

### §4.4 Hook 链语义（R2 详细约定）

| Hook 触发点 | 流式行为 |
|------------|---------|
| `PreToolCall` | 在 `tool_call_start` event 发出**前**触发。Hook 拒绝 → emit `tool_call_error` + 进入 state machine LoopEvent。Hook 允许 → emit `tool_call_start` + 执行 tool。 |
| `PostToolCall` | 在 `tool_call_result` event 发出**前**触发。Hook 改写 → 改写后 result 进 emit。 |
| `CheckUserInput` | 在 RunStream 入口、emit `stream_start` 之后。拒绝 → emit `terminal` with `permission_denied`。 |
| `CheckLLMOutput` | 在 `assistant_message` event 发出**前**触发（即每个 step 收尾时）。Hook 拒绝 → emit `tool_call_error` 或 `terminal`。 |

Hook 不在 `token_delta` / `reasoning_delta` 粒度运行，这两个事件是 raw passthrough（仅作前端显示）。审计 / compliance 在 message boundary 一次性看到完整 content。

### §4.5 单订阅锁（R4 实现细节）

**File**: `internal/numind/biz/agent/stream/lock.go` (new, ~30 LOC)

```go
type SubscriptionLock struct {
    mu      sync.Mutex
    locked  map[uint64]struct{}
}

func (l *SubscriptionLock) Acquire(runID uint64) bool {
    l.mu.Lock()
    defer l.mu.Unlock()
    if _, ok := l.locked[runID]; ok {
        return false
    }
    l.locked[runID] = struct{}{}
    return true
}

func (l *SubscriptionLock) Release(runID uint64) {
    l.mu.Lock()
    defer l.mu.Unlock()
    delete(l.locked, runID)
}
```

进程内 lock。多进程场景（kubernetes 多 pod）暂不支持——agent run 的 goroutine 当前也是 stick-to-one-pod（没有跨 pod 迁移机制），所以单进程锁够用。S3 plan 标注，未来若上水平扩容再加 Redis 锁。

### §4.6 取消/断流处理（R3 实现细节）

- 客户端断开 → gin's `c.Request.Context()` Done → `runCtx` cancel → eino `sr.Close()` + 取消 ChatStream goroutine → 状态机走 `LoopEventCtxCanceled` 路径 → emit `terminal` with `aborted`
- DB 中 agent_run 状态正常落 `terminated` + `state_reason=aborted_streaming`
- 浏览器收到后续 GET /v1/agent-runs/:id 时能看到一致状态

### §4.7 持久化时机（R5 详细约定）

- `agent_run.status` 在 `RunStream` 入口设为 `running`，与 `Run` 一致
- `agent_run.messages` JSON 仅在 stream **结束时**写入（与 `Run` 一致）—— 流式中间状态不落 DB
- `agent_run.terminal_metadata` 在 `RunStream` 收尾写入（与 `Run` 一致）
- Stream 事件**不持久化**——是 transport，不是 SoT

前端收到 `terminal` 事件后，调用现有 `GET /v1/agent-runs/:id` 拉最终 messages 做对账（防止流式累积的与 DB 写入的不一致，例如压缩/scrubbing 处理过的 final text）。

---

## §5 前端设计

### §5.1 SSE 消费器

**File**: `numind-web-v3/src/api/agent-stream.ts` (new, ~80 LOC)

复用 `sales.ts:readSSEStream` + `fetchSSE` 模式：

```typescript
export async function streamAgentRun(
  req: CreateRunRequest,
  onEvent: (e: AgentStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const response = await fetch('/v1/agent-runs?stream=1', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${getToken()}` },
    body: JSON.stringify(req),
    signal,
  })
  if (response.status === 409) {
    throw new AgentStreamConflict(await response.json())  // triggers fallback to poll
  }
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  if (!response.body) throw new Error('no stream body')
  await readSSEStream(response, (chunk) => {
    const event = parseSseChunk<AgentStreamEvent>(chunk)
    if (event) onEvent(event)
  })
}
```

### §5.2 Composable

**File**: `numind-web-v3/src/composables/useAgentStream.ts` (new, replaces `useAgentRun.ts` polling parts)

```typescript
export function useAgentStream(store: AgentChatStore) {
  const abort = ref<AbortController | null>(null)
  const isStreaming = ref(false)
  const fallbackPolling = ref(false)

  async function start(req: CreateRunRequest) {
    isStreaming.value = true
    abort.value = new AbortController()
    try {
      await streamAgentRun(req, (e) => store.applyStreamEvent(e), abort.value.signal)
    } catch (err) {
      if (err instanceof AgentStreamConflict) {
        // Fall back to polling existing run
        fallbackPolling.value = true
        store.startStatusPolling(err.runId)
      } else {
        store.applyError(err)
      }
    } finally {
      isStreaming.value = false
    }
  }

  function stop() { abort.value?.abort() }

  return { start, stop, isStreaming, fallbackPolling }
}
```

### §5.3 Store 改造

**File**: `numind-web-v3/src/stores/agentChat.ts` — 新增 `applyStreamEvent(event)` action：

```typescript
function applyStreamEvent(e: AgentStreamEvent) {
  switch (e.type) {
    case 'stream_start':
      // Mark connection live
      break
    case 'token_delta': {
      const msg = ensureStreamingAssistantMessage(e.data.message_id)
      msg.markdown += e.data.text
      msg.isStreaming = true
      break
    }
    case 'reasoning_delta': {
      const msg = ensureStreamingAssistantMessage(e.data.message_id)
      msg.reasoning = (msg.reasoning || '') + e.data.text
      break
    }
    case 'assistant_message': {
      const msg = findMessage(e.data.message_id)
      if (msg) {
        msg.isStreaming = false
        msg.markdown = e.data.content  // authoritative final
      }
      break
    }
    case 'tool_call_start': {
      const group = ensureToolGroupForStep(e.step)
      group.tool_calls.push({
        tool_call_id: e.data.tool_call_id,
        tool_name: e.data.tool_name,
        current_state: 'queued',
        events: [],
      })
      break
    }
    case 'tool_call_progress': {
      updateToolCall(e.data.tool_call_id, (tc) => {
        tc.current_state = 'progress'
        tc.events.push({ state: 'progress', message: e.data.message, ts: e.ts })
      })
      break
    }
    case 'tool_call_result': {
      updateToolCall(e.data.tool_call_id, (tc) => {
        tc.current_state = 'result'
        tc.preview = e.data.preview
      })
      break
    }
    case 'question_prompt': {
      messages.value.push({ type: 'question_prompt', ...e.data, answer_status: 'pending' })
      break
    }
    case 'terminal': {
      // Finalize: push final_answer + trigger one GET for DB reconciliation
      void reconcileFromDB(e.run_id)
      break
    }
    case 'error': {
      pushSystemMessage('failed', e.data.message)
      break
    }
  }
}
```

### §5.4 组件改造

**主要改动**：

| 文件 | 改动 |
|-----|------|
| [AssistantTextMessage.vue]（如不存在则新建） | 加 `isStreaming` prop，结尾闪烁光标 `▎`（参考 Claude Code [AssistantTextMessage.tsx]） |
| [ToolGroupMessage.vue] | 已有，加上每个 tool_call 的状态徽章（queued/use/progress/result/error → 灰/蓝/蓝转/绿/红） |
| [AgentChatView.vue] | `handleSend` 改用 `useAgentStream().start()`；输入框 disable / "中止" 按钮逻辑 |
| ChatComposer.vue | 添加 "中止" 按钮，调用 `useAgentStream().stop()` |

不需要重写，是增量调整。

### §5.5 旧轮询路径保留

`useAgentRun.ts` 的 `startStatusPolling` **保留**——用于 fallback 场景（R3 断流恢复 / R4 多标签）。

---

## §6 API 契约

### §6.1 新增 endpoint

**`POST /v1/agent-runs?stream=1`**

请求体：与现有 `POST /v1/agent-runs` 完全一致（`CreateRunRequest`）。

响应：
- **200 OK + `Content-Type: text/event-stream`**：SSE 流，event 协议见 §3
- **409 Conflict**：该 run 已有活跃 SSE 订阅
  ```json
  {"code": <ErrAgentStreamAlreadyAttached.Code>, "message": "...", "data": {"run_id": 42}}
  ```
- **400/401/500**：与现有同步路径行为一致

### §6.2 errno 新增

`internal/pkg/errno/agent.go`（已存在）新增：

```go
var ErrAgentStreamAlreadyAttached = errno.New(40901, "agent stream already attached for this run")
```

### §6.3 router 注册

`internal/numind/router.go`：

```go
authGroup.POST("/agent-runs", c.CreateOrStream)  // 同一 handler 内部按 ?stream=1 分流
// 或：authGroup.POST("/agent-runs/stream", c.CreateStream)（S3 决定）
```

---

## §7 Langfuse Trace Topology

| Trace 起点 | RunStream 入口（与 Run 完全一致；既有 trace ID 不变）|
|-----------|-----------|
| Generation 点 | 每次 ReAct iteration 一个 generation（流式不改变粒度）。output 字段在 stream 关闭时用累积的 final text 写入，**不**逐 chunk 写。|
| 关键 metadata | `agent_run_id`, `step_index`, `stream_protocol_version=v2`, `subscriber_count=1` (R4 决策后固定 1) |
| 新增 span | `sse.connection`（覆盖 SSE 连接生命周期，记录 first_byte_ms / event_count / disconnect_reason）|

具体接线：`internal/numind/biz/agent/stream/langfuse.go` (new, ~60 LOC)，按 `.claude/rules/ai-service.md` §1 模板。

---

## §8 S5 验证策略

> 按 NDF 规则 10：S5 验证策略必须在 S3 plan 中作为独立 task，并在 S2 spec 中先定方向。

**选定方式**：**Playwright E2E + 后端 Go test 双覆盖**（不仅 gstack /qa）

理由：
- agent-mode 是高风险业务逻辑核心（用户 prompt → 多步骤 LLM + 工具）。一次性 /qa 截图不构成回归保护
- 流式涉及时序敏感行为（断流、并发订阅、终止），Playwright 能精确控制 EventSource / fetch reader
- 后端事件协议是契约，单元测试比 e2e 快且更覆盖边界

**Playwright E2E (`numind-web-v3/e2e/agent-streaming.spec.ts`)** 覆盖关键路径：
- [ ] 普通流：提交问题 → 看到第一个 token 在 ≤2s 内出现 → 看到 tool_call_start 卡片 → 看到 result → 看到 final_answer
- [ ] 中止：流式中点击"中止" → 立即停止渲染 → 后端 agent_run.state_reason = `aborted_streaming`
- [ ] 多标签：开第二个标签 → 收到 409 → 自动 fallback 到轮询 → 收到与 stream 一致的最终结果
- [ ] 断流恢复：手动 abort fetch（模拟网络）→ fallback poll 拉到 final state
- [ ] question_prompt：流式触发 ask_user_question → 弹出选项 → 选择后继续流

**后端 Go test (`internal/numind/biz/agent/stream/*_test.go`)** 覆盖单元：
- [ ] event protocol serialization round-trip
- [ ] consumeEinoStream 处理各种 schema.Message 序列（assistant text / tool_call / tool_result / mixed）
- [ ] SubscriptionLock 并发 Acquire/Release
- [ ] hook 链在流式路径下的触发顺序（mock einoAgent）
- [ ] 中途 ctx cancel 不泄露 goroutine（go vet -leakcheck）
- [ ] terminal_metadata 在流式失败时正确写入（已有 model_error 修复的延伸覆盖）

**Langfuse 验证**：
- [ ] dev 启动后触发一次 agent run，确认 Langfuse UI 中能看到 trace + generations + `sse.connection` span
- [ ] sse.first_byte_ms metadata 出现在 trace 上

---

## §9 PRD 验收标准 → 实现 mapping

| PRD 验收项 | 实现 |
|-----------|-----|
| 新增 SSE 接口 | §4.1 |
| 14 种事件类型协议 | §3.1 |
| LLM token-level ≤500ms 到达前端 | 走 eino Stream() + ChatStream，已是 token 级 |
| Eino 原生流式（首选） | §2 R1 + §4.2 |
| 旧 POST /v1/agent-runs 保留 | 不删 `RunSvc.Create` 路径 |
| 5 hook 链兼容 | §4.4 |
| terminal_metadata / messages 一致性 | §4.7 |
| SSE 异常不泄露资源 | §4.6 |
| Langfuse trace 完整 | §7 |
| 前端 ≤500ms 首反馈 | SSE 立刻发 `stream_start` |
| LLM 文本逐字渲染 | §5.3 token_delta + §5.4 AssistantTextMessage |
| Tool call 状态徽章 | §5.4 ToolGroupMessage |
| 多工具按时间序分组 | step_index 已分组 |
| 断流 fallback | §5.2 useAgentStream conflict 处理 |
| 用户中止 | §5.2 abort + §4.6 ctx cancel |
| question_prompt 流式 | §3.1 + §5.3 |
| 边界情况（空流/超长/极快/极慢/断网/崩溃/多标签/取消） | §5.4 + §5.5 + §4.6 + §4.4 |

---

## §10 留给 S3 plan 的决策

S2 已收敛主架构。以下细节在 S3 plan 阶段确定：

1. **路由命名**：`POST /v1/agent-runs?stream=1` vs `POST /v1/agent-runs/stream` —— 前者复用 controller，后者单独 handler 更清晰
2. **AssistantTextMessage 组件**：当前 `messages` 里 `assistant` 类型有 `markdown` 字段，是否复用还是新增子类型 `assistant_streaming` —— 倾向加 `isStreaming` 字段复用
3. **Tool call 状态徽章的视觉设计**：参考 Manus 还是自研一套 —— 倾向 Manus 截图风格
4. **流式光标位置和动画**：`▎` 闪烁还是其他变体 —— 参考 Claude Code 实现
5. **Langfuse span 写入时机**：sse.connection span 在 SSE close 时一次性写还是 lifecycle 分阶段写
6. **errno 编号**：`40901` 是否冲突 —— S3 决定时查
7. **AcquireStreamLock 内部签名**：`func(ctx, userID, req CreateRunRequest) (runID uint64, acquired bool, err error)` —— 同时处理新 run 创建 + 锁获取 vs 拆两步

---

## §11 不在本 spec 范围

- Eino 框架本身的 ChatModel 接口扩展（如需 vision streaming 支持，未来另开 feature）
- Multi-pod 分布式锁（当前 single-pod，未来若需水平扩容再设计）
- Server-Push 给管理端 admin-web 的 agent 监控（已在 S0 排除）
- 流式恢复时的 event seq 续接（暂只占位 seq 字段，不做断点续传）
