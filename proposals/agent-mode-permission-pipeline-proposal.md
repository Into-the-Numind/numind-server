# NDF S1 Proposal + PRD · `agent-mode-permission-pipeline`

**Track**：Standard
**Feature ID**：`agent-mode-permission-pipeline`（14-feature 分解 #6/14）
**起草日期**：2026-05-21
**状态**：S1 草案
**前置 stage**：S0 通过（commit `48c40244`）

---

## 1. 目标与背景

### 1.1 商业价值

Numind Agent 模式让机构父账户给子账户配置专属 AI 助手。子账户使用 Agent 时，**LLM 决定调用什么工具**——这是不可信的决策来源：
- LLM 可能调用沙箱里的 bash_exec 执行 `rm -rf /workdir`
- LLM 可能调用 web_search 搜索竞品（违反父账户配置的"禁止讨论 X 平台"）
- LLM 可能调用 file_read 读 `/etc/passwd`（沙箱外路径，越权）

**Permission Pipeline 是平台与"AI 决策不确定性"之间的缓冲层**：
- 父账户配置规则（L2）→ DB 表 `agent_permission_config`
- 平台硬规则（L1）→ 代码硬编码（bash 8 P0 / 沙箱越权 / 跨租户）
- 学员实时确认（L3）→ 高危工具触发 ask 弹窗（v1 简化为 deny）

### 1.2 业务目标

- **零故障安全网**：所有工具调用前都经过决策，audit log 100% 可回放
- **可配置租户隔离**：每个 B2B 父账户独立规则集，互不影响（基于 `parent_user_id`）
- **学员体验不阻塞**：审计写入异步，permission decision P99 < 50ms（含 DB 读）
- **可观察可调试**：每次 deny 都有 `validator_id` + `decision_reason` 字段；管理端摩擦报告（#10）

### 1.3 技术目标

- biz/permission 子包覆盖率 ≥ 80%（plan 硬性）
- biz/agent / bashvalidator 覆盖率不下降
- `go test -race ./...` PASS（异步审计写入 race-safe）
- HookAction = 3 / TerminalReason 第 13 个 / LoopEvent 新值 — 与现有 12 个状态机不冲突
- 0 prod 影响

---

## 2. 用户故事（User Stories）

### US-1：学员调用 bash 执行受限命令（L1 硬规则路径）

```
背景：学员 A 让 Agent 帮他清理工作目录里的临时文件
LLM 决定：调用 bash_exec({"command": "rm -rf /workdir/*.tmp\rstuff"})  ← 含 \r 控制字符
Hook chain: permission.Check → 命中 PlatformHardRuleValidator (bashvalidator ControlCharValidator)
                            → Behavior=deny, DecisionReason=rule, ValidatorID=PlatformHardRule:BashControlChar
runner.PreToolCall 返回 HookActionPermissionDeny
state.Transition(LoopEventPermissionDenied) → TerminalPermissionDenied
agent_run.state_reason = "permission_denied"
RunResult.PermissionDenial = {ToolName: "bash_exec", Behavior: "deny", ValidatorID: "PlatformHardRule:BashControlChar", Message: "BashControlChar: ASCII 控制字符 — pattern=..."}
```

容器不启动（permission → sandbox 顺序），bash_exec 不执行，审计日志异步写入。

### US-2：父账户配置 L2 租户规则（禁讨论竞品）

```
背景：张老师不希望 Agent 推荐竞品 "X 平台"
配置（v1 由 admin 直接 INSERT；UI 在 #10）：
  INSERT INTO agent_permission_config (parent_user_id, rule_type, rule_key, rule_value, action, message, is_active)
  VALUES (10, 'tool_input_regex_deny', 'web_search', '.*X 平台.*', 'deny', '该话题暂不支持讨论', 1);

学员调用：LLM 决定 web_search({"query": "X 平台 vs Y 平台 对比"})
TenantAdminRuleValidator.Validate:
  → 读 cache/DB 取 parent_user_id=10 active 规则
  → 匹配 tool_name="web_search" + rule_value 正则匹配 input.query
  → 命中：Behavior=deny, DecisionReason=rule, Message="该话题暂不支持讨论"
PermissionGate.Check 返回该结果，runner 拒绝调用 web_search。
```

### US-3：高危工具（IsDestructive）→ L3 deny 友好理由

```
背景：tool_image_gen IsDestructive=false; tool_bash_exec IsDestructive=true
LLM 决定：调用 bash_exec({"command": "ls /workdir"})
PlatformHardRule passthrough (合法命令)
SandboxOverride passthrough (v1 stub)
TenantAdminRule passthrough (无匹配规则)
WorkingDir passthrough (bash 不查路径)
ToolFlag passthrough (tool_flags 没禁用 bash)
UserSessionRuleValidator.Validate:
  → 仅看 tool.IsDestructive() == true（v1 不查 session authorization 状态 — P2 reviewer fix）
  → Behavior=deny, DecisionReason=mode, Message="该操作可能修改你的数据，需要管理员授权后才能执行"
（v1 直接 deny；session 状态查询/学员 ask 弹窗 → #11 / #14 接续）

→ runner.PreToolCall 返回 HookActionPermissionDeny
→ TerminalPermissionDenied
```

### US-4：所有 validators passthrough → 默认 allow（白名单兜底）

```
背景：学员调用 get_current_date（无害工具）
所有 7 个 validator 全 passthrough
→ pipeline.Check 默认返回 Behavior=allow, DecisionReason=other, ValidatorID="DefaultAllow"
→ runner.PreToolCall 返回 HookActionContinue
→ sandbox 启动（如配置）→ 工具执行
→ PostToolCall 透传 sandbox 清理
→ audit log 写入 {behavior: "allow", reason: "other", validator_id: "DefaultAllow"}
```

### US-5：tool_flags 禁用某工具（来自 #5 Skill 配置）

```
背景：父账户在 #5 questionnaire Q9 设置 web_search=false（"网络搜索允许"=否）
agent_definition.tool_flags = {"web_search": false}
学员调用：LLM 决定 web_search({"query": "..."})
ToolFlagValidator.Validate:
  → 取 req.AgentDefinitionID → store.GetTool flags
  → flags["web_search"] == false
  → Behavior=deny, DecisionReason=rule, ValidatorID=ToolFlag:web_search, Message="该 Agent 暂未启用网络搜索功能"
→ TerminalPermissionDenied
```

---

## 3. 系统设计概览

### 3.0 PermissionResult 完整字段（P2 reviewer fix — 蓝本 §4.4.2 完整对齐）

```go
type PermissionResult struct {
    Behavior       string
    DecisionReason DecisionReasonType
    ValidatorID    string
    Message        string
    UpdatedInput   map[string]any
    Pending        *PendingClassifierCheck // v1 stub
    Suggestions    []PermissionUpdate      // v1 zero-length；#10 摩擦报告消费
}

type PermissionUpdate struct {
    RuleID      uint64
    Suggestion  string
}
```

v1 行为：
- `Suggestions` 字段实际类型存在，validators 永远返回 zero-length slice；#10 落地时 validators 才填充
- `Pending` v1 永远 nil；#14 落地异步 classifier 才填充
- `UpdatedInput` v1 永远 nil；framework 透传完整路径就绪

### 3.1 组件关系图（P1 reviewer fix — Registry.Record 调用点在 wrapper 不在 adapter）

```
┌────────────────────────────────────────────────────────────────┐
│ AgentRunner.Run(ctx, req)                                       │
│                                                                  │
│   adapter_full_to_eino.go (透传)                                 │
│   └──→ hooks.PreToolCall(ctx, tool, input)  ← wrapper 在此挂载   │
│                ↓                                                  │
│                                                                  │
│   permission.HooksWrapper.PreToolCall(ctx, t, in):              │
│     1. result := gate.Check(ctx, buildReq(t, in))               │
│     2. if result.Behavior == "deny" or "ask":                   │
│           Registry.Record(HookActionPermissionDeny)  ← 在此 Record│
│           sink <- buildDetail(result)  (非阻塞 select default)  │
│           return (HookActionPermissionDeny, nil)  → 短路        │
│     3. else:                                                     │
│           input' = applyUpdatedInput(in, result.UpdatedInput)   │
│           return base.PreToolCall(ctx, t, input')  → sandbox 启动│
│                                                                  │
│   PermissionGate.Check(ctx, req):                                │
│     PermissionPipeline.Check (sync, 7 validators serial):        │
│       for v in validators:                                       │
│         result = v.Validate(ctx, req)                            │
│         if result.Behavior != "passthrough":                     │
│           break (early termination)                              │
│       default: Behavior=allow, DecisionReason=other              │
│     audit <- (req, result)  (异步 buffered channel)              │
│     return result                                                │
│                                                                  │
│   runner.Run 末尾:                                                │
│     select { case detail := <-sink: runResult.PermissionDenial   │
│       = detail; default: }                                       │
└────────────────────────────────────────────────────────────────┘
```

### 3.2 7 个 Validator 决策矩阵

| Validator | 适用工具 | 决策来源 | v1 typical Behavior | DecisionReason |
|---|---|---|---|---|
| PlatformHardRule | bash_exec only | bashvalidator 8 P0 | deny / passthrough | rule |
| SandboxOverride | 所有（v1 stub）| - | passthrough | sandboxOverride（v1 不产生）|
| TenantAdminRule | 所有 | DB agent_permission_config | deny / passthrough | rule |
| WorkingDir | file_* tools | input path 白名单 | deny / passthrough | workingDir |
| ToolFlag | 所有 | agent_definition.tool_flags | deny / passthrough | rule |
| UserSessionRule | IsDestructive=true | tool 元数据 | deny | mode |
| Classifier | 所有（v1 stub）| - | passthrough | classifier（v1 不产生）|

---

## 4. API / 接口设计

### 4.1 用户端 HTTP — 本 feature **不引入**

audit log 查询 / 规则 CRUD 在 #10 `agent-mode-configurator-ux` 落地（管理端）。本 feature 仅 biz 层 + DB 表 + runner 集成。

### 4.2 biz/permission 包公开接口

```go
// gate.go
type PermissionGate struct { ... }

type PermissionGateOption func(*PermissionGate)

func NewPermissionGate(opts ...PermissionGateOption) *PermissionGate
func WithStore(s IPermissionStore) PermissionGateOption
func WithSkillStore(s store.IAgentDefinitionStore) PermissionGateOption  // P1 reviewer fix — 用 store 层接口（与 runner.WithSkillStore 一致），不是 biz/skill
func WithValidators(vs ...Validator) PermissionGateOption                // 覆盖默认 7 个，便于测试
func WithAuditChannelSize(n int) PermissionGateOption                    // 默认 1024
func WithAuditLogger(l AuditLogger) PermissionGateOption                 // 默认写 DB；测试时换 mock

// PermissionGate.Check 是主入口，hook 内调
func (g *PermissionGate) Check(ctx context.Context, req PermissionRequest) PermissionResult

// Close 优雅停止 audit goroutine
// 语义（P1 reviewer fix）：
//   - 向 audit channel send sentinel
//   - sync.WaitGroup.Wait() 阻塞至 drainer goroutine 退出（先消费 channel 中残留 entries 再退）
//   - drain 超时 5s（防卡死）；超时则 zap.Warn 并强制返回
//   - Close 后再调 Check 行为：drain 完后 audit 写改为 zap.Warn 同步落日志（不阻塞 Check 返回）
func (g *PermissionGate) Close()
```

### 4.3 biz/agent 包改造

```go
// hooks.go：HookAction 加值
const HookActionPermissionDeny HookAction = 3

// state.go：TerminalReason / LoopEvent 加
const TerminalPermissionDenied TerminalReason = "permission_denied"
const LoopEventPermissionDenied LoopEvent = 12 // 沿用现有 iota
// 验证集合更新到 13 元素

// state.go Transition switch 必须加 case（P1 reviewer fix — 不依赖 default 兜底）：
//   case LoopEventPermissionDenied:
//       state.TerminalReason = TerminalPermissionDenied
//       return LoopActionTerminate

// hooks.go：HookActionToLoopEvent 加 case
case HookActionPermissionDeny: return LoopEventPermissionDenied

// runner.go：RunResult / RunRequest
type RunResult struct {
    RunID            string
    TerminalReason   TerminalReason
    PermissionDenial *PermissionDenialDetail // NEW; nil when not denied
}

type PermissionDenialDetail struct {
    ToolName       string
    Behavior       string
    DecisionReason string
    ValidatorID    string
    Message        string
}

// 新 option
func WithPermissionGate(g *permission.PermissionGate) RunnerOption
```

### 4.4 wire 顺序（biz.go）

**职责拆分**（P0 reviewer fix — 消除 `WithDefaultHooks(wrapped)` 与 `WithPermissionGate(permGate)` 职责重叠）：
- `WithDefaultHooks(wrapped)` → runner 用 wrapped 作 hook chain；wrapped 内部按"permission → sandbox"顺序调用
- `WithPermissionGate(permGate)` → **不**重复持有 gate；仅给 runner 一个"runID-scoped denial detail channel"用于把 wrapper 内的 deny detail 回传给 runner 填 `RunResult.PermissionDenial`
- runner 内部不直接调 permGate.Check（已被 wrapper 包了）；runner 只读 denial channel（每 Run 一个）

**Denial detail 传回机制**（P0 reviewer fix — 明确机制）：
- 在 RunRequest 加 `permissionDenialSink chan<- *PermissionDenialDetail`（unbuffered，size=1；每 Run 独立实例避免 cross-run race）
- wrapper.PreToolCall 拿到 deny result → 构造 PermissionDenialDetail → 非阻塞 send 到 sink（`select { case sink <- d: default: }`）
- runner.Run 末尾从 sink 读取（非阻塞）→ 填 `RunResult.PermissionDenial`

```go
// 现有 #5 顺序
runner := agent.NewRunner(runStore, ...,
    agent.WithDefaultHooks(sandboxHookMgr.AsRunHooks()),
    agent.WithSkillStore(...),
)

// 加 #6 permission gate
permGate := permission.NewPermissionGate(
    permission.WithStore(permStore),
    permission.WithSkillStore(skillStore), // S2 决定：用 store.IAgentDefinitionStore（与 runner 一致）
)

// 新 wrapper 接管 hooks chain（顺序：permission → sandbox）
wrapped := permission.WrapHooks(sandboxHookMgr.AsRunHooks(), permGate)
// wrapped.PreToolCall 内：先 gate.Check；deny → Registry.Record(HookActionPermissionDeny) + send 到 sink + 短路返回，不调 base
// allow → 透传 base.PreToolCall（sandbox 启动容器）

runner := agent.NewRunner(runStore, ...,
    agent.WithDefaultHooks(wrapped),       // hook chain
    agent.WithSkillStore(...),
    agent.WithPermissionGate(permGate),    // 仅用于：每 Run 构造 sink channel 并塞入 wrapper ctx
)
```

> **Registry.Record 调用点**（P1 reviewer fix）：在 `HooksWrapper.PreToolCall` 内调（不在 adapter）。adapter 透传 hook 返回的 error 不感知 permission 逻辑。

---

## 5. 数据库设计

### 5.1 表结构（DDL 草稿，最终在 S2 spec 定型）

```sql
-- agent_permission_config
CREATE TABLE agent_permission_config (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    parent_user_id INT UNSIGNED NOT NULL,
    rule_type VARCHAR(32) NOT NULL,
    rule_key VARCHAR(255) NOT NULL,
    rule_value TEXT,
    action VARCHAR(16) NOT NULL DEFAULT 'deny',
    message VARCHAR(500),
    is_active TINYINT(1) NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    INDEX idx_apc_parent_active (parent_user_id, is_active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- agent_permission_decision_log
CREATE TABLE agent_permission_decision_log (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    agent_run_id VARCHAR(64) NOT NULL,
    user_id INT UNSIGNED NOT NULL,
    parent_user_id INT UNSIGNED NOT NULL,
    agent_definition_id BIGINT UNSIGNED NOT NULL,
    tool_name VARCHAR(64) NOT NULL,
    tool_input_digest CHAR(64) NOT NULL,
    behavior VARCHAR(16) NOT NULL,
    decision_reason VARCHAR(32) NOT NULL,
    validator_id VARCHAR(64) NOT NULL,
    message TEXT,
    latency_ms INT NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_apdl_run_tool (agent_run_id, tool_name),
    INDEX idx_apdl_parent_created (parent_user_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

> **created_at DEFAULT**（P2 reviewer fix）：日志表 `agent_permission_decision_log.created_at` 用 `CURRENT_TIMESTAMP` 兜底（MySQL strict mode 下 Go 未赋值不报错）。config 表 created_at/updated_at 由 GORM auto fields 管理，无需 DB DEFAULT。

### 5.2 Migration 双文件

- `migrations/{YYYYMMDD_HHMMSS}_agent_permission_pipeline.sql` — 含上述两表 CREATE
- `migrations/{YYYYMMDD_HHMMSS}_agent_permission_pipeline_rollback.sql` — `DROP TABLE IF EXISTS ...`

### 5.3 AutoMigrate

在 `internal/numind/helper.go` 找现有 `&model.AgentDefinition{}` 注册块，紧邻处加：

```go
&model.AgentPermissionConfig{},
&model.AgentPermissionDecisionLog{},
```

### 5.4 Index 选择理由

- `idx_apc_parent_active`：TenantAdminRuleValidator 每次 tool call 查 active rules by parent — 高频
- `idx_apdl_run_tool`：按 agent_run_id 看一次 run 的所有 permission 决策（debug）
- `idx_apdl_parent_created`：按父账户 + 日期范围看摩擦报告（#10）

---

## 6. 向后兼容性

- **HookAction 加值 3**：现有 atomic.Int32 字段类型不变；旧 Record(0/1/2) 行为不变；新值 3 仅在加 case 处生效。**回滚安全**：不加 case 时旧路径仍可工作。
- **TerminalReason 12→13**：state.go 验证集合更新；下游 `agent_run.state_reason` 字符串列长度足够（VARCHAR 64+）；#11/#13 消费者在加新文案前，旧 reason 仍可正确展示。
- **RunResult 加 PermissionDenial 字段**：nil 时与旧行为一致；JSON 序列化加 `omitempty` 标签。
- **biz/agent.RunRequest.Hooks 字段类型不变**：仍 `*RunHooks`。WrapHooks 返回的 `*RunHooks` 与旧实例同类型。
- **现有测试不动**：#2-#5 所有 RunHooks 测试 base hooks 假设保留；新 wrapper 仅在 wire 层注入。
- **migration 双文件**：rollback SQL 可恢复 schema。

---

## 7. Rollout 计划

```
Phase  Stage  内容
─────────────────────────────────────────────────────
1      S2     技术 spec（11 reason 详细 / wrapper 算法 / audit 异步细节）
2      S3     task plan 分解（预计 M1-M13）
3      S4     编码（按 disjoint file 分批并行）
4      S5     acceptance：go test -race PASS + 覆盖率达标
5      S6     ndf-done（develop merge）
（dev 部署由用户触发，prod 永不部署 — autopilot 规则）
```

---

## 8. 关键决策点（S0 已锁，S1 再确认）

1. **Hook chain 顺序：permission → sandbox**（S0 决策 sd0-1）— 避免 deny 时白启动容器
2. **tool_input_digest 完整 SHA-256 64 hex**（S0 sd0-2）— 用途单一对账匹配
3. **UpdatedInput v1 透传 framework 就绪**（S0 sd0-3）— #13 SandboxOverride 落地零改 wrapper
4. **SafetyCheckValidator 推迟 #13**（S0 sd0-4）— 保留 DecisionReason 枚举，v1 不主动 emit
5. **23 个 Bash validator 全扩展推迟 backlog**（S0 sd0-5）— v1 沿用 8 P0 包装

---

## 9. 风险与缓解（继承 S0 §4 + 补充）

继承 S0 §4 所有 8 条；S1 新增：

9. **PermissionPipeline 同步 Check P99 延迟**
   - 风险：7 个 validator 依次串行，每个 1-3ms DB 读，总 ~10-20ms 可控
   - 缓解：S2 明确 TenantAdminRule 内部 in-memory cache（TTL 60s）可选；v1 不强制
   - 测试：S5 加端到端延迟测试，目标 P99 < 50ms

10. **audit channel 满**
    - 风险：高并发突发可能 channel buf 满，Log 调用阻塞
    - 缓解：channel 满时 select default 走 zap warn 不阻塞；测试覆盖

11. **PermissionDenialDetail 字段在 RunResult 上**
    - 风险：旧消费者反序列化 RunResult 不识别新字段
    - 缓解：JSON tag `omitempty`；Go struct 字段加新字段不破坏旧消费者（除非用 DisallowUnknownFields）

---

## 10. 相关文档

- S0：`numind-server/requirements/agent-mode-permission-pipeline.md`
- 蓝本 §4.4 + §4.4.5：`docs/agent-mode/architecture-v1.md`
- #5 验收（HookActionRegistry 模式）：`numind-server/docs/superpowers/qa/2026-05-22-agent-mode-skill-system-s5-acceptance.md`
- #4 验收（SandboxHookManager 模式）：`numind-server/docs/superpowers/qa/2026-05-22-agent-mode-sandbox-integration-s5-acceptance.md`

---

**S1 完结。S2 写技术 spec（详尽 API + 文件清单 + audit 算法）。**
