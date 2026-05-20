# NDF S1 Proposal + PRD · `agent-mode-compact`

**Track**：Standard
**Feature ID**：`agent-mode-compact`（14-feature 分解 #9/14）
**起草日期**：2026-05-21
**状态**：S1 草案
**前置 stage**：S0 通过（commit `95d6a945`）

---

## 1. 目标与背景

### 1.1 商业价值

qwen-plus 单次上下文窗口 128k tokens。长对话+工具调用历史+文件附件几轮就吃满。一旦 LLM API 返回 `context_length_exceeded`，整个 Run 终止 → 学员已扣的积分白付，体验崩坏。

**Compact 系统的产品定位**：让 Agent 在长对话里"活下来" —— 上下文超限时自动压缩历史，输出被截断时自动重试更大 max_tokens，学员断网/换页重连时恢复对话状态。

### 1.2 业务目标

- **长对话不掉链**：107k tokens 触发自动 compact，对话可继续到学员主动结束
- **输出截断自愈**：max_output_tokens 链 3 次升级（8192 → 65536）覆盖大多数长 tool_use
- **重连无缝**：学员从历史会话点击恢复 → 自动读取 CompactSummary + 最近 N 轮 → 继续对话
- **积分不浪费**：compact 失败时 outer PTL retry 2 次 + inner head-drop 3 次兜底，仅穷尽后才 TerminalPromptTooLong

### 1.3 技术目标（属于本 feature）

- biz/compact 子包覆盖率 ≥ 80%；biz/agent 不降级
- agent_run 表 ALTER 加列（不新建表）
- 不动 #2 state.go 状态机（LoopEventLLMErrPTL / LoopEventLLMErrMaxOutput 已就绪）
- CompactProvider interface 完整 + Mock 实现就位（#14 接真实 aiservice）
- 0 prod 影响

---

## 2. 用户故事（User Stories）

### US-1：学员长对话不被中断（内部用户 = #14 ReAct loop）

```
作为：ReAct loop（runner.Run 真实集成由 #14 落地）
当：tokens_used 估算 > 107k（AutoCompactThreshold）
我想：自动触发 compact 压缩 History 到 30% 后继续
以便：学员对话不被打断

完成路径（v1 仅 plumbing，真实流程 #14）：
1. ReAct loop 调 r.tryPreLLMCompact(ctx, messages)
2. helper 估算 tokens（粗算公式）
3. > AutoCompactThreshold → 调 compactProvider.Compact(ctx, req)
4. v1 MockCompactProvider 返回固定占位 summary
5. 写 agent_run.compact_summary + compact_state
6. 返回 newMessages = [CompactSummary, ...recent4Turns]
7. ReAct loop 用 newMessages 继续 LLM 调用
```

### US-2：LLM 返回 context_length_exceeded → PTL 链自动恢复（内部用户）

```
作为：ReAct loop
当：LLM 返回 error.type == "context_length_exceeded"
我想：自动 collapse_drain → 失败再 reactive_compact → 失败终止
以便：尽可能恢复

完成路径（v1 仅 helper + state machine，真实集成 #14）：
1. LLM 调用返回 PTL error
2. ReAct loop 调 r.handlePTLError(ctx, st, messages)
3. st.Transition(LoopEventLLMErrPTL)
4. outer PTLRetries=1 → ContinueCollapseDrainRetry
   - helper 调 compact.CollapseDrain(messages, keepTurns=4) → newMessages（剥离 tool_result）
   - return (LoopEventLLMErrPTL, newMessages)
5. ReAct loop 用 newMessages 重试
6. 再次 PTL → state.Transition(LoopEventLLMErrPTL)
7. outer PTLRetries=2 → ContinueReactiveCompactRetry
   - helper 调 compact.ReactiveCompact(ctx, provider, messages)
     - 内部尝试压缩
     - 失败 → headDropRetry inner 循环最多 3 次
     - 仍失败 → return err
   - return (LoopEventLLMErrPTL, newMessages)
8. 第 3 次 PTL → outer PTLRetries=3 > MaxPTLRetries=2 → TerminalPromptTooLong
```

### US-3：LLM 输出截断 → max_output 链升级（内部用户）

```
作为：ReAct loop
当：LLM stop_reason == "max_tokens"
我想：自动升级 max_tokens 8192 → 65536 重试
以便：让 LLM 写完长 tool_use 块

完成路径（v1 仅 helper + state machine，真实集成 #14）：
1. LLM 返回 stop_reason == "max_tokens"
2. ReAct loop 调 r.handleMaxOutputError(ctx, st, currentMaxTokens=8192)
3. st.Transition(LoopEventLLMErrMaxOutput)
4. outer MaxOutputRetries=1 → ContinueMaxOutputEscalate
   - helper 调 compact.EscalateMaxTokens(8192) → 65536
   - return (LoopEventLLMErrMaxOutput, 65536)
5. ReAct loop 用 65536 重试
6. 仍截断 → MaxOutputRetries=2 → ContinueMaxOutputRecovery
   - helper return (LoopEventLLMErrMaxOutput, 65536) — 同 max_tokens
7. 第 3 次截断 → MaxOutputRetries=3 > MaxOutputRetriesLimit=2 → TerminalErrorMaxBudget
```

### US-4：学员断网重连 → 会话恢复（内部用户 = #11 学员端）

```
作为：#11 学员端 ResumeRun handler（HTTP API 由 #11 落地）
当：学员点击历史会话"继续对话"
我想：调 compact.Restore(run) 获得清洗后的 messages + 恢复 narration
以便：LLM 收到清洗后的上下文继续对话

完成路径（v1 仅 helper，HTTP API #11）：
1. #11 handler 读 agent_run row
2. 调 compact.Restore(run) → RestoredSession{
     Messages: []Message,            // 清洗后
     SystemNarration: string,         // 恢复 narration
     FirstTurnNoTools: bool,          // true（§4.8.6 step 5）
   }
3. Restore 内部 3 道清洗（去悬空 tool_use / 孤立 thinking / 空 assistant）
4. 注入"学员已重新打开这个会话..."narration
5. v1 不实际禁用工具（#11 handler 收到 FirstTurnNoTools=true 后自己实施）
```

---

## 3. 设计选项

### Option A：agent_run 表 ALTER 加列（**选**）

**做法**：在 #2 既有的 `agent_run` 表上 `ALTER TABLE` 加 `compact_state JSON NULL` + `compact_summary TEXT NULL` 两列。

**利**：
- 蓝本 §4.8.6 明确说"compact_state 字段持久化在 agent_run.compact_state" → 直接遵蓝本
- 数据归属清晰：CompactSummary 与 messages / state_reason 同生命周期，同一 run 一行就够
- 无新表 → 无新 FK 关系 → 无新索引设计 → S4 实施简单
- 旧 agent_run 行兼容（NULL）→ 不破坏 #1-#8 数据
- AutoMigrate 自动加列 → 无需手工 SSH 执行 ALTER

**弊**：
- 单 Run 多次 compact 时旧 summary 被覆写 → 历史 summary 丢失（缓解：messages 链本身已记录边界）
- 列宽：`compact_summary TEXT`（max 64KB）可能不够极长摘要 → S2 决定是否升级 LONGTEXT
- 与 #6/#7/#8 并行 ALTER 同表潜在 SQL 冲突（缓解：列名不重；GORM AutoMigrate 检测列存在 skip）

### Option B：独立 `agent_run_compact` 表 (1:N agent_run)

**做法**：新建表存储每次 compact 事件。

```sql
CREATE TABLE agent_run_compact (
  id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  agent_run_id BIGINT UNSIGNED NOT NULL,
  compact_at DATETIME(3) NOT NULL,
  boundary_message_id VARCHAR(64),
  attempts INT,
  summary TEXT,
  state JSON,
  FOREIGN KEY (agent_run_id) REFERENCES agent_run(id) ON DELETE CASCADE
);
```

**利**：
- 多次 compact 历史完整保留
- 审计友好（一行 = 一次事件）

**弊**：
- 蓝本 §4.8.6 明确把 compact_state 放在 agent_run 行（违蓝本）
- FK + index + AutoMigrate 顺序复杂度增加
- v1 没有"展示历史 compact 列表"的产品需求（#10 / #11 都不需要） → 过度设计
- 与 #6/#7/#8 都不动 agent_run 表本身 → 但本 feature 偏要建新表 → 与项目惯性背离

### Option C：messages 列内嵌 compact 边界（无新字段）

**做法**：完全不动 schema，把 compact summary 作为特殊 system message 内联进 messages JSON 列。

**利**：
- 完全无 schema 变更
- compact 边界由 messages 序列体现

**弊**：
- 蓝本 §4.8.6 明确要求 `compact_state` 独立字段（违蓝本）
- 恢复时遍历 messages 找 compact 边界很慢（O(n) 扫整 JSON）
- 状态字段（last_compact_at / consecutive_failures）没地方放
- 不可索引 → 查询历史"哪些 run 经历过 compact"困难

### **选 A**：遵蓝本 + 简单 + #14 真实集成时无需迁移数据

---

## 4. PRD（功能规格）

### 4.1 数据库

**表 1：agent_run**（既有，加 2 列）

| 列 | 类型 | NULL | 默认 | 说明 |
|---|---|---|---|---|
| compact_state | JSON | YES | NULL | CompactStateV1 序列化；含 last_compact_at / last_boundary_message_id / total_compact_attempts / consecutive_failures / summary_token_count / strategy_used |
| compact_summary | LONGTEXT | YES | NULL | 最新 CompactSummary 全文（恢复时快速读，避免遍历 messages）。S1 reviewer P2 fix：升 LONGTEXT 应对 9 节摘要（含 verbatim 用户消息）潜在 > 64KB 场景；MySQL LONGTEXT 上限 4GB，存储开销与 TEXT 一致 |

**Migration**：
- `migrations/20260521_010000_alter_agent_run_add_compact_columns.sql`
- `migrations/20260521_010000_alter_agent_run_add_compact_columns_rollback.sql`
- AutoMigrate 同步（不替代 SQL；按 `project_dev_deploy_migration_gap` 记忆，dev 部署需手工 SSH 执行 migration）

### 4.2 biz/compact 子包

```
internal/numind/biz/compact/
├── threshold.go          # CompactConfig + qwen-plus 默认值 + ContextWindowSafetyMargin / PTLCollapseKeepTurns
├── prompt.go             # BASE_COMPACT_PROMPT (9 节) + NO_TOOLS_PREAMBLE
├── provider.go           # CompactProvider interface + MockCompactProvider + token estimation
├── ptl_chain.go          # CollapseDrain + ReactiveCompact + headDropRetry (inner)
├── max_output_chain.go   # EscalateMaxTokens (8192 → 65536)
├── restore.go            # Restore + 3 道清洗 + 恢复 narration + FirstTurnNoTools
└── attachments.go        # AttachmentReinjector interface + NullAttachmentReinjector
```

#### threshold.go

```go
package compact

// Config 控制 compact 触发阈值；默认值适配 qwen-plus（蓝本 §4.8.4）。
type Config struct {
    ContextWindow                     int     // 128_000
    EffectiveContextWindow            int     // 120_000 = 128k - 8k maxOutput
    AutoCompactThreshold              int     // 107_000 = 120k - 13k buffer
    MaxConsecutiveAutoCompactFailures int     // 3
    MaxCompactOutputTokens            int     // 8_000
    ContextWindowSafetyMargin         float64 // 0.95 — 本地估算触发阈值
    PTLCollapseKeepTurns              int     // 4 — CollapseDrain 保留最近 N 轮（§4.1.6）
}

func DefaultConfig() Config { /* return qwen-plus values */ }
```

#### prompt.go

```go
const NoToolsPreamble = `【重要】你现在的任务是生成对话摘要...` // §4.8.2

const BaseCompactPrompt = `请按以下 9 节结构输出摘要：
1. 主要请求和意图
...
9. 可选下一步（verbatim 引用，防 task drift）
` // §4.8.3
```

#### provider.go

```go
type CompactProvider interface {
    Compact(ctx context.Context, req *CompactRequest) (*CompactResult, error)
}

type CompactRequest struct {
    Messages     []Message
    SystemPrompt string  // 含 NO_TOOLS_PREAMBLE + BASE_COMPACT_PROMPT
    MaxOutputTokens int
}

type CompactResult struct {
    Summary      string
    InputTokens  int
    OutputTokens int
}

// MockCompactProvider v1：返回固定占位 summary，不调外部 API
type MockCompactProvider struct {
    PlaceholderSummary string
}

func (m *MockCompactProvider) Compact(ctx context.Context, req *CompactRequest) (*CompactResult, error) {
    return &CompactResult{Summary: m.PlaceholderSummary, InputTokens: 0, OutputTokens: 0}, nil
}

// EstimateTokens 粗算 token 数（1 中文字 ≈ 1.5 token / 1 英文字符 ≈ 0.25 token）
func EstimateTokens(text string) int { /* 粗算公式 */ }
```

#### ptl_chain.go

```go
// CollapseDrain 是 PTL 链 outer Step 1：仅剥离最近 keepTurns 外的 tool_result 块，
// 不动 user/assistant 文本（蓝本 §4.1.6 Step 1）。
// 保留 compact summary 标记的消息，保留含文件引用的消息。
func CollapseDrain(messages []Message, keepTurns int) []Message { /* ... */ }

// ReactiveCompact 是 PTL 链 outer Step 2：调 LLM 全量压缩。
// 失败时内部递归 headDropRetry（蓝本 §4.8.5）最多 maxRetries=3 次。
// cfg 必传：MaxCompactOutputTokens / 边界规则（S1 reviewer P1 fix — 签名稳定，#14 接真实 aiservice 无需改）
func ReactiveCompact(ctx context.Context, provider CompactProvider, messages []Message, cfg Config) (*CompactResult, error) {
    result, err := tryCompact(ctx, provider, messages, cfg)
    if err == nil { return result, nil }
    // inner retry loop
    innerDropAttempts := 0
    truncated := messages
    for innerDropAttempts < 3 {
        truncated = headDropRetry(truncated, 0.25)
        result, err = tryCompact(ctx, provider, truncated, cfg)
        if err == nil { return result, nil }
        innerDropAttempts++
    }
    return nil, fmt.Errorf("ReactiveCompact: exhausted innerDropAttempts: %w", err)
}

// headDropRetry 内部 head-drop（蓝本 §4.8.5）：按消息组（每轮=user+assistant）drop 头部
// dropPercent。保留规则：
// - 保留 compact summary 标记的消息组
// - 保留最近 10 轮
// - 保留含用户文件引用的消息组（蓝本 §4.8.5；S1 reviewer P2 fix）
func headDropRetry(messages []Message, dropPercent float64) []Message { /* ... */ }
```

#### max_output_chain.go

```go
const (
    DefaultMaxTokens   = 8192
    EscalatedMaxTokens = 65536
)

// EscalateMaxTokens 升级 max_tokens（蓝本 §4.1.6 max_output 链 Step 1）。
// 8192 → 65536；已 65536 → 仍 65536（recovery 阶段不再升级）。
func EscalateMaxTokens(current int) int {
    if current < EscalatedMaxTokens { return EscalatedMaxTokens }
    return EscalatedMaxTokens
}
```

#### restore.go

```go
type RestoredSession struct {
    Messages         []Message
    SystemNarration  string  // 恢复 narration（§4.8.6 step 3）
    FirstTurnNoTools bool    // true — caller 收到后自己实施禁工具
}

// Restore 读 agent_run.compact_summary + messages，3 道清洗后返回 RestoredSession。
// reinjector 必传（v1 caller 传 &NullAttachmentReinjector{}）；S1 reviewer P1 fix —
// 接口 DI 稳定，#11/#14 接入真实 reinjector 时无需改 signature。
func Restore(ctx context.Context, run *model.AgentRun, reinjector AttachmentReinjector) (*RestoredSession, error) { /* ... */ }
```

#### attachments.go

```go
type AttachmentReinjector interface {
    // Reinject 在 systemPrompt 末尾追加文件/Skill/MCP delta 上下文。
    // v1 NullAttachmentReinjector return systemPrompt 不动。
    // #11/#14 实现真实文件读取 / Skill 注入 / MCP delta 计算。
    Reinject(ctx context.Context, systemPrompt string, runID uint64) (string, error)
}

type NullAttachmentReinjector struct{}

func (n *NullAttachmentReinjector) Reinject(ctx context.Context, systemPrompt string, runID uint64) (string, error) {
    return systemPrompt, nil
}
```

### 4.3 Runner 集成

**RunnerOption 新增**：

```go
// WithCompactProvider sets the CompactProvider for PTL/MaxOutput recovery chains.
// Real LLM compact is wired in #14; v1 uses MockCompactProvider.
func WithCompactProvider(p compact.CompactProvider) RunnerOption {
    return func(r *agentRunner) { r.compactProvider = p }
}

// WithCompactConfig overrides default qwen-plus thresholds.
func WithCompactConfig(cfg compact.Config) RunnerOption {
    return func(r *agentRunner) { r.compactConfig = cfg }
}
```

**agentRunner 字段新增**：

```go
type agentRunner struct {
    // ... 既有字段
    compactProvider compact.CompactProvider // #9 compact: wired by biz.go via WithCompactProvider
    compactConfig   compact.Config          // #9 compact: defaults to compact.DefaultConfig()
}
```

**3 个 helper（Run 主流程外）**：

```go
// tryPreLLMCompact 在每次 LLM 调用前估算 tokens，超阈值则触发 compact。
// v1：估算 + provider.Compact mock；真实 ReAct loop 调用由 #14 wire。
// 返回 (newMessages []Message, didCompact bool)
func (r *agentRunner) tryPreLLMCompact(ctx context.Context, messages []Message) ([]Message, bool) { /* ... */ }

// handlePTLError 内部消费 st.Transition(LoopEventLLMErrPTL) 一次（S1 reviewer P1 fix —
// 不返回原始 LoopEvent，避免 caller 二次调用造成 PTLRetries 双重计数）。
// PTLRetries==1 → CollapseDrain → return (ContinueCollapseDrainRetry, newMessages, false, "")
// PTLRetries==2 → ReactiveCompact → return (ContinueReactiveCompactRetry, newMessages, false, "")
// PTLRetries>2 → return ("", nil, true, TerminalPromptTooLong)
func (r *agentRunner) handlePTLError(ctx context.Context, st *LoopState, messages []Message) (cont ContinueReason, newMessages []Message, isTerminal bool, terminal TerminalReason) { /* ... */ }

// handleMaxOutputError 内部消费 st.Transition(LoopEventLLMErrMaxOutput) 一次（同 PTL）。
// MaxOutputRetries==1 → escalate → return (ContinueMaxOutputEscalate, EscalatedMaxTokens=65536, false, "")
// MaxOutputRetries==2 → recovery → return (ContinueMaxOutputRecovery, currentMaxTokens, false, "")
//   注：recovery 阶段不再调 EscalateMaxTokens（已 65536），保持 currentMaxTokens 等待 LLM 完整输出（S1 reviewer P2 fix）
// MaxOutputRetries>2 → return ("", 0, true, TerminalErrorMaxBudget)
func (r *agentRunner) handleMaxOutputError(ctx context.Context, st *LoopState, currentMaxTokens int) (cont ContinueReason, newMaxTokens int, isTerminal bool, terminal TerminalReason) { /* ... */ }
```

**#2 mock 主流程不动**（仅加 helper + RunnerOption + struct 字段，Run() 主流程不调 helper）；#14 ReAct loop 集成时调 3 个 helper。

### 4.4 biz.go wire

```go
// 既有：
runner := agent.NewAgentRunner(runStore, registry,
    agent.WithDefaultHooks(...),
    agent.WithSkillStore(...),
)

// #9 新增：
runner := agent.NewAgentRunner(runStore, registry,
    agent.WithDefaultHooks(...),
    agent.WithSkillStore(...),
    agent.WithCompactProvider(&compact.MockCompactProvider{
        PlaceholderSummary: "[v1 placeholder summary — real LLM compact in #14]",
    }),
    agent.WithCompactConfig(compact.DefaultConfig()),
)
```

### 4.5 不动 state.go

- `LoopEventLLMErrPTL` / `LoopEventLLMErrMaxOutput` 已在 #2 就绪
- `LoopEventCollapseDrainRetry` / `LoopEventMaxOutputEscalate` 已在 #2 就绪
- state.go Transition 已处理 PTLRetries / MaxOutputRetries 计数
- #9 不改 state.go，仅 helper 函数调 `st.Transition(LoopEventLLMErrPTL)` 触发既有逻辑

### 4.6 测试矩阵

| 测试 | 文件 | 范围 |
|------|------|------|
| TestCollapseDrain_StripsToolResults | ptl_chain_test.go | 仅剥离 tool_result |
| TestCollapseDrain_KeepsTextBlocks | ptl_chain_test.go | user/assistant 文本不动 |
| TestCollapseDrain_RespectsCompactSummary | ptl_chain_test.go | 含 summary 标记不被动 |
| TestCollapseDrain_RespectsRecentTurns | ptl_chain_test.go | 最近 4 轮全保留 |
| TestHeadDropRetry_DropsByGroup | ptl_chain_test.go | 整组 drop |
| TestHeadDropRetry_RespectsMaxAttempts | ptl_chain_test.go | maxRetries=3 |
| TestReactiveCompact_MockProvider | ptl_chain_test.go | mock 返回 summary |
| TestReactiveCompact_InnerRetryOnError | ptl_chain_test.go | provider err → headDrop |
| TestEscalateMaxTokens | max_output_chain_test.go | 8192 → 65536 + 65536 → 65536 |
| TestEstimateTokens | provider_test.go | 粗算公式 |
| TestRestore_3CleansingPasses | restore_test.go | 悬空/孤立/空清洗 |
| TestRestore_InjectsNarration | restore_test.go | §4.8.6 step 3 |
| TestRestore_FirstTurnNoTools | restore_test.go | 标志 true |
| TestRestore_NoCompactSummary | restore_test.go | fall through 空 string |
| TestRestore_CompactStateRoundTrip | restore_test.go | JSON 部分字段 unmarshal 幂等（P2 fix） |
| TestRestore_NullReinjectorPassthrough | restore_test.go | NullAttachmentReinjector 不改 systemPrompt（S1 P1 fix） |
| TestRunner_TryPreLLMCompact_Trigger | runner_compact_test.go | tokens > threshold |
| TestRunner_HandlePTLError_Step1Step2 | runner_compact_test.go | retry 1 collapse / 2 reactive；ContinueReason 准确 |
| TestRunner_HandlePTLError_Terminal | runner_compact_test.go | retry > 2 → isTerminal=true + TerminalPromptTooLong |
| TestRunner_HandlePTLError_NoDoubleCounting | runner_compact_test.go | helper 内部消费 Transition 一次（S1 P1 fix） |
| TestRunner_HandleMaxOutputError_Escalate | runner_compact_test.go | retry 1 → EscalatedMaxTokens=65536 + ContinueMaxOutputEscalate |
| TestRunner_HandleMaxOutputError_Recovery | runner_compact_test.go | retry 2 → currentMaxTokens 保持 + ContinueMaxOutputRecovery（S1 P2 fix） |
| TestRunner_HandleMaxOutputError_Terminal | runner_compact_test.go | retry > 2 → isTerminal=true + TerminalErrorMaxBudget |
| TestRunner_HelpersRaceSafe | runner_compact_test.go | `go test -race` |
| TestAgentRunModel_CompactColumnsRoundTrip | agent_run_test.go | DB roundtrip |

---

## 5. 不在 v1 的事（明确划线）

- **真实 LLM compact 调用** — MockCompactProvider 占位，真实 aiservice.Chat 走 BaseCompactPrompt 由 #14 落地
- **学员端会话恢复 HTTP API** — #11 落地（用 compact.Restore helper）
- **管理端阈值配置 CRUD UI** — #10 落地（直接读 CompactConfig）
- **trySessionMemoryCompaction**（§4.8.1 廉价首选）— v1 直接走 reactive_compact；优化 backlog
- **熔断器 MaxConsecutiveAutoCompactFailures 真实拦截** — v1 配置就位但 #9 不主动 check；#14 wire ReAct loop 时实施
- **真实文件/Skill/MCP delta 重注入** — v1 NullAttachmentReinjector，真实在 #11/#14
- **prod 部署** — develop merge 后停

---

## 6. 风险与缓解（在 S0 基础上补充）

S0 已列 9 条主要风险。S1 新增：

10. **CompactProvider interface 设计的 LLM 调用风格** — 风险：v1 mock 用同步 Compact()，但真实 aiservice.Chat 是流式 SSE，interface 不兼容
    - 缓解：interface 用同步签名 `Compact(ctx, req) (*CompactResult, error)`；#14 真实 provider 内部用 aiservice.Chat 流式但聚合为同步返回；caller 不感知

11. **token estimation 与 qwen-plus 真实 tokenizer 偏差** — 风险：粗算公式高估或低估 → AutoCompactThreshold 触发不准
    - 缓解：S2 spec 加 `WithTokenEstimator(fn func(string) int) Option`；v1 默认粗算；#14 接 tiktoken-go 或 aliyun token API

12. **runner.go merge 与 #6/#7 冲突** — 风险：3 个并行 session 都改 runner.go
    - 缓解：(a) #9 改动仅在 import / struct 字段 / RunnerOption / Run() 末尾（不动 step 4 SystemPrompt 装配区）；(b) S6 merge 时手工解决；(c) 与 #6/#7 协同位置已识别在 S0 manifest decisions

---

## 7. 简单时间线

S1（本卡） → S2 spec → S3 plan → S4 编码（M1-M~8）→ S5 验收 → S6 ndf-done

每阶段独立 Sonnet reviewer。

---

**S1 完结。S2 写架构 spec（含 GORM model audit + interface 完整签名 + 包架构）。**
