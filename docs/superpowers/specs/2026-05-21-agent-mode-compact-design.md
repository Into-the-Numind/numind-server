# Agent 模式 Compact 系统 — 技术设计

> NDF v2 S2 spec | Feature: agent-mode-compact | #9/14

## §1 目标与不变量

落地蓝本 §4.8 Compact（含会话恢复）：长对话不掉链、输出截断自愈、会话重连无缝。

| 不变量 | 说明 |
|--------|------|
| I1 | `AgentRunner.Run` 接口（method + 单返回值 `*RunResult`）不变（#2 契约） |
| I2 | `state.go` LoopState / LoopEvent / Transition 不变（#2 契约 — PTL/MaxOutput LoopEvent 已就绪） |
| I3 | `RunHooks` / `HookAction` 不变（#2/#4 契约） |
| I4 | `FullTool` 接口不变（#3 契约） |
| I5 | `agent_definition` / `agent_definition_history` / `skill_template` schema 不变（#5 契约） |
| I6 | `aiservice` 5 入口不变（v1 MockCompactProvider 不调 LLM；#14 接入真实 aiservice.Chat） |
| I7 | `credit_transaction.source_type` CHECK constraint 零修改 |
| I8 | `config_prod.yaml` 不修改 |
| I9 | feature 分支不推 GitHub（pre-push hook） |
| I10 | `agent_run.chk_ar_state_reason` CHECK 不动（#2 已含 `collapse_drain_retry` / `reactive_compact_retry` / `max_output_escalate` / `max_output_recovery`） |
| I11 | #2 mock 主流程（runner.Run）不动；本 feature 仅加 helper + RunnerOption + struct 字段 |
| I12 | #4 sandbox hooks / #5 Skill 注入 / #6 permission / #7 memory / #8 narration 等已注入或预期注入的段位不动 |

## §2 数据模型

### §2.1 agent_run 表 ALTER（加 2 列）

**Migration 文件**：`migrations/20260521_010000_alter_agent_run_add_compact_columns.sql`

```sql
-- agent-mode #9 compact: 加 2 列存压缩状态与最新摘要
-- 不动既有 #2 列，旧 agent_run 行 NULL 兼容

ALTER TABLE agent_run
  ADD COLUMN compact_state    JSON     NULL COMMENT 'CompactStateV1 序列化；含 last_compact_at / last_boundary_message_id / total_compact_attempts / consecutive_failures / summary_token_count / strategy_used',
  ADD COLUMN compact_summary  LONGTEXT NULL COMMENT '最新 CompactSummary 全文；恢复时快速读避免遍历 messages';
```

**Rollback**：`migrations/20260521_010000_alter_agent_run_add_compact_columns_rollback.sql`

```sql
ALTER TABLE agent_run
  DROP COLUMN compact_state,
  DROP COLUMN compact_summary;
```

注释：
- `compact_state` JSON nullable — 旧 run 行不破坏，新 run 首次 compact 才填
- `compact_summary` LONGTEXT 而非 TEXT — 9 节摘要含 verbatim 用户消息易超 64KB（S1 reviewer P2 fix）
- 无新 index — compact_state / compact_summary 不参与查询过滤（仅在按 ID 取单 run 时读出）
- AutoMigrate 自动加列 — `helper.go` agent_run model AutoMigrate 注册已就位（#2），#9 仅改 model struct，无需在 helper.go 加新注册

### §2.2 model.AgentRun 加字段

`internal/pkg/model/agent_run.go`：

```go
import (
    "time"
    "gorm.io/datatypes"
)

type AgentRun struct {
    ID            uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
    UserID        uint           `gorm:"not null;index:idx_ar_user_started" json:"user_id"`
    SessionID     string         `gorm:"size:64;index:idx_ar_session" json:"session_id"`
    Status        string         `gorm:"size:20;not null;default:'running';index:idx_ar_status_started" json:"status"`
    StateReason   string         `gorm:"size:50" json:"state_reason,omitempty"`
    Messages      datatypes.JSON `gorm:"type:json;not null" json:"messages"`
    ReservationID *uint64        `json:"reservation_id,omitempty"`
    StartedAt     time.Time      `gorm:"type:datetime(3);not null;index:idx_ar_user_started;index:idx_ar_status_started" json:"started_at"`
    EndedAt       *time.Time     `gorm:"type:datetime(3)" json:"ended_at,omitempty"`
    // #9 compact 加 2 字段
    CompactState   datatypes.JSON `gorm:"type:json" json:"compact_state,omitempty"`
    CompactSummary string         `gorm:"type:longtext" json:"compact_summary,omitempty"`
    CreatedAt     time.Time      `gorm:"type:datetime(3);autoCreateTime" json:"created_at"`
    UpdatedAt     time.Time      `gorm:"type:datetime(3);autoUpdateTime" json:"updated_at"`
}
```

**GORM `default:true` bool audit**（S0 风险 9）：
- AgentRun 既有字段：Status (string, default:'running') / Messages (JSON) / 无 bool — 安全
- #9 加字段：CompactState (JSON) / CompactSummary (string) — 无 bool — 安全
- 结论：本 feature **零 `default:true` bool gotcha 风险**

### §2.3 CompactStateV1 struct (JSON marshal/unmarshal)

`internal/numind/biz/compact/types.go`：

```go
package compact

import "time"

// CompactStateV1 是 agent_run.compact_state JSON 列的 Go 表示。
// 所有字段 omitempty + nullable — v2 加字段不破坏旧 row（蓝本 §4.8.6）。
type CompactStateV1 struct {
    LastCompactAt          time.Time `json:"last_compact_at,omitempty"`
    LastBoundaryMessageID  string    `json:"last_boundary_message_id,omitempty"`
    TotalCompactAttempts   int       `json:"total_compact_attempts,omitempty"`
    ConsecutiveFailures    int       `json:"consecutive_failures,omitempty"`
    SummaryTokenCount      int       `json:"summary_token_count,omitempty"`
    StrategyUsed           string    `json:"strategy_used,omitempty"` // "reactive_compact" / "session_memory" (#14)
}
```

**JSON marshaling**：
- 用 `encoding/json` 标准库（不引入第三方 lib）
- 不开启 strict mode（不 `DisallowUnknownFields`）— v2 加字段时旧消费者忽略
- Unmarshal 缺失字段返回 zero value（Go 默认行为）

## §3 包结构

```
internal/numind/biz/compact/
├── types.go              # CompactStateV1 + Message + CompactRequest + CompactResult + RestoredSession
├── threshold.go          # Config + DefaultConfig (qwen-plus)
├── prompt.go             # NoToolsPreamble + BaseCompactPrompt
├── provider.go           # CompactProvider interface + MockCompactProvider + EstimateTokens
├── ptl_chain.go          # CollapseDrain + ReactiveCompact + headDropRetry
├── max_output_chain.go   # EscalateMaxTokens
├── restore.go            # Restore (含 3 道清洗 + 注入 narration)
├── attachments.go        # AttachmentReinjector interface + NullAttachmentReinjector
├── types_test.go
├── threshold_test.go
├── prompt_test.go
├── provider_test.go
├── ptl_chain_test.go
├── max_output_chain_test.go
├── restore_test.go
└── attachments_test.go
```

**单向依赖**：biz/compact 不依赖 biz/agent；biz/agent 依赖 biz/compact（runner.go 通过 `import "numind-server/internal/numind/biz/compact"` 使用）。

## §4 接口完整签名

### §4.1 types.go

```go
package compact

import (
    "encoding/json"
    "time"
)

// Message 是 compact 输入输出的通用消息抽象（与 Eino schema.Message 解耦）。
// 由 caller 转换：#14 ReAct loop 拿到 []*schema.Message 后 toCompactMessages() 喂 compact API；
// compact 返回 []Message 后 fromCompactMessages() 转回 schema.Message。
type Message struct {
    Role        string          `json:"role"`           // "user" / "assistant" / "system" / "tool"
    Content     string          `json:"content,omitempty"`
    ToolCalls   json.RawMessage `json:"tool_calls,omitempty"`   // OpenAI 协议 tool_calls 数组
    ToolCallID  string          `json:"tool_call_id,omitempty"` // tool_result 关联的 tool_call_id
    // 元信息（compact 决策需要）
    HasFileRef     bool `json:"has_file_ref,omitempty"`     // 含文件引用 — 不被 drop
    IsCompactMark  bool `json:"is_compact_mark,omitempty"`  // 含 compact summary 标记 — 不被 drop
}

// CompactRequest 是 CompactProvider.Compact 的输入。
type CompactRequest struct {
    Messages        []Message
    SystemPrompt    string // 含 NoToolsPreamble + BaseCompactPrompt
    MaxOutputTokens int
}

// CompactResult 是 CompactProvider.Compact 的返回。
type CompactResult struct {
    Summary      string
    InputTokens  int
    OutputTokens int
}

// RestoredSession 是 Restore 返回的清洗后会话。
type RestoredSession struct {
    Messages         []Message
    SystemNarration  string // 恢复 narration（§4.8.6 step 3）
    FirstTurnNoTools bool   // §4.8.6 step 5 — caller 收到后自己实施禁工具
}

// CompactStateV1 在 §2.3 定义。
```

### §4.2 threshold.go

```go
package compact

// Config 控制 compact 触发阈值；默认值适配 qwen-plus（蓝本 §4.8.4）。
type Config struct {
    ContextWindow                     int
    EffectiveContextWindow            int
    AutoCompactThreshold              int
    MaxConsecutiveAutoCompactFailures int
    MaxCompactOutputTokens            int
    ContextWindowSafetyMargin         float64
    PTLCollapseKeepTurns              int
}

// DefaultConfig 返回 qwen-plus 默认值。
func DefaultConfig() Config {
    return Config{
        ContextWindow:                     128_000,
        EffectiveContextWindow:            120_000, // 128k - 8k maxOutput
        AutoCompactThreshold:              107_000, // 120k - 13k buffer
        MaxConsecutiveAutoCompactFailures: 3,
        MaxCompactOutputTokens:            8_000,
        ContextWindowSafetyMargin:         0.95,
        PTLCollapseKeepTurns:              4, // 蓝本 §4.1.6
    }
}
```

### §4.3 prompt.go

```go
package compact

// NoToolsPreamble 注入到压缩请求 system prompt 开头（§4.8.2）。
// 实测：工具调用率从 2.79% → 0.01%。
const NoToolsPreamble = `【重要】你现在的任务是生成对话摘要。在本次任务中：
- 禁止调用任何工具
- 禁止使用 function_call / tool_use 格式
- 只需输出纯文本摘要
- 不需要向用户提问

请直接输出摘要内容，无需任何前缀或解释。`

// BaseCompactPrompt 9 节模板（§4.8.3）。
// 第 6 节（用户消息原文）和第 9 节（verbatim 下一步）防 intent drift / task drift。
const BaseCompactPrompt = `请按以下 9 节结构输出对话摘要：

1. 主要请求和意图
   ────────────────
   用 1-3 句话描述学员最初想解决什么问题。精确，不加推断。

2. 关键技术概念
   ────────────────
   本次会话涉及的专业名词、工具、方法论。bullet list，每项一行。

3. 文件和代码片段
   ────────────────
   学员上传的文件名、处理结果、生成的产物。代码片段仅保留函数签名和关键注释。

4. 错误和修复
   ────────────────
   出现过的错误及其解决方案。格式：问题 → 原因 → 解决方案。

5. 问题解决过程
   ────────────────
   agent 尝试过哪些策略，哪些成功，哪些失败及原因。

6. 所有用户消息原文（防 intent drift）
   ────────────────
   verbatim 引用每条用户消息，不压缩、不改写。

7. 待办任务
   ────────────────
   明确承诺给学员但尚未完成的事项。

8. 当前进展
   ────────────────
   截至压缩点，已完成了什么，到达了哪个阶段。

9. 可选下一步（verbatim 引用，防 task drift）
   ────────────────
   如果 agent 已说"接下来我会..."，原文引用。若未说，此节留空。
`

// FullCompactSystemPrompt 拼接 preamble + base，供 CompactProvider.Compact 注入。
func FullCompactSystemPrompt() string {
    return NoToolsPreamble + "\n\n" + BaseCompactPrompt
}
```

### §4.4 provider.go

```go
package compact

import (
    "context"
)

// CompactProvider 是 LLM 压缩调用抽象。
// v1 MockCompactProvider 返回固定占位 summary；#14 真实 provider 内部用 aiservice.Chat。
type CompactProvider interface {
    Compact(ctx context.Context, req *CompactRequest) (*CompactResult, error)
}

// MockCompactProvider v1 占位实现。
type MockCompactProvider struct {
    PlaceholderSummary string
    // 可选：模拟失败（测试用）
    FailureSequence []error // FailureSequence[i] != nil → 第 i+1 次调用返回 error
    callCount       int
}

func (m *MockCompactProvider) Compact(ctx context.Context, req *CompactRequest) (*CompactResult, error) {
    defer func() { m.callCount++ }()
    if m.callCount < len(m.FailureSequence) && m.FailureSequence[m.callCount] != nil {
        return nil, m.FailureSequence[m.callCount]
    }
    return &CompactResult{
        Summary:      m.PlaceholderSummary,
        InputTokens:  EstimateTokens(joinMessages(req.Messages)),
        OutputTokens: EstimateTokens(m.PlaceholderSummary),
    }, nil
}

// EstimateTokens 粗算 token 数（不依赖外部 tokenizer）。
// 公式：CJK / 非 ASCII 字符 ≈ 1.5 token / ASCII 字符 ≈ 0.25 token。
// 边界（S2 reviewer P1 fix）：覆盖 CJK 扩展区（Ext-A 0x3400-0x4DBF / Ext-B 0x20000+）、
//   日文假名（0x3040-0x309F / 0x30A0-0x30FF）、韩文（0xAC00-0xD7AF）等非 ASCII。
//   非 ASCII 即按 1.5 估算，避免日韩混合内容低估 → AutoCompactThreshold 误触发。
// 误差预算：与真实 qwen-plus tokenizer 偏差 ≤ 15%（S3 fixture 验证）。
func EstimateTokens(text string) int {
    multi := 0  // CJK / 非 ASCII
    ascii := 0
    for _, r := range text {
        if r <= 0x7F {
            ascii++
        } else {
            multi++
        }
    }
    return int(float64(multi)*1.5 + float64(ascii)*0.25)
}

// joinMessages 把 []Message 合并为单 string（仅供 EstimateTokens 用）。
func joinMessages(msgs []Message) string {
    var s string
    for _, m := range msgs {
        s += m.Content + "\n"
    }
    return s
}
```

### §4.5 ptl_chain.go

```go
package compact

import (
    "context"
    "fmt"
)

// CollapseDrain 是 PTL 链 outer Step 1：仅剥离最近 keepTurns 外的 tool_result。
// 保留规则：
// - 不动 user / assistant 文本消息
// - 不动含 compact summary 标记的消息（IsCompactMark=true）
// - 不动含用户文件引用的消息（HasFileRef=true）
// - 不动最近 keepTurns 的所有消息
func CollapseDrain(messages []Message, keepTurns int) []Message {
    // 边界：keepTurns >= len → 返回原 slice
    // 边界：keepTurns <= 0 → 用 default 4
    if keepTurns <= 0 { keepTurns = 4 }
    if keepTurns >= len(messages) { return messages }

    // 找最后 keepTurns 个 user 消息边界，保留之后所有消息
    userIndices := []int{}
    for i, m := range messages {
        if m.Role == "user" {
            userIndices = append(userIndices, i)
        }
    }
    var keepFrom int
    if len(userIndices) <= keepTurns {
        keepFrom = 0
    } else {
        keepFrom = userIndices[len(userIndices)-keepTurns]
    }

    out := make([]Message, 0, len(messages))
    for i, m := range messages {
        if i >= keepFrom { out = append(out, m); continue }
        if m.IsCompactMark || m.HasFileRef { out = append(out, m); continue }
        if m.Role == "tool" {
            // 剥离 tool_result（不加入 out）
            continue
        }
        out = append(out, m) // user / assistant 文本保留
    }
    return out
}

// ReactiveCompact 是 PTL 链 outer Step 2：调 LLM 全量压缩 → CompactSummary。
// 失败时内部递归 headDropRetry（蓝本 §4.8.5）最多 maxRetries=3 次。
// 返回 (result, finalMessages, err)：finalMessages 是最终成功 compact 时实际喂给 LLM 的
//   消息集合（可能比传入的 messages 少 — 内部 headDrop 截断后才成功的情形）。
//   caller 必须用 finalMessages 做 CollapseDrain（S2 reviewer P1 fix — 避免 summary 配
//   完整 collapsed 导致语义不一致）。失败 → return (nil, nil, err)。
func ReactiveCompact(ctx context.Context, provider CompactProvider, messages []Message, cfg Config) (*CompactResult, []Message, error) {
    if provider == nil {
        return nil, nil, fmt.Errorf("ReactiveCompact: nil provider")
    }
    req := &CompactRequest{
        Messages:        messages,
        SystemPrompt:    FullCompactSystemPrompt(),
        MaxOutputTokens: cfg.MaxCompactOutputTokens,
    }
    result, err := provider.Compact(ctx, req)
    if err == nil {
        return result, messages, nil
    }
    // inner retry loop（蓝本 §4.8.5）
    innerDropAttempts := 0
    truncated := messages
    maxInner := 3
    for innerDropAttempts < maxInner {
        truncated = headDropRetry(truncated, 0.25)
        req.Messages = truncated
        result, err = provider.Compact(ctx, req)
        if err == nil {
            return result, truncated, nil
        }
        innerDropAttempts++
    }
    return nil, nil, fmt.Errorf("ReactiveCompact: exhausted %d innerDropAttempts: %w", maxInner, err)
}

// headDropRetry 按消息组（每轮=user+assistant）drop 头部 dropPercent。
// 保留规则（S2 reviewer P2 明确）：
// - 永不 drop 含 compact summary 标记的消息组
// - 永不 drop 最近 10 轮
// - 永不 drop 含用户文件引用的消息组（S1 reviewer P2 fix）
// - **遇到受保护 turn 即停止整个 drop 推进**（不"跳过保护轮继续 drop 后续"）
//   理由：保留时间连续性，避免消息时序错乱（早期保护轮 + 中期被 drop 轮 + 晚期保护轮
//   组合会让 LLM 上下文跳跃，影响理解）。要更激进 drop 由 caller 用更高 dropPercent
//   重试，而非穿插式 drop。
func headDropRetry(messages []Message, dropPercent float64) []Message {
    if len(messages) == 0 || dropPercent <= 0 { return messages }

    // 按 user 消息找 turn 边界
    turnStarts := []int{}
    for i, m := range messages {
        if m.Role == "user" { turnStarts = append(turnStarts, i) }
    }
    if len(turnStarts) <= 10 { return messages } // 最近 10 轮全保留

    numTurns := len(turnStarts)
    keepRecentTurns := 10
    maxDropTurns := numTurns - keepRecentTurns
    dropCount := int(float64(numTurns) * dropPercent)
    if dropCount > maxDropTurns { dropCount = maxDropTurns }
    if dropCount <= 0 { return messages }

    // 从头检查每个候选 turn，遇到保护 turn 即停止推进（不穿插式 drop）
    actualDrop := 0
    dropEndIdx := 0 // exclusive
    for t := 0; t < dropCount && t < len(turnStarts); t++ {
        startIdx := turnStarts[t]
        endIdx := len(messages)
        if t+1 < len(turnStarts) { endIdx = turnStarts[t+1] }
        // 检查该 turn 内是否有保留消息
        protected := false
        for j := startIdx; j < endIdx; j++ {
            if messages[j].IsCompactMark || messages[j].HasFileRef {
                protected = true
                break
            }
        }
        if protected {
            break // 停止推进，避免穿插 drop
        }
        dropEndIdx = endIdx
        actualDrop++
    }
    if actualDrop == 0 { return messages }
    return messages[dropEndIdx:]
}
```

### §4.6 max_output_chain.go

```go
package compact

const (
    DefaultMaxTokens    = 8192
    EscalatedMaxTokens  = 65536
)

// EscalateMaxTokens 升级 max_tokens（蓝本 §4.1.6 max_output 链 Step 1）。
// 调用方仅在 escalate 阶段（首次 LLMErrMaxOutput）调；recovery 阶段不调（保持 currentMaxTokens）。
// 8192 → 65536；已 ≥ 65536 → 仍 65536（上限）。
func EscalateMaxTokens(current int) int {
    if current < EscalatedMaxTokens { return EscalatedMaxTokens }
    return EscalatedMaxTokens
}
```

### §4.7 restore.go

```go
package compact

import (
    "context"
    "encoding/json"
    "fmt"

    "numind-server/internal/pkg/model"
)

const RestorationNarration = `学员已重新打开这个会话。
请根据历史记录继续之前的工作，
第一条响应请简短总结上次进展，不要立即调用工具。`

// Restore 从 agent_run 读 compact_summary + messages，3 道清洗后返回 RestoredSession。
// reinjector 必传（v1 NullAttachmentReinjector，#11/#14 真实实现）— S1 reviewer P1 fix。
func Restore(ctx context.Context, run *model.AgentRun, reinjector AttachmentReinjector) (*RestoredSession, error) {
    if reinjector == nil {
        return nil, fmt.Errorf("Restore: nil reinjector")
    }
    // 反序列化 messages
    var raw []Message
    if len(run.Messages) > 0 {
        if err := json.Unmarshal(run.Messages, &raw); err != nil {
            return nil, fmt.Errorf("Restore: unmarshal messages: %w", err)
        }
    }

    // 3 道清洗（§4.8.6 step 2）
    cleaned := cleanseMessages(raw)

    // 注入 compact_summary 为首条 system message（如有）
    if run.CompactSummary != "" {
        cleaned = append([]Message{{
            Role:          "system",
            Content:       run.CompactSummary,
            IsCompactMark: true,
        }}, cleaned...)
    }

    // 调 reinjector 给 systemNarration 加附件（v1 Null 不改）
    narration, err := reinjector.Reinject(ctx, RestorationNarration, run.ID)
    if err != nil {
        return nil, fmt.Errorf("Restore: reinjector: %w", err)
    }

    return &RestoredSession{
        Messages:         cleaned,
        SystemNarration:  narration,
        FirstTurnNoTools: true, // §4.8.6 step 5
    }, nil
}

// cleanseMessages 3 道清洗：
// (1) 去悬空 tool_use：有 tool_use 但无对应 tool_result 的 → 删该 tool_use
// (2) 去孤立 thinking：无配对 assistant 消息的 thinking 块 → 删
// (3) 去空 assistant：content == "" 且无 tool_calls 的 assistant 消息 → 删
//
// **已知限制**（S2 reviewer P2 fix）：v1 (1) 的判定是"全部 tool_calls 都无 result 且
//   content 为空才 drop"。若 assistant 消息有多个 tool_calls 部分有 result 部分悬空，
//   整条保留，LLM 可能因悬空 call 感到"调了工具但没收到结果"的幻觉。
//   v1 接受此限制（实际场景罕见）；v2 改为细粒度过滤 ToolCalls JSON 中无 result 的 call。
func cleanseMessages(msgs []Message) []Message {
    // 第一步：找所有有 tool_result 的 tool_call_id
    haveResult := map[string]bool{}
    for _, m := range msgs {
        if m.Role == "tool" && m.ToolCallID != "" {
            haveResult[m.ToolCallID] = true
        }
    }
    out := make([]Message, 0, len(msgs))
    for _, m := range msgs {
        // (1) 悬空 tool_use 检测
        if m.Role == "assistant" && len(m.ToolCalls) > 0 {
            // 解析 tool_calls 检查至少一个 id 有 result
            var calls []struct {
                ID string `json:"id"`
            }
            _ = json.Unmarshal(m.ToolCalls, &calls)
            anyHasResult := false
            for _, c := range calls {
                if haveResult[c.ID] { anyHasResult = true; break }
            }
            if !anyHasResult && m.Content == "" {
                // 全悬空且无 content → drop
                continue
            }
        }
        // (3) 空 assistant 检测
        if m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) == 0 {
            continue
        }
        // (2) 孤立 thinking 检测 — v1 简化：thinking 块以 "thinking" role 标识，无配对则删
        if m.Role == "thinking" {
            // v1 直接 drop（蓝本恢复策略：thinking 不持久化跨 session）
            continue
        }
        out = append(out, m)
    }
    return out
}
```

### §4.8 attachments.go

```go
package compact

import "context"

// AttachmentReinjector 是恢复时往 systemPrompt 末尾追加附件上下文的接口。
// v1 NullAttachmentReinjector return systemPrompt 不动。
// #11（学员端会话恢复） / #14（真实 ReAct）实现真实文件读取 / Skill 注入 / MCP delta。
type AttachmentReinjector interface {
    Reinject(ctx context.Context, systemPrompt string, runID uint64) (string, error)
}

// NullAttachmentReinjector v1 实现：返回 systemPrompt 不动。
type NullAttachmentReinjector struct{}

func (n *NullAttachmentReinjector) Reinject(ctx context.Context, systemPrompt string, runID uint64) (string, error) {
    return systemPrompt, nil
}
```

## §5 Runner 集成

### §5.1 RunnerOption 新增

`internal/numind/biz/agent/runner.go`：

```go
import (
    // 既有 import...
    "numind-server/internal/numind/biz/compact"
)

// WithCompactProvider sets the CompactProvider for PTL/MaxOutput recovery chains.
// Real LLM compact is wired in #14; v1 uses compact.MockCompactProvider.
func WithCompactProvider(p compact.CompactProvider) RunnerOption {
    return func(r *agentRunner) { r.compactProvider = p }
}

// WithCompactConfig overrides default qwen-plus thresholds.
func WithCompactConfig(cfg compact.Config) RunnerOption {
    return func(r *agentRunner) { r.compactConfig = cfg }
}
```

### §5.2 agentRunner struct 新增字段

```go
type agentRunner struct {
    runStore        store.IAgentRunStore
    registry        AgentToolRegistry
    cancels         map[uint64]context.CancelFunc
    mu              sync.Mutex
    defaultHooks    *RunHooks
    skillStore      store.IAgentDefinitionStore
    // #9 compact
    compactProvider compact.CompactProvider // wired by biz.go via WithCompactProvider; may be nil → helpers no-op
    compactConfig   compact.Config          // defaults set in NewAgentRunner
}
```

### §5.3 NewAgentRunner 默认值

```go
func NewAgentRunner(runStore store.IAgentRunStore, registry AgentToolRegistry, opts ...RunnerOption) AgentRunner {
    r := &agentRunner{
        runStore:      runStore,
        registry:      registry,
        cancels:       make(map[uint64]context.CancelFunc),
        compactConfig: compact.DefaultConfig(), // #9 default qwen-plus
    }
    for _, opt := range opts { opt(r) }
    return r
}
```

### §5.4 3 个 helper（runner.go 末尾追加 — 不动 Run() 主流程）

```go
// tryPreLLMCompact 在每次 LLM 调用前估算 tokens，超阈值则触发 compact。
// v1：估算 + provider.Compact mock；真实 ReAct loop 集成由 #14 wire 调用此 helper。
// 返回 (newMessages, didCompact, err)
// S2 reviewer P1 fix：用 ReactiveCompact 返回的 finalMessages 做 CollapseDrain，
//   避免 inner headDrop 截断后 summary 与 collapsed 语义不一致。
func (r *agentRunner) tryPreLLMCompact(ctx context.Context, messages []compact.Message) ([]compact.Message, bool, error) {
    if r.compactProvider == nil { return messages, false, nil }
    text := ""
    for _, m := range messages { text += m.Content + "\n" }
    tokens := compact.EstimateTokens(text)
    if tokens < r.compactConfig.AutoCompactThreshold {
        return messages, false, nil
    }
    // 超阈值 → 调 reactive compact
    result, finalMessages, err := compact.ReactiveCompact(ctx, r.compactProvider, messages, r.compactConfig)
    if err != nil {
        return messages, false, err
    }
    // 用 finalMessages（compact 实际成功的输入）做 CollapseDrain，保证 summary 范围一致
    collapsed := compact.CollapseDrain(finalMessages, r.compactConfig.PTLCollapseKeepTurns)
    summary := compact.Message{
        Role:          "system",
        Content:       result.Summary,
        IsCompactMark: true,
    }
    return append([]compact.Message{summary}, collapsed...), true, nil
}

// handlePTLError 内部消费 st.Transition(LoopEventLLMErrPTL) 一次（S1 P1 fix）。
// 返回 (continue ContinueReason, newMessages []Message, isTerminal bool, terminal TerminalReason, err error)
// caller 不二次调 Transition 避免 PTLRetries 双重计数。
func (r *agentRunner) handlePTLError(
    ctx context.Context, st *LoopState, messages []compact.Message,
) (ContinueReason, []compact.Message, bool, TerminalReason, error) {
    term, cont, isTerm := st.Transition(LoopEventLLMErrPTL)
    if isTerm {
        return "", nil, true, term, nil
    }
    switch cont {
    case ContinueCollapseDrainRetry:
        collapsed := compact.CollapseDrain(messages, r.compactConfig.PTLCollapseKeepTurns)
        return cont, collapsed, false, "", nil
    case ContinueReactiveCompactRetry:
        if r.compactProvider == nil {
            return "", nil, true, TerminalPromptTooLong, fmt.Errorf("handlePTLError: nil compactProvider, cannot reactive_compact")
        }
        // S2 reviewer P1 fix：用 ReactiveCompact 返回的 finalMessages 做 CollapseDrain
        result, finalMessages, err := compact.ReactiveCompact(ctx, r.compactProvider, messages, r.compactConfig)
        if err != nil {
            return "", nil, true, TerminalPromptTooLong, err
        }
        summary := compact.Message{
            Role:          "system",
            Content:       result.Summary,
            IsCompactMark: true,
        }
        collapsed := compact.CollapseDrain(finalMessages, r.compactConfig.PTLCollapseKeepTurns)
        return cont, append([]compact.Message{summary}, collapsed...), false, "", nil
    default:
        return "", nil, true, TerminalModelError, fmt.Errorf("handlePTLError: unexpected continue reason %s", cont)
    }
}

// handleMaxOutputError 内部消费 st.Transition(LoopEventLLMErrMaxOutput) 一次（S1 P1 fix）。
// 返回 (continue ContinueReason, newMaxTokens int, isTerminal bool, terminal TerminalReason)
// escalate 阶段返回 EscalatedMaxTokens；recovery 阶段保持 currentMaxTokens（S1 P2 fix）。
func (r *agentRunner) handleMaxOutputError(
    ctx context.Context, st *LoopState, currentMaxTokens int,
) (ContinueReason, int, bool, TerminalReason) {
    term, cont, isTerm := st.Transition(LoopEventLLMErrMaxOutput)
    if isTerm {
        return "", 0, true, term
    }
    switch cont {
    case ContinueMaxOutputEscalate:
        return cont, compact.EscalateMaxTokens(currentMaxTokens), false, ""
    case ContinueMaxOutputRecovery:
        return cont, currentMaxTokens, false, "" // 不再升级，等待 LLM 完整输出
    default:
        return "", 0, true, TerminalModelError
    }
}
```

### §5.5 biz.go wire

`internal/numind/biz/biz.go`：

```go
// 既有：
runner := agent.NewAgentRunner(runStore, registry,
    agent.WithDefaultHooks(...),
    agent.WithSkillStore(...),
)

// #9 新增（与既有 options 顺序无关）：
// TODO(#14): replace MockCompactProvider with real aiservice.Chat-backed provider.
// MockCompactProvider 仅 v1 占位，PlaceholderSummary 文本仅用于 dev/test 验证 compact
// 触发路径；#14 ReAct loop 真实集成时替换为 real provider，PlaceholderSummary 不再用。
runner := agent.NewAgentRunner(runStore, registry,
    agent.WithDefaultHooks(...),
    agent.WithSkillStore(...),
    agent.WithCompactProvider(&compact.MockCompactProvider{
        PlaceholderSummary: "[v1 placeholder summary — real LLM compact in #14]",
    }),
    // WithCompactConfig optional — 不调时用 DefaultConfig (qwen-plus)
)
```

### §5.6 与 #7 memory merge 协同

#7 memory 改 runner.go Run() step 4 SystemPrompt 装配区，加 `memory.SystemBlock` 段：
```go
req.SystemPrompt = skill.PlatformBasePrompt + tenantHardRules + body + memorySystemBlock + toolsSection + skill.PlatformSafetyFooter
```

#9 改动**全部在 Run() 主流程外**：
- import 加 `numind-server/internal/numind/biz/compact`
- agentRunner struct 加 2 字段
- NewAgentRunner 加默认 compactConfig
- RunnerOption 加 2 个
- 函数末尾追加 3 个 helper

**预期 merge 冲突区域**：
- runner.go imports（加 compact import 行）
- agentRunner struct 字段（#7 加 memoryStore，#9 加 compactProvider/compactConfig）
- NewAgentRunner 内默认值初始化（#7 / #9 都加 1 行默认值）
- RunnerOption 列表（顺序合并）

**不冲突区域**：
- Run() 主流程 step 4 SystemPrompt 装配（仅 #7 改）
- Run() 主流程末尾 hook propagation（#6 改）
- runner.go 文件末尾新增函数（#9 / #7 都追加新函数）

**merge 策略**：#7 先 merge → #9 二次 merge 时 `git pull origin develop`，conflict 仅在 import / struct fields / NewAgentRunner，手工 union 解决。

## §6 测试矩阵详细

| Test | 文件 | Scope |
|------|------|-------|
| TestEstimateTokens_ChineseEnglishMix | provider_test.go | 1 中文 ≈ 1.5；1 英文 ≈ 0.25 |
| TestEstimateTokens_EmptyString | provider_test.go | 0 |
| TestMockCompactProvider_HappyPath | provider_test.go | 返回 PlaceholderSummary |
| TestMockCompactProvider_FailureSequence | provider_test.go | FailureSequence 控制失败模式 |
| TestDefaultConfig | threshold_test.go | qwen-plus 字段值 |
| TestFullCompactSystemPrompt | prompt_test.go | preamble + base 拼接 |
| TestCollapseDrain_StripsToolResults | ptl_chain_test.go | 5 turn → 第 1 turn tool_result 剥离 |
| TestCollapseDrain_KeepsTextBlocks | ptl_chain_test.go | user/assistant 文本不动 |
| TestCollapseDrain_RespectsCompactSummary | ptl_chain_test.go | IsCompactMark=true 不动 |
| TestCollapseDrain_RespectsFileRef | ptl_chain_test.go | HasFileRef=true 不动 |
| TestCollapseDrain_RespectsRecentTurns | ptl_chain_test.go | 最近 keepTurns 全保留 |
| TestCollapseDrain_EmptyOrShorterThanKeep | ptl_chain_test.go | 边界 |
| TestHeadDropRetry_DropsByGroup | ptl_chain_test.go | dropPercent=0.25 12 turn → 3 turn drop |
| TestHeadDropRetry_KeepsRecentTen | ptl_chain_test.go | 最近 10 轮全保留 |
| TestHeadDropRetry_RespectsCompactMark | ptl_chain_test.go | 含 IsCompactMark 的组不 drop |
| TestHeadDropRetry_RespectsFileRef | ptl_chain_test.go | 含 HasFileRef 的组不 drop |
| TestReactiveCompact_HappyPath | ptl_chain_test.go | mock 返回 summary |
| TestReactiveCompact_InnerRetryOnError | ptl_chain_test.go | provider err → headDrop → retry |
| TestReactiveCompact_ExhaustsInnerRetries | ptl_chain_test.go | 4 次失败 → return err |
| TestReactiveCompact_NilProvider | ptl_chain_test.go | nil provider → err |
| TestEscalateMaxTokens | max_output_chain_test.go | 8192 → 65536 |
| TestEscalateMaxTokens_AlreadyMax | max_output_chain_test.go | 65536 → 65536 |
| TestCleanseMessages_DropsDanglingToolUse | restore_test.go | tool_use 无 tool_result + 无 content → drop |
| TestCleanseMessages_DropsEmptyAssistant | restore_test.go | content="" + 无 tool_calls → drop |
| TestCleanseMessages_DropsThinking | restore_test.go | role="thinking" → drop |
| TestRestore_InjectsNarration | restore_test.go | RestorationNarration 注入 |
| TestRestore_FirstTurnNoTools | restore_test.go | 标志 true |
| TestRestore_NoCompactSummary | restore_test.go | fall through |
| TestRestore_WithCompactSummary | restore_test.go | summary 作首条 system message |
| TestRestore_NilReinjector | restore_test.go | 返回 err |
| TestRestore_NullReinjectorPassthrough | restore_test.go | systemPrompt 不动 |
| TestCompactStateV1_JSON_RoundTrip | types_test.go | 序列化 + 反序列化幂等 |
| TestCompactStateV1_PartialFields | types_test.go | 缺失字段返回 zero value |
| TestRunner_TryPreLLMCompact_Skip | runner_compact_test.go | tokens < threshold → unchanged |
| TestRunner_TryPreLLMCompact_Trigger | runner_compact_test.go | tokens > threshold → compact 触发 |
| TestRunner_TryPreLLMCompact_NilProvider | runner_compact_test.go | nil → no-op |
| TestRunner_HandlePTLError_Step1Collapse | runner_compact_test.go | retry 1 → CollapseDrain |
| TestRunner_HandlePTLError_Step2Reactive | runner_compact_test.go | retry 2 → ReactiveCompact |
| TestRunner_HandlePTLError_Terminal | runner_compact_test.go | retry > 2 → TerminalPromptTooLong |
| TestRunner_HandlePTLError_NoDoubleCounting | runner_compact_test.go | helper 消费 Transition 一次 |
| TestRunner_HandleMaxOutputError_Escalate | runner_compact_test.go | retry 1 → 65536 + ContinueMaxOutputEscalate |
| TestRunner_HandleMaxOutputError_Recovery | runner_compact_test.go | retry 2 → currentMaxTokens 保持 |
| TestRunner_HandleMaxOutputError_Terminal | runner_compact_test.go | retry > 2 → TerminalErrorMaxBudget |
| TestRunner_HelpersRaceSafe | runner_compact_test.go | `go test -race` 跑 3 helper 并发场景 |
| TestAgentRunModel_CompactColumnsRoundTrip | (in agent_run_test.go 或独立 model_test) | DB roundtrip CompactState/CompactSummary |

**覆盖率目标**：biz/compact ≥ 80%；biz/agent 不下降。

## §7 风险与权衡

S0/S1 已列。S2 补充：

**R1**：S0 list 9 项均有缓解，本 S2 spec 实现。

**R2**：mock provider call_count 跨测试干扰 — fixture 用法每个测试新建 MockCompactProvider 实例；S3 plan task 中明确测试隔离。

**R3**：runner_compact_test.go 需 mock LoopState — 直接构造 LoopState{} 即可（公开类型，无需 mock 框架）。

**R4**：CollapseDrain 边界 keepTurns >= len(messages) → S2 spec 已写明返回原 slice，避免数据丢失。

## §8 实施顺序提示（S3 plan 入口）

S3 plan 拆 task 时建议顺序：

- M1 migration SQL + rollback（无依赖）
- M2 model 加字段（依赖 M1）
- M3 biz/compact types.go + threshold.go + prompt.go（无依赖，3 文件并行）
- M4 biz/compact provider.go + ptl_chain.go + max_output_chain.go（依赖 M3，3 文件并行）
- M5 biz/compact restore.go + attachments.go（依赖 M3，2 文件并行）
- M6 runner.go 改造（依赖 M2 + M4 + M5）
- M7 biz.go wire（依赖 M6）
- M8 集成测试 + 覆盖率验证

**Tier 3 并行可行性**：
- M3 / M4 / M5 文件互不相交 → Tier 3 可行（runner.go 不动；3 文件归属清晰）
- M6 / M7 同改 runner.go / biz.go → 串行

S4 编码时按上述顺序，每个 task 完成后双 reviewer。

---

**S2 完结。S3 拆 task 并制定 S5 验收策略。**

---

## §S5-strategy 验证策略（NDF 规则 10 要求，M8 task 落地）

**选择**：**仅后端 TDD**（Go 单测 + 集成测试），不走 Playwright / gstack `/qa`。

### 为什么 TDD-only 是对的

- **零前端 UI 改动**：#9 范围 100% 在 numind-server，无 Vue 组件 / 无 HTML / 无 CSS。学员端会话恢复 UI 由 #11 落地。
- **零新 HTTP 端点**：#9 不引入 controller / router 注册。compact 暴露为 Go 库 + AgentRunner helper；HTTP API 由 #11 学员端 + #14 ReAct 集成时落地。
- **测试覆盖完整**：48+ 单测覆盖 happy path + 边界 + race，跨 6 个 _test.go 文件持久化在代码库。
- **gstack `/qa` 无价值**：是一次性浏览器 QA，无持久化测试代码 → 对 Go 库无意义（commit 即留回归保护）。
- **Playwright E2E 不适用**：需要 HTTP 端点；#9 没有。

### S5 验收清单（10 项）

每项 S5 必须通过：

1. `go test -race ./internal/numind/biz/compact/...` PASS（biz/compact 整包 7 .go + 7 _test.go）
2. `go test -race ./internal/numind/biz/agent/...` PASS（含 #2 既有 + #9 新增 runner_compact_test.go）
3. `go test -race ./internal/numind/store/...` PASS（agent_run_test.go DDL 已扩展）
4. `go test -race ./internal/pkg/model/...` PASS（新增 agent_run_test.go）
5. `go vet ./...` exit 0
6. `task lint` PASS（golangci-lint）
7. biz/compact **整包覆盖率 ≥ 80%**（实际目前 97.8%）— `go test -cover ./internal/numind/biz/compact/...`
8. biz/agent **覆盖率不下降**（目前 80.9%，与 develop 基线对照）— `go test -cover ./internal/numind/biz/agent/...`
9. `go build ./...` PASS（M7 wire 不引入编译错误）
10. **审计**：`grep -n "config_prod.yaml" *.diff` 应零差异（0 prod 影响硬规则）

### 回归保护诚实声明

- biz/compact 7 个 _test.go = **永久回归保护**（任何 #14 / #11 改动破坏现有契约都会触发测试失败）
- runner_compact_test.go = 同上
- AutoMigrate dev 自检（agent_run 新 2 列）= **一次性验证**（user `/deploy-dev server` 后手工 SSH MySQL 跑 `ALTER` SQL 或验证 GORM AutoMigrate 自动同步）— 不留持久化测试代码，由 #11/#14 单测捕获 schema 不匹配

### 不在 S5 范围（移交 #11 / #14 后续）

- 真实 LLM compact 端到端测试（#14 接 aiservice.Chat-backed provider 后做）
- 学员端 UI 会话恢复流程（#11 落地）
- 跨设备 session sync（v2）
- L3 session archive（v2）
- 真实文件 / Skill / MCP delta 重注入（#11/#14 落地 AttachmentReinjector）

### S5 acceptance 文档位置

`numind-server/docs/superpowers/qa/2026-05-21-agent-mode-compact-s5-acceptance.md` — S5 阶段（S6 ndf-done 前）由主 session 写，含上 10 项实际执行结果 + 累计 P0/P1/P2 reviewer 统计。

---

**M8 完结。S4 完成。进入 S5 验收。**
