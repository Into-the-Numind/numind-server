# NDF S0 Requirement Card · `agent-mode-permission-pipeline`

**Track**：Standard
**Feature ID**：`agent-mode-permission-pipeline`（14-feature 分解 #6/14）
**起草日期**：2026-05-21
**起草人**：AI（autopilot）
**状态**：S0 草案
**依赖**：
- #2 `agent-mode-runtime-skeleton`（merged `45770bb5`）— RunHooks / HookAction / TerminalReason
- #3 `agent-mode-tool-registry`（merged `e0ae5da9`）— FullTool 38 字段（含 IsReadOnly / IsDestructive / InterruptBehavior）
- #4 `agent-mode-sandbox-integration`（merged `8c883533`）— SandboxHookManager / bashvalidator 子包（8 P0 validators）
- #5 `agent-mode-skill-system`（merged `e05498b6`）— agent_definition.tool_flags JSON / HookActionRegistry race-safe / WithSkillStore option pattern

**阻塞**：#11 `agent-mode-student-ux`（异步 ask UI）/ #13 `agent-mode-compliance-3layer`（L1/L2/L3 输出过滤层叠）

---

## 1. 起因（Why now）

Agent 模式 14-feature 分解的 **#6/14** —— Permission Pipeline 是 Agent 模式的"安全核心"（蓝本 §4.4）。

**核心矛盾**：Agent 在沙箱中执行 LLM 决定的工具调用，但 LLM 决策不可信。必须在工具执行前插入可观察、可配置、可回放的权限检查层。

**前 5 个 feature 完成度**：
- #1 V3 8 个 P0 Bash validator ✓（已迁移至 biz/agent/bashvalidator/，#4 wired）
- #2 Runtime skeleton（HookAction + TerminalReason 12 个）✓
- #3 Tool Registry（38 字段含 IsReadOnly/IsDestructive/InterruptBehavior）✓
- #4 Sandbox 集成（SandboxHookManager 走 PreToolCall hook）✓
- #5 Skill 系统（agent_definition.tool_flags + HookActionRegistry race-safe）✓

但 **Agent 仍然没法说"该工具的这次输入不允许执行"**——所有 tool input 都直接进入工具实现。

**1:N 多 Validator 链**：单点策略不够，蓝本 §4.4.3 要求 **passthrough 流水线**：多个 Validator 链式串联，每个可返回 allow/ask/deny/passthrough；所有 passthrough → 默认 allow（白名单兜底）。

---

## 2. 业务范围

> **关键术语对齐**：蓝本 §4.4 使用 `tenant_id` / `Validator`。Numind 实装：tenant_id → `parent_user_id`（B2B2C 父账户，与 #5 一致）；Validator → 接口；validators 实现集中在 `biz/permission/validators/` 子包。
>
> **DecisionReason 11 种 canonical**：以蓝本 §4.4.5 表为权威源。

### In scope

#### 2.1 DB 层（2 张新表 + 双 migration 文件）

**`agent_permission_config` 表（L2 租户管理员规则）**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | BIGINT UNSIGNED PK AI | 主键 |
| parent_user_id | INT UNSIGNED NOT NULL INDEX | 隶属父账户（B2B2C 顶级账户）|
| rule_type | VARCHAR(32) NOT NULL | `tool_blacklist` / `tool_input_regex_deny` / `topic_blacklist` 等 |
| rule_key | VARCHAR(255) NOT NULL | 规则键（如工具名 / 主题词）|
| rule_value | TEXT | 规则值（如正则 / 关键词列表 JSON）|
| action | VARCHAR(16) NOT NULL DEFAULT 'deny' | `deny` / `ask` |
| message | VARCHAR(500) | 触发后展示给学员的友好理由 |
| is_active | TINYINT(1) NOT NULL DEFAULT 1 | 启用开关（GORM `default:true` 坑：要 Save 或 UpdateColumn fixup）|
| created_at / updated_at | DATETIME NOT NULL | |

复合索引：`idx_apc_parent_active (parent_user_id, is_active)`。

**`agent_permission_decision_log` 表（审计日志）**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | BIGINT UNSIGNED PK AI | 主键 |
| agent_run_id | VARCHAR(64) NOT NULL INDEX | 关联 agent_run.id |
| user_id | INT UNSIGNED NOT NULL INDEX | 学员（实际触发的子账户）|
| parent_user_id | INT UNSIGNED NOT NULL INDEX | 父账户（决定哪条 L2 规则适用）|
| agent_definition_id | BIGINT UNSIGNED NOT NULL INDEX | agent_definition.id（#5 表）|
| tool_name | VARCHAR(64) NOT NULL | 工具名 |
| tool_input_digest | CHAR(64) NOT NULL | SHA-256 完整 64 hex 字符（**用途单一：对账匹配** — 同 input 在不同 run 间可精确比对，便于规则触发回归测试；防 PII 是副作用因为原文未入库）|
| behavior | VARCHAR(16) NOT NULL | `allow` / `ask` / `deny` |
| decision_reason | VARCHAR(32) NOT NULL | 11 种 canonical 之一 |
| validator_id | VARCHAR(64) NOT NULL | 触发决策的 validator 标识（如 `PlatformHardRule:BashControlChar`）|
| message | TEXT | 给学员的展示文案（仅 ask/deny 有）|
| latency_ms | INT NOT NULL DEFAULT 0 | 决策耗时（用于慢规则诊断）|
| created_at | DATETIME NOT NULL | |

复合索引：`idx_apdl_run_tool (agent_run_id, tool_name)` + `idx_apdl_parent_created (parent_user_id, created_at)`。

#### 2.2 biz/permission 子包（新建）

```
internal/numind/biz/permission/
├── result.go              # PermissionResult struct + 11 DecisionReason consts
├── validator.go           # Validator interface + Result helper builders
├── pipeline.go            # PermissionPipeline + Check 主入口
├── gate.go                # PermissionGate top-level (持 store + pipeline + audit logger)
├── audit.go               # 异步写 agent_permission_decision_log（不阻塞 hook 返回）
├── digest.go              # tool_input_digest 计算（SHA-256 完整 64 hex 用于对账匹配）
├── classifier_stub.go     # PendingClassifierCheck v1 placeholder（注释式占位）
└── validators/
    ├── platform_hard_rule.go   # L1 — 包 bashvalidator + 蓝本 §4.4.1 平台硬规则
    ├── sandbox_override.go     # L1 — 沙箱内只读自动放行（v1 passthrough，#13 完善）
    ├── tenant_admin_rule.go    # L2 — 读 agent_permission_config 表
    ├── tool_flag.go            # L2 — 检查 agent_definition.tool_flags[toolName]
    ├── working_dir.go          # L2 — 路径白名单（默认 /workdir/ 前缀）
    ├── user_session_rule.go    # L3 — 高危工具 deny + 友好理由（v1 不做 ask UI）
    └── classifier_placeholder.go # L3 — placeholder，v1 永远 passthrough（#14 完善）
```

**Validator 接口（蓝本 §4.4.3）**：

```go
type Validator interface {
    ID() string  // 用于审计日志 validator_id 字段
    Validate(ctx context.Context, req PermissionRequest) PermissionResult
}

type PermissionRequest struct {
    AgentRunID        string
    UserID            uint
    ParentUserID      uint
    AgentDefinitionID uint64
    Tool              agent.FullTool  // #3 接口
    InputJSON         string          // 工具调用的原始 JSON 输入
    SandboxID         string          // 如果在沙箱中执行，#4 sandbox_session.id；否则空字符串
}

type PermissionResult struct {
    Behavior         string                  // "allow" | "ask" | "deny" | "passthrough"
    DecisionReason   DecisionReasonType      // 11 种 canonical
    ValidatorID      string                  // 触发的 validator
    Message          string                  // ask/deny 文案；passthrough 时弃权原因（仅日志）
    UpdatedInput     map[string]any          // 允许 validator 清洗输入（v1 暂不使用，预留字段）
    Pending          *PendingClassifierCheck // 仅 ask 可设置（v1 stub，永远 nil）
}
```

#### 2.3 11 DecisionReason canonical（与蓝本 §4.4.5 一致）

```go
type DecisionReasonType string

const (
    DecisionReasonRule                = "rule"
    DecisionReasonMode                = "mode"
    DecisionReasonSubcommandResults   = "subcommandResults"
    DecisionReasonPermissionPromptTool = "permissionPromptTool"
    DecisionReasonHook                = "hook"
    DecisionReasonAsyncAgent          = "asyncAgent"
    DecisionReasonSandboxOverride     = "sandboxOverride"
    DecisionReasonClassifier          = "classifier"
    DecisionReasonWorkingDir          = "workingDir"
    DecisionReasonSafetyCheck         = "safetyCheck"
    DecisionReasonOther               = "other"
)
```

#### 2.4 PermissionGate 顶层入口

```go
type PermissionGate struct {
    pipeline *PermissionPipeline
    audit    AuditLogger
    store    IPermissionStore
}

func (g *PermissionGate) Check(ctx context.Context, req PermissionRequest) PermissionResult
```

调用约定：
- `Check` 返回 `PermissionResult`（含 Behavior allow / ask / deny；不会返回 passthrough，已被 pipeline 内化）
- 异步审计：每次 Check 末尾 `go audit.Log(...)`（**不阻塞** hook 返回；race-safe channel buffer）

#### 2.5 Runner / Hook 集成（核心改造点）

**A. AgentRunner 新选项**：

```go
func WithPermissionGate(g *permission.PermissionGate) RunnerOption
```

biz.go wire 顺序：`SandboxHookManager.AsRunHooks()` + `WithPermissionGate(pg)`；两者**互不冲突**——permission gate 是另一个独立的 RunHooks 包装层（蓝本 §4.4.3 multiple validators serial）。

**B. RunHooks chain 合并设计**：

当前 #4 设计：`RunRequest.Hooks *RunHooks` 是单实例。`AsRunHooks()` 返回的实例已带 PreToolCall / PostToolCall 函数指针，**permission 不能简单地再赋值一个 RunHooks** 否则会覆盖 sandbox。

**方案**（S2 详细技术决策；P0 reviewer fix 修正顺序为 permission → sandbox）：
- (a) 新增 `permission.HooksWrapper`：接受一个 base *RunHooks（sandbox 的），构造一个新的 *RunHooks
- (b) 顺序：**permission（拦截）→ sandbox（容器生命周期）**。理由：sandbox PreToolCall 启动容器是昂贵副作用（kvm/Docker container provision）；permission 必须在 tool 进入沙箱**前**判定，否则每次 deny 白启一个容器再销毁。沙箱本身仅是另一层 validator，无"必须先跑的基础设施"语义（蓝本 §4.4.3 注册顺序中沙箱 validator 不含容器生命周期，是 SandboxHooks 才有生命周期；本 wrapper 是关于 hook 顺序，不是 validator 顺序）。
- (c) 在 PreToolCall 内：先调 `permission.Check(ctx, req)`；deny → 直接返回 `(HookActionPermissionDeny, nil)`，**不调 base.PreToolCall**，sandbox 容器**不启动**；allow → 再调 `base.PreToolCall(ctx, t, input)` 透传 sandbox 启动；如果 permission 返回 UpdatedInput（v1 暂不使用，预留），则替换 input 后再调 base
- (d) PostToolCall 透传 base.PostToolCall（容器已启动则需清理；permission 不在 PostToolCall 做新决策）

**UpdatedInput 透传**：即使 v1 所有 validator 均返回 `UpdatedInput=nil`，wrapper 也要有完整的"如果 result.UpdatedInput 非 nil 则 marshal 回 input 字符串后传给 base"的代码框架（P1 reviewer fix），保证后续 #13 SandboxOverride 落地时无需改 wrapper。

**C. HookAction 新枚举值**：

```go
const (
    HookActionContinue       HookAction = iota // 0
    HookActionStop                              // 1
    HookActionBlockingStop                      // 2
    HookActionPermissionDeny                    // 3  — NEW
)
```

**D. TerminalReason 新值**：

```go
const TerminalPermissionDenied TerminalReason = "permission_denied"  // 第 13 个
```

**E. LoopEvent 新值**：

```go
const LoopEventPermissionDenied LoopEvent = ... // 12  — NEW
```

**F. state.go 改动**：

- `HookActionToLoopEvent(HookActionPermissionDeny) → LoopEventPermissionDenied`
- `Transition(LoopEventPermissionDenied)` → `TerminalPermissionDenied`
- 验证集合：13 个 TerminalReason 数组 + 13 LoopEvent

**G. RunResult 字段**：

```go
type RunResult struct {
    RunID          string
    TerminalReason TerminalReason
    // M-X: NEW field for #6 permission denial detail (nil 当 TerminalReason != TerminalPermissionDenied)
    PermissionDenial *PermissionDenialDetail
}

type PermissionDenialDetail struct {
    ToolName     string
    Behavior     string  // "deny" | "ask"
    DecisionReason DecisionReasonType
    ValidatorID  string
    Message      string  // 友好文案
}
```

#### 2.6 流水线注册顺序（v1，蓝本 §4.4.3）

1. `PlatformHardRuleValidator`（L1，硬规则；包 #1 V3 bashvalidator 8 个 P0；仅对 `bash_exec` 工具生效，其他工具直接 passthrough）
2. `SandboxOverrideValidator`（L1，沙箱内放行只读；v1 永远 passthrough，stub）
3. `TenantAdminRuleValidator`（L2，DB 规则；新表 agent_permission_config）
4. `WorkingDirValidator`（L2，文件路径白名单；DecisionReason=workingDir）
5. `ToolFlagValidator`（L2，agent_definition.tool_flags 检查）
6. `UserSessionRuleValidator`（L3，IsDestructive 高危 → deny 友好理由；**v1 内化"safetyCheck reason"语义**——蓝本 §4.4.3 第 5 位 `SafetyCheckValidator` 推迟到 #13 拆出独立 validator，本 feature DecisionReason 表保留 `safetyCheck` 枚举但 v1 不主动 emit；文档化于此 validator 注释）
7. `ClassifierValidator`（L3，placeholder；v1 永远 passthrough）

所有 passthrough → 默认 allow + `DecisionReasonOther`（白名单兜底，蓝本默认）。

> **蓝本与 v1 差异说明**：蓝本 §4.4.3 第 5 位是 `SafetyCheckValidator`（内容安全），本 v1 推迟到 #13 `agent-mode-compliance-3layer`；ToolFlagValidator 是 v1 增补，位次置于 L2 第 5 位（蓝本未列），合理因为 ToolFlag 来自 #5 agent_definition 表，是 v1 才有的能力。

#### 2.7 单测覆盖目标

- **biz/permission 覆盖率 ≥80%**（plan 硬性要求；validators 子包 ≥80%）
- **平台硬规则**：bashvalidator 已 100% 覆盖（#4 维持），不下降
- **集成测试**：
  - (a) Runner + PermissionGate + mock einoAgent：tool_flag 禁用 → tool 不执行 → terminal_reason = "permission_denied"
  - (b) 高危工具（IsDestructive=true）→ UserSessionRuleValidator deny → terminal_reason = "permission_denied"
  - (c) 多 validator chain：前 N 个 passthrough + 第 N+1 个 deny → 正确决策 + 审计日志条目正确
  - (d) `go test -race ./...` PASS（audit 异步写 race-safe 验证）

### Out of scope（明确划线）

- **管理端 UI**（`agent_permission_config` CRUD 在 #10 `agent-mode-configurator-ux`）
- **学员端 UI**（ask 确认弹窗 / 拒绝友好提示 UI 在 #11 `agent-mode-student-ux`）
- **PendingClassifierCheck 真实 LLM 调用**（#14 — 异步 qwen-turbo classifier；v1 仅占位 nil）
- **SandboxOverrideValidator 真实路径**（要 sandbox_id ctx 传播完整；v1 永远 passthrough，#13 完善）
- **23 个 Bash validator 扩展**（保留 #1 V3 落地的 8 个 P0；扩展到 23 个为 follow-up backlog；v1 把 8 个包进 PlatformHardRule）
- **试聊配额检查**（#12 `agent-mode-billing-integration` — credit 池检查）
- **L3 输出过滤**（#13 `agent-mode-compliance-3layer` — 工具输出脱敏 / 内容安全）
- **prod 部署**

---

## 3. 验收条件（Definition of Done）

S6 ndf-done 准入门槛：

### 工件 + 测试

- [ ] `agent_permission_config` + `agent_permission_decision_log` 表 migration（含 _rollback.sql；2 张表）
- [ ] GORM model `AgentPermissionConfig` + `AgentPermissionDecisionLog` 已定义（**含 `default:true` bool Create test — UpdateColumn fixup pattern**）
- [ ] AutoMigrate 在 `internal/numind/helper.go` 已注册（2 张表）
- [ ] `internal/numind/store/` 加 `IAgentPermissionStore`（list active rules by parent / log decision）
- [ ] `internal/numind/biz/permission/` 子包：result + validator + pipeline + gate + audit + digest + 7 个 validators 实现
- [ ] **HookAction 加 `HookActionPermissionDeny = 3`**（保持 0/1/2 现有语义不动）
- [ ] **TerminalReason 加 `TerminalPermissionDenied = "permission_denied"`**（_ = [13]TerminalReason 验证集合更新）
- [ ] **LoopEvent 加 `LoopEventPermissionDenied`** + state.go Transition 处理
- [ ] **`HookActionToLoopEvent` 加 case**
- [ ] **AgentRunner `WithPermissionGate` option**
- [ ] **RunResult `PermissionDenial *PermissionDenialDetail` 字段**（nil when TerminalReason != TerminalPermissionDenied）
- [ ] **biz/agent.RunHooks chain 合并** — permission HooksWrapper 接受 sandbox base hooks，PreToolCall 顺序：sandbox → permission；deny 立即短路返回 HookActionPermissionDeny
- [ ] **biz.go wire**：PermissionGate 注入 AgentRunner（与 SandboxHookManager 兼容）
- [ ] 单元测试：validators 子包每个 validator ≥3 case（allow/deny/passthrough/边界）
- [ ] 单元测试：PermissionPipeline.Check 多 validator chain 顺序正确（前 passthrough 后 deny → 取后者；任何 allow → 提前终止）
- [ ] 单元测试：PermissionGate 异步审计 race-safe（buffered channel + `go test -race` PASS）
- [ ] 集成测试：Runner + mock einoAgent + permission deny → terminal_reason = "permission_denied" + RunResult.PermissionDenial 非 nil 且字段正确
- [ ] 集成测试：tool_flags 禁用 → 工具未执行 → 审计日志写入
- [ ] 集成测试：高危工具（IsDestructive=true）→ UserSessionRule deny → terminal_reason = "permission_denied"
- [ ] biz/permission 包覆盖率 ≥80%
- [ ] biz/agent 包覆盖率不下降（保持 80%+）
- [ ] biz/agent/bashvalidator 包覆盖率不下降（保持 100%）
- [ ] `go test -race ./...` PASS（含 audit 异步写 race-safe）
- [ ] `go vet ./...` exit 0
- [ ] `task lint` PASS

### 安全 + 合规

- [ ] 所有 DB 操作走 GORM query builder（不裸 raw SQL）
- [ ] 控制器层零业务逻辑（本 feature **不引入新 HTTP 端点**——audit log 查询 / 规则 CRUD 在 #10 落地）
- [ ] 异步审计写入失败 → 仅日志，**不影响 permission decision 返回** —— 不阻塞学员体验
- [ ] `tool_input_digest` 用 SHA-256 完整 64 hex（对账匹配；防 PII 是原文不入库的副作用）
- [ ] 验证：`credit_transaction.source_type` CHECK constraint 零修改（与 #5 一致）
- [ ] 验证：`agent_definition.tool_flags` 字段语义不变（#5 落地，#6 只读不改）
- [ ] 验证：#4 SandboxHookManager 行为不变（permission HooksWrapper 透传）
- [ ] 验证：#5 HookActionRegistry 兼容（permission deny 也通过 Registry Record；runner LastAction 读取 + state 转换）
- [ ] **单元测试：HookActionRegistry.Record(HookActionPermissionDeny) → LastAction() = HookActionPermissionDeny**（atomic 读写正确；不 panic；新值 3 落在 int32 合法区间）
- [ ] **单元测试：HooksWrapper.PreToolCall 当 result.UpdatedInput 非 nil 时正确 marshal 回 input 字符串后传给 base.PreToolCall**（v1 没有 validator 返回非 nil 的 case，但 framework 需可工作；测试用 stub validator 返回固定 UpdatedInput 验证）

### 0 prod 影响

- [ ] `config_prod.yaml` zero diff
- [ ] 不打 git tag
- [ ] 不调 `/deploy-prod`
- [ ] feature 分支不推 GitHub（pre-push hook 拦）

---

## 4. 风险

1. **Hook chain 合并复杂度** —— 风险：permission wrapper 必须把 sandbox 的 PreToolCall + PostToolCall 都透传，否则容器生命周期断开
   - 缓解：S2 用 `permission.WrapHooks(base *RunHooks, gate *PermissionGate) *RunHooks`，base 为 nil 时退化为单 layer permission；test 验证 nil base + non-nil base 两路径

2. **HookActionPermissionDeny 与现有 HookActionRegistry 行为兼容性** —— 风险：#5 M10 落地的 HookActionRegistry 只识别 0/1/2，加 3 后 LastAction() 返回 unknown 值
   - 缓解：HookActionRegistry 用 atomic.Int32，3 是合法范围；`HookActionToLoopEvent(3)` 加 case 返回 `LoopEventPermissionDenied`；新增 case 不影响旧 0/1/2 路径

3. **审计日志慢规则阻塞 hook** —— 风险：synchronous DB INSERT 每 tool call 500-2000ms 会延迟工具执行
   - 缓解：异步 buffered channel + dedicated goroutine drain；channel 满（极端情况）→ 日志 warning 但不阻塞 Check 返回；测试覆盖 buffer 慢消费场景

4. **DecisionReason 11 种 vs 现实 7 个 validator 数量** —— 风险：v1 不会触发所有 11 种 reason
   - 缓解：保留 11 种 canonical 枚举但 v1 实际产生 7 种（rule / sandboxOverride / workingDir / classifier / safetyCheck / mode / other）；测试不要求覆盖全 11 种，只要求每个 v1 validator 至少触发其指定 reason

5. **TenantAdminRule 缓存策略** —— 风险：每次 tool call 都 DB 读 agent_permission_config 表是慢路径
   - 缓解：v1 不实装 cache（每次查表）；plan 标注后续 cache layer 为 backlog；TTL 5 分钟 in-memory cache 是 #14 优化项；v1 测试覆盖每次查表正确性

6. **TerminalReason 增长（12 → 13）影响下游消费者** —— 风险：#11 student-ux / #13 compliance 已经预设 12 个终止 reason 列表
   - 缓解：在蓝本 §4.1.5 文档同步加 13；本 feature 不动除 state.go 外其他文件的 "12" 文案；review 检查 grep "12" 没改错位置

7. **PlatformHardRuleValidator 直接复用 bashvalidator 与 tool 名耦合** —— 风险：bashvalidator 只对 bash_exec 工具有效，但当前 Validator 接口在所有 tool 上调用
   - 缓解：PlatformHardRuleValidator.Validate 内部检查 `tool.Name() == "bash_exec"`；其他工具 passthrough；测试覆盖

8. **prod 部署阻断条件** —— 风险：autopilot 跑到 dev 部署后被用户误触发 prod
   - 缓解：deploy-checklist-feature-6.md 文档明确"勿 prod"；不打 git tag

---

## 5. 简单时间线（参考）

S0（本卡） → S1 proposal/PRD → S2 spec → S3 plan → S4 编码（M1-M~13）→ S5 验收 → S6 ndf-done

每阶段独立 Sonnet reviewer，遵循 `feedback_review_each_stage`。

---

## 6. 相关文档

- 蓝本 §4.4 Permission Pipeline：`docs/agent-mode/architecture-v1.md`
- 蓝本 §4.4.5 DecisionReason 11 canonical：`docs/agent-mode/architecture-v1.md`
- 蓝本 §3 原则 5（判别联合权限结果 + passthrough）：`docs/agent-mode/architecture-v1.md`
- #1 V3 决策 ADR（8 P0 bash validators）：`numind-server/.ndf/decisions/agent-mode-phase0-verification/0001-bash-validators.md`
- #4 验收（SandboxHookManager 模式）：`numind-server/docs/superpowers/qa/2026-05-22-agent-mode-sandbox-integration-s5-acceptance.md`
- #5 验收（HookActionRegistry race-safe + WithSkillStore option）：`numind-server/docs/superpowers/qa/2026-05-22-agent-mode-skill-system-s5-acceptance.md`

---

**S0 完结。S1 写 proposal + PRD。**
