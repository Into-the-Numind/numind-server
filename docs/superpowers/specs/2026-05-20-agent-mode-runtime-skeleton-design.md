# Agent 模式 Runtime Skeleton — 技术设计

**Spec date**: 2026-05-20
**Feature ID**: agent-mode-runtime-skeleton（#2/14）
**Track**: Standard
**Status**: DRAFT（S2，待 reviewer pass）
**架构蓝本**: `docs/agent-mode/architecture-v1.md` §4.1（Runtime 详设计）+ §4.1.5（12 Terminal）+ §4.1.9（7 Continue + Go interface 签名）+ §8（数据模型）

## §1 设计概览

### 1.1 目标

把 Phase 0 V2 demo（`cmd/agent-phase0-eino-demo/`）的 adapter 模式工程化为 `internal/numind/biz/agent/` 下的 production Runtime 骨架。**核心契约稳定性**：本 feature 定义的 `AgentRunner` / `RunHooks` / `Tool` interface 是后续 11 个 feature 不能轻易破坏的跨 feature 协议。

### 1.2 核心修复（从 Phase 0 V2 demo 升级）

| Phase 0 V2 demo | #2 production |
|---|---|
| `model.ChatModel` (deprecated) | `model.ToolCallingChatModel` (3 方法 + WithTools 克隆体) |
| `react.AgentConfig.Model` | `react.AgentConfig.ToolCallingModel` |
| 无 hook 接口 | `RunHooks{PreToolCall, PostToolCall}` + `HookAction` enum |
| `cmd/` 下 demo 二进制 | `internal/numind/biz/agent/` 主 server 主路径 |
| 无 DB 持久化 | `agent_run` 表 + WriteTurn 整体覆写 |
| `taskID="phase0-eino-demo"` 常量 | `taskID="agent-runner-<runID>"` 动态 |

### 1.3 关键不变量

1. **agent_run.messages JSON 列 turn 级整体覆写**：禁止 tool 级 incremental UPDATE / append；保证并发安全（蓝本 §4.1.3 P0-1 race 防护）
2. **19 reason 字符串值固化**：DB CHECK constraint + Go typed string constants，跨 feature 永不重命名
3. **RunHooks 接口稳定**：`PreToolCall(ctx, tool, input) (HookAction, error)` / `PostToolCall(ctx, tool, output, err) (HookAction, error)` 两方法 + `HookAction` 三值 enum
4. **AbortController 三层 ctx 派生**：queryCtx → batchCtx → toolCtx，cancel 严格级联
5. **Withhold 两条 chain 互斥优先级**：PTL chain 优先于 max_output_tokens chain（先 compact 才能 retry max_output）
6. **aiservice 唯一入口**：adapter 调 `aiservice.Chat(ctx, taskID, req)` 3 参数，不裸 HTTP
7. **Langfuse trace 完整性**：CreateTrace（含 user_id / agent_run_id metadata）+ ≥1 Generation（LLM）+ ≥1 Span（工具执行 / state transition）
8. **prod 零影响**：不动 config_prod.yaml / 不 SSH prod / 不打 v* tag / 止步 dev container 部署

---

## §2 M1 数据模型设计

### 2.1 agent_run 表 DDL

```sql
CREATE TABLE IF NOT EXISTS agent_run (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id         INT UNSIGNED NOT NULL COMMENT 'FK to user.id; 真实 user 触发的 run',
  session_id      VARCHAR(64) NULL COMMENT '会话标识，多个 run 串成 session（#11 student-ux 用）',
  status          VARCHAR(20) NOT NULL DEFAULT 'running'
                  COMMENT 'running | terminated（其他短期不引入）',
  state_reason    VARCHAR(50) NULL
                  COMMENT '终止/继续原因，值必须命中 19 reason 之一（CHECK constraint）',
  messages        JSON NOT NULL COMMENT 'Eino messages 列表，turn 级整体覆写',
  reservation_id  BIGINT UNSIGNED NULL
                  COMMENT 'FK to credit_reservation.id；#2 创建 NULL，#12 billing-integration 填充',
  started_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  ended_at        DATETIME(3) NULL,
  created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_ar_user_started (user_id, started_at),
  KEY idx_ar_session (session_id),
  KEY idx_ar_status_started (status, started_at),
  CONSTRAINT chk_ar_status CHECK (status IN ('running', 'terminated')),
  CONSTRAINT chk_ar_state_reason CHECK (
    state_reason IS NULL OR state_reason IN (
      'completed','blocking_limit','image_error','model_error',
      'aborted_streaming','prompt_too_long','stop_hook_prevented',
      'aborted_tools','hook_stopped','max_turns','error_max_budget','error_max_retries',
      'next_turn','collapse_drain_retry','reactive_compact_retry',
      'max_output_escalate','max_output_recovery','stop_hook_blocking','token_budget_continue'
    )
  )
);
```

### 2.1.bis 蓝本字段延迟说明（#2 不建 / 谁建）

蓝本 §8 `agent_run` 表含 16+ 字段，#2 只建 9 字段（上面 DDL）。每个延迟字段归属：

| 蓝本字段 | 类型 | #2 不建原因 | 由哪个 feature 添加 |
|----------|------|------------|-------------------|
| `agent_id` | BIGINT FK | #2 用 mock tool，无 agent 配置概念 | #10 `agent-mode-configurator-ux`（agent_definition 表 + agent_id 引用） |
| `tenant_id` | BIGINT | #2 不分租户 | #13 `agent-mode-compliance-3layer`（L2 租户级隔离时引入） |
| `skill_id` | BIGINT FK | #2 无 skill 概念 | #5 `agent-mode-skill-system` |
| `step_count` | INT | runtime 内部状态，不必持久化 | #11 `agent-mode-student-ux`（要可见进度时引入） |
| `turn_count` | INT | 同上 | #11 同上 |
| `token_usage` | JSON | aiservice 已经写 credit_reservation，重复 | #12 `agent-mode-billing-integration`（统一计费视图时聚合） |
| `credits_used / credits_reserved` | BIGINT | credit_reservation 已记 | #12 同上 |
| `last_event_at` | DATETIME | stall detection 用 | #11 stall detector 添加 |
| `retry_attempt` | INT | LoopState 内存字段，#2 不持久化 | #9 `agent-mode-compact`（needs durable retry count when compact restarts） |
| `compact_state` | JSON | compact 数据 | #9 同上 |
| `parent_agent_run_id` | BIGINT | v2 Multi-Agent | v2 features（M5+） |

**关键设计原则**：#2 只引入 Runtime "活" 所必需的最小字段，避免提前建未使用的字段（dead column 不会自然消失，迁移成本高）。新字段由实际需要它的 feature 自己 ALTER TABLE 添加（migration 双文件命名 + 测试覆盖）。

### 2.2 Migration 文件

- Forward：`migrations/20260520_120000_create_agent_run_table.sql`（上面 DDL + DROP TABLE IF EXISTS preamble 仅供 dev 重置）
- Rollback：`migrations/20260520_120000_create_agent_run_table_rollback.sql`（`DROP TABLE IF EXISTS agent_run;`）

### 2.3 GORM Model

```go
// internal/pkg/model/agent_run.go
package model

import (
    "time"
    "gorm.io/datatypes"
)

type AgentRun struct {
    ID             uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
    UserID         uint           `gorm:"not null;index:idx_ar_user_started" json:"user_id"`
    SessionID      string         `gorm:"size:64;index:idx_ar_session" json:"session_id"`
    Status         string         `gorm:"size:20;not null;default:'running';index:idx_ar_status_started" json:"status"`
    StateReason    string         `gorm:"size:50" json:"state_reason,omitempty"`
    Messages       datatypes.JSON `gorm:"type:json;not null" json:"messages"`
    ReservationID  *uint64        `json:"reservation_id,omitempty"`
    StartedAt      time.Time      `gorm:"type:datetime(3);index:idx_ar_user_started;index:idx_ar_status_started" json:"started_at"`
    EndedAt        *time.Time     `gorm:"type:datetime(3)" json:"ended_at,omitempty"`
    CreatedAt      time.Time      `gorm:"type:datetime(3);autoCreateTime" json:"created_at"`
    UpdatedAt      time.Time      `gorm:"type:datetime(3);autoUpdateTime" json:"updated_at"`
}

func (AgentRun) TableName() string { return "agent_run" }
```

**关键 design 点**：
- `ReservationID *uint64`：指针表示 nullable，#2 不填，#12 billing 填
- `Messages datatypes.JSON`：用 `gorm.io/datatypes` JSON 类型，自动 Marshal/Unmarshal
- 不用 `gorm.Model`（避免引入 `DeletedAt` 软删除——本表不需要）

---

## §3 M2 Store 设计

### 3.1 Interface

```go
// internal/numind/store/agent_run.go
package store

import (
    "context"
    "encoding/json"
    "time"

    "numind-server/internal/pkg/model"
)

type IAgentRunStore interface {
    Create(ctx context.Context, run *model.AgentRun) error
    Get(ctx context.Context, id uint64) (*model.AgentRun, error)
    UpdateState(ctx context.Context, id uint64, status string, stateReason string, endedAt *time.Time) error
    WriteTurn(ctx context.Context, id uint64, messages json.RawMessage) error // turn 级整体覆写
    ListBySession(ctx context.Context, sessionID string, offset, limit int) ([]model.AgentRun, int64, error)
}
```

### 3.2 关键实现细节

**WriteTurn 实现**（保证 turn 级整体覆写，禁止 incremental）：

```go
func (s *agentRunStore) WriteTurn(ctx context.Context, id uint64, messages json.RawMessage) error {
    // 严格 UPDATE 整列 + 更新 updated_at（GORM 自动处理）
    result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
        Where("id = ?", id).
        Update("messages", datatypes.JSON(messages))
    if result.Error != nil {
        return fmt.Errorf("WriteTurn: %w", result.Error)
    }
    if result.RowsAffected == 0 {
        return fmt.Errorf("WriteTurn: no row updated for id=%d", id)
    }
    return nil
}
```

**UpdateState 同样原子**：

```go
func (s *agentRunStore) UpdateState(ctx context.Context, id uint64, status, stateReason string, endedAt *time.Time) error {
    updates := map[string]interface{}{
        "status":        status,
        "state_reason":  stateReason,
    }
    if endedAt != nil {
        updates["ended_at"] = *endedAt
    }
    return s.db.WithContext(ctx).Model(&model.AgentRun{}).
        Where("id = ?", id).
        Updates(updates).Error  // map 形式，bool 字段无 default:true 问题
}
```

### 3.3 测试覆盖

- `agent_run_test.go` 用 in-memory SQLite + AutoMigrate
- 覆盖：Create / Get（存在 + 不存在）/ UpdateState / WriteTurn / ListBySession
- **并发 WriteTurn race detector**：两个 goroutine 并发 WriteTurn 同一 id，断言**只有一个 commit 留存**（GORM 事务隔离）

---

## §4 M3 AgentRunner 设计

### 4.1 入口接口

```go
// internal/numind/biz/agent/runner.go
package agent

import (
    "context"
    "github.com/cloudwego/eino/components/model"
    "github.com/cloudwego/eino/components/tool"
)

type RunRequest struct {
    UserID    uint
    SessionID string
    Input     string         // 用户输入
    Tools     []tool.BaseTool // 当前可用工具（#2 用 mock，#3 从 Tool Registry 取）
    Hooks     *RunHooks      // 可选，#4 sandbox 注入
}

type RunResult struct {
    AgentRunID     uint64
    TerminalReason TerminalReason
    FinalOutput    string
    StepCount      int
    Duration       time.Duration
}

type AgentRunner interface {
    Run(ctx context.Context, req RunRequest) (*RunResult, error)
    // Cancel 取消正在运行的 agent_run（蓝本 §4.1.9 接口契约）。
    // #2 实现 noop（标记 follow-up）：仅在 in-memory 记录中查找该 runID 的 queryCancel func 并调用；
    //   不持久化（#11 HTTP cancel handler 落地时补 in-mem registry 的 cluster-aware 版本）。
    // 返回值：true = 该 runID 存在且 cancel 信号已发送；false = runID 不存在或已 terminated。
    Cancel(runID uint64) bool
}
```

> **RunRequest 字段裁剪**（蓝本 §4.1.9 字段 vs spec §4.1）：蓝本 RunRequest 还有 `RunID / AgentID / SkillID / RestoreFrom`，spec 暂未包含的原因：
> - `RunID` → AgentRunner 内部生成（store.Create 后），不由调用方传入
> - `AgentID` → #10 configurator-ux 引入（agent_definition 表存在后才有意义）
> - `SkillID` → #5 skill-system 引入
> - `RestoreFrom` → #9 compact 引入（需要 compact_state 列后才有 restore 概念）

### 4.2 RunHooks struct

```go
// internal/numind/biz/agent/hooks.go
package agent

import (
    "context"
    "github.com/cloudwego/eino/components/tool"
)

type HookAction int
const (
    HookActionContinue     HookAction = iota // 0 — 正常继续
    HookActionStop                            // 1 → terminal reason "hook_stopped"
    HookActionBlockingStop                    // 2 → terminal reason "stop_hook_prevented"
)

type RunHooks struct {
    // PreToolCall: 工具调用前调用。返回 Stop 时终止 + reason=hook_stopped；返回 BlockingStop 时终止 + reason=stop_hook_prevented
    PreToolCall  func(ctx context.Context, tool tool.BaseTool, input string) (HookAction, error)
    // PostToolCall: 工具调用后调用。同样三种 action
    PostToolCall func(ctx context.Context, tool tool.BaseTool, output string, err error) (HookAction, error)
}
```

### 4.3 AiserviceAdapter（实现 model.ToolCallingChatModel）

```go
// internal/numind/biz/agent/adapter.go
package agent

import (
    "context"
    "github.com/cloudwego/eino/components/model"
    "github.com/cloudwego/eino/schema"
    "numind-server/internal/pkg/aiservice"
    "numind-server/internal/pkg/langfuse"
)

type aiserviceAdapter struct {
    modelName string
    taskID    string         // "agent-runner-<runID>"
    tools     []*schema.ToolInfo // 不可变，WithTools 返回克隆体
}

// 实现 model.ToolCallingChatModel（3 方法）
var _ model.ToolCallingChatModel = (*aiserviceAdapter)(nil)

func (a *aiserviceAdapter) Generate(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.Message, error) {
    req := convertReq(in, a.tools, a.modelName, opts...)
    resp, err := aiservice.Chat(ctx, a.taskID, req)
    if err != nil { return nil, err }
    return convertResp(resp), nil
}

func (a *aiserviceAdapter) Stream(ctx context.Context, in []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
    req := convertReq(in, a.tools, a.modelName, opts...)
    ch, err := aiservice.ChatStream(ctx, a.taskID, req)
    if err != nil { return nil, err }
    return wrapStreamReader(ch), nil
}

// WithTools 返回克隆体（核心：线程安全，不变更 receiver）
func (a *aiserviceAdapter) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
    clone := *a
    clone.tools = append([]*schema.ToolInfo(nil), tools...) // defensive copy
    return &clone, nil
}
```

### 4.4 Run() 主流程伪代码

```go
func (r *agentRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
    // 1. 建 DB 行
    run := &model.AgentRun{
        UserID:    req.UserID,
        SessionID: req.SessionID,
        Status:    "running",
        Messages:  json.RawMessage("[]"),
        StartedAt: time.Now(),
    }
    if err := r.store.Create(ctx, run); err != nil { return nil, err }

    // 2. 建 Langfuse trace
    traceID := langfuse.TraceID()
    langfuse.CreateTrace(traceID, "agent-runtime-run",
        langfuse.WithUserID(req.UserID),
        langfuse.WithTraceInput(...),
        langfuse.WithTraceTags("agent-runtime-skeleton"),
    )
    ctx = langfuse.WithTrace(ctx, traceID)

    // 3. AbortController 三层
    queryCtx, queryCancel := context.WithCancel(ctx)
    defer queryCancel()
    // batchCtx 在每个 LLM 调用前派生；toolCtx 在每个工具调用前派生（M5 详细）

    // 4. 构造 Eino ReAct Agent
    adapter := &aiserviceAdapter{
        modelName: "qwen-turbo", // 通过 aiservice 路由动态
        taskID:    fmt.Sprintf("agent-runner-%d", run.ID),
    }
    agent, err := react.NewAgent(queryCtx, react.AgentConfig{
        ToolCallingModel: adapter,  // **不是 deprecated Model 字段**
        ToolsConfig:      compose.ToolsNodeConfig{Tools: req.Tools},
        MaxStep:          30,
    })
    if err != nil { return nil, err }

    // 5. 执行 + Hook 接入 + 状态机
    state := &loopState{}
    for stepCount := 0; stepCount < 30; stepCount++ {
        // ... 在 ReAct loop 内部，每个 tool call 触发 PreToolCall hook
        // ... PostToolCall hook
        // ... 检查 Continue/Terminal reason

        // 转换为 Eino schema.Message → Run agent.Generate
        msg, err := agent.Generate(queryCtx, msgs)
        // ... 处理 error，触发 Withhold recovery（M6）
        // ... turn 末写 messages JSON（store.WriteTurn）

        if state.isTerminal() {
            break
        }
    }

    // 6. 终止 + UpdateState
    endedAt := time.Now()
    _ = r.store.UpdateState(ctx, run.ID, "terminated", string(state.terminalReason), &endedAt)
    return &RunResult{...}, nil
}
```

---

## §5 M4 状态机设计

### 5.1 19 reason 完整定义

```go
// internal/numind/biz/agent/state.go
package agent

type TerminalReason string
const (
    TerminalCompleted          TerminalReason = "completed"
    TerminalBlockingLimit      TerminalReason = "blocking_limit"
    TerminalImageError         TerminalReason = "image_error"
    TerminalModelError         TerminalReason = "model_error"
    TerminalAbortedStreaming   TerminalReason = "aborted_streaming"
    TerminalPromptTooLong      TerminalReason = "prompt_too_long"
    TerminalStopHookPrevented  TerminalReason = "stop_hook_prevented"
    TerminalAbortedTools       TerminalReason = "aborted_tools"
    TerminalHookStopped        TerminalReason = "hook_stopped"
    TerminalMaxTurns           TerminalReason = "max_turns"
    TerminalErrorMaxBudget     TerminalReason = "error_max_budget"
    TerminalErrorMaxRetries    TerminalReason = "error_max_retries"
)

type ContinueReason string
const (
    ContinueNextTurn             ContinueReason = "next_turn"
    ContinueCollapseDrainRetry   ContinueReason = "collapse_drain_retry"
    ContinueReactiveCompactRetry ContinueReason = "reactive_compact_retry"
    ContinueMaxOutputEscalate    ContinueReason = "max_output_escalate"
    ContinueMaxOutputRecovery    ContinueReason = "max_output_recovery"
    ContinueStopHookBlocking     ContinueReason = "stop_hook_blocking"
    ContinueTokenBudgetContinue  ContinueReason = "token_budget_continue"
)

// 编译期不变量：长度
var _ = [12]TerminalReason{TerminalCompleted, TerminalBlockingLimit, TerminalImageError, TerminalModelError,
    TerminalAbortedStreaming, TerminalPromptTooLong, TerminalStopHookPrevented, TerminalAbortedTools,
    TerminalHookStopped, TerminalMaxTurns, TerminalErrorMaxBudget, TerminalErrorMaxRetries}
var _ = [7]ContinueReason{ContinueNextTurn, ContinueCollapseDrainRetry, ContinueReactiveCompactRetry,
    ContinueMaxOutputEscalate, ContinueMaxOutputRecovery, ContinueStopHookBlocking, ContinueTokenBudgetContinue}
```

### 5.2 State 结构 + transitions

```go
type LoopState struct {
    StepCount      int
    TerminalReason TerminalReason
    ContinueReason ContinueReason  // last continue
    PTLRetries     int  // M6 Withhold PTL chain counter
    MaxOutputRetries int  // M6 Withhold max_output chain counter
}

// 状态迁移表（部分例子）：
// Event "LLM_OK + has_tool_call" → ContinueNextTurn
// Event "LLM_OK + no_tool_call"  → TerminalCompleted
// Event "LLM_ERR_PTL"            → ContinueReactiveCompactRetry (若 PTLRetries < N) else TerminalPromptTooLong
// Event "MaxStep reached"        → TerminalMaxTurns
// Event "ctx.Done()"             → TerminalAbortedStreaming
// Event "ToolErr"                → TerminalAbortedTools
// Event "Hook returns Stop"      → TerminalHookStopped
// Event "Hook returns BlockingStop" → TerminalStopHookPrevented

func (s *LoopState) Transition(event LoopEvent) (TerminalReason, ContinueReason, bool /*isTerminal*/) {
    // ... switch / lookup table
}
```

---

## §6 M5 AbortController 三层

### 6.1 派生链

```go
// 三层派生关系（蓝本 §4.1.5）
queryCtx, queryCancel  := context.WithCancel(parentCtx)     // 顶层：整个 Run 生命周期
batchCtx, batchCancel  := context.WithCancel(queryCtx)       // 中层：一个 LLM 调用 batch（含 streaming）
toolCtx, toolCancel    := context.WithCancel(batchCtx)       // 底层：单次工具调用
// cancel 严格父→子级联
```

### 6.2 关键不变量

- 用户取消请求（HTTP 中断 / explicit cancel API）→ queryCancel() → 所有子立即收到 ctx.Done()
- 单次 tool 超时 → toolCancel() 但不影响 batchCtx / queryCtx（隔离 tool 异常）
- LLM 调用超时 → batchCancel() → toolCtx 也 cancel，但 queryCtx 不（可能 retry 触发 new batch）

### 6.3 测试

```go
// abort_test.go
func TestAbortController_CancelPropagation(t *testing.T) {
    parent := context.Background()
    queryCtx, queryCancel := context.WithCancel(parent)
    batchCtx, batchCancel := context.WithCancel(queryCtx)
    defer batchCancel()
    toolCtx, toolCancel := context.WithCancel(batchCtx)
    defer toolCancel()
    _ = batchCtx // batchCtx 仅作派生跳板，本测试不直接监听

    done := make(chan struct{})
    go func() {
        <-toolCtx.Done()
        close(done)
    }()

    queryCancel()
    select {
    case <-done:
        // expected
    case <-time.After(100 * time.Millisecond):
        t.Fatal("cancel did not propagate")
    }
}
```

> **Lint 友好**：cancel 函数全部 `defer` 调用，不丢弃；`batchCtx` 用 `_ = ` 显式声明意图（虽未直接读，但保证 derived ctx chain 真实建立）。

---

## §7 M6 Withhold Recovery 两 chain

> **Retry 上限取蓝本 §4.1.6 canonical 值**：PTL chain 与 max_output chain 各 **2 步**（不是早期 spec 草稿写的 3 步）。

```go
const (
    MaxPTLRetries          = 2  // 蓝本 §4.1.6: PTL chain Step 1 (collapse_drain) + Step 2 (reactive_compact) = 2 步
    MaxOutputRetriesLimit  = 2  // 蓝本 §4.1.6: max_output chain Step 1 (escalate) + Step 2 (recovery) = 2 步
)
```

### 7.1 PromptTooLong (PTL) chain

```
LLM 返回 token_limit_exceeded
  ↓
state.PTLRetries++
  ↓
PTLRetries <= 2 (MaxPTLRetries)?
  ↓ yes
ContinueReason = collapse_drain_retry (Step 1) → reactive_compact_retry (Step 2)
触发 Compact (#9 feature 真实实现；#2 用 mock noop)
  ↓
重试 LLM
  ↓ no
TerminalReason = prompt_too_long
```

### 7.2 max_output_tokens chain

```
LLM 返回 max_tokens_exceeded
  ↓
state.MaxOutputRetries++
  ↓
MaxOutputRetries <= 2 (MaxOutputRetriesLimit)?
  ↓ yes
ContinueReason = max_output_escalate (Step 1, 升级到更大 context window model)
  → max_output_recovery (Step 2, 从 partial output continue)
  ↓
重试 LLM
  ↓ no
TerminalReason = error_max_budget
```

### 7.3 互斥优先级

**PTL chain > max_output_tokens chain**：如果 PTL 触发（context window 超），必须先 compact 才能 retry；不能直接升级 model。

```go
func (s *LoopState) handleError(err error) (LoopEvent, error) {
    if isPTL(err) {
        if s.PTLRetries < MaxPTLRetries {
            s.PTLRetries++
            return LoopEventPTLRetry, nil
        }
        return LoopEventTerminalPTL, nil
    }
    if isMaxOutput(err) {
        if s.MaxOutputRetries < MaxOutputRetriesLimit {
            s.MaxOutputRetries++
            return LoopEventMaxOutputRetry, nil
        }
        return LoopEventTerminalBudget, nil
    }
    return LoopEventModelErr, nil
}
```

---

## §8 M7 最小 Tool interface

```go
// internal/numind/biz/agent/tool.go
package agent

import (
    "context"
    "encoding/json"
    "github.com/cloudwego/eino/components/tool"
    "github.com/cloudwego/eino/schema"
)

// 最小 Tool interface（#3 tool-registry 会扩展到 38 字段）
type Tool interface {
    Name() string
    Description() string
    Run(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}

// 适配 Eino 的 tool.BaseTool + tool.InvokableTool
type einoToolAdapter struct {
    impl Tool
}

func (a *einoToolAdapter) Info(_ context.Context) (*schema.ToolInfo, error) {
    return &schema.ToolInfo{
        Name: a.impl.Name(),
        Desc: a.impl.Description(),
        // ParamsOneOf 在 #3 时通过 schema 自动推导；#2 用 raw JSON
    }, nil
}

func (a *einoToolAdapter) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
    out, err := a.impl.Run(ctx, json.RawMessage(args))
    if err != nil { return "", err }
    return string(out), nil
}

// 编译期断言
var _ tool.InvokableTool = (*einoToolAdapter)(nil)
```

---

## §9 关键不变量汇总（spec 自检）

| # | 不变量 | 验证手段 |
|---|--------|---------|
| 1 | agent_run.messages turn 级覆写 | store WriteTurn 实现 + race detector 测试 |
| 2 | 19 reason 字符串值 + DB CHECK | migration SQL + state.go 常量 + 单测 19 case |
| 3 | RunHooks 三 action enum 稳定 | hooks.go HookAction 常量 + 单测覆盖三种返回 |
| 4 | AbortController 三层派生 | abort.go derived ctx + race detector 单测 |
| 5 | Withhold 两 chain 互斥优先级 | M6 handleError switch + 单测覆盖 PTL/MaxOutput 顺序 |
| 6 | aiservice 唯一入口 | adapter.go 仅 import aiservice 包，禁止 import net/http |
| 7 | Langfuse trace 完整 | runner.go CreateTrace + adapter 内 Generation + tool span |
| 8 | prod 零影响 | reviewer 检查 commit 不含 config_prod.yaml / prod tag |

---

## §10 风险 + 缓解（补充 S1 R1-R7）

| ID | 风险 | 缓解 |
|----|------|------|
| R1 (S1) | Eino ToolCallingChatModel 接口签名 | 已实测核对，§4.3 给出精确实现 |
| R2 (S1) | GORM JSON 列 nil/empty/full | datatypes.JSON + 单测覆盖三种 case |
| R3 (S1) | AbortController 三层 ctx 传播 | §6 严格父子链 + race detector |
| R4 (S1) | Withhold chain 互相干扰 | §7.3 显式优先级 + handleError switch |
| R5 (S1) | hook_stopped 在 #2 不真实实现 | M4 用 mock RunHooks 触发；真实实现转 #5 follow-up |
| R6 (S1) | DB CHECK 在 MySQL 5.7 不支持 | VARCHAR + CHECK 在 5.7+ 支持；app 层 validation 兜底 |
| R7 (S1) | Eino transitive deps 冲突 | Phase 0 已验证 build clean |
| R8 (S2) | adapter 多次 WithTools 调用产生大量克隆体 | clone 是浅拷贝 + 共享 immutable fields；性能可接受 |
| R9 (S2) | state machine transitions table 复杂度爆炸 | switch-case 实现 + 单测每个 event 独立验证 |
| R10 (S2) | M3 Run() 主流程的 Eino + state + WriteTurn 编排复杂 | 拆 helper 函数：`startRun` / `processLoop` / `terminateRun` |

---

## §11 实施依赖图（送 S3 plan）

```
M1 (DB schema) ──────┐
M2 (Store)     ──────┤ → M3 (AgentRunner) → M8 (Tests)
M4 (状态机)    ──────┤
M5 (Abort)     ──────┤
M6 (Withhold)  ──────┤
M7 (Tool iface) ─────┘
```

并行机会：
- M1 + M4 + M7 完全独立可并行（不同文件，无依赖）
- M2 依赖 M1（store 用 model）
- M5 + M6 独立于 M2/M3（在 biz/agent/ 自己的文件里）
- M3 是核心集合点，依赖 M1/M2/M4/M5/M6/M7 全部就绪
- M8 最后做（依赖所有）

---

## §12 蓝本一致性

| Spec § | 蓝本 § | 是否冲突 |
|--------|--------|---------|
| §2 agent_run DDL | §4.1.10 / §8 | 已调和（取 BIGINT，蓝本 §4.1.10 UUID 改 BIGINT；蓝本 §8 reservation_id 保留） |
| §3 Store WriteTurn 整体覆写 | §4.1.3 不变量 | 一致 |
| §4 AgentRunner + RunHooks + Adapter | §4.1.9 interface + §4.1.5 Hook 触发 | 一致，补充 HookAction enum 三值 |
| §5 状态机 19 reason | §4.1.5 + §4.1.9 | 一致 |
| §6 AbortController 三层 | §4.1.5 | 一致 |
| §7 Withhold 两 chain | §4.1.5 + §4.1.9 | 一致（优先级在 spec 显式化） |
| §8 Tool interface 最小版 | §4.2 完整版（38 字段） | 不冲突（#2 是最小，#3 扩展） |

---

**Spec 完成。等待独立 reviewer 审。**
