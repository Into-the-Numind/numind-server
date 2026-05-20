# NDF S2 Technical Spec · `agent-mode-permission-pipeline`

**Track**：Standard
**Feature ID**：`agent-mode-permission-pipeline`（14-feature 分解 #6/14）
**起草日期**：2026-05-21
**状态**：S2 草案
**前置 stage**：S1 通过（commit `37690f7c`）

---

## §1 目标与不变量

### 1.1 目标

把 Agent 工具调用前的"判别 + 审计"层落地。范围严格按 S0/S1：
- 2 张新表 + biz/permission 子包（7 validator + gate + pipeline + audit）
- 改 hooks.go / state.go / runner.go 加新枚举值
- 不引入 HTTP 端点

### 1.2 不变量（违反 = review FAIL）

1. **#5 HookActionRegistry race-safe atomic.Int32 兼容**：新值 3 落 int32 合法区间，旧 0/1/2 行为 0 改动
2. **TerminalReason 12 → 13**：验证集合 `_ = [13]TerminalReason{...}` 编译期检查
3. **现有 #4 SandboxHookManager 行为 0 改动**：仅在 wire 层加 wrapper，hook 实现不动
4. **审计写入 race-safe + 不阻塞 Check**：`go test -race` PASS
5. **PermissionGate.Check 在 base.PreToolCall 之前**（permission → sandbox 顺序）
6. **agent_run.id 是 uint64**（不是 VARCHAR）；decision_log.agent_run_id 类型对齐 BIGINT UNSIGNED
7. **0 prod 影响**：config_prod.yaml 0 diff

---

## §2 数据模型

### §2.1 agent_permission_config 表

```sql
CREATE TABLE agent_permission_config (
    id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    parent_user_id INT UNSIGNED NOT NULL COMMENT '隶属父账户（B2B2C 顶级账户）',
    rule_type      VARCHAR(32)  NOT NULL COMMENT 'tool_blacklist / tool_input_regex_deny / topic_blacklist',
    rule_key       VARCHAR(255) NOT NULL COMMENT '规则键（工具名 / 主题词）',
    rule_value     TEXT                  COMMENT '规则值（正则字符串 / 关键词列表 JSON）',
    action         VARCHAR(16)  NOT NULL DEFAULT 'deny' COMMENT 'deny / ask',
    message        VARCHAR(500)          COMMENT '触发后展示给学员的友好理由',
    is_active      TINYINT(1)   NOT NULL DEFAULT 1 COMMENT '启用开关',
    created_at     DATETIME     NOT NULL,
    updated_at     DATETIME     NOT NULL,
    INDEX idx_apc_parent_active (parent_user_id, is_active)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Agent 模式 #6 — L2 租户管理员权限规则配置';
```

**GORM `default:true` 坑**：`is_active` 字段需 `db.Save()` 或 `db.Updates(map)` 或 `UpdateColumn` fixup 才能正确持久化 `false`（详见 `.claude/rules/database.md §6`）。本 feature 不引入 Create 端点，规则由 admin 端 #10 创建。但单元测试中如果用 `db.Create` 构造测试数据必须用 `UpdateColumn` fixup 才能正确测 `is_active=false` 场景。

### §2.2 agent_permission_decision_log 表

```sql
CREATE TABLE agent_permission_decision_log (
    id                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    agent_run_id        BIGINT UNSIGNED NOT NULL COMMENT 'agent_run.id（uint64 对齐）',
    user_id             INT UNSIGNED NOT NULL COMMENT '学员（子账户）',
    parent_user_id      INT UNSIGNED NOT NULL COMMENT '父账户（决定 L2 规则范围）',
    agent_definition_id BIGINT UNSIGNED NOT NULL COMMENT 'agent_definition.id',
    tool_name           VARCHAR(64)  NOT NULL,
    tool_input_digest   CHAR(64)     NOT NULL COMMENT 'SHA-256 完整 64 hex（对账匹配）',
    behavior            VARCHAR(16)  NOT NULL COMMENT 'allow / ask / deny',
    decision_reason     VARCHAR(32)  NOT NULL COMMENT '11 种 canonical 之一',
    validator_id        VARCHAR(64)  NOT NULL COMMENT '触发决策的 validator',
    message             TEXT                  COMMENT '展示文案（ask/deny 有，allow 一般 NULL）',
    latency_ms          INT          NOT NULL DEFAULT 0 COMMENT '决策耗时（ms）',
    created_at          DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_apdl_run_tool (agent_run_id, tool_name),
    INDEX idx_apdl_parent_created (parent_user_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Agent 模式 #6 — 权限决策审计日志';
```

**`created_at DEFAULT CURRENT_TIMESTAMP`**：审计日志写入是异步路径，CURRENT_TIMESTAMP 兜底防 Go 未赋值导致 strict mode INSERT 失败。

### §2.3 Migration 命名

- `migrations/20260521_120000_agent_permission_pipeline.sql`
- `migrations/20260521_120000_agent_permission_pipeline_rollback.sql`

Rollback 内容：`DROP TABLE IF EXISTS agent_permission_decision_log; DROP TABLE IF EXISTS agent_permission_config;`

### §2.4 GORM Models

`internal/pkg/model/agent_permission.go`：

```go
package model

import "time"

// AgentPermissionConfig — L2 租户管理员权限规则
type AgentPermissionConfig struct {
    ID           uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    ParentUserID uint      `gorm:"not null;index:idx_apc_parent_active" json:"parent_user_id"`
    RuleType     string    `gorm:"size:32;not null" json:"rule_type"`
    RuleKey      string    `gorm:"size:255;not null" json:"rule_key"`
    RuleValue    string    `gorm:"type:text" json:"rule_value"`
    Action       string    `gorm:"size:16;not null;default:'deny'" json:"action"`
    Message      string    `gorm:"size:500" json:"message"`
    IsActive     bool      `gorm:"not null;default:true;index:idx_apc_parent_active" json:"is_active"`
    CreatedAt    time.Time `gorm:"not null;autoCreateTime" json:"created_at"` // P2 reviewer fix — autoCreateTime
    UpdatedAt    time.Time `gorm:"not null;autoUpdateTime" json:"updated_at"` // P2 reviewer fix — autoUpdateTime
}

func (AgentPermissionConfig) TableName() string { return "agent_permission_config" }

// AgentPermissionDecisionLog — 权限决策审计日志
type AgentPermissionDecisionLog struct {
    ID                uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    AgentRunID        uint64    `gorm:"not null;index:idx_apdl_run_tool" json:"agent_run_id"`
    UserID            uint      `gorm:"not null" json:"user_id"`
    ParentUserID      uint      `gorm:"not null;index:idx_apdl_parent_created" json:"parent_user_id"`
    AgentDefinitionID uint64    `gorm:"not null" json:"agent_definition_id"`
    ToolName          string    `gorm:"size:64;not null;index:idx_apdl_run_tool" json:"tool_name"`
    ToolInputDigest   string    `gorm:"type:char(64);not null" json:"tool_input_digest"`
    Behavior          string    `gorm:"size:16;not null" json:"behavior"`
    DecisionReason    string    `gorm:"size:32;not null" json:"decision_reason"`
    ValidatorID       string    `gorm:"size:64;not null" json:"validator_id"`
    Message           string    `gorm:"type:text" json:"message"`
    LatencyMs         int       `gorm:"not null;default:0" json:"latency_ms"`
    CreatedAt         time.Time `gorm:"not null;default:CURRENT_TIMESTAMP;index:idx_apdl_parent_created" json:"created_at"`
}

func (AgentPermissionDecisionLog) TableName() string { return "agent_permission_decision_log" }
```

### §2.5 AutoMigrate

`internal/numind/helper.go` 找 `&model.AgentDefinition{}` 紧邻处加：

```go
&model.AgentPermissionConfig{},
&model.AgentPermissionDecisionLog{},
```

---

## §3 Store 层

`internal/numind/store/agent_permission.go`：

```go
package store

import (
    "context"
    "fmt"

    "gorm.io/gorm"

    "numind-server/internal/pkg/model"
)

// IAgentPermissionStore 定义 agent_permission_config + agent_permission_decision_log 的存取接口。
type IAgentPermissionStore interface {
    // ListActiveByParent 返回某父账户下所有 is_active=true 的规则。
    // ToleranceAdminRuleValidator 每次工具调用都查这个。
    ListActiveByParent(ctx context.Context, parentUserID uint) ([]model.AgentPermissionConfig, error)

    // CreateRule 用于测试 fixture 或 #10 admin 端调用。
    // 注意：is_active=false 时由调用方走 UpdateColumn fixup（database.md §6）。
    CreateRule(ctx context.Context, rule *model.AgentPermissionConfig) error

    // CreateDecisionLog 写一条审计日志（同步 INSERT；audit goroutine 内调用）。
    CreateDecisionLog(ctx context.Context, log *model.AgentPermissionDecisionLog) error
}

type agentPermissionStore struct {
    db *gorm.DB
}

var _ IAgentPermissionStore = (*agentPermissionStore)(nil)

func newAgentPermissionStore(db *gorm.DB) IAgentPermissionStore {
    return &agentPermissionStore{db: db}
}

func (s *agentPermissionStore) ListActiveByParent(ctx context.Context, parentUserID uint) ([]model.AgentPermissionConfig, error) {
    var rules []model.AgentPermissionConfig
    err := s.db.WithContext(ctx).
        Where("parent_user_id = ? AND is_active = ?", parentUserID, true).
        Order("id ASC").
        Find(&rules).Error
    if err != nil {
        return nil, fmt.Errorf("ListActiveByParent: %w", err)
    }
    return rules, nil
}

// CreateRule — 按 .claude/rules/database.md §6 GORM `default:true` bool Create 坑应用 UpdateColumn fixup。
// （P1 reviewer fix — 与 agent_definition_store.CreateTx 模式对齐）
func (s *agentPermissionStore) CreateRule(ctx context.Context, rule *model.AgentPermissionConfig) error {
    wantActive := rule.IsActive // 捕获调用方意图
    if err := s.db.WithContext(ctx).Create(rule).Error; err != nil {
        return fmt.Errorf("CreateRule: %w", err)
    }
    // GORM 可能把 struct.IsActive 写回 DB default（true）
    if !wantActive && rule.IsActive {
        if err := s.db.WithContext(ctx).Model(rule).UpdateColumn("is_active", false).Error; err != nil {
            return fmt.Errorf("CreateRule UpdateColumn fixup: %w", err)
        }
        rule.IsActive = false
    }
    return nil
}

func (s *agentPermissionStore) CreateDecisionLog(ctx context.Context, log *model.AgentPermissionDecisionLog) error {
    return s.db.WithContext(ctx).Create(log).Error
}
```

注册到 `store.go`：

```go
// IStore interface 加
AgentPermissions() IAgentPermissionStore

// datastore 实例方法加
func (ds *datastore) AgentPermissions() IAgentPermissionStore {
    return newAgentPermissionStore(ds.db)
}
```

---

## §4 biz/permission 子包结构

```
internal/numind/biz/permission/
├── result.go              # PermissionResult / DecisionReasonType / PermissionDenialDetail
├── request.go             # PermissionRequest struct
├── validator.go           # Validator interface + Result builders
├── pipeline.go            # PermissionPipeline + Check 主入口
├── gate.go                # PermissionGate top-level + options
├── audit.go               # AuditLogger interface + dbAuditLogger 默认实现 + 异步 goroutine
├── digest.go              # tool_input_digest 计算（SHA-256 完整 64 hex）
├── wrap_hooks.go          # WrapHooks(base *agent.RunHooks, gate *PermissionGate) *agent.RunHooks
├── sink.go                # PermissionDenialSink type + ctx key
└── validators/
    ├── platform_hard_rule.go   # L1 — 包 bashvalidator 8 P0
    ├── sandbox_override.go     # L1 — v1 stub 永远 passthrough
    ├── tenant_admin_rule.go    # L2 — 读 agent_permission_config
    ├── working_dir.go          # L2 — 文件路径白名单
    ├── tool_flag.go            # L2 — 读 agent_definition.tool_flags
    ├── user_session_rule.go    # L3 — IsDestructive 高危
    └── classifier_placeholder.go # L3 — v1 stub
```

### §4.1 result.go

> **P1 reviewer fix（包架构调整）**：`PermissionDenialDetail` 移至 **biz/agent** 包（独立文件 `permission_denial.go`），不放在 biz/permission。这样 `RunResult.PermissionDenial` 字段类型可声明为 `*agent.PermissionDenialDetail`（不是 `any`），`json:"omitempty"` 正确工作（nil pointer omit），消费者无需 type assertion。biz/permission 包 import biz/agent（已有），不引入新依赖；biz/agent 不 import biz/permission（单向依赖）。Validator 实现里 `permission.Deny(...)` 返回的 `PermissionResult` 在 wrapper 构造 `*agent.PermissionDenialDetail` 时填入字段。

`biz/agent/permission_denial.go`（NEW）：

```go
package agent

import "encoding/json"

// PermissionDenialDetail — 工具调用被 permission gate 拒绝后的详情；
// 由 wrap_hooks 构造，通过 PermissionSink 注入 ctx，runner.Run 末尾收并填 RunResult.PermissionDenial。
type PermissionDenialDetail struct {
    ToolName       string `json:"tool_name"`
    Behavior       string `json:"behavior"`       // "deny" | "ask"
    DecisionReason string `json:"decision_reason"`
    ValidatorID    string `json:"validator_id"`
    Message        string `json:"message"`
}

// String — 便于日志输出
func (d *PermissionDenialDetail) String() string {
    if d == nil {
        return "<nil PermissionDenialDetail>"
    }
    b, _ := json.Marshal(d)
    return string(b)
}
```

`biz/permission/result.go`：

```go
package permission

// DecisionReasonType — 11 种 canonical (蓝本 §4.4.5)
type DecisionReasonType string

const (
    DecisionReasonRule                 DecisionReasonType = "rule"
    DecisionReasonMode                 DecisionReasonType = "mode"
    DecisionReasonSubcommandResults    DecisionReasonType = "subcommandResults"
    DecisionReasonPermissionPromptTool DecisionReasonType = "permissionPromptTool"
    DecisionReasonHook                 DecisionReasonType = "hook"
    DecisionReasonAsyncAgent           DecisionReasonType = "asyncAgent"
    DecisionReasonSandboxOverride      DecisionReasonType = "sandboxOverride"
    DecisionReasonClassifier           DecisionReasonType = "classifier"
    DecisionReasonWorkingDir           DecisionReasonType = "workingDir"
    DecisionReasonSafetyCheck          DecisionReasonType = "safetyCheck"
    DecisionReasonOther                DecisionReasonType = "other"
)

// PermissionResult — pipeline Validator 的返回结构（蓝本 §4.4.2）
type PermissionResult struct {
    Behavior       string             // "allow" | "ask" | "deny" | "passthrough"
    DecisionReason DecisionReasonType
    ValidatorID    string             // 触发决策的 validator 标识（用于审计 + Langfuse trace）
    Message        string             // ask/deny 展示文案；passthrough 时弃权原因（仅日志）
    UpdatedInput   map[string]any     // 允许 validator 清洗输入；v1 所有 validator nil；wrapper 透传 framework
    Pending        *PendingClassifierCheck // 仅 ask 可设置；v1 永远 nil（占位）
    Suggestions    []PermissionUpdate // #10 摩擦报告消费；v1 永远 nil（占位）
}

// PendingClassifierCheck — 异步 LLM 分类器（v1 占位，#14 落地）
type PendingClassifierCheck struct {
    ClassifierID string
    TimeoutMs    int
    OnApprove    string
    OnReject     string
}

// PermissionUpdate — 给 #10 的规则调整建议
type PermissionUpdate struct {
    RuleID     uint64
    Suggestion string
}

// PermissionDenialDetail — 类型别名指向 biz/agent.PermissionDenialDetail（P1 reviewer fix；避免 import cycle）
// 在 permission 包内使用 PermissionDenialDetail 等同于 agent.PermissionDenialDetail。
// 用 import alias 实现：
//   import agentmod "numind-server/internal/numind/biz/agent"
//   type PermissionDenialDetail = agentmod.PermissionDenialDetail
// 见 result.go 顶部 import 声明。

// Behavior constants
const (
    BehaviorAllow       = "allow"
    BehaviorAsk         = "ask"
    BehaviorDeny        = "deny"
    BehaviorPassthrough = "passthrough"
)
```

### §4.2 request.go

```go
package permission

import "numind-server/internal/numind/biz/agent"

// PermissionRequest — pipeline 输入（hook chain 内 wrapper 构造）
type PermissionRequest struct {
    AgentRunID        uint64
    UserID            uint
    ParentUserID      uint
    AgentDefinitionID uint64
    Tool              agent.FullTool // #3 接口
    InputJSON         string         // tool 调用原始 JSON
    SandboxID         string         // 若在沙箱，#4 session.id；否则空字符串
}
```

### §4.3 validator.go

```go
package permission

import "context"

// Validator — pipeline 中单个权限校验器
type Validator interface {
    ID() string // 用于审计 validator_id 字段
    Validate(ctx context.Context, req PermissionRequest) PermissionResult
}

// Helper builders 便于 validator 实现

func Passthrough(validatorID string, reason DecisionReasonType, why string) PermissionResult {
    return PermissionResult{
        Behavior:       BehaviorPassthrough,
        DecisionReason: reason,
        ValidatorID:    validatorID,
        Message:        why,
    }
}

func Allow(validatorID string, reason DecisionReasonType, why string) PermissionResult {
    return PermissionResult{
        Behavior:       BehaviorAllow,
        DecisionReason: reason,
        ValidatorID:    validatorID,
        Message:        why,
    }
}

func Deny(validatorID string, reason DecisionReasonType, message string) PermissionResult {
    return PermissionResult{
        Behavior:       BehaviorDeny,
        DecisionReason: reason,
        ValidatorID:    validatorID,
        Message:        message,
    }
}

func Ask(validatorID string, reason DecisionReasonType, message string) PermissionResult {
    return PermissionResult{
        Behavior:       BehaviorAsk,
        DecisionReason: reason,
        ValidatorID:    validatorID,
        Message:        message,
    }
}
```

### §4.4 pipeline.go

```go
package permission

import (
    "context"
)

// PermissionPipeline — validator 链
type PermissionPipeline struct {
    validators []Validator
}

func NewPipeline(validators ...Validator) *PermissionPipeline {
    return &PermissionPipeline{validators: validators}
}

// Check — 同步串行执行（蓝本 §4.4.3）
//
//   for v in validators:
//     result := v.Validate(ctx, req)
//     if result.Behavior != passthrough: break (early termination)
//   default: allow + DecisionReasonOther + ValidatorID="DefaultAllow"
func (p *PermissionPipeline) Check(ctx context.Context, req PermissionRequest) PermissionResult {
    for _, v := range p.validators {
        result := v.Validate(ctx, req)
        if result.Behavior != BehaviorPassthrough {
            return result
        }
    }
    return Allow("DefaultAllow", DecisionReasonOther, "all validators passthrough")
}
```

### §4.5 gate.go

```go
package permission

import (
    "context"
    "sync"
    "time"

    "numind-server/internal/numind/store"
    "numind-server/internal/pkg/log"
)

// AuditLogger — 异步审计接口（默认实现 dbAuditLogger 写 store）
type AuditLogger interface {
    Log(ctx context.Context, entry AuditEntry) // 非阻塞
}

type AuditEntry struct {
    Req       PermissionRequest
    Result    PermissionResult
    LatencyMs int
}

// PermissionGate — 顶层入口
type PermissionGate struct {
    pipeline      *PermissionPipeline
    audit         AuditLogger
    permStore     store.IAgentPermissionStore
    skillStore    store.IAgentDefinitionStore
    auditChan     chan AuditEntry
    auditWG       sync.WaitGroup
    closeOnce     sync.Once
    closeCh       chan struct{}
    chanSize      int
    closed        bool
    closedMu      sync.RWMutex
}

// Options
type Option func(*PermissionGate)

func WithStore(s store.IAgentPermissionStore) Option {
    return func(g *PermissionGate) { g.permStore = s }
}

func WithSkillStore(s store.IAgentDefinitionStore) Option {
    return func(g *PermissionGate) { g.skillStore = s }
}

func WithValidators(vs ...Validator) Option {
    return func(g *PermissionGate) { g.pipeline = NewPipeline(vs...) }
}

func WithAuditChannelSize(n int) Option {
    return func(g *PermissionGate) { g.chanSize = n }
}

func WithAuditLogger(l AuditLogger) Option {
    return func(g *PermissionGate) { g.audit = l }
}

// NewPermissionGate — 构造 + 启动 audit goroutine
func NewPermissionGate(opts ...Option) *PermissionGate {
    g := &PermissionGate{
        chanSize: 1024,
        closeCh:  make(chan struct{}),
    }
    for _, opt := range opts {
        opt(g)
    }
    // 默认 pipeline = 7 个 validator（在 biz.go wire 时通过 WithValidators 覆盖）
    if g.pipeline == nil {
        g.pipeline = NewPipeline() // empty — 等同 default-allow，仅测试可控
    }
    // 默认 audit logger = dbAuditLogger
    if g.audit == nil && g.permStore != nil {
        g.audit = newDBAuditLogger(g.permStore)
    }
    // 启动 audit drainer
    g.auditChan = make(chan AuditEntry, g.chanSize)
    g.auditWG.Add(1)
    go g.drainAudit()
    return g
}

// Check — 主入口（hook 内调）
func (g *PermissionGate) Check(ctx context.Context, req PermissionRequest) PermissionResult {
    start := time.Now()
    result := g.pipeline.Check(ctx, req)
    latency := int(time.Since(start) / time.Millisecond)

    // 异步审计（不阻塞）
    g.closedMu.RLock()
    closed := g.closed
    g.closedMu.RUnlock()
    if closed {
        // 同步 warn 落日志
        log.Warnw("PermissionGate.Check after Close: audit dropped",
            "agent_run_id", req.AgentRunID,
            "tool", req.Tool.Name(),
            "behavior", result.Behavior)
    } else {
        select {
        case g.auditChan <- AuditEntry{Req: req, Result: result, LatencyMs: latency}:
        default:
            // channel full — log warn, don't block
            log.Warnw("PermissionGate.Check: audit channel full, dropping entry",
                "agent_run_id", req.AgentRunID,
                "tool", req.Tool.Name(),
                "behavior", result.Behavior)
        }
    }
    return result
}

// drainAudit goroutine — 消费 channel + 调 AuditLogger.Log
func (g *PermissionGate) drainAudit() {
    defer g.auditWG.Done()
    for {
        select {
        case entry := <-g.auditChan:
            // 用 background ctx；不传 Check 时的 ctx 因为 hook 已返回后 ctx 可能取消
            g.audit.Log(context.Background(), entry)
        case <-g.closeCh:
            // drain 残留 entries
            for {
                select {
                case entry := <-g.auditChan:
                    g.audit.Log(context.Background(), entry)
                default:
                    return
                }
            }
        }
    }
}

// Close — 优雅停止
//
//   语义：
//     1. 标记 closed=true（新 Check 改为同步 warn 不进 channel）
//     2. close(closeCh) 触发 drainer 进入 drain-and-exit 分支
//     3. WaitGroup.Wait() 阻塞至 drainer 退出；5s 超时强制返回
//
//   已知 close-race 语义（P1 reviewer fix）：
//     - close() 与 Check() 之间存在极小竞争窗口：
//       Check goroutine RLock 读到 closed=false，进入 select 写 auditChan；
//       此时另一 goroutine Close() 设 closed=true，close(closeCh)，drainer 进入
//       内层 drain；若 Check 的 send 发生在内层 drain 之后则该 entry 丢失（无 warn）。
//     - 本设计 **接受** 此 trade-off：Close 是一次性 shutdown 路径，残留 in-flight Check
//       的审计条目允许丢失（性能优先于审计 100% 完整性）。
//     - 不接受场景：高频运行时（非 Close）必须保证 audit 完整 — 由 channel buffered 1024 + warn
//       on full 保证。运行时丢失场景仅在 channel buf 满（极端高并发突发），有 zap.Warn 可观察。
func (g *PermissionGate) Close() {
    g.closeOnce.Do(func() {
        g.closedMu.Lock()
        g.closed = true
        g.closedMu.Unlock()

        close(g.closeCh)

        done := make(chan struct{})
        go func() {
            g.auditWG.Wait()
            close(done)
        }()

        select {
        case <-done:
        case <-time.After(5 * time.Second):
            log.Warnw("PermissionGate.Close: drain timed out after 5s, residual audit entries dropped")
        }
    })
}
```

### §4.6 audit.go

```go
package permission

import (
    "context"
    "time"

    "numind-server/internal/numind/store"
    "numind-server/internal/pkg/log"
    "numind-server/internal/pkg/model"
)

type dbAuditLogger struct {
    store store.IAgentPermissionStore
}

func newDBAuditLogger(s store.IAgentPermissionStore) AuditLogger {
    return &dbAuditLogger{store: s}
}

func (l *dbAuditLogger) Log(ctx context.Context, entry AuditEntry) {
    row := &model.AgentPermissionDecisionLog{
        AgentRunID:        entry.Req.AgentRunID,
        UserID:            entry.Req.UserID,
        ParentUserID:      entry.Req.ParentUserID,
        AgentDefinitionID: entry.Req.AgentDefinitionID,
        ToolName:          entry.Req.Tool.Name(),
        ToolInputDigest:   Digest(entry.Req.InputJSON),
        Behavior:          entry.Result.Behavior,
        DecisionReason:    string(entry.Result.DecisionReason),
        ValidatorID:       entry.Result.ValidatorID,
        Message:           entry.Result.Message,
        LatencyMs:         entry.LatencyMs,
        CreatedAt:         time.Now(),
    }
    if err := l.store.CreateDecisionLog(ctx, row); err != nil {
        log.Warnw("AuditLogger: CreateDecisionLog failed",
            "agent_run_id", entry.Req.AgentRunID,
            "tool", entry.Req.Tool.Name(),
            "error", err)
    }
}
```

### §4.7 digest.go

```go
package permission

import (
    "crypto/sha256"
    "encoding/hex"
)

// Digest — SHA-256 完整 64 hex 字符（对账匹配；防 PII 副作用）
func Digest(s string) string {
    h := sha256.Sum256([]byte(s))
    return hex.EncodeToString(h[:])
}
```

### §4.8 sink.go — **删除（P2 reviewer fix）**

sink ctx key + WithPermissionSink/PermissionSinkFromCtx **全部放在 biz/agent 包**（详见 §5.3 `permission_sink.go`），permission 包不重复定义。wrap_hooks.go 直接 import biz/agent 调 `agent.PermissionSinkFromCtx`。

### §4.9 wrap_hooks.go

```go
package permission

import (
    "context"
    "encoding/json"

    einotool "github.com/cloudwego/eino/components/tool"

    "numind-server/internal/numind/biz/agent"
    "numind-server/internal/pkg/log"
)

// WrapHooks — 把 base hooks 包成 permission-aware hooks
//
//   PreToolCall 顺序（S0 sd0-1 / S1 sd1-1）：
//     1. permission.Check(ctx, req)
//     2. if deny/ask:
//          Registry.Record(HookActionPermissionDeny)
//          send sink <- detail (non-blocking)
//          return (HookActionPermissionDeny, nil)
//     3. if result.UpdatedInput != nil:
//          input' = marshal(updated)  // v1 永远不走这条；framework 透传
//     4. return base.PreToolCall(ctx, t, input')  // sandbox 启动容器
//
//   PostToolCall 透传 base.PostToolCall（permission 不在 post 做决策）
func WrapHooks(base *agent.RunHooks, gate *PermissionGate) *agent.RunHooks {
    return &agent.RunHooks{
        PreToolCall: func(ctx context.Context, t einotool.BaseTool, input string) (agent.HookAction, error) {
            req, err := buildRequest(ctx, t, input, gate)
            if err != nil {
                // 构造失败 = 系统问题；不阻塞工具执行（fail-open，但写 warn 日志）
                log.Warnw("WrapHooks.PreToolCall: buildRequest failed; permission check skipped",
                    "tool", info(ctx, t),
                    "error", err)
                // 透传 base
                if base != nil && base.PreToolCall != nil {
                    return base.PreToolCall(ctx, t, input)
                }
                return agent.HookActionContinue, nil
            }

            result := gate.Check(ctx, req)

            switch result.Behavior {
            case BehaviorDeny, BehaviorAsk:
                // Record + sink + 短路
                if registry := registryFromBase(base); registry != nil {
                    registry.Record(agent.HookActionPermissionDeny)
                }
                if sink := agent.PermissionSinkFromCtx(ctx); sink != nil {
                    detail := &agent.PermissionDenialDetail{
                        ToolName:       req.Tool.Name(),
                        Behavior:       result.Behavior,
                        DecisionReason: string(result.DecisionReason),
                        ValidatorID:    result.ValidatorID,
                        Message:        result.Message,
                    }
                    select {
                    case sink <- detail:
                    default:
                        // sink 满（理论不应；size=1 + 每 Run 只产一次 deny）
                        log.Warnw("WrapHooks.PreToolCall: sink full",
                            "agent_run_id", req.AgentRunID,
                            "tool", req.Tool.Name())
                    }
                }
                return agent.HookActionPermissionDeny, nil
            case BehaviorAllow, BehaviorPassthrough:
                // UpdatedInput 透传 framework（v1 永远 nil）
                effectiveInput := input
                if result.UpdatedInput != nil {
                    if b, err := json.Marshal(result.UpdatedInput); err == nil {
                        effectiveInput = string(b)
                    } else {
                        log.Warnw("WrapHooks.PreToolCall: UpdatedInput marshal failed; using original input",
                            "tool", req.Tool.Name(),
                            "error", err)
                    }
                }
                if base != nil && base.PreToolCall != nil {
                    return base.PreToolCall(ctx, t, effectiveInput)
                }
                return agent.HookActionContinue, nil
            default:
                // 未知 behavior — fail-open
                log.Warnw("WrapHooks.PreToolCall: unknown behavior; fail-open",
                    "behavior", result.Behavior,
                    "tool", req.Tool.Name())
                if base != nil && base.PreToolCall != nil {
                    return base.PreToolCall(ctx, t, input)
                }
                return agent.HookActionContinue, nil
            }
        },
        PostToolCall: func(ctx context.Context, t einotool.BaseTool, output string, err error) (agent.HookAction, error) {
            if base != nil && base.PostToolCall != nil {
                return base.PostToolCall(ctx, t, output, err)
            }
            return agent.HookActionContinue, nil
        },
        // Registry 字段：透传 base.Registry；如 base nil，wrapper 不创建（runner.Run auto-inject）
        Registry: registryFromBase(base),
    }
}

func registryFromBase(base *agent.RunHooks) *agent.HookActionRegistry {
    if base == nil {
        return nil
    }
    return base.Registry
}

// buildRequest — 从 ctx + tool + input 构造 PermissionRequest
func buildRequest(ctx context.Context, t einotool.BaseTool, input string, gate *PermissionGate) (PermissionRequest, error) {
    runID := agent.RunIDFromContext(ctx)
    userID, _ := agent.UserIDFromAgentCtx(ctx) // see §5.7 — 包内独立读取（不依赖 middleware 包）
    agentDefID, parentUserID := agent.AgentDefAndParentFromCtx(ctx)

    info, err := t.Info(ctx)
    if err != nil {
        return PermissionRequest{}, err
    }

    // Resolve FullTool from registry（PermissionRequest.Tool 字段）
    // adapter_full_to_eino 把 FullTool 包装为 einotool；这里反查 wrapper 暴露的 FullTool。
    // 简化：通过 ctx 注入 FullTool（runner.Run 在 adapter 构造时 stash 到 ctx）
    fullToolAny := agent.FullToolFromCtx(ctx, info.Name)
    var fullTool agent.FullTool
    if fullToolAny != nil {
        fullTool = fullToolAny
    }
    if fullTool == nil {
        // adapter 未注入 → fall back 用 einotool wrapper minimal 元数据
        // v1: log warn + fail-open（让 base.PreToolCall 处理）
        log.Warnw("WrapHooks.buildRequest: FullTool not in ctx; permission can only see einotool metadata",
            "tool", info.Name)
    }

    return PermissionRequest{
        AgentRunID:        runID,
        UserID:            userID,
        ParentUserID:      parentUserID,
        AgentDefinitionID: agentDefID,
        Tool:              fullTool,
        InputJSON:         input,
        SandboxID:         "", // v1: SandboxOverrideValidator 用不到，stub
    }, nil
}

func info(ctx context.Context, t einotool.BaseTool) string {
    if i, err := t.Info(ctx); err == nil {
        return i.Name
    }
    return "?"
}
```

> **buildRequest 中 FullToolFromCtx**：runner.Run 在 ctx 上 stash `name → FullTool` map（在装配 einoTools 时同步 stash）。详见 §6.3。

### §4.10 Validators — 7 个实现

#### §4.10.1 platform_hard_rule.go (L1)

```go
package validators

import (
    "context"
    "encoding/json"
    "strings"

    "numind-server/internal/numind/biz/agent/bashvalidator"
    "numind-server/internal/numind/biz/permission"
)

type PlatformHardRule struct{}

func NewPlatformHardRule() permission.Validator { return &PlatformHardRule{} }

func (v *PlatformHardRule) ID() string { return "PlatformHardRule" }

func (v *PlatformHardRule) Validate(ctx context.Context, req permission.PermissionRequest) permission.PermissionResult {
    if req.Tool == nil || req.Tool.Name() != "bash_exec" {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "not bash_exec")
    }
    // 从 InputJSON 提取 command 字段
    cmd := extractBashCommand(req.InputJSON)
    if cmd == "" {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no command field")
    }
    allow, reason := bashvalidator.Validate(cmd)
    if !allow {
        return permission.Deny(v.ID()+":"+firstColonField(reason), permission.DecisionReasonRule, reason)
    }
    return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "bash command allowed by hard rules")
}

// extractBashCommand — 解析 InputJSON 取 "command" 字段（{"command": "..."}）
func extractBashCommand(input string) string {
    var m map[string]any
    if err := json.Unmarshal([]byte(input), &m); err != nil {
        return ""
    }
    s, _ := m["command"].(string)
    return s
}

// firstColonField — bashvalidator.Validate 返回 reason 形如 "ControlChar: ASCII 控制字符 — pattern=..."；
// 取首个冒号前作为细分 validator ID。
// P0 reviewer fix — 实现完整给出。
func firstColonField(s string) string {
    parts := strings.SplitN(s, ":", 2)
    return parts[0] // bashvalidator ID 不含空格（如 "ControlChar" / "Unicode" / "CommandSubstitution"）
}
```

#### §4.10.2 sandbox_override.go (L1 stub)

```go
package validators

import (
    "context"
    "numind-server/internal/numind/biz/permission"
)

type SandboxOverride struct{}

func NewSandboxOverride() permission.Validator { return &SandboxOverride{} }

func (v *SandboxOverride) ID() string { return "SandboxOverride" }

// v1 永远 passthrough（#13 真实落地：req.SandboxID != "" && tool.IsReadOnly() → allow）
func (v *SandboxOverride) Validate(_ context.Context, _ permission.PermissionRequest) permission.PermissionResult {
    return permission.Passthrough(v.ID(), permission.DecisionReasonSandboxOverride, "v1 stub")
}
```

#### §4.10.3 tenant_admin_rule.go (L2)

```go
package validators

import (
    "context"
    "regexp"
    "strings"

    "numind-server/internal/numind/biz/permission"
    "numind-server/internal/numind/store"
    "numind-server/internal/pkg/log"
    "numind-server/internal/pkg/model"
)

// P2 reviewer fix tech debt 标注：regex 编译无缓存（每次 Validate 都 regexp.Compile）；
// v1 接受；后续优化项：in-memory cache by rule.ID + pre-compile pool。

type TenantAdminRule struct {
    store store.IAgentPermissionStore
}

func NewTenantAdminRule(s store.IAgentPermissionStore) permission.Validator {
    return &TenantAdminRule{store: s}
}

func (v *TenantAdminRule) ID() string { return "TenantAdminRule" }

func (v *TenantAdminRule) Validate(ctx context.Context, req permission.PermissionRequest) permission.PermissionResult {
    if v.store == nil || req.ParentUserID == 0 {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no store or parent")
    }
    rules, err := v.store.ListActiveByParent(ctx, req.ParentUserID)
    if err != nil {
        log.Warnw("TenantAdminRule.Validate: ListActiveByParent failed; fail-open",
            "parent_user_id", req.ParentUserID,
            "error", err)
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "store error fail-open")
    }
    for _, rule := range rules {
        if !ruleMatches(rule, req) {
            continue
        }
        // 命中
        validatorID := v.ID() + ":" + rule.RuleType
        action := rule.Action
        if action == "" {
            action = permission.BehaviorDeny
        }
        message := rule.Message
        if message == "" {
            message = "本规则不允许该操作"
        }
        switch action {
        case permission.BehaviorAsk:
            return permission.Ask(validatorID, permission.DecisionReasonRule, message)
        default:
            return permission.Deny(validatorID, permission.DecisionReasonRule, message)
        }
    }
    return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no rule matched")
}

func ruleMatches(rule model.AgentPermissionConfig, req permission.PermissionRequest) bool {
    switch rule.RuleType {
    case "tool_blacklist":
        return rule.RuleKey == req.Tool.Name()
    case "tool_input_regex_deny":
        if rule.RuleKey != req.Tool.Name() {
            return false
        }
        re, err := regexp.Compile(rule.RuleValue)
        if err != nil {
            return false
        }
        return re.MatchString(req.InputJSON)
    case "topic_blacklist":
        // 简化：input 含 RuleKey 关键词
        return strings.Contains(req.InputJSON, rule.RuleKey)
    default:
        return false
    }
}
```

#### §4.10.4 working_dir.go (L2)

```go
package validators

import (
    "context"
    "encoding/json"
    "strings"

    "numind-server/internal/numind/biz/permission"
)

type WorkingDir struct {
    allowedPrefix string
}

func NewWorkingDir(prefix string) permission.Validator {
    if prefix == "" {
        prefix = "/workdir/"
    }
    return &WorkingDir{allowedPrefix: prefix}
}

func (v *WorkingDir) ID() string { return "WorkingDir" }

// 仅对 tool 名以 "file_" 开头的工具检查（如 file_read / file_write）
func (v *WorkingDir) Validate(_ context.Context, req permission.PermissionRequest) permission.PermissionResult {
    if req.Tool == nil || !strings.HasPrefix(req.Tool.Name(), "file_") {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "not file_ tool")
    }
    var m map[string]any
    if err := json.Unmarshal([]byte(req.InputJSON), &m); err != nil {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "input not JSON")
    }
    path, _ := m["path"].(string)
    if path == "" {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no path field")
    }
    if !strings.HasPrefix(path, v.allowedPrefix) {
        return permission.Deny(v.ID(), permission.DecisionReasonWorkingDir,
            "文件路径必须在 "+v.allowedPrefix+" 下")
    }
    return permission.Passthrough(v.ID(), permission.DecisionReasonWorkingDir, "path in allowed dir")
}
```

#### §4.10.5 tool_flag.go (L2)

```go
package validators

import (
    "context"
    "encoding/json"

    "numind-server/internal/numind/biz/permission"
    "numind-server/internal/numind/store"
    "numind-server/internal/pkg/log"
)

type ToolFlag struct {
    skillStore store.IAgentDefinitionStore
}

func NewToolFlag(s store.IAgentDefinitionStore) permission.Validator {
    return &ToolFlag{skillStore: s}
}

func (v *ToolFlag) ID() string { return "ToolFlag" }

func (v *ToolFlag) Validate(ctx context.Context, req permission.PermissionRequest) permission.PermissionResult {
    if v.skillStore == nil || req.AgentDefinitionID == 0 {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no skillStore or definition")
    }
    ad, err := v.skillStore.GetByIDIncludeInactive(ctx, req.AgentDefinitionID)
    if err != nil {
        log.Warnw("ToolFlag.Validate: skill lookup failed; fail-open",
            "agent_definition_id", req.AgentDefinitionID, "error", err)
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "skill lookup error fail-open")
    }
    if len(ad.ToolFlags) == 0 {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "no tool_flags configured")
    }
    var flags map[string]bool
    if err := json.Unmarshal(ad.ToolFlags, &flags); err != nil {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "tool_flags unmarshal error")
    }
    toolName := req.Tool.Name()
    enabled, present := flags[toolName]
    if !present {
        // 字段未配置 = 默认启用（不限制；与 #5 语义对齐）
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "tool not in flags")
    }
    if !enabled {
        return permission.Deny(v.ID()+":"+toolName, permission.DecisionReasonRule,
            "该 Agent 暂未启用 "+toolName+" 功能")
    }
    return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "tool enabled")
}
```

#### §4.10.6 user_session_rule.go (L3)

```go
package validators

import (
    "context"
    "numind-server/internal/numind/biz/permission"
)

type UserSessionRule struct{}

func NewUserSessionRule() permission.Validator { return &UserSessionRule{} }

func (v *UserSessionRule) ID() string { return "UserSessionRule" }

// v1 行为（S1 sd1-8）：仅查 IsDestructive=true 即 deny；session auth 查询推迟 #11/#14
func (v *UserSessionRule) Validate(_ context.Context, req permission.PermissionRequest) permission.PermissionResult {
    if req.Tool == nil || !req.Tool.IsDestructive() {
        return permission.Passthrough(v.ID(), permission.DecisionReasonOther, "not destructive")
    }
    return permission.Deny(v.ID(), permission.DecisionReasonMode,
        "该操作可能修改你的数据，需要管理员授权后才能执行")
}
```

#### §4.10.7 classifier_placeholder.go (L3 stub)

```go
package validators

import (
    "context"
    "numind-server/internal/numind/biz/permission"
)

type ClassifierPlaceholder struct{}

func NewClassifierPlaceholder() permission.Validator { return &ClassifierPlaceholder{} }

func (v *ClassifierPlaceholder) ID() string { return "ClassifierPlaceholder" }

// v1 永远 passthrough（#14 真实实装：异步 qwen-turbo classifier）
func (v *ClassifierPlaceholder) Validate(_ context.Context, _ permission.PermissionRequest) permission.PermissionResult {
    return permission.Passthrough(v.ID(), permission.DecisionReasonClassifier, "v1 stub")
}
```

---

## §5 biz/agent 改造

### §5.1 hooks.go

加 `HookActionPermissionDeny = 3`：

```go
const (
    HookActionContinue       HookAction = iota // 0
    HookActionStop                              // 1
    HookActionBlockingStop                      // 2
    HookActionPermissionDeny                    // 3 — NEW (#6)
)
```

`HookActionToLoopEvent` 加 case：

```go
case HookActionPermissionDeny:
    return LoopEventPermissionDenied
```

`HookActionRegistry` 字段类型不变（atomic.Int32 涵盖 3）。

> **LoopEvent 无编译期数组验证**（P1 reviewer fix — 消除歧义）：
> 现有 state.go 仅对 TerminalReason（[12]）和 ContinueReason（[7]）做编译期数组验证；LoopEvent 没有 `_ = [N]LoopEvent{...}`。本 feature 沿用现有约定：TerminalReason 从 [12] 改 [13]，LoopEvent **不**加编译期数组验证；新增 `LoopEventPermissionDenied` 只需更新 iota 顺序和 Transition switch case，无其他验证点。

### §5.2 state.go

加 `TerminalPermissionDenied` + `LoopEventPermissionDenied`：

```go
const (
    ...
    TerminalErrorMaxRetries   TerminalReason = "error_max_retries"
    TerminalPermissionDenied  TerminalReason = "permission_denied" // NEW (#6)
)

// 编译期不变量更新
var _ = [13]TerminalReason{
    TerminalCompleted, TerminalBlockingLimit, TerminalImageError, TerminalModelError,
    TerminalAbortedStreaming, TerminalPromptTooLong, TerminalStopHookPrevented, TerminalAbortedTools,
    TerminalHookStopped, TerminalMaxTurns, TerminalErrorMaxBudget, TerminalErrorMaxRetries,
    TerminalPermissionDenied,
}

// LoopEvent 加 — 注意 iota 顺序：放在 LoopEventMaxOutputEscalate 之后保持 unknown event 兜底语义不变
const (
    ...
    LoopEventMaxOutputEscalate          // 17
    LoopEventPermissionDenied           // 18 — NEW (#6)
)
```

`Transition` switch 加 case：

```go
case LoopEventPermissionDenied:
    s.TerminalReason = TerminalPermissionDenied
    return TerminalPermissionDenied, "", true
```

### §5.3 runner.go

**包架构决策**（P1 reviewer fix — 一锤定音，不要再修订）：

1. `PermissionDenialDetail` struct 定义在 **biz/agent** 包（§4.1 给出 `biz/agent/permission_denial.go`）
2. sink ctx key + `WithPermissionSink` / `PermissionSinkFromCtx` 也在 **biz/agent** 包（独立文件 `permission_sink.go`）
3. fullToolMap / agentDefCtx ctx key 在 **biz/agent** 包（已有的 ctx 助手文件）
4. biz/permission **import** biz/agent（已有依赖；不引入新）
5. biz/agent **不** import biz/permission（单向依赖严格保持）
6. `RunResult.PermissionDenial *PermissionDenialDetail`（精确类型，不是 any）

`biz/agent/permission_sink.go`（NEW）：

```go
package agent

import "context"

type permissionSinkKey struct{}

// WithPermissionSink 把 sink channel 存入 ctx（每 Run 一个 unbuffered size=1 chan）
func WithPermissionSink(ctx context.Context, sink chan<- *PermissionDenialDetail) context.Context {
    return context.WithValue(ctx, permissionSinkKey{}, sink)
}

// PermissionSinkFromCtx 取 sink；permission wrapper 通过此函数取出 send detail
func PermissionSinkFromCtx(ctx context.Context) chan<- *PermissionDenialDetail {
    s, _ := ctx.Value(permissionSinkKey{}).(chan<- *PermissionDenialDetail)
    return s
}
```

`RunResult` 加字段：

```go
type RunResult struct {
    AgentRunID       uint64
    TerminalReason   TerminalReason
    FinalOutput      string
    StepCount        int
    Duration         time.Duration
    SkillVersion     int
    PermissionDenial *PermissionDenialDetail `json:"permission_denial,omitempty"` // NEW (#6); nil 当非 deny
}
```

> `*PermissionDenialDetail` 是 biz/agent 包内类型；nil pointer + `omitempty` 正确工作。
>
> `RunRequest` 不改字段（sink 通过 ctx 注入，不污染 RunRequest）。
>
> `agentRunner` struct 不加 permGate 字段（runner 完全不感知 permission；wrapper 内部持有 gate）。
>
> **取消** `WithPermissionGate` option（S1 sd1-1 重新评估）：wrapper 已经持有 gate 实例；runner 不需要单独引用。biz.go wire 时直接：
>
> ```go
> wrapped := permission.WrapHooks(sandboxHookMgr.AsRunHooks(), permGate)
> runner := agent.NewAgentRunner(..., agent.WithDefaultHooks(wrapped), agent.WithSkillStore(...))
> // 无需 WithPermissionGate option
> ```

runner.Run 内 ctx 注入：

```go
// Step 4.1 (in Run after skill lookup)
sinkCh := make(chan *PermissionDenialDetail, 1)
ctx = WithPermissionSink(ctx, sinkCh)
if skillVer > 0 {
    ctx = WithAgentDefCtx(ctx, req.AgentDefinitionID, ad.ParentUserID)
}
// Step 4.2 装配 toolMap 后
ctx = WithFullToolMap(ctx, toolMap)

// 末尾（在 state.Transition hook propagation 之后，UpdateState 之前）
var permDetail *PermissionDenialDetail
select {
case d := <-sinkCh:
    permDetail = d
default:
}
// ...
return &RunResult{
    ...
    PermissionDenial: permDetail,
}, nil
```

### §5.4 FullToolFromCtx — runner stash + permission wrapper retrieve

`biz/agent/full_tool_ctx.go`（NEW）：

```go
package agent

import "context"

type fullToolMapKey struct{}

// WithFullToolMap 把 name → FullTool map 存入 ctx
func WithFullToolMap(ctx context.Context, m map[string]FullTool) context.Context {
    return context.WithValue(ctx, fullToolMapKey{}, m)
}

// FullToolFromCtx 取某个工具的 FullTool 实例
func FullToolFromCtx(ctx context.Context, name string) FullTool {
    m, _ := ctx.Value(fullToolMapKey{}).(map[string]FullTool)
    if m == nil {
        return nil
    }
    return m[name]
}
```

runner.Run 在装配 einoTools 时：

```go
toolMap := make(map[string]FullTool)
for _, name := range req.ToolNames {
    if ft, ok := r.registry.GetTool(name); ok {
        einoTools = append(einoTools, adaptFullToEinoTool(ft, effectiveHooks))
        toolMap[name] = ft
    }
}
ctx = WithFullToolMap(ctx, toolMap)
```

### §5.5 UserIDFromAgentCtx（不引入；用现有 middleware）

P0 reviewer fix 中提到的 `agent.UserIDFromAgentCtx` 实际上**不需要新增**——permission wrap_hooks.go 直接 `import "numind-server/internal/pkg/middleware"` 并调 `middleware.UserIDFromCtx(ctx)`。runner.Run 第 0 步 `ctx = middleware.NewContextWithUserID(ctx, req.UserID)` 已经把 userID 注入 ctx，wrapper 直接读即可。

修订 §4.9 wrap_hooks.go import 列表（最终版）：

```go
import (
    "context"
    "encoding/json"

    einotool "github.com/cloudwego/eino/components/tool"

    "numind-server/internal/numind/biz/agent"
    "numind-server/internal/pkg/log"
    "numind-server/internal/pkg/middleware"  // userID 取自现有 middleware ctx key
)
```

§4.9 中 `agent.UserIDFromAgentCtx(ctx)` 改回 `middleware.UserIDFromCtx(ctx)`。

### §5.6 AgentDefAndParentFromCtx

`biz/agent/agent_def_ctx.go`（NEW）：

```go
package agent

import "context"

type agentDefCtxKey struct{}

type agentDefCtx struct {
    AgentDefinitionID uint64
    ParentUserID      uint
}

// WithAgentDefCtx 注入 agent_definition_id 和 parent_user_id（runner.Run 在 skill lookup 后调）
func WithAgentDefCtx(ctx context.Context, agentDefID uint64, parentUserID uint) context.Context {
    return context.WithValue(ctx, agentDefCtxKey{}, &agentDefCtx{
        AgentDefinitionID: agentDefID,
        ParentUserID:      parentUserID,
    })
}

// AgentDefAndParentFromCtx 取
func AgentDefAndParentFromCtx(ctx context.Context) (uint64, uint) {
    v, _ := ctx.Value(agentDefCtxKey{}).(*agentDefCtx)
    if v == nil {
        return 0, 0
    }
    return v.AgentDefinitionID, v.ParentUserID
}
```

> 注：permission 包通过 `agent.AgentDefAndParentFromCtx(ctx)` 取；不重复维护 ctx key。

### §5.7 RunRequest 不动

`RunRequest` 字段 0 增减。所有信息通过 ctx 注入（sink / fullToolMap / agentDefCtx）。

---

## §6 wire 顺序（biz.go）

```go
// 1. 现有：sandbox hook manager
sandboxHookMgr := agent.NewSandboxHookManager(pool, ds.AgentSandboxSessions())
agent.SetDefaultHookManager(sandboxHookMgr)

// 2. NEW: permission gate（构造 + 启动 audit goroutine）
permGate := permission.NewPermissionGate(
    permission.WithStore(ds.AgentPermissions()),
    permission.WithSkillStore(ds.AgentDefinitions()),
    permission.WithValidators(
        validators.NewPlatformHardRule(),
        validators.NewSandboxOverride(),
        validators.NewTenantAdminRule(ds.AgentPermissions()),
        validators.NewWorkingDir(""),  // 默认 /workdir/
        validators.NewToolFlag(ds.AgentDefinitions()),
        validators.NewUserSessionRule(),
        validators.NewClassifierPlaceholder(),
    ),
)

// 3. NEW: wrap sandbox hooks with permission
wrappedHooks := permission.WrapHooks(sandboxHookMgr.AsRunHooks(), permGate)

// 4. Runner wire
runner := agent.NewAgentRunner(ds.AgentRuns(), toolRegistry,
    agent.WithDefaultHooks(wrappedHooks),  // permission → sandbox chain
    agent.WithSkillStore(ds.AgentDefinitions()),
    // 注意：runner.WithPermissionGate option 不需要（runner 不持有 gate；
    // sink + agentDef ctx 由 runner.Run 内部 inject；wrapper 通过 ctx 拿）
)

// 5. Shutdown hook（main.go shutdownSeq）
// 在 server.Shutdown 之后调 permGate.Close() drain audit goroutine
```

> 注意：**取消** §4 提到的 `WithPermissionGate` option（S1 P0 fix 阶段加的），简化为 wrapper 内部 hold gate；runner 完全不感知 gate 实例。

### §6.1 runner.Run 改动

在 Step 4 `req.SystemPrompt = ...` 之后注入 ctx：

```go
// 4.1. #6 permission-pipeline: 注入 sink + agentDef ctx
sinkCh := make(chan any, 1)
ctx = WithPermissionSink(ctx, sinkCh)
if skillVer > 0 {
    ctx = WithAgentDefCtx(ctx, req.AgentDefinitionID, ad.ParentUserID)
}
```

在装配 einoTools 之后 + 构造 ctx 已加 sink/agentDef 之后，加 stash full tool map：

```go
// 4.2. #6 permission-pipeline: stash FullTool map for wrapper.buildRequest
toolMap := make(map[string]FullTool)
for _, name := range req.ToolNames {
    if ft, ok := r.registry.GetTool(name); ok {
        toolMap[name] = ft
    }
}
ctx = WithFullToolMap(ctx, toolMap)
```

在 hook propagation 后（state.Transition 处理 LastAction）：

```go
// 7. #6 permission-pipeline: 收 sink detail（如有）
select {
case detail := <-sinkCh:
    // detail 是 *permission.PermissionDenialDetail（type assertion 由调用方做；
    // 在 runner 内部我们只持 any）
    runResult := <return result>
    runResult.PermissionDenial = detail
default:
}
```

具体顺序（修订完整流程）：
1. Step 6 简化状态机末尾，从 sinkCh 收 detail
2. 若 LastAction == HookActionPermissionDeny → state.Transition(LoopEventPermissionDenied) → TerminalPermissionDenied
3. RunResult.PermissionDenial = detail（如有）

### §6.2 main.go shutdown sequence

```go
// shutdownSeq 末尾
permGate.Close()  // 阻塞 5s 内 drain audit goroutine
```

---

## §7 测试矩阵

| 文件 | 包 | 测试目标 | 覆盖率目标 |
|---|---|---|---|
| `digest_test.go` | permission | SHA-256 完整 64 hex 输出 | 100% |
| `result_test.go` | permission | Allow/Deny/Ask/Passthrough builder 构造 | 100% |
| `pipeline_test.go` | permission | (a) 全 passthrough → default allow；(b) 第 N 个 deny → 取该 result；(c) ask 不被 deny 覆盖 | 100% |
| `gate_test.go` | permission | (a) Check + audit sync write；(b) channel full → warn 不阻塞；(c) Close drain + 5s 超时；(d) Close 后 Check 走 warn | ≥85% |
| `audit_test.go` | permission | dbAuditLogger.Log 写一行；store 错误 → warn 不 panic | ≥80% |
| `wrap_hooks_test.go` | permission | (a) deny 短路 Registry.Record + sink send；(b) allow 透传 base.PreToolCall；(c) UpdatedInput nil 透传 input；(d) UpdatedInput 非 nil marshal；(e) base 为 nil 退化；(f) PostToolCall 透传 | ≥85% |
| `validators/platform_hard_rule_test.go` | validators | (a) 非 bash 工具 passthrough；(b) bash 合法命令 passthrough；(c) bash control char deny；(d) bash 无 command 字段 passthrough | 100% |
| `validators/sandbox_override_test.go` | validators | passthrough only | 100% |
| `validators/tenant_admin_rule_test.go` | validators | (a) 无规则 passthrough；(b) tool_blacklist 命中 deny；(c) tool_input_regex_deny 命中 deny；(d) topic_blacklist 命中 deny；(e) action='ask' 命中返回 ask；(f) store 错误 fail-open | ≥85% |
| `validators/working_dir_test.go` | validators | (a) 非 file_ tool passthrough；(b) /workdir/ 下 passthrough；(c) /etc/passwd deny；(d) input 无 path 字段 passthrough | 100% |
| `validators/tool_flag_test.go` | validators | (a) 无 skillStore passthrough；(b) tool_flags 启用 passthrough；(c) tool_flags 禁用 deny；(d) skill lookup error fail-open | ≥85% |
| `validators/user_session_rule_test.go` | validators | (a) 非 destructive passthrough；(b) destructive deny | 100% |
| `validators/classifier_placeholder_test.go` | validators | passthrough only | 100% |
| `internal/numind/biz/agent/state_test.go` (扩) | agent | LoopEventPermissionDenied → TerminalPermissionDenied | 不下降 |
| `internal/numind/biz/agent/hooks_test.go` (扩) | agent | (a) HookActionRegistry.Record(HookActionPermissionDeny) → LastAction 返回 3；(b) HookActionToLoopEvent(HookActionPermissionDeny) → LoopEventPermissionDenied | 不下降 |
| `internal/numind/biz/agent/permission_sink_test.go` (NEW) | agent | WithPermissionSink + PermissionSinkFromCtx round trip | 100% |
| `internal/numind/biz/agent/full_tool_ctx_test.go` (NEW) | agent | WithFullToolMap + FullToolFromCtx round trip | 100% |
| `internal/numind/biz/agent/agent_def_ctx_test.go` (NEW) | agent | WithAgentDefCtx + AgentDefAndParentFromCtx | 100% |
| `internal/numind/biz/agent/runner_integration_test.go` (扩) | agent | (a) mock einoAgent + WrapHooks + ToolFlag deny → terminal=permission_denied + PermissionDenial 字段填充；(b) Pre returns deny → 不进 base.PreToolCall（验证 sandbox borrow 不发生） | biz/agent 不下降 |
| `internal/numind/store/agent_permission_test.go` (NEW) | store | ListActiveByParent + Create rule + CreateDecisionLog 三方法 in-mem SQLite | ≥85% |

`go test -race ./...` 验证：
- audit channel 并发 send race-safe
- HookActionRegistry.Record/Load race-safe（沿用 atomic.Int32）
- sink channel send/recv race-safe（每 Run 独立 chan）

---

## §8 错误码

`internal/pkg/errno/permission.go`（NEW）：

```go
package errno

var (
    ErrPermissionGateUnavailable = &Errno{HTTP: 500, Code: "BizError.Permission.GateUnavailable", Message: "权限网关不可用"}
    ErrPermissionDenied          = &Errno{HTTP: 403, Code: "BizError.Permission.Denied", Message: "操作被拒绝"}
)
```

> v1 不抛错码（permission deny 是 hook 内 short-circuit；RunResult.PermissionDenial 字段携带信息）；errno 预留给 #10/#11 controller 层。**本 feature 实际不会用到这两个 errno**，但 #10 spec 已经引用，故 v1 落地占位。

---

## §9 不在 S2 范围

- 管理端 #10 controller / API / UI
- 学员端 #11 ask 弹窗 UI / 友好拒绝展示
- 异步 LLM classifier（#14 真实 qwen-turbo 调用）
- SandboxOverride 真实路径（#13 sandbox_id ctx 传播完整）
- safety check（内容安全过滤）— #13 拆出独立 validator
- B2B 父账户规则 cache TTL — 5 分钟 in-memory cache 是 #14 优化项
- 23 个 Bash validator 扩展 — backlog
- prod 部署

---

## §10 验证策略（指向 S3 + S5）

S3 plan 最后一个 task 必须包含"**S5 验证策略**"任务，明确：
- 验证方式：**纯后端 TDD**（biz/permission 单元测试 + runner 集成测试 + store 单元测试）；不需要 Playwright/gstack 浏览器测试（本 feature 无 HTTP/UI）
- 理由：本 feature 不产生 HTTP/UI；所有验收路径都是 Go 单元 + 集成测试
- 关键路径列表：
  1. PermissionPipeline.Check 全 passthrough → default allow
  2. PlatformHardRule deny bash control char → terminal=permission_denied
  3. ToolFlag deny → terminal=permission_denied + PermissionDenial.ValidatorID="ToolFlag:<name>"
  4. TenantAdminRule deny via DB rule → audit log 写入 + terminal=permission_denied
  5. UserSessionRule deny IsDestructive → terminal=permission_denied
  6. WrapHooks PreToolCall deny → 不调 base.PreToolCall（sandbox 不启动）
  7. WrapHooks PreToolCall allow → 透传 base.PreToolCall（sandbox 启动）
  8. Close 后 Check 走同步 warn 不阻塞

---

**S2 完结。S3 写 task plan（M1-M~13 分解 + 文件归属表 + S5 验证策略 task）。**
