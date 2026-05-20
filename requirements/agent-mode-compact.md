# NDF S0 Requirement Card · `agent-mode-compact`

**Track**：Standard
**Feature ID**：`agent-mode-compact`（14-feature 分解 #9/14）
**起草日期**：2026-05-21
**起草人**：AI（autopilot）
**状态**：S0 草案
**依赖**：#2 `agent-mode-runtime-skeleton`（merged `45770bb5`）— LoopState / LoopEvent PTL / MaxOutput 状态机已就绪
**阻塞**：#11 `agent-mode-student-ux`（会话恢复 UI）+ #14 `agent-mode-e2e-rollout`（真实 LLM compact 调用）

---

## 1. 起因（Why now）

Agent 模式底座 14-feature 分解的 **#9/14** —— Compact 是 Agent 模式的"对话延续核心"（蓝本 §4.8）。

**核心矛盾**：

- qwen-plus 单次上下文窗口 128k tokens；学员长对话 +工具调用历史 +文件附件，几轮就吃满
- LLM API 返回 `context_length_exceeded` 没有自动恢复 → 整个 Run 终止 → 学员 5 元 / 100 积分白付
- 输出截断（`stop_reason=max_tokens`）：tool_use 块不完整 → 后续 ReAct 步骤失败

**解决方案**（蓝本 §4.8）：

1. **三级压缩**（§4.8.1）：
   - L1 turn-level：单次 LLM 输出（不动，#2 默认行为）
   - L2 conversation-level compact：对话历史压缩（**本 feature 落地**）
   - L3 session archive：跨会话归档（v2 留接口）
2. **PTL 链**（§4.1.6 + §4.8.5）两步恢复 — **两层计数器**：
   - **outer PTL retry**（state.go `PTLRetries`，MaxPTLRetries=2）：state machine 层
     - retry 1 → `LoopEventCollapseDrainRetry`（Step 1：仅剥离最近 4 轮外的 `tool_result`，user/assistant 文本不动）
     - retry 2 → `LoopEventReactiveCompactRetry`（Step 2：调 LLM 全量压缩 → CompactSummary）
     - retry > 2 → `TerminalPromptTooLong`
   - **inner head-drop retry**（ptl_chain.go 内部循环，最多 3 次）：仅 Step 2 reactive_compact 失败后的递归 head-drop（按蓝本 §4.8.5"drop 最早 N 组消息组"），与 outer retry 是不同语义层；3 次仍失败 → 返回 error 给 outer，outer 视为 retry 用尽
   - 不同变量名避免混淆（spec §2 明确）
3. **max_output_tokens 链**（§4.1.6）：8192 → 65536 升级 → 最多 3 次 recovery
4. **会话恢复**（§4.8.6）：学员断网/换页重连 → 读最新 CompactSummary + 最近 N 轮 → 继续对话
5. **压缩后重注入 3 类附件**（§4.8.7）：最近文件 / 调用过的 Skill / MCP delta

**#2 至 #8 完成度（截至 2026-05-21）**：

- #1 V5 ADR 沙箱选型 ✓
- #2 Runtime skeleton（含 LoopState / Transition / PTL+MaxOutput LoopEvent 状态机骨架）✓
- #3 Tool Registry ✓
- #4 Sandbox 集成 ✓
- #5 Skill 系统 ✓
- #6 Permission Pipeline（并行进行中）— 不依赖 #9
- #7 Memory（并行进行中）— **本 feature 会与 #7 在 runner.go SystemPrompt 装配区有 merge 冲突**（#7 加 memory.SystemBlock 段；#9 加 token estimate + PTL 拦截）
- #8 Narration（并行进行中）— 不依赖 #9

但 **Agent 仍然没法"在长对话里活下来"** —— #2 的 LoopState.Transition 写了 PTL/MaxOutput 转移，但没有真正的恢复 action 实现。这就是 #9 解决的问题。

**1:1 约束**：`compact_state` 字段挂在 `agent_run` 表上（与 `messages` 同生命周期）；每个 Run 至多 1 个最新 CompactSummary（历史 summary 体现在 messages 链里）。

---

## 2. 业务范围

> **关键术语翻译**：蓝本 §4.8 用 anthropic / claude 上下文窗口（200k）做例子；Numind 默认 LLM 是 qwen-plus（128k 上下文）。本 feature 阈值配置完全按 qwen-plus 适配（§4.8.4）。
>
> **不引入新 source_type**：CompactProvider 的 LLM 调用走 `aiservice.Chat`，复用现有 `subscription`/`cycle`/`booster` source_type；不动 `credit_transaction.source_type` CHECK constraint。
>
> **Mock CompactProvider for v1**：真实 LLM 压缩留给 #14 ReAct loop 集成；v1 在 biz/compact 提供 mock provider（生成固定占位 summary），保留 interface 让 #14 接真实 aiservice。

> **三级压缩范围 canonical（§4.8.1）**：
>
> | 级别 | 触发点 | 调 LLM？| #9 落地？|
> |------|--------|---------|---------|
> | L1 turn-level | 每次 LLM 输出后 | 否 | 否（#2 默认） |
> | L2 conversation compact | tokens > threshold | 是（mock） | **是** |
> | L3 session archive | 跨 Run 归档 | — | 否（v2 留接口）|

### In scope

1. **DB 层**

   - `agent_run` 表新增 2 个字段（**新 migration 加列**，不动 #2 既有 schema）：
     - `compact_state` JSON NULL — 含 `last_compact_at` / `last_boundary_message_id` / `total_compact_attempts` / `consecutive_failures` / `summary_token_count` / `strategy_used`
     - `compact_summary` TEXT NULL — 当前最新 CompactSummary 全文（用于会话恢复时快速读取，避免遍历 messages 链）
   - 不新建表（compact 不需要独立审计表；mile compact 事件留给 #14 在 langfuse 记）
   - GORM model 字段同步在 `internal/pkg/model/agent_run.go`（加 `CompactState datatypes.JSON` / `CompactSummary string`）
   - AutoMigrate 不动（agent_run 已注册，加列由 `ALTER TABLE` migration SQL 完成 + GORM `AutoMigrate` 同步 schema）
   - migration SQL（双文件 含 `_rollback.sql`）

2. **biz/compact 子包**

   - `threshold.go`：CompactConfig + qwen-plus 默认值（§4.8.4）
     - `ContextWindow=128000`
     - `EffectiveContextWindow=120000`（128k - 8k maxOutput）
     - `AutoCompactThreshold=107000`（120k - 13k buffer）
     - `MaxConsecutiveAutoCompactFailures=3`
     - `MaxCompactOutputTokens=8000`
     - 常量：`ContextWindowSafetyMargin=0.95` / `PTLCollapseKeepTurns=4`（蓝本 §4.1.6）
   - `prompt.go`：BASE_COMPACT_PROMPT 9 节模板（§4.8.3） + NO_TOOLS_PREAMBLE（§4.8.2）
   - `provider.go`：`CompactProvider` interface + `MockCompactProvider`（v1 占位） + token estimation 函数（按字符数粗算：1 token ≈ 1.5 中文字 / 4 英文字）
   - `ptl_chain.go`：
     - `CollapseDrain(messages, keepTurns=4) []Message` — Step 1：仅剥离最近 4 轮外的 `tool_result`（**不动 user/assistant 文本**），保留 compact summary 标记的消息，保留含文件引用的消息（蓝本 §4.1.6 Step 1）
     - `ReactiveCompact(ctx, provider, messages) (*CompactResult, error)` — Step 2：调 LLM 全量压缩 → CompactSummary
     - 内部 `headDropRetry(messages, dropPercent=0.25, maxRetries=3)`：reactive_compact 失败后的递归 head-drop（蓝本 §4.8.5）；与 outer state machine PTLRetries 独立，用 `innerDropAttempts` 变量名区分
   - `max_output_chain.go`：`EscalateMaxTokens(currentMax int) int`（8192 → 65536）
   - `restore.go`：`Restore(run *AgentRun) (*RestoredSession, error)` — 读 compact_summary + 最近 N 轮 + 注入 3 道清洗（去悬空 tool_use / 去孤立 thinking / 去空 assistant）+ 注入恢复 narration（§4.8.6 step 3）
   - `attachments.go`：占位接口 `AttachmentReinjector`，v1 实现返回空（真实文件/Skill/MCP 注入由 #11/#14 落地）

3. **Runner 集成**

   - `RunnerOption` 加 `WithCompactProvider(p compact.CompactProvider) RunnerOption`（biz.go wire MockCompactProvider）
   - `RunnerOption` 加 `WithCompactConfig(cfg compact.Config) RunnerOption`（默认 qwen-plus）
   - `runner.Run` **不改 #2 mock 主流程**（#9 仅加 plumbing，真实 ReAct loop 触发 PTL/MaxOutput 留 #14）；新增 helper 函数：
     - `r.tryPreLLMCompact(ctx, messages) (newMessages, didCompact bool)` — 在 LLM 调用前估算 token，超 `AutoCompactThreshold` 则 trigger compact
     - `r.handlePTLError(ctx, st, messages) (event LoopEvent, newMessages []Message)` — 接 state.go Transition 结果调 collapse_drain / reactive_compact
     - `r.handleMaxOutputError(ctx, st, currentMaxTokens) (event LoopEvent, newMaxTokens int)` — 接 state.go Transition 结果调 EscalateMaxTokens
   - 这 3 个 helper 单独写 + 单独单测 + race-safe；#14 ReAct loop 真实接入时只需 wire helper

4. **状态机不动**

   - `state.go` 的 PTL/MaxOutput LoopEvent + Transition 在 #2 已建好，#9 不改 state.go
   - 新增的 helper 函数返回 `LoopEvent` 由 caller（#14）调 `st.Transition()`

5. **重注入 3 类附件占位**（§4.8.7）

   - v1 在 `attachments.go` 提供 interface + nil 实现
   - 注入入口在 `restore.go` Restore 时调 `AttachmentReinjector.Reinject(systemPrompt, runID) string`
   - 真实文件读取 / Skill 注入 / MCP delta 计算由 #11（学员端）/ #14（真实 ReAct）落地

### Out of scope（明确划线）

- **真实 LLM compact 调用** — v1 用 MockCompactProvider；真实 `aiservice.Chat` 调用走 BASE_COMPACT_PROMPT 由 #14 落地
- **真实文件/Skill/MCP 重注入** — v1 仅接口，真实读取由 #11/#14 落地
- **学员端会话恢复 UI** — #11 `agent-mode-student-ux` 落地
- **跨设备 session sync / L3 session archive** — v2 不在 #9 范围
- **管理端 compact 阈值配置 CRUD** — #10 `agent-mode-configurator-ux` 落地
- **trySessionMemoryCompaction**（§4.8.1 廉价首选优化） — v1 直接走 reactive_compact；优化项 backlog
- **熔断器（MaxConsecutiveAutoCompactFailures=3）真实拦截** — v1 配置项就位但不在 #9 主动触发；由 #14 ReAct loop 调用 helper 时实施
- **prod 部署** — develop merge 后停（不打 git tag、不动 prod）
- **真实 LLM 跑通完整 ReAct loop** — 仅 mock + state machine 路径 + helper 函数；完整 ReAct loop 由 #14 落地

---

## 3. 验收条件（Definition of Done）

S6 ndf-done 准入门槛：

### 工件 + 测试

- [ ] `agent_run` 表 ALTER 加列 migration（双文件含 `_rollback.sql`）— compact_state JSON / compact_summary TEXT
- [ ] GORM model `AgentRun` 加 `CompactState datatypes.JSON` + `CompactSummary string` 字段
- [ ] `internal/numind/biz/compact/` 子包：
  - threshold.go（CompactConfig + qwen-plus 默认值）
  - prompt.go（BASE_COMPACT_PROMPT + NO_TOOLS_PREAMBLE 常量）
  - provider.go（CompactProvider interface + MockCompactProvider + token estimation）
  - ptl_chain.go（CollapseDrain + ReactiveCompact）
  - max_output_chain.go（EscalateMaxTokens + recovery counter）
  - restore.go（Restore + 3 道消息清洗 + 恢复 narration 注入）
  - attachments.go（AttachmentReinjector interface + NullAttachmentReinjector）
- [ ] `AgentRunner` 加 `WithCompactProvider` / `WithCompactConfig` options
- [ ] `runner.go` 新增 3 个 helper：tryPreLLMCompact / handlePTLError / handleMaxOutputError（**不改 #2 mock 主流程**）
- [ ] biz.go wire MockCompactProvider（默认 wire-in）
- [ ] **单元测试覆盖：BASE_COMPACT_PROMPT 9 节齐全**（每节有标识符 + NO_TOOLS_PREAMBLE 注入位置正确）
- [ ] **单元测试覆盖：CollapseDrain（仅剥离 tool_result，不动 user/assistant）**
  - (a) keepTurns=4，messages 5 turn → 第 1 turn 的 `tool_result` 块被剥离，但该 turn 的 `user`/`assistant` 文本块保留
  - (b) 已有 compact summary 标记的消息（任何块）不被动
  - (c) 含用户文件引用的消息（任何块）不被动
  - (d) 最近 4 轮的 `tool_result` 全部保留（蓝本 §4.1.6 Step 1）
- [ ] **单元测试覆盖：headDropRetry（reactive_compact 内部递归）**
  - (a) dropPercent=0.25，9 轮消息 → drop 头 2 组（user+assistant 整组）
  - (b) maxRetries=3，3 次后返回 error 给 caller
  - (c) compact summary 标记的消息组永不被 head-drop（蓝本 §4.8.5）
  - (d) 最近 10 轮永不被 head-drop（蓝本 §4.8.5）
- [ ] **单元测试覆盖：ReactiveCompact mock 路径**
  - (a) MockCompactProvider 返回固定占位 summary
  - (b) 写入 agent_run.compact_summary + compact_state 字段
  - (c) consecutive_failures 增减逻辑正确
- [ ] **单元测试覆盖：Restore**
  - (a) 3 道清洗（去悬空 tool_use / 孤立 thinking / 空 assistant）
  - (b) 恢复 narration 自动注入（§4.8.6 step 3）
  - (c) 无 compact_summary 时 fall through 空 string
  - (d) RestoredSession.FirstTurnNoTools=true 标志被设置（§4.8.6 step 5 — 第一轮禁工具，caller 第二轮后置 false；v1 仅在 RestoredSession 结构体提供 bool，真实的 LLM 调用强制由 #11/#14 实施）
- [ ] **单元测试覆盖：EscalateMaxTokens**
  - (a) 8192 → 65536
  - (b) recovery counter ≥ 3 → 不再 escalate
- [ ] **单元测试覆盖：token estimation**（粗算公式正确，与字符数线性）
- [ ] **单元测试覆盖：runner helper 3 个**
  - (a) tryPreLLMCompact 触发条件（tokens > threshold）
  - (b) handlePTLError 触发 LoopEventLLMErrPTL 并返回正确 newMessages
  - (c) handleMaxOutputError 触发 LoopEventLLMErrMaxOutput 并返回正确 newMaxTokens
  - (d) 3 个 helper 在 `go test -race` 下无 data race
- [ ] 集成测试：agent_run 加列 migration → AutoMigrate 后字段就位 → Create/Read 字段 round-trip + **compact_state JSON 部分字段写入后 unmarshal 回 CompactStateV1 struct 幂等（缺失字段 zero value，禁 strict mode）**（P2-3 修复）
- [ ] biz/compact 包覆盖率 ≥80%
- [ ] biz/agent 包覆盖率不下降（保持 80%+）
- [ ] `go test -race ./...` PASS
- [ ] `go vet ./...` exit 0
- [ ] `task lint` PASS

### 安全 + 合规

- [ ] CompactProvider interface 仅声明，真实 LLM 调用走 `aiservice.Chat`（由 #14 落地）— v1 不调外部 API
- [ ] 所有数据库变更走 GORM query builder（不裸 raw SQL）
- [ ] 控制器层零业务逻辑（本 feature 无新 controller）
- [ ] 验证：compact_state 字段 nullable，旧 run 行不破坏
- [ ] 验证：`credit_transaction.source_type` CHECK constraint 零修改
- [ ] 验证：`config_prod.yaml` zero diff

### 与 #7 memory 协同

- [ ] runner.go SystemPrompt 装配区与 #7 memory.SystemBlock 段位**不直接冲突**（#7 改 `req.SystemPrompt = ... + memory.SystemBlock + ...`；#9 加 helper 函数在 Run 主流程外）
- [ ] merge 时若 SystemPrompt 装配区有 conflict，由 #9 session 负责解决（#7 优先 → memory 段位保留，#9 helper 函数照旧加在 Run 主流程外）

### 0 prod 影响

- [ ] `config_prod.yaml` zero diff
- [ ] 不打 git tag
- [ ] 不调 `/deploy-prod`
- [ ] feature 分支不推 GitHub（pre-push hook 拦）

---

## 4. 风险

1. **token estimation 精度不足导致 threshold 误触发** — 风险：1 token ≈ 1.5 中文字 / 4 英文字粗算与真实 tokenizer 偏差大，AutoCompactThreshold=107k 可能提前/滞后触发
   - 缓解：(a) v1 用粗算 + 安全 margin（ContextWindowSafetyMargin=0.95 = 局部 5% buffer）；(b) helper 接口预留 `WithTokenEstimator(fn func(string) int) Option`，#14 接入真实 tiktoken-go 或 aliyun-sdk token API；(c) 单测 fixture 跑 5 个典型对话样本验证粗算误差 ≤ 15%

2. **CompactSummary 持久化在 agent_run 单行 → 单 Run 多次 compact 历史丢失** — 风险：每次 reactive_compact 覆写 compact_summary 字段，无法追溯历史
   - 缓解：(a) v1 接受：messages 链本身保留 compact summary 边界（last_boundary_message_id 字段记录）；(b) 历史 summary 不再用于 LLM 调用（旧的 summary 已被新的 summary 覆盖语义吸收）；(c) 长期归档由 L3 session archive（v2）实现

3. **PTL 链 reactive_compact 失败兜底（§4.8.5 硬截断）** — 风险：连续 3 次 compact 失败后蓝本要求"硬截断至 effectiveContextWindow"，本 feature 未实装硬截断逻辑
   - 缓解：(a) v1 不实装硬截断；reactive_compact 失败 3 次后 state.Transition 自动走 `TerminalPromptTooLong`（state.go MaxPTLRetries=2 限制 — 注：state.go 已限制 2 次，蓝本写 3 次是不同语境）；(b) 文档记录差异在 spec 中

4. **重注入 3 类附件未实装 → compact 后 LLM 丢失上下文** — 风险：v1 只有 interface，真实重注入在 #11/#14 落地，期间 compact 后 LLM 可能"忘记"读过的文件
   - 缓解：(a) v1 接受功能缺口（#9 范围明确）；(b) interface 设计完整，#11/#14 接入只需实现 AttachmentReinjector；(c) restore.go Restore 默认使用 NullAttachmentReinjector，行为可预测

5. **runner.go merge 与 #7 memory 冲突** — 风险：#7 在 SystemPrompt 装配区加 `memory.SystemBlock` 段，#9 加 helper 函数；#7 先 merge 时 #9 需手工 rebase
   - 缓解：(a) #9 helper 函数加在 Run 主流程外（不动 step 4 SystemPrompt 装配区）；(b) 仅 runner.go 文件顶部 import 增加 + struct 字段增加 + 函数追加；(c) S6 merge 时手工解决（预期冲突区域：runner.go imports / agentRunner struct / NewAgentRunner options 列表 / Run() 末尾辅助函数链）

6. **CompactState JSON schema 演进** — 风险：v2 加新字段时旧 run 行 unmarshal 失败
   - 缓解：(a) S2 定义 `CompactStateV1` Go struct 含所有字段 `omitempty` 标签；(b) Restore 对缺失字段返回 graceful default；(c) 禁止 strict mode

7. **MockCompactProvider 与 #14 真实 provider interface 不一致** — 风险：v1 mock 设计的 interface 在 #14 接真实 aiservice 时需大改
   - 缓解：(a) S2 spec 完整定义 CompactProvider interface，含 `Compact(ctx, *CompactRequest) (*CompactResult, error)` 完整签名；(b) MockCompactProvider 用相同 interface 实现，#14 只需替换实现不动签名；(c) interface 设计参考 §4.8.3 prompt 9 节结构 + §4.8.4 阈值配置作为 input/output

8. **agent_run schema migration 与 #6/#7/#8 并行修改冲突** — 风险：3 个并行 session 都可能加列到 agent_run
   - 缓解：(a) #9 加列名 `compact_state` / `compact_summary` 是 §4.8 蓝本唯一字段名；(b) 若并行 session 也加同名列，merge 时 SQL 重复声明会冲突，但 GORM AutoMigrate 检测列存在自动 skip；(c) S6 merge 时检查 `git diff develop migrations/` 列出所有新 migration 文件，确保无重复 ALTER

9. **AgentRun 既有字段 `default:true` bool GORM gotcha**（`.claude/rules/database.md §6` 风险）— 风险：S4 加 GORM model 字段时若误加 `default:true` bool，Create 时学员 false 请求被静默改为 true
   - 缓解：(a) 本 feature 加的 `CompactState datatypes.JSON` 是 JSON 类型，`CompactSummary string` 是 string 类型，**无新 bool 字段**；(b) S2 spec 做一次 AgentRun model audit，确保现有字段无 default:true bool；(c) 若 #14 后续加 bool 字段（如 `compact_enabled bool default:true`），必须按规则 §6 UpdateColumn 两步法

---

## 5. 简单时间线（参考）

S0（本卡） → S1 proposal/PRD → S2 spec → S3 plan → S4 编码（M1-M~10）→ S5 验收 → S6 ndf-done

每阶段独立 Sonnet reviewer，遵循 `feedback_review_each_stage`。

---

## 6. 相关文档

- 蓝本 §4.8 Compact 全章：`docs/agent-mode/architecture-v1.md`
- 蓝本 §4.1.6 Withhold 错误恢复链：`docs/agent-mode/architecture-v1.md`
- #2 验收：`numind-server/docs/superpowers/qa/2026-05-2X-agent-mode-runtime-skeleton-s5-acceptance.md`
- #2 state.go（PTL/MaxOutput LoopEvent 已就绪）：`internal/numind/biz/agent/state.go`
- aiservice 入口（#14 接入点）：`internal/numind/biz/aiservice/`

---

**S0 完结。S1 写 proposal + PRD。**
