# Agent 模式 Compact — Task Plan

> NDF v2 S3 plan | Feature: agent-mode-compact | #9/14
> 上游：S2 spec `docs/superpowers/specs/2026-05-21-agent-mode-compact-design.md`

## §1 任务总览

8 个 M task。预估总工时：单 agent 串行 ≈ 5-6 小时；autopilot 高度并行 wall clock ≈ 3 小时（Wave 3/4/5 多文件并行）。

| # | Task 名 | 主要文件（写/改） | 依赖 | 估时 |
|---|---|---|---|---|
| M1 | DB migration ALTER + rollback | `migrations/20260521_010000_alter_agent_run_add_compact_columns.sql` (+ rollback) | — | 15 min |
| M2 | GORM model AgentRun 加字段 + audit no-bool 单测 | `internal/pkg/model/agent_run.go` + `_test.go` | M1 | 20 min |
| M3 | biz/compact 基础（types + threshold + prompt 3 文件） | `internal/numind/biz/compact/{types,threshold,prompt}.go` + `_test.go` | — | 30 min |
| M4 | biz/compact 算法（provider + ptl_chain + max_output_chain 3 文件） | `internal/numind/biz/compact/{provider,ptl_chain,max_output_chain}.go` + `_test.go` | M3 | 60 min |
| M5 | biz/compact 恢复（restore + attachments 2 文件） | `internal/numind/biz/compact/{restore,attachments}.go` + `_test.go` | M3, M2 | 45 min |
| M6 | runner.go 集成（RunnerOption + struct 字段 + 3 helper + 单测） | `internal/numind/biz/agent/runner.go` + `runner_compact_test.go` | M3, M4, M5 | 60 min |
| M7 | biz.go wire MockCompactProvider | `internal/numind/biz/biz.go` | M6 | 15 min |
| M8 | S5 验证策略文档 | spec doc 补 §S5-strategy | — | 15 min |

## §2 任务详情

### M1 — DB migration ALTER + rollback

**目标**：建 ALTER SQL 加 2 列 + rollback。

**文件**：
- `migrations/20260521_010000_alter_agent_run_add_compact_columns.sql`
- `migrations/20260521_010000_alter_agent_run_add_compact_columns_rollback.sql`

**内容**：S2 §2.1 已给完整 SQL，直接抄。

**注意**：
- ALTER TABLE 加 2 列；不动既有列；不动 CHECK constraint（`chk_ar_state_reason` 已含 §9 需要的 continue reason）
- AutoMigrate 同步：`helper.go` agent_run model 注册已就位（#2），M2 改 model struct 自动 trigger AutoMigrate 加列
- 按 memory `project_dev_deploy_migration_gap` — dev/prod CI 不跑 migration，上线时需手工 SSH 执行（本 feature 不影响 prod）

**单测**：无（migration 是 SQL）

**验收**：
- 2 个 SQL 文件存在
- ALTER 语法正确
- rollback 干净（DROP COLUMN 顺序对）

**commit**：`feat(agent-compact): M1 DB migration — ALTER agent_run add compact_state + compact_summary`

---

### M2 — GORM model AgentRun 加字段

**目标**：在 `internal/pkg/model/agent_run.go` 加 2 字段；audit 无 `default:true` bool gotcha 风险。

**文件**：
- `internal/pkg/model/agent_run.go`（改）
- `internal/pkg/model/agent_run_test.go`（**新**或扩展）

**model 字段**（S2 §2.2 完整给出）：
```go
CompactState   datatypes.JSON `gorm:"type:json" json:"compact_state,omitempty"`
CompactSummary string         `gorm:"type:longtext" json:"compact_summary,omitempty"`
```

**单测**：`internal/pkg/model/agent_run_test.go`
- `TestAgentRun_TableName_returnsAgentRun`（如已有跳过）
- `TestAgentRun_CompactColumnsRoundTrip` — in-memory SQLite via newTestDB(t)；写 AgentRun row 含 compact_state JSON + compact_summary string → 读出 → 字段一致
- `TestAgentRun_CompactStateNullable` — 不设 CompactState → 写读 round-trip OK（NULL 默认）
- `TestAgentRun_CompactSummaryNullable` — 不设 CompactSummary → 写读 round-trip OK（空 string）

**audit 声明**（不写 bool 单测）：本 feature 加字段无 bool — 风险声明在 commit message 中。

**验收**：
- model 加 2 字段 + GORM tag 准确
- 3 个单测 PASS
- `go test -race ./internal/pkg/model/...` PASS
- `task lint` PASS

**commit**：`feat(agent-compact): M2 AgentRun model add compact_state + compact_summary fields`

---

### M3 — biz/compact 基础（types + threshold + prompt）

**目标**：建 biz/compact 包基础类型 + 阈值配置 + prompt 模板。**3 文件 Tier 3 并行可行**（文件互不相交，无 import 互相依赖）。

**文件**：
- `internal/numind/biz/compact/types.go`（Message, CompactRequest, CompactResult, RestoredSession, CompactStateV1）
- `internal/numind/biz/compact/threshold.go`（Config, DefaultConfig）
- `internal/numind/biz/compact/prompt.go`（NoToolsPreamble, BaseCompactPrompt, FullCompactSystemPrompt）
- 配套测试：`types_test.go` + `threshold_test.go` + `prompt_test.go`

**内容**：S2 §4.1 / §4.2 / §4.3 已给完整代码，直接抄。

**单测**：
- `TestCompactStateV1_JSONRoundTrip` — types_test.go
- `TestCompactStateV1_PartialFieldsZeroValue` — types_test.go (S0 P2 DoD)
- `TestMessage_OmitemptyFields` — types_test.go (HasFileRef / IsCompactMark omitempty)
- `TestDefaultConfig_QwenPlus` — threshold_test.go
- `TestFullCompactSystemPrompt_ContainsBothSections` — prompt_test.go
- `TestBaseCompactPrompt_Has9Sections` — prompt_test.go（grep 9 段标识）
- `TestNoToolsPreamble_Substantive` — prompt_test.go（长度 > 50 字符）

**验收**：
- 3 个 .go 文件 + 3 个 _test.go 文件
- 7+ 单测 PASS
- `go test -race ./internal/numind/biz/compact/...` PASS
- coverage：M3 文件局部目标 ≈ 90%（仅供 implementer 参考，不作 gate）

**commit**：`feat(agent-compact): M3 biz/compact types + threshold + prompt`

---

### M4 — biz/compact 算法（provider + ptl_chain + max_output_chain）

**目标**：核心算法实现 — token 估算、PTL 链、max_output 链。**3 文件 Tier 3 并行可行**（文件互不相交；都 import 同包 types/threshold/prompt）。

**文件**：
- `internal/numind/biz/compact/provider.go`（CompactProvider interface + MockCompactProvider + EstimateTokens + joinMessages）
- `internal/numind/biz/compact/ptl_chain.go`（CollapseDrain + ReactiveCompact + headDropRetry）
- `internal/numind/biz/compact/max_output_chain.go`（DefaultMaxTokens + EscalatedMaxTokens + EscalateMaxTokens）
- 配套测试：`provider_test.go` + `ptl_chain_test.go` + `max_output_chain_test.go`

**内容**：S2 §4.4 / §4.5 / §4.6 已给完整代码（S2 reviewer P1 fix 已应用）。

**单测覆盖（S2 §6 测试矩阵）**：

`provider_test.go`：
- `TestEstimateTokens_ASCII`（"hello" = 5 × 0.25 = 1）
- `TestEstimateTokens_ChineseMix`（"hello 你好" = 5*0.25 + 2*1.5 = 4.25 → 4）
- `TestEstimateTokens_JapaneseKana`（カタカナ → 1.5 × 4）— **S2 P1 CJK 扩展区**
- `TestEstimateTokens_KoreanHangul`（한글 → 1.5 × 2）
- `TestEstimateTokens_CJKExtension`（𠀀 = CJK Ext-B 0x20000 → 1.5）
- `TestEstimateTokens_EmptyString`
- `TestMockCompactProvider_HappyPath`
- `TestMockCompactProvider_FailureSequence` — FailureSequence[0]=err → 第 1 次返回 err

`ptl_chain_test.go`：
- `TestCollapseDrain_StripsToolResults` — 5 turn → 第 1 turn tool_result drop
- `TestCollapseDrain_KeepsTextBlocks` — user/assistant 文本不动
- `TestCollapseDrain_RespectsCompactSummary` — IsCompactMark=true 不动
- `TestCollapseDrain_RespectsFileRef` — HasFileRef=true 不动
- `TestCollapseDrain_RespectsRecentTurns` — 最近 keepTurns 全保留
- `TestCollapseDrain_KeepTurnsBoundary` — keepTurns <= 0 / >= len 边界
- `TestHeadDropRetry_DropsByGroup` — 12 turn 25% → drop 3 turn
- `TestHeadDropRetry_KeepsRecentTen` — 8 turn 时 return 原 slice
- `TestHeadDropRetry_StopsAtProtectedTurn` — 中间 turn IsCompactMark=true → 该 turn 起停止推进
- `TestHeadDropRetry_StopsAtFileRef` — 中间 turn HasFileRef=true → 停止推进
- `TestReactiveCompact_HappyPath` — mock 返回 summary + finalMessages == 原 messages
- `TestReactiveCompact_InnerRetryThenSuccess` — FailureSequence=[err, nil] → 第 2 次成功 → finalMessages 为 truncated
- `TestReactiveCompact_ExhaustsInnerRetries` — FailureSequence=[err, err, err, err] → 第 4 次仍 err → return err
- `TestReactiveCompact_NilProvider` — return err

`max_output_chain_test.go`：
- `TestEscalateMaxTokens_FromDefault` — 8192 → 65536
- `TestEscalateMaxTokens_AlreadyMax` — 65536 → 65536
- `TestEscalateMaxTokens_AboveMax` — 100000 → 65536

**验收**：
- 3 .go + 3 _test.go
- 25+ 单测 PASS
- `go test -race` PASS
- coverage：M4 文件局部目标 ≈ 85%（仅供 implementer 参考，不作 gate）

**唯一 gate 覆盖率目标**：biz/compact 整包 ≥ 80%（S5 验收时统计；S3 reviewer P2 fix — 三处数字统一）

**commit**：`feat(agent-compact): M4 biz/compact provider + ptl_chain + max_output_chain`

---

### M5 — biz/compact 恢复（restore + attachments）

**目标**：会话恢复 helper + AttachmentReinjector interface。**2 文件 Tier 3 并行可行**。

**文件**：
- `internal/numind/biz/compact/restore.go`（Restore + cleanseMessages + RestorationNarration 常量）
- `internal/numind/biz/compact/attachments.go`（AttachmentReinjector interface + NullAttachmentReinjector）
- 配套测试：`restore_test.go` + `attachments_test.go`

**内容**：S2 §4.7 / §4.8 已给完整代码（S2 reviewer P2 cleanse 限制文档化已应用）。

**单测**：

`restore_test.go`：
- `TestCleanseMessages_DropsDanglingToolUse` — assistant tool_use 全悬空 + content="" → drop
- `TestCleanseMessages_KeepsContentWithDanglingTool` — 有 content 即使 tool_use 悬空也保留
- `TestCleanseMessages_DropsEmptyAssistant` — content="" + no tool_calls → drop
- `TestCleanseMessages_DropsThinking` — role="thinking" → drop
- `TestCleanseMessages_KeepsValidToolPair` — tool_use 配对 tool_result → 都保留
- `TestRestore_NilReinjectorReturnsErr` — reinjector=nil → err
- `TestRestore_InjectsNarration` — RestorationNarration 在 SystemNarration
- `TestRestore_FirstTurnNoTools` — 标志 true
- `TestRestore_NoCompactSummary` — Messages 清洗后返回，summary 不前置
- `TestRestore_WithCompactSummary` — compact_summary 作首条 system message + IsCompactMark=true
- `TestRestore_NullReinjectorPassthrough` — systemPrompt 等于 RestorationNarration（不改动）
- `TestRestore_EmptyMessages` — run.Messages = nil → empty cleansed
- `TestRestore_MalformedMessagesJSON` — 解析失败 → err

`attachments_test.go`：
- `TestNullAttachmentReinjector_ReturnsSystemPromptUnchanged`

**验收**：
- 2 .go + 2 _test.go
- 14+ 单测 PASS
- coverage：M5 文件局部目标 ≈ 85%（仅供 implementer 参考，不作 gate）

**commit**：`feat(agent-compact): M5 biz/compact restore + attachments`

---

### M6 — runner.go 集成

**目标**：runner.go 加 RunnerOption + struct 字段 + 3 helper + 配套单测。**不动 #2 mock Run() 主流程**。

**文件**：
- `internal/numind/biz/agent/runner.go`（改）
- `internal/numind/biz/agent/runner_compact_test.go`（新）

**改动点**（S2 §5）：
- imports：加 `"numind-server/internal/numind/biz/compact"`
- agentRunner struct：加 `compactProvider compact.CompactProvider` + `compactConfig compact.Config`
- NewAgentRunner：加默认 `compactConfig: compact.DefaultConfig()`
- 2 个 RunnerOption：WithCompactProvider / WithCompactConfig
- 3 个 helper：tryPreLLMCompact / handlePTLError / handleMaxOutputError
- Run() 主流程**不调** helper（#14 真实集成时 wire），不动

**单测**（runner_compact_test.go）：
- `TestRunner_NewAgentRunner_DefaultCompactConfig` — 不传 WithCompactConfig → DefaultConfig 生效
- `TestRunner_WithCompactProvider_NilByDefault` — 不传 option → compactProvider nil
- `TestRunner_TryPreLLMCompact_Skip` — tokens < threshold → 原 messages, didCompact=false
- `TestRunner_TryPreLLMCompact_Trigger` — tokens > threshold + mock → summary + collapsed
- `TestRunner_TryPreLLMCompact_NilProvider` — nil compactProvider → no-op
- `TestRunner_HandlePTLError_Step1Collapse` — PTLRetries=0 → state.Transition → ContinueCollapseDrainRetry + collapsed messages
- `TestRunner_HandlePTLError_Step2Reactive` — PTLRetries=1 → ContinueReactiveCompactRetry + summary + collapsed
- `TestRunner_HandlePTLError_NilProviderInStep2` — Step 2 nil provider → terminal + err
- `TestRunner_HandlePTLError_Terminal` — PTLRetries=2 → MaxPTLRetries 用尽 → TerminalPromptTooLong + isTerminal=true
- `TestRunner_HandlePTLError_NoDoubleCounting` — 调 helper 一次 → st.PTLRetries==1（不是 2）
- `TestRunner_HandleMaxOutputError_Escalate` — MaxOutputRetries=0 → ContinueMaxOutputEscalate + 65536
- `TestRunner_HandleMaxOutputError_Recovery` — MaxOutputRetries=1 → ContinueMaxOutputRecovery + currentMaxTokens 保持
- `TestRunner_HandleMaxOutputError_Terminal` — MaxOutputRetries=2 → TerminalErrorMaxBudget + isTerminal=true
- `TestRunner_HandleMaxOutputError_NoDoubleCounting` — 调 helper 一次 → st.MaxOutputRetries==1
- `TestRunner_HelpersRaceSafe` — `go test -race` 跑 3 helper 并发调用（各 100 次）；**每个 goroutine 用独立 LoopState 实例（模拟独立 run），不共享**（S3 reviewer P2 fix — handlePTLError / handleMaxOutputError 接受 *LoopState 时 caller 必须保证同一 LoopState 不被并发 mutate；race test 必须模拟独立 run 间并发，而非同一 run 内部错误地共享 state）

**race-safety 设计**：
- 3 helper 用 stateless 思路（LoopState 由 caller 持有 + Transition 内部 mutate）
- 不引入新 mutex（compactProvider / compactConfig 在 NewAgentRunner 后 read-only）
- LoopState 并发：由 caller 保证（同一 run 只在主 goroutine 调；不同 run 各自 LoopState 实例）— 单 run 单线程不需要 mutex

**验收**：
- runner.go 改动行数 ≤ 100（仅加项）
- 14+ 单测 PASS
- `go test -race ./internal/numind/biz/agent/...` PASS（含 #2 现有测试不动）
- biz/agent 覆盖率不下降

**commit**：`feat(agent-compact): M6 runner.go integration — RunnerOption + 3 helpers`

---

### M7 — biz.go wire MockCompactProvider

**目标**：biz wire 时默认注入 MockCompactProvider。

**文件**：
- `internal/numind/biz/biz.go`（已 grep 确认；唯一正确路径）
- 不写新单测（wire 是集成，由 M6 + M8 系统测试覆盖）

**改动**：S2 §5.5 已给完整代码（含 TODO(#14) 注释）。

**验收**（S3 reviewer P1 fix — 加 agent 包测试 gate）：
- biz.go diff 仅加 1-2 个 option 调用
- `go build ./...` PASS
- `go test ./internal/numind/biz/agent/... PASS`（验证 M6 单测在 M7 wire 后仍 PASS — wire 注入 MockCompactProvider 后 NewAgentRunner 调用链变更，需确认无 nil-provider 假设 break）
- 不破坏既有 wire（#4 sandbox hooks / #5 skill store）

**commit**：`feat(agent-compact): M7 biz.go wire MockCompactProvider with #14 TODO`

---

### M8 — S5 验证策略

**目标**：补 S5 验证策略到 spec 文件（NDF 规则 10 要求 S3 plan 必含此 task）。

**文件**：
- `docs/superpowers/specs/2026-05-21-agent-mode-compact-design.md`（追加 §S5-strategy 段）

**S5 验证策略**：

**选择**：**仅后端 TDD**（Go 单测 + 集成测试，不走 Playwright / gstack QA）

**理由**：
- #9 范围内**零前端 UI 改动**（#11 学员端会话恢复 UI 独立 feature）
- 零新 HTTP 端点（#10 / #11 落地）
- 所有产出是 Go 库（biz/compact 子包）+ runner helper + DB schema
- 测试矩阵 45+ 单测已覆盖 happy path + 边界 + race；CompactState DB roundtrip 已覆盖
- gstack `/qa` 一次性验证，无持久化测试代码 → 对 Go 库无价值（commit 即留回归保护）
- Playwright E2E 需 HTTP 端点 → #9 无端点 → E2E 也无意义

**关键验收路径（S5 时一一打 prompt 验证）**：

1. `go test -race ./internal/numind/biz/compact/...` PASS（biz/compact 7 文件 + 7 测试文件）
2. `go test -race ./internal/numind/biz/agent/...` PASS（含 #2 现有 + #9 新增 runner_compact_test.go）
3. `go test -race ./internal/pkg/model/...` PASS（agent_run_test.go DB roundtrip）
4. `go vet ./...` exit 0
5. `task lint` PASS（golangci-lint）
6. 覆盖率：biz/compact ≥ 80%（runs `go test -cover ./internal/numind/biz/compact/...`）
7. biz/agent 覆盖率不下降（对照 develop 基线）
8. `go build ./...` PASS（M7 wire 不引入编译错误）
9. AutoMigrate 自检：跑 dev 环境 helper.go AutoMigrate（手工 SSH dev DB）→ agent_run 表新 2 列就位（这步留 ndf-done 后由用户 `/deploy-dev server` 触发）

**回归保护诚实声明**：
- biz/compact 单测是持久化测试代码 → **永久回归保护**
- runner_compact_test.go 同上
- AutoMigrate dev 自检不持久化测试代码 — **属于一次性验证**（agent_run schema 改动在 #14 真实 ReAct loop 接入时若有破坏将由 #14 单测捕获）

**不在 S5**（移交 #11 / #14 后续）：
- 真实 LLM compact 端到端（#14）
- 学员端 UI 恢复流程（#11）
- 跨设备 session sync（v2）

**commit**：`docs(agent-compact): M8 S5 verification strategy — TDD-only rationale`

---

## §3 文件归属表（Tier 3 并行验证）

S4 编码时多 implementer 并行 dispatch 前必须用 `ndf-check-disjoint.sh` 验证（NDF 规则 12）。

**Wave 1**（M1 串行，无依赖）：
- Agent A 拥有：`migrations/20260521_010000_alter_agent_run_add_compact_columns.sql`, `migrations/20260521_010000_alter_agent_run_add_compact_columns_rollback.sql`

**Wave 2**（M2 串行，依赖 M1）：
- Agent A 拥有：`internal/pkg/model/agent_run.go`, `internal/pkg/model/agent_run_test.go`

**Wave 3 — Tier 3 并行（M3 三文件互不相交）**：
- Agent A: `internal/numind/biz/compact/types.go`, `internal/numind/biz/compact/types_test.go`
- Agent B: `internal/numind/biz/compact/threshold.go`, `internal/numind/biz/compact/threshold_test.go`
- Agent C: `internal/numind/biz/compact/prompt.go`, `internal/numind/biz/compact/prompt_test.go`
- `ndf-check-disjoint.sh "internal/numind/biz/compact/types.go,internal/numind/biz/compact/types_test.go" "internal/numind/biz/compact/threshold.go,internal/numind/biz/compact/threshold_test.go" "internal/numind/biz/compact/prompt.go,internal/numind/biz/compact/prompt_test.go"` → exit 0

**Wave 4 — Tier 3 并行（M4 三文件互不相交）**：
- Agent A: `internal/numind/biz/compact/provider.go`, `internal/numind/biz/compact/provider_test.go`
- Agent B: `internal/numind/biz/compact/ptl_chain.go`, `internal/numind/biz/compact/ptl_chain_test.go`
- Agent C: `internal/numind/biz/compact/max_output_chain.go`, `internal/numind/biz/compact/max_output_chain_test.go`
- `ndf-check-disjoint.sh "internal/numind/biz/compact/provider.go,internal/numind/biz/compact/provider_test.go" "internal/numind/biz/compact/ptl_chain.go,internal/numind/biz/compact/ptl_chain_test.go" "internal/numind/biz/compact/max_output_chain.go,internal/numind/biz/compact/max_output_chain_test.go"` → exit 0（S3 reviewer P2 fix — 补完整参数）

**Wave 5 — Tier 3 并行（M5 两文件互不相交）**：
- 完整参数：`ndf-check-disjoint.sh "internal/numind/biz/compact/restore.go,internal/numind/biz/compact/restore_test.go" "internal/numind/biz/compact/attachments.go,internal/numind/biz/compact/attachments_test.go"` → exit 0

**Wave 5 — Tier 3 并行（M5 两文件互不相交）**：
- Agent A: `internal/numind/biz/compact/restore.go`, `internal/numind/biz/compact/restore_test.go`
- Agent B: `internal/numind/biz/compact/attachments.go`, `internal/numind/biz/compact/attachments_test.go`
（完整参数已在 Wave 4 区域补充）

**Wave 6**（M6 串行，依赖 M3+M4+M5）：
- Agent A: `internal/numind/biz/agent/runner.go`, `internal/numind/biz/agent/runner_compact_test.go`

**Wave 7**（M7 串行，依赖 M6）：
- Agent A: `internal/numind/biz/biz.go`（grep 确认唯一路径；S3 reviewer P1 fix）

**Wave 8**（M8 串行，文档）：
- 主 session 自己写 spec 段落，不 dispatch agent

## §4 Reviewer 双轮 (规则 6)

每个 task 完成后：
1. 主 session dispatch 2 个 reviewer subagent **并行**：
   - Spec Compliance Review (Sonnet)
   - Code Quality Review (Sonnet)
2. reviewer 输出结构化 `<severity>: <file>:<line> — <rule-id> — <problem> — fix: <suggestion>`
3. P0 必修 → 重新 review；P1 必修；P2 顺手修
4. 通过后 manifest `progress.reviewed_tasks += 1`

## §5 manifest progress 字段

`.ndf/manifest.yaml` `agent-mode-compact` entry 在 S4 启动前更新：

```yaml
progress:
  total_tasks: 8
  completed_tasks: 0
  reviewed_tasks: 0
  current_task: 'M1 — DB migration'
```

每个 task 完成后递增 completed_tasks / reviewed_tasks。

## §6 S6 ndf-done 前置

S5 全通过后：
- `progress.reviewed_tasks == progress.completed_tasks == 8`
- spec 已加 §S5-strategy
- S5 acceptance 文档：`numind-server/docs/superpowers/qa/2026-05-21-agent-mode-compact-s5-acceptance.md`（M8 之后 + 跑 `task lint` 后写）
- `git log feature/agent-mode-compact` 看每个 task 的 commit

S6 跑 ndf-done（NDF 工具自动 merge + 删 worktree + 清 state）。如果 ndf-done 失败，手动 merge（README 流程，本 plan §6 不重复）。

---

**S3 plan 完结。S4 编码按 Wave 1-8 顺序，每 Wave 严格 Tier 3 check 后并行。**
