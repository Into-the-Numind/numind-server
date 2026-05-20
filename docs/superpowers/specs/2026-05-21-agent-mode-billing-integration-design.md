# NDF S2 Technical Spec · `agent-mode-billing-integration`

**Track**：Standard
**Feature ID**：`agent-mode-billing-integration`（14-feature 分解 #12/14）
**起草日期**：2026-05-21
**状态**：S2 草案
**前置 stage**：S1 通过（commit `4a021df7`）

---

## §1 目标与不变量

### 1.1 目标

把 Agent 模式的"成本与失控防线"完整落地。范围严格按 S0/S1：

1. **失控保护 BudgetTracker 4 维**（biz/budget 子包）— PreToolCall hook 内每轮 check
2. **试聊配额 admin_test**（credit_admin_test_grant 新表 + credit_transaction.source_type CHECK ALTER）
3. **学员积分透明扩展**（BalanceBreakdown 加 AdminTestPool + admin_test_pool JSON 字段）
4. **状态机/Hook 扩展**（state.go LoopEventErrorMaxBudget 新增 / hooks.go HookActionBudgetExceeded=4）
5. **runner.go 集成**（WithBudgetTracker option + budget.WrapHooks wire）
6. **agent_run.terminal_metadata** ALTER ADD COLUMN

### 1.2 不变量（违反 = review FAIL）

1. **现有 SOP/Chatbot Reserve/Reconcile/GetBalance 调用方代码 0 改动** — credit_service.go 内的既有方法签名、行为完全保留
2. **现有 6 个 source_type 枚举 + NULL 行 ALTER 后全过 CHECK** — pre-check 查询验证无非法行
3. **`agent_run.terminal_metadata` ALTER ADD COLUMN 不影响既有行**（NULL 默认，DEFAULT NULL）
4. **admin_test 池不 fallback 三池** — 耗尽返回 ErrAdminTestExhausted，不动 cycle / booster / trial
5. **BudgetTracker in-memory state per Run** — `map[runID]*state` + RWMutex，Run 间 isolated
6. **HookAction 0-4 全部落 atomic.Int32 区间** — 无 enum 溢出风险
7. **state.go `[13]TerminalReason{}` 编译期不变量数组**保持，本 feature 不引入新 TerminalReason
8. **budget 包不 import credit 包** — 避免循环依赖；接口 `budget.AdminTestConsumer` 注入到 credit
9. **0 prod 影响**：config_prod.yaml 0 diff / 不打 git tag / 不调 /deploy-prod / feature 分支 pre-push hook 拦 / migration SQL 不在 dev/prod CI 自动跑 / 不动 prod SSH + 环境变量

---

## §2 数据模型

### §2.1 credit_admin_test_grant 新表

```sql
CREATE TABLE credit_admin_test_grant (
    id                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    parent_user_id    INT UNSIGNED NOT NULL COMMENT 'B2B 父账户 user.id（独立账户也算父，即 parent_user_id = self.id）',
    granted_amount    INT UNSIGNED NOT NULL DEFAULT 5000 COMMENT '当月赠送积分（运营可调）',
    used_amount       INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '当月已用积分',
    remaining_amount  INT GENERATED ALWAYS AS (CAST(granted_amount AS SIGNED) - CAST(used_amount AS SIGNED)) STORED COMMENT '剩余积分（生成列，覆盖索引避免回表）',
    period_start      DATE NOT NULL COMMENT '当月起始日（YYYY-MM-01）',
    period_end        DATE NOT NULL COMMENT '当月最后一天（月底失效）',
    granted_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '入账时间',
    last_used_at      DATETIME NULL COMMENT '最近一次试聊扣费时间',
    UNIQUE KEY uq_parent_period (parent_user_id, period_start),
    INDEX idx_period_remaining (period_end, remaining_amount)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='Agent 模式 #12 — 配置者试聊独立测试配额（每月赠送 5000，月底作废，不累积）';
```

**字段说明**：
- `parent_user_id` INT UNSIGNED — 对齐 user.id 类型（与 #5 一致）
- `granted_amount` UNSIGNED — 不为负，运营手动 SQL UPDATE 调整
- `used_amount` UNSIGNED — 累计已用，refund 时 -=（要求 used_amount >= refund，否则 SIGNED 减法防止下溢，故 generated column 用 CAST AS SIGNED）
- `remaining_amount` 生成列 — 始终 `granted - used`；UNSIGNED 减法可能下溢，故内部 CAST 为 SIGNED（remaining 允许暂时为负即 used > granted，但业务路径不允许，由代码层保护）
- `period_start` / `period_end` 不带时区 — UTC 计算（与 credit_cycle 一致）。S2 决策：所有 period 计算用 `time.UTC` （与 MembershipService.GetBalance(now) 一致）
- `last_used_at` 可空 — 首次试聊前为 NULL

**GORM model 同步**（`internal/pkg/model/credit_admin_test_grant.go`）：

```go
package model

import "time"

// CreditAdminTestGrant — Agent 模式 #12 试聊配额表
type CreditAdminTestGrant struct {
    ID              uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    ParentUserID    uint      `gorm:"column:parent_user_id;not null;uniqueIndex:uq_parent_period,priority:1" json:"parent_user_id"`
    GrantedAmount   uint32    `gorm:"column:granted_amount;type:int unsigned;not null;default:5000" json:"granted_amount"`
    UsedAmount      uint32    `gorm:"column:used_amount;type:int unsigned;not null;default:0" json:"used_amount"`
    // RemainingAmount 是 DB 生成列；GORM 标 "->;type:int as ..." read-only。
    // SQLite 测试场景下不依赖 generated column — 用 Remaining() 方法在 Go 层计算。
    RemainingAmount int32     `gorm:"column:remaining_amount;->;type:int GENERATED ALWAYS AS (CAST(granted_amount AS SIGNED) - CAST(used_amount AS SIGNED)) STORED" json:"remaining_amount"`
    PeriodStart     time.Time `gorm:"column:period_start;type:date;not null;uniqueIndex:uq_parent_period,priority:2;index:idx_period_remaining,priority:1" json:"period_start"`
    PeriodEnd       time.Time `gorm:"column:period_end;type:date;not null" json:"period_end"`
    GrantedAt       time.Time `gorm:"column:granted_at;type:datetime;not null;default:CURRENT_TIMESTAMP" json:"granted_at"`
    LastUsedAt      *time.Time `gorm:"column:last_used_at;type:datetime" json:"last_used_at,omitempty"`
}

func (CreditAdminTestGrant) TableName() string { return "credit_admin_test_grant" }

// Remaining returns the Go-computed remaining amount (safe for SQLite tests where
// generated columns may not be populated by AutoMigrate).
func (g *CreditAdminTestGrant) Remaining() int64 {
    return int64(g.GrantedAmount) - int64(g.UsedAmount)
}
```

**SQLite 兼容**（GORM v2 + sqlite driver）：
- AutoMigrate 解析 GORM tag 时遇到 `->;` 前缀 → SQLite 后端忽略 GENERATED ALWAYS AS 部分，回退为普通列
- 测试代码不依赖 DB generated 行为，调 `grant.Remaining()` Go 计算
- prod MySQL 8.0.30+ 走 DB 生成列，索引可覆盖

### §2.2 credit_transaction.source_type CHECK ALTER（双 migration）

**UP migration**：

```sql
-- migrations/20260521_140000_agent_billing_source_type_admin_test.sql
-- Up: extend chk_ct_source_type to include 'admin_test' (Agent #12)

-- Pre-check: ensure no rows would violate new CHECK
SELECT
  'pre_check_no_invalid_source_type' AS check_name,
  COUNT(*) AS invalid_rows
FROM credit_transaction
WHERE source_type NOT IN ('trial', 'subscription', 'cycle', 'booster', 'admin', 'system', 'admin_test')
  AND source_type IS NOT NULL;
-- Expected: 0

-- ALTER: drop and re-add with new enum
ALTER TABLE credit_transaction DROP CONSTRAINT chk_ct_source_type;
ALTER TABLE credit_transaction
  ADD CONSTRAINT chk_ct_source_type
  CHECK (source_type IN ('trial', 'subscription', 'cycle', 'booster', 'admin', 'system', 'admin_test')
         OR source_type IS NULL);
```

**DOWN migration**（rollback）：

```sql
-- migrations/20260521_140000_agent_billing_source_type_admin_test_rollback.sql
-- Down: revert chk_ct_source_type to original 6 enum

-- Pre-check: ensure no admin_test rows would orphan
SELECT
  'rollback_check_no_admin_test_rows' AS check_name,
  COUNT(*) AS orphan_rows
FROM credit_transaction
WHERE source_type = 'admin_test';
-- Expected: 0 (else rollback would block these rows)

ALTER TABLE credit_transaction DROP CONSTRAINT chk_ct_source_type;
ALTER TABLE credit_transaction
  ADD CONSTRAINT chk_ct_source_type
  CHECK (source_type IN ('trial', 'subscription', 'cycle', 'booster', 'admin', 'system')
         OR source_type IS NULL);
```

**MySQL 8 ALTER CHECK 行为**：MySQL 8.0.16+ ALTER CHECK 是 instant DDL，不锁表数据；预期 prod 执行 < 1s。SQLite 不强制 CHECK constraint，pre-check 在 SQLite 永远 OK。

### §2.3 agent_run.terminal_metadata 字段 ALTER ADD

**UP migration**：

```sql
-- migrations/20260521_140100_agent_run_terminal_metadata.sql
-- Up: add terminal_metadata JSON column to agent_run for #12 BudgetExceeded detail

ALTER TABLE agent_run
  ADD COLUMN terminal_metadata JSON NULL COMMENT 'Terminal 时机的结构化元数据（如 budget_dimension）'
  AFTER state_reason;
```

**DOWN migration**：

```sql
-- migrations/20260521_140100_agent_run_terminal_metadata_rollback.sql
-- Down: drop terminal_metadata column

ALTER TABLE agent_run DROP COLUMN terminal_metadata;
```

**`internal/pkg/model/agent_run.go` 改动**：

```go
type AgentRun struct {
    ID            uint64         `gorm:"primaryKey;autoIncrement" json:"id"`
    UserID        uint           `gorm:"not null;index:idx_ar_user_started" json:"user_id"`
    SessionID     string         `gorm:"size:64;index:idx_ar_session" json:"session_id"`
    Status        string         `gorm:"size:20;not null;default:'running';index:idx_ar_status_started" json:"status"`
    StateReason   string         `gorm:"size:50" json:"state_reason,omitempty"`
    // ↓ 本 feature 新增
    TerminalMetadata datatypes.JSON `gorm:"type:json" json:"terminal_metadata,omitempty"`
    Messages         datatypes.JSON `gorm:"type:json;not null" json:"messages"`
    ReservationID    *uint64        `json:"reservation_id,omitempty"`
    StartedAt        time.Time      `gorm:"type:datetime(3);not null;index:idx_ar_user_started;index:idx_ar_status_started" json:"started_at"`
    EndedAt          *time.Time     `gorm:"type:datetime(3)" json:"ended_at,omitempty"`
    CompactState     datatypes.JSON `gorm:"type:json" json:"compact_state,omitempty"`
    CompactSummary   string         `gorm:"type:longtext" json:"compact_summary,omitempty"`
    CreatedAt        time.Time      `gorm:"type:datetime(3);autoCreateTime" json:"created_at"`
    UpdatedAt        time.Time      `gorm:"type:datetime(3);autoUpdateTime" json:"updated_at"`
}
```

**store 加方法**（`internal/numind/store/agent_run.go`）：

```go
// IAgentRunStore 加
UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error

// 实现
func (s *agentRunStore) UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error {
    result := s.db.WithContext(ctx).Model(&model.AgentRun{}).
        Where("id = ?", id).
        Update("terminal_metadata", metadata)
    if result.Error != nil {
        return fmt.Errorf("agentRunStore.UpdateTerminalMetadata(id=%d): %w", id, result.Error)
    }
    if result.RowsAffected == 0 {
        return fmt.Errorf("agentRunStore.UpdateTerminalMetadata: no row matched id=%d", id)
    }
    return nil
}
```

---

## §3 包架构 + Import 方向

```
┌──────────────────────────────────────────────────────────────┐
│ controller/v1/credit/credit.go                                │
│   GetBalance → biz.ICreditService.GetBalance                  │
└──────────────────┬───────────────────────────────────────────┘
                   ▼
┌──────────────────────────────────────────────────────────────┐
│ biz/credit/                                                   │
│   ICreditService 加方法 ReserveAgentTest / ReconcileAgentTest │
│   creditsImpl 持 BudgetAdminTestConsumer 字段                  │
│   GetBalance 扩展返回 AdminTestPool                            │
└────────────┬─────────────────────────────────────────────────┘
             │ depends on (注入)
             ▼
┌──────────────────────────────────────────────────────────────┐
│ biz/budget/                                                   │
│   - tracker.go      BudgetTracker interface + 实现             │
│   - dimensions.go   4 维 Dimension enum                        │
│   - gate.go         BudgetGate 顶层入口 + WrapHooks            │
│   - r2_estimator.go R2 估算 wrapper                             │
│   - admin_test.go   AdminTestConsumer interface + 实现         │
│   - types.go        AdminTestStatus / BudgetExceededDetail     │
│   - errno.go        ErrAdminTestExhausted / ErrBudgetExceeded  │
└────────────┬─────────────────────────────────────────────────┘
             │ depends on
             ▼
┌──────────────────────────────────────────────────────────────┐
│ store/                                                        │
│   - credit_admin_test_grant.go  IAdminTestGrantStore           │
│   - agent_run.go                IAgentRunStore.UpdateTerminalMetadata │
│   - credit_transaction（既有，本 feature 不改 store 方法签名） │
└──────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────┐
│ biz/agent/runner.go                                           │
│   - WithBudgetTracker option                                  │
│   - Run() 主流程加 BudgetTracker.Start/Close + terminal_metadata 写入 │
│   - state.go LoopEventErrorMaxBudget 新增                      │
│   - hooks.go HookActionBudgetExceeded=4 新增                   │
└──────────────────────────────────────────────────────────────┘
             ↑ depends on
┌──────────────────────────────────────────────────────────────┐
│ biz.go wire                                                   │
│   sandbox.AsRunHooks                                          │
│      → budget.WrapHooks(base, budgetGate)                     │
│         → permission.WrapHooks(hooks1, permGate)              │
│   inject all into agent.NewAgentRunner                        │
└──────────────────────────────────────────────────────────────┘
```

**单向 import 规则**：
- `biz/credit` → `biz/budget`（注入 AdminTestConsumer 接口）
- `biz/budget` → `store`（直接写 credit_admin_test_grant / credit_transaction）
- `biz/budget` ✕ `biz/credit` — **禁止反向**（若 budget 需要 credit 类型，把类型移到 budget/types.go 自己定义）
- `biz/agent` → `biz/budget`（WrapHooks + 接口）

**编译期验证**：S4 实施时跑 `go vet ./...` + `goimports` 静态分析 import 树是否单向。

---

## §4 核心接口签名（完整）

### §4.1 biz/budget/tracker.go

```go
package budget

import (
    "context"
    "sync"
    "time"
)

// BudgetTracker tracks the 4-dimensional budget state per agent run.
// All methods are safe for concurrent use within a single Run (single-threaded loop),
// and for concurrent Runs (different runIDs use disjoint state slots).
type BudgetTracker interface {
    // Start initializes the per-Run state with the given limits.
    // Called from runner.Run before the main loop.
    Start(ctx context.Context, runID uint64, limits Limits)

    // Close releases the per-Run state slot. Idempotent (calling twice OK).
    // Called via defer in runner.Run.
    Close(runID uint64)

    // RecordStep increments the turn counter for runID.
    // Called from runner.Run main loop before each LLM call.
    RecordStep(ctx context.Context, runID uint64)

    // CanProceed checks all 4 dimensions against limits.
    // Called from PreToolCall hook (and from runner.Run main loop before LLM calls).
    // Returns exceeded=true with the offending dimension when any limit is hit.
    CanProceed(ctx context.Context, runID uint64) (exceeded bool, dim Dimension, detail map[string]any)

    // RecordUsage adds tokens to the credit counter (post-LLM-call accounting).
    // tokens = prompt + completion tokens from the LLM response.
    // Called from PostToolCall hook (or runner.Run after LLM call).
    RecordUsage(ctx context.Context, runID uint64, tokens int)

    // Snapshot returns the current 4-dim state (for audit / debug).
    Snapshot(ctx context.Context, runID uint64) Snapshot
}

// Limits is the 4-dimensional budget configuration passed to Start.
type Limits struct {
    MaxTurns        int           // default 50; from agent_definition.max_turns_per_run or default
    MaxCredits      int64         // from agent_definition.credit_cap_per_session (×coefficient for R2)
    MaxWallTime     time.Duration // default 300s
    MaxDailyCredits int64         // from agent_definition.daily_credit_cap or default 2000
}

// Snapshot is the runtime view of a Run's budget counters.
type Snapshot struct {
    Turns         int
    Credits       int64
    Elapsed       time.Duration
    DailyCredits  int64
    Limits        Limits
    StartedAt     time.Time
}

// budgetTracker is the in-memory implementation.
// TODO(#14): replace dailyCache with Redis INCRBY when prod becomes multi-instance.
type budgetTracker struct {
    mu     sync.RWMutex
    states map[uint64]*runState // keyed by runID

    // dailyCache 跨 Run 日累计（in-memory cache，30s lazy sync to DB）
    // map keyed by userID → struct{Date string; Used int64; ...}
    dailyCache *dailyAggregateCache

    store IBudgetStore // 注入：写 daily aggregate (#14 backed by Redis)
}

type runState struct {
    Limits     Limits
    StartedAt  time.Time
    TurnCount  int
    CreditUsed int64
    UserID     uint
}

// NewTracker constructs a fresh in-memory BudgetTracker.
// store 可为 nil（v1 dev 环境）— RecordUsage 不持久化 daily aggregate，仅 in-memory。
func NewTracker(store IBudgetStore) BudgetTracker {
    return &budgetTracker{
        states:     make(map[uint64]*runState),
        dailyCache: newDailyAggregateCache(),
        store:      store,
    }
}
```

### §4.2 biz/budget/dimensions.go

```go
package budget

// Dimension labels which axis of the 4-dim budget tripped a CanProceed=exceeded.
type Dimension string

const (
    DimMaxTurns        Dimension = "max_turns"
    DimMaxCredits      Dimension = "max_credits"
    DimMaxWallTime     Dimension = "max_wall_time"
    DimMaxDailyCredits Dimension = "max_daily_credits"
)

// DefaultLimits returns the 4-dim defaults used when agent_definition fields are zero.
func DefaultLimits() Limits {
    return Limits{
        MaxTurns:        50,
        MaxCredits:      800,
        MaxWallTime:     300 * time.Second,
        MaxDailyCredits: 2000,
    }
}

// LimitsFromAgentDef reads limits from agent_definition row fields.
// Zero / nil values fall back to DefaultLimits (so callers don't need to handle nil-or-zero).
//
// 实际字段类型（model.AgentDefinition #5 落地）：
//   - CreditCapPerSession *uint
//   - DailyCreditCap      *uint
// 必须做 nil-pointer 守护。
func LimitsFromAgentDef(ad *model.AgentDefinition) Limits {
    d := DefaultLimits()
    if ad == nil {
        return d
    }
    if ad.CreditCapPerSession != nil && *ad.CreditCapPerSession > 0 {
        d.MaxCredits = int64(*ad.CreditCapPerSession)
    }
    if ad.DailyCreditCap != nil && *ad.DailyCreditCap > 0 {
        d.MaxDailyCredits = int64(*ad.DailyCreditCap)
    }
    // MaxTurns: agent_definition 表 #5 未引入 max_turns_per_run 字段；
    // v1 走 DefaultLimits 的 50；后续 feature 决策是否落地字段。
    return d
}
```

> S2 决策：`agent_definition.max_turns_per_run` 字段在 #5 落地时**未引入**（仅 daily_credit_cap + credit_cap_per_session）。本 feature 也**不**新加该字段；MaxTurns 走 DefaultLimits 的 50。`LimitsFromAgentDef` 不引用不存在字段（防编译失败）。

### §4.3 biz/budget/gate.go

```go
package budget

import (
    "context"
    "encoding/json"

    "github.com/cloudwego/eino/components/tool"

    "numind-server/internal/numind/biz/agent"
    "numind-server/internal/pkg/log"
)

// BudgetGate is the top-level entry the hook layer calls into.
type BudgetGate struct {
    tracker        BudgetTracker
    adminConsumer  AdminTestConsumer
    runStore       IBudgetRunStore // small subset of IAgentRunStore — write terminal_metadata
}

func NewBudgetGate(t BudgetTracker, a AdminTestConsumer, rs IBudgetRunStore) *BudgetGate {
    return &BudgetGate{tracker: t, adminConsumer: a, runStore: rs}
}

// WrapHooks decorates the base hooks with budget checks.
//
// PreToolCall order:
//   1. tracker.CanProceed(runID) → exceeded? → Record(HookActionBudgetExceeded) + sink + 短路
//   2. allow → forward to base.PreToolCall
//
// PostToolCall order:
//   1. base.PostToolCall (run + return)
//   2. tracker.RecordUsage(runID, tokens) — tokens extracted from ctx (set by aiservice adapter)
func (g *BudgetGate) WrapHooks(base *agent.RunHooks) *agent.RunHooks {
    return &agent.RunHooks{
        PreToolCall: func(ctx context.Context, t tool.BaseTool, input string) (agent.HookAction, error) {
            runID := agent.RunIDFromContext(ctx)
            if runID == 0 {
                return forwardPre(ctx, base, t, input)
            }
            exceeded, dim, detail := g.tracker.CanProceed(ctx, runID)
            if exceeded {
                if reg := registryFromBase(base); reg != nil {
                    reg.Record(agent.HookActionBudgetExceeded)
                }
                // 写 terminal_metadata（async；不阻塞 hook 返回）
                go g.writeTerminalMetadata(ctx, runID, dim, detail)
                return agent.HookActionBudgetExceeded, nil
            }
            return forwardPre(ctx, base, t, input)
        },
        PostToolCall: func(ctx context.Context, t tool.BaseTool, output string, err error) (agent.HookAction, error) {
            // 1. forward to base first（sandbox 关容器 / 写日志）
            action, baseErr := forwardPost(ctx, base, t, output, err)
            // 2. record usage（不阻塞 base 返回）
            runID := agent.RunIDFromContext(ctx)
            if runID != 0 {
                tokens := tokensFromCtxOrOutput(ctx, output)
                if tokens > 0 {
                    g.tracker.RecordUsage(ctx, runID, tokens)
                }
            }
            return action, baseErr
        },
        Registry: registryFromBase(base),
        // PRESERVE NarrationProvider / NarrationRunID from base
        NarrationProvider: narrationProviderFromBase(base),
        NarrationRunID:    narrationRunIDFromBase(base),
    }
}

func forwardPre(ctx context.Context, base *agent.RunHooks, t tool.BaseTool, input string) (agent.HookAction, error) {
    if base != nil && base.PreToolCall != nil {
        return base.PreToolCall(ctx, t, input)
    }
    return agent.HookActionContinue, nil
}

func forwardPost(ctx context.Context, base *agent.RunHooks, t tool.BaseTool, output string, err error) (agent.HookAction, error) {
    if base != nil && base.PostToolCall != nil {
        return base.PostToolCall(ctx, t, output, err)
    }
    return agent.HookActionContinue, nil
}

func registryFromBase(base *agent.RunHooks) *agent.HookActionRegistry {
    if base == nil {
        return nil
    }
    return base.Registry
}

func (g *BudgetGate) writeTerminalMetadata(ctx context.Context, runID uint64, dim Dimension, detail map[string]any) {
    if g.runStore == nil {
        return
    }
    meta := map[string]any{
        "budget_dimension": string(dim),
    }
    for k, v := range detail {
        meta[k] = v
    }
    b, err := json.Marshal(meta)
    if err != nil {
        log.Warnw("BudgetGate.writeTerminalMetadata: marshal failed", "agent_run_id", runID, "error", err)
        return
    }
    if err := g.runStore.UpdateTerminalMetadata(ctx, runID, datatypes.JSON(b)); err != nil {
        log.Warnw("BudgetGate.writeTerminalMetadata: update failed",
            "agent_run_id", runID, "dim", dim, "error", err)
    }
}

// IBudgetRunStore is the minimal subset of IAgentRunStore needed by BudgetGate.
// 签名与 IAgentRunStore.UpdateTerminalMetadata 完全一致（datatypes.JSON），避免 adapter 类型转换。
type IBudgetRunStore interface {
    UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error
}
```

注意：`WrapHooks` 返回的 hooks 必须**保留** `NarrationProvider` / `NarrationRunID` 字段，避免被外层 permission.WrapHooks（#6 已落地）再包一层时丢失。本 feature 同步在 spec §11 列出"#6 顺手补丁"项 —— `permission.WrapHooks` 也补这两字段透传（属于 #6 既有 P2 修补，本 feature 顺手 inline fix）。

### §4.4 biz/budget/admin_test.go

```go
package budget

import (
    "context"
    "fmt"
    "time"

    "gorm.io/gorm"

    "numind-server/internal/numind/store"
    "numind-server/internal/pkg/model"
)

// AdminTestConsumer governs the credit_admin_test_grant pool used by parent users
// in Agent Builder Modal "试聊" path.
type AdminTestConsumer interface {
    // Consume reserves `amount` from the parent's current-month grant.
    // Lazy-creates the grant row if absent (idempotent via UNIQUE KEY uq_parent_period).
    // Returns ErrAdminTestExhausted when remaining < amount.
    Consume(ctx context.Context, parentUserID uint, amount int64) (txID uint64, err error)

    // Refund decreases used_amount for the given parent / txID.
    // Idempotent — calling twice with the same txID is OK.
    Refund(ctx context.Context, parentUserID uint, txID uint64, refundAmount int64) error

    // Status returns the current-month grant state (lazy-creates if absent).
    // GetBalance UI uses this to render AdminTestPool.
    Status(ctx context.Context, parentUserID uint, now time.Time) (*AdminTestStatus, error)
}

// adminTestConsumer is the GORM-backed implementation.
type adminTestConsumer struct {
    s store.IStore
}

func NewAdminTestConsumer(s store.IStore) AdminTestConsumer {
    return &adminTestConsumer{s: s}
}

func (c *adminTestConsumer) Consume(ctx context.Context, parentUserID uint, amount int64) (uint64, error) {
    var txID uint64
    err := c.s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        periodStart, periodEnd := currentMonthBoundaries(time.Now().UTC())

        // 1. Lazy-create grant row (idempotent via uq_parent_period)
        // 注意：GrantedAmount 类型是 uint32，DefaultAdminTestGrant 已是 uint32（见下方常量定义）
        grant := &model.CreditAdminTestGrant{
            ParentUserID:  parentUserID,
            GrantedAmount: DefaultAdminTestGrant,  // uint32(5000)
            UsedAmount:    0,
            PeriodStart:   periodStart,
            PeriodEnd:     periodEnd,
        }
        if err := tx.Clauses(clause.OnConflict{
            Columns:   []clause.Column{{Name: "parent_user_id"}, {Name: "period_start"}},
            DoNothing: true,
        }).Create(grant).Error; err != nil {
            return fmt.Errorf("AdminTestConsumer.Consume: create grant: %w", err)
        }
        // Re-fetch with row lock
        if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
            Where("parent_user_id = ? AND period_start = ?", parentUserID, periodStart).
            First(grant).Error; err != nil {
            return fmt.Errorf("AdminTestConsumer.Consume: lock grant: %w", err)
        }

        // 2. Check remaining
        if grant.Remaining() < amount {
            return ErrAdminTestExhausted
        }

        // 3. Update used_amount + last_used_at
        now := time.Now()
        if err := tx.Model(grant).Updates(map[string]any{
            "used_amount":  grant.UsedAmount + uint32(amount),
            "last_used_at": now,
        }).Error; err != nil {
            return fmt.Errorf("AdminTestConsumer.Consume: update grant: %w", err)
        }

        // 4. INSERT credit_transaction (source_type='admin_test')
        sourceType := "admin_test"
        rec := &model.CreditTransaction{
            UserID:     parentUserID,
            Amount:     -amount,
            SourceType: &sourceType,
            SourceID:   nil,  // admin_test 池没有外键 ID（独立池）
            Operation:  "agent_test_reserve",
            BizRefType: "admin_test",
            BizRefID:   fmt.Sprintf("grant_%d", grant.ID),
        }
        if err := tx.Create(rec).Error; err != nil {
            return fmt.Errorf("AdminTestConsumer.Consume: insert tx: %w", err)
        }
        txID = rec.ID
        return nil
    })
    return txID, err
}

func (c *adminTestConsumer) Refund(ctx context.Context, parentUserID uint, txID uint64, refundAmount int64) error {
    return c.s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        // 1. fetch original tx
        var orig model.CreditTransaction
        if err := tx.Where("id = ?", txID).First(&orig).Error; err != nil {
            return fmt.Errorf("AdminTestConsumer.Refund: fetch tx %d: %w", txID, err)
        }
        if orig.UserID != parentUserID {
            return fmt.Errorf("AdminTestConsumer.Refund: tx user mismatch (expected %d, got %d)", parentUserID, orig.UserID)
        }
        if orig.SourceType == nil || *orig.SourceType != "admin_test" {
            return fmt.Errorf("AdminTestConsumer.Refund: tx source_type not admin_test")
        }

        // 2. lock current-month grant
        periodStart, _ := currentMonthBoundaries(time.Now().UTC())
        var grant model.CreditAdminTestGrant
        if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
            Where("parent_user_id = ? AND period_start = ?", parentUserID, periodStart).
            First(&grant).Error; err != nil {
            return fmt.Errorf("AdminTestConsumer.Refund: lock grant: %w", err)
        }

        // 3. cap refund to current used_amount (safety: never go below 0)
        cap := int64(grant.UsedAmount)
        if refundAmount > cap {
            refundAmount = cap
        }
        if refundAmount <= 0 {
            return nil // nothing to refund
        }

        // 4. Update used_amount
        if err := tx.Model(&grant).Update("used_amount", uint32(int64(grant.UsedAmount)-refundAmount)).Error; err != nil {
            return fmt.Errorf("AdminTestConsumer.Refund: update grant: %w", err)
        }

        // 5. INSERT credit_transaction (+refund)
        sourceType := "admin_test"
        rec := &model.CreditTransaction{
            UserID:     parentUserID,
            Amount:     refundAmount,
            SourceType: &sourceType,
            SourceID:   nil,
            Operation:  "agent_test_refund",
            BizRefType: "admin_test",
            BizRefID:   fmt.Sprintf("tx_%d", txID),
        }
        return tx.Create(rec).Error
    })
}

func (c *adminTestConsumer) Status(ctx context.Context, parentUserID uint, now time.Time) (*AdminTestStatus, error) {
    periodStart, periodEnd := currentMonthBoundaries(now.UTC())
    var grant model.CreditAdminTestGrant
    err := c.s.DB().WithContext(ctx).
        Where("parent_user_id = ? AND period_start = ?", parentUserID, periodStart).
        First(&grant).Error
    if errors.Is(err, gorm.ErrRecordNotFound) {
        // No grant yet this month — treat as freshly granted defaults
        return &AdminTestStatus{
            Granted:      DefaultAdminTestGrantInt64,
            Used:         0,
            Remaining:    DefaultAdminTestGrantInt64,
            PeriodStart:  periodStart,
            PeriodEnd:    periodEnd,
            DaysToExpire: daysUntil(periodEnd, now),
        }, nil
    }
    if err != nil {
        return nil, fmt.Errorf("AdminTestConsumer.Status: %w", err)
    }
    return &AdminTestStatus{
        Granted:      int64(grant.GrantedAmount),
        Used:         int64(grant.UsedAmount),
        Remaining:    grant.Remaining(),
        PeriodStart:  grant.PeriodStart,
        PeriodEnd:    grant.PeriodEnd,
        DaysToExpire: daysUntil(grant.PeriodEnd, now),
    }, nil
}

// currentMonthBoundaries returns the first and last day of the month containing now (UTC).
// Both are time.Date values with H=M=S=0.
func currentMonthBoundaries(now time.Time) (start, end time.Time) {
    y, m, _ := now.Date()
    start = time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
    end = start.AddDate(0, 1, -1)  // last day of this month
    return
}

func daysUntil(target, now time.Time) int {
    d := target.Sub(now) / (24 * time.Hour)
    if d < 0 {
        return 0
    }
    return int(d)
}

// DefaultAdminTestGrant — uint32 类型以匹配 model.CreditAdminTestGrant.GrantedAmount 字段类型
const DefaultAdminTestGrant uint32 = 5000

// DefaultAdminTestGrantInt64 — 同一值的 int64 形式，给 Status 返回 / 余额 API 用
const DefaultAdminTestGrantInt64 int64 = 5000
```

### §4.5 biz/budget/r2_estimator.go

```go
package budget

import (
    "context"
    "fmt"

    "numind-server/internal/pkg/pricing"
)

// AgentTurnEstimate is the per-turn R2 estimate.
type AgentTurnEstimate struct {
    EstimatedPromptTokens     int
    EstimatedCompletionTokens int
    EstimatedCredits          int64 // cost cents == credits
    Model                     string
    Provider                  string
}

// EstimateAgentTurn estimates a single Agent turn's credit cost.
// promptCharCount is the user-supplied input + system prompt char count.
// completionEstimate is the conservative upper-bound on completion tokens (default 500).
func EstimateAgentTurn(ctx context.Context, pc pricing.ICalculator,
    provider, model string, promptCharCount, completionEstimate int) (*AgentTurnEstimate, error) {
    if completionEstimate <= 0 {
        completionEstimate = DefaultCompletionEstimate
    }

    // CN/EN mixed: 1 token ≈ 2 chars (conservative)
    estPromptTokens := promptCharCount / 2
    if estPromptTokens < 1 {
        estPromptTokens = 1
    }

    if pc == nil {
        return nil, fmt.Errorf("EstimateAgentTurn: pricing calculator is nil")
    }
    costCents, err := pc.CalculateCost(ctx, "llm_chat", provider, model, estPromptTokens, completionEstimate)
    if err != nil {
        return nil, fmt.Errorf("EstimateAgentTurn: CalculateCost: %w", err)
    }
    return &AgentTurnEstimate{
        EstimatedPromptTokens:     estPromptTokens,
        EstimatedCompletionTokens: completionEstimate,
        EstimatedCredits:          costCents,
        Model:                     model,
        Provider:                  provider,
    }, nil
}

const DefaultCompletionEstimate = 500
```

### §4.6 biz/budget/types.go + errno.go

```go
// types.go
package budget

import "time"

type AdminTestStatus struct {
    Granted      int64
    Used         int64
    Remaining    int64
    PeriodStart  time.Time
    PeriodEnd    time.Time
    DaysToExpire int
}

type BudgetExceededDetail struct {
    Dimension Dimension
    Used      int64
    Limit     int64
}

// errno.go — package-local sentinel errors（与全局 errno.Errno 分离）
package budget

import "errors"

var (
    ErrAdminTestExhausted = errors.New("admin_test pool exhausted for parent user this month")
    ErrBudgetExceeded     = errors.New("budget tracker dimension exceeded")
)
```

**全局 errno 包扩展**（`internal/pkg/errno/credits.go` 加新 errno）：

```go
// ErrAdminTestExhausted 配置者试聊本月独立测试配额已用完（#12 agent-mode-billing-integration）
// HTTP 429（Too Many Requests）— 不允许 fallback 到正式积分
ErrAdminTestExhausted = &Errno{HTTP: 429, Code: "Credits.AdminTestExhausted", Message: "本月测试配额已用完，请等待下月刷新"}
```

`creditService.ReserveAgentTest` 内 `errno.ErrAdminTestExhausted.SetMessage(...)` 走全局 errno；biz/budget 包内部用 sentinel `errors.New` 走 `errors.Is` 判定。两者通过 `errors.Is` 桥接。

### §4.7 biz/credit 改造

```go
// types.go 加字段（向后兼容）
type BalanceBreakdown struct {
    // ... 既有 9 字段不变
    AdminTestPool *AdminTestPoolView `json:"admin_test_pool,omitempty"`
}

type AdminTestPoolView struct {
    Granted      int64  `json:"granted"`
    Used         int64  `json:"used"`
    Remaining    int64  `json:"remaining"`
    PeriodEnd    string `json:"period_end"`
    DaysToExpire int    `json:"days_to_expire"`
}

// credit_service.go 加字段 / 方法

type ICreditService interface {
    // ... 既有 8 方法不变
    ReserveAgentTest(ctx context.Context, parentUser *model.User, estimated int64, idempotencyKey *string) (*Reservation, error)
    ReconcileAgentTest(ctx context.Context, reservationID uint64, actualCostCents int64) error
}

// 现有 creditService struct（credit_service.go:47-53）字段名是 `store`（不是 `ds`）。
// 本 feature **保持既有字段名**，仅追加 adminConsumer 字段：
type creditService struct {
    store         store.IStore           // existing
    biz           ICreditBiz             // existing
    pricing       pricing.ICalculator    // existing
    membershipSvc *membership.MembershipService // existing
    adminConsumer budget.AdminTestConsumer // ← 新增字段 (#12)
    credits       *creditsImpl           // existing
}

// NewCreditService 既有签名 4 参数；本 feature 通过 functional option pattern 注入
// adminConsumer，避免改既有 callers 签名（biz.go wire 改）。
//
// 决策：v1 走 setter 而非新增参数 — 避免改 4 个既有 caller。
func (s *creditService) SetAdminTestConsumer(c budget.AdminTestConsumer) {
    s.adminConsumer = c
}

func (s *creditService) ReserveAgentTest(ctx context.Context, parentUser *model.User, estimated int64, idempotencyKey *string) (*Reservation, error) {
    if s.adminConsumer == nil {
        return nil, errno.ErrInternalDBError.SetMessage("admin_test consumer not wired")
    }
    txID, err := s.adminConsumer.Consume(ctx, parentUser.ID, estimated)
    if err != nil {
        if errors.Is(err, budget.ErrAdminTestExhausted) {
            return nil, errno.ErrAdminTestExhausted.SetMessage("本月测试配额已用完，请等待下月刷新")
        }
        return nil, fmt.Errorf("ReserveAgentTest: %w", err)
    }
    // Insert minimal credit_reservation row for audit + Reconcile linkage
    // 实际 CreditReservation 字段（credit_reservation.go:10）：
    //   ID / UserID / Operation / Status / ReservedCredits / IdempotencyKey
    //   ActualCostCents *int64 / Delta *int64 / EstimationSource string / ...
    //   ReferenceType / ReferenceID — 存在于 credit_reservation 表（既有）
    rsv := &model.CreditReservation{
        UserID:           parentUser.ID,
        Operation:        "agent_test",
        ReferenceType:    "agent_test",
        ReferenceID:      fmt.Sprintf("admin_test_tx:%d", txID),
        ReservedCredits:  estimated,
        EstimationSource: "agent_test",
        Status:           "reserved",
    }
    if idempotencyKey != nil {
        rsv.IdempotencyKey = idempotencyKey
    }
    if err := s.store.DB().WithContext(ctx).Create(rsv).Error; err != nil {
        return nil, fmt.Errorf("ReserveAgentTest: create reservation: %w", err)
    }
    return &Reservation{
        ID:              rsv.ID,
        UserID:          parentUser.ID,
        ReferenceType:   "agent_test",
        ReferenceID:     rsv.ReferenceID,
        Operation:       "agent_test",
        ReservedCredits: estimated,
        Status:          StatusReserved,
        CreatedAt:       rsv.CreatedAt,
    }, nil
}

func (s *creditService) ReconcileAgentTest(ctx context.Context, reservationID uint64, actualCostCents int64) error {
    // 1. fetch reservation
    var rsv model.CreditReservation
    if err := s.store.DB().WithContext(ctx).First(&rsv, reservationID).Error; err != nil {
        return fmt.Errorf("ReconcileAgentTest: fetch reservation %d: %w", reservationID, err)
    }
    if rsv.Operation != "agent_test" {
        return fmt.Errorf("ReconcileAgentTest: reservation %d is not agent_test (got %s)", reservationID, rsv.Operation)
    }
    if rsv.Status != "reserved" {
        return fmt.Errorf("ReconcileAgentTest: reservation %d already %s", reservationID, rsv.Status)
    }

    // 2. parse original tx ID from reference (admin_test_tx:N)
    var origTxID uint64
    if _, err := fmt.Sscanf(rsv.ReferenceID, "admin_test_tx:%d", &origTxID); err != nil {
        return fmt.Errorf("ReconcileAgentTest: parse ref ID: %w", err)
    }

    // 3. compute refund (reserved - actual)
    refund := rsv.ReservedCredits - actualCostCents
    if refund > 0 {
        if err := s.adminConsumer.Refund(ctx, rsv.UserID, origTxID, refund); err != nil {
            return fmt.Errorf("ReconcileAgentTest: refund: %w", err)
        }
    } else if refund < 0 {
        // Underestimated — top up by consuming more from grant
        topup := -refund
        if _, err := s.adminConsumer.Consume(ctx, rsv.UserID, topup); err != nil {
            return fmt.Errorf("ReconcileAgentTest: topup: %w", err)
        }
    }

    // 4. mark reservation reconciled
    return s.store.DB().WithContext(ctx).Model(&rsv).Updates(map[string]any{
        "status":             "reconciled",
        "actual_cost_cents":  actualCostCents,
        "delta":              actualCostCents - rsv.ReservedCredits,
        "reconciled_at":      time.Now().UTC(),
    }).Error
}

// GetBalance 扩展 — 既有路径不变，仅末尾加 AdminTestPool 填充
func (s *creditService) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
    bal, err := s.credits.GetBalance(ctx, user)
    if err != nil {
        return nil, err
    }
    // 父账户特例：parent_user_id == self.ID 时附加 AdminTestPool
    if isParentAccount(user) && s.adminConsumer != nil {
        status, err := s.adminConsumer.Status(ctx, user.ID, time.Now().UTC())
        if err != nil {
            // 不阻塞主路径，仅 warn
            log.Warnw("GetBalance: admin_test status fetch failed", "user_id", user.ID, "error", err)
        } else {
            bal.AdminTestPool = &AdminTestPoolView{
                Granted:      status.Granted,
                Used:         status.Used,
                Remaining:    status.Remaining,
                PeriodEnd:    status.PeriodEnd.Format("2006-01-02"),
                DaysToExpire: status.DaysToExpire,
            }
        }
    }
    return bal, nil
}

// isParentAccount returns true when user is a "parent" (B2B 父账户) in B2B2C model.
// Conservative v1 rule: user.ParentUserID == 0 (means top-level account).
func isParentAccount(u *model.User) bool {
    return u != nil && u.ParentUserID == nil
}
```

### §4.8 biz/agent 改动（hooks.go / state.go / runner.go）

**hooks.go**：

```go
// hooks.go
const (
    HookActionContinue        HookAction = iota // 0
    HookActionStop                              // 1
    HookActionBlockingStop                      // 2
    HookActionPermissionDeny                    // 3 — #6 落地
    HookActionBudgetExceeded                    // 4 — #12 本 feature 新增
)

// HookActionRegistry struct comment 同步更新：
// // 0=Continue 1=Stop 2=BlockingStop 3=PermissionDeny 4=BudgetExceeded

// HookActionToLoopEvent 加 case
func HookActionToLoopEvent(action HookAction) LoopEvent {
    switch action {
    case HookActionStop:
        return LoopEventHookActionStop
    case HookActionBlockingStop:
        return LoopEventHookActionBlockStop
    case HookActionPermissionDeny:
        return LoopEventPermissionDenied
    case HookActionBudgetExceeded:
        return LoopEventErrorMaxBudget
    default:
        return LoopEventInvalid
    }
}
```

**state.go**：

```go
// state.go LoopEvent 加 (#12)
LoopEventErrorMaxBudget   // 19 — NEW (#12 agent-mode-billing-integration)

// state.go Transition switch 加 case
case LoopEventErrorMaxBudget:
    s.TerminalReason = TerminalErrorMaxBudget
    return TerminalErrorMaxBudget, "", true
```

**runner.go**：

```go
// 新增 RunnerOption
func WithBudgetTracker(t budget.BudgetTracker) RunnerOption {
    return func(r *agentRunner) {
        r.budgetTracker = t
    }
}

// agentRunner 加字段
budgetTracker budget.BudgetTracker

// Run() 主流程改动（最小化）：
// 步骤 1.8: budget tracker init（在 #6 sink 创建之后、main loop 之前）
if r.budgetTracker != nil && ad != nil {
    limits := budget.LimitsFromAgentDef(ad)
    r.budgetTracker.Start(ctx, run.ID, limits)
    defer r.budgetTracker.Close(run.ID)
}

// Run() 末尾：处理 BudgetExceeded terminal_metadata（已由 BudgetGate.WrapHooks
// 异步写入 terminal_metadata 字段，runner.Run 不直接 write）
```

### §4.9 biz.go wire（biz/agent/biz.go 现状的最小化扩展）

```go
// biz.go 加 wire（在 hooks chain 装配处）
budgetTracker := budget.NewTracker(nil) // v1 dev: no IBudgetStore yet
adminConsumer := budget.NewAdminTestConsumer(s)
budgetGate := budget.NewBudgetGate(budgetTracker, adminConsumer, agentRunStoreAsBudgetRunStore(s.AgentRuns()))

// hooks chain（嵌套）
sandboxHooks := sandboxMgr.AsRunHooks()              // base — #4 落地
budgetWrapped := budgetGate.WrapHooks(sandboxHooks)  // 中层 — 本 feature
permWrapped := permission.WrapHooks(budgetWrapped, permGate) // 外层 — #6 落地

runner := agent.NewAgentRunner(s.AgentRuns(), toolRegistry,
    agent.WithDefaultHooks(permWrapped),
    agent.WithSkillStore(s.AgentDefinitions()),
    agent.WithCompactProvider(compactProvider),
    agent.WithNarrationProvider(narrationProvider),
    agent.WithMemoryProvider(memoryProvider),
    agent.WithBudgetTracker(budgetTracker), // ← 新增
)
```

`agentRunStoreAsBudgetRunStore` 是 adapter helper 把 `IAgentRunStore` 适配为 `IBudgetRunStore`：

```go
// biz.go 私有 helper
func agentRunStoreAsBudgetRunStore(s store.IAgentRunStore) budget.IBudgetRunStore {
    return &arsAdapter{s: s}
}

type arsAdapter struct{ s store.IAgentRunStore }

func (a *arsAdapter) UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error {
    return a.s.UpdateTerminalMetadata(ctx, id, metadata)
}
```

`BudgetGate.writeTerminalMetadata` 内部用 `json.Marshal([]byte)` 后转 `datatypes.JSON(b)` 调 store。

### §4.10 helper.go AutoMigrate

```go
return db.AutoMigrate(
    // ... 既有列表
    &model.CreditAdminTestGrant{},  // ← 本 feature 新增
)
```

---

## §5 测试矩阵

| 模块 | 测试类型 | 主要 case |
|---|---|---|
| `budget/tracker_test.go` | unit | Start/Close 状态隔离 / RecordStep 累加 / RecordUsage 累加 / CanProceed 4 维独立 trip / 并发 Run race-safe |
| `budget/dimensions_test.go` | unit | DefaultLimits 值 / LimitsFromAgentDef 0 fallback / 非 0 覆盖 |
| `budget/gate_test.go` | unit | WrapHooks PreToolCall exceeded 路径 / allow 透传 / PostToolCall RecordUsage 调用 / nil base 兜底 |
| `budget/r2_estimator_test.go` | unit | 字符数 → token 估算 / completion 默认 500 / nil calculator 报错 |
| `budget/admin_test_test.go` | unit (in-memory SQLite) | Consume lazy-create / Consume exhausted → ErrAdminTestExhausted / Refund cap to used_amount / Status no-grant 返回默认值 / 并发 Consume 互斥 |
| `credit/credit_service_admin_test.go` | unit | ReserveAgentTest 成功 / ReserveAgentTest 耗尽 / ReconcileAgentTest refund 路径 / ReconcileAgentTest topup 路径 / 双重 Reconcile 失败 |
| `credit/balance_admin_test_test.go` | unit | GetBalance 父账户附加 AdminTestPool / 子账户为 nil / adminConsumer.Status 错误 warn 不阻塞 |
| `agent/state_budget_test.go` | unit | Transition(LoopEventErrorMaxBudget) → TerminalErrorMaxBudget |
| `agent/hooks_budget_test.go` | unit | HookActionToLoopEvent(BudgetExceeded) → LoopEventErrorMaxBudget / atomic.Int32 边界（值 4 落 int32）|
| `agent/runner_budget_integration_test.go` | unit | WithBudgetTracker option / Start+Close 路径 / nil tracker 兜底 |
| `store/credit_admin_test_grant_test.go` | unit (in-memory SQLite) | Create / Get / FindOrCreate idempotent / UNIQUE KEY 幂等 |
| `store/agent_run_terminal_metadata_test.go` | unit | UpdateTerminalMetadata 写 + 读 / 不存在行报错 |

**Race detector 覆盖目标**：
- BudgetTracker 并发 Run（10 个 runID 同时 Start/RecordStep/CanProceed/Close）
- AdminTestConsumer.Consume 并发（同 parentUserID，期望串行化或一个成功 + 其他报 ErrAdminTestExhausted）

**覆盖率目标**：
- biz/budget overall ≥ 80%
- biz/credit 不下降（既有 ~ 85%）
- biz/agent 不下降（既有 ~ 80%）
- biz/membership 不下降（既有 ~ 85%）

---

## §6 与其他 feature 的交互

| feature | 交互 | 风险 |
|---|---|---|
| **#2 runtime-skeleton** | TerminalErrorMaxBudget 已落地；本 feature 加 LoopEventErrorMaxBudget + Transition case | 低 |
| **#5 skill-system** | 读 agent_definition.daily_credit_cap / credit_cap_per_session（不改 schema）| 低 |
| **#6 permission-pipeline** | hooks chain 嵌套：permission → budget → sandbox；HookActionRegistry race-safe atomic.Int32 容纳值 4 | 中（merge conflict 在 wrap chain wire 处，已预告）|
| **#7 memory-system** | 共用 runner.go SystemPrompt 段位（本 feature 不改段位）；共用 ctx token 写入（PostToolCall RecordUsage 依赖 ctx token）| 低 |
| **#8 narration-layer** | NarrationProvider / NarrationRunID 字段在 hooks chain 透传；本 feature 在 budget.WrapHooks 保留两字段 | 低 |
| **#9 compact** | compact 不在 hook 内调；不冲突 | 无 |
| **#10 configurator-ux** | 管理端 admin_test 池查询 / 调额 UI；本 feature 出后端契约（GetBalance + Status）就绪 | 低 |
| **#11 student-ux** | 前端读 admin_test_pool 字段渲染（试聊模式提示条 + 余额）；本 feature 出后端契约 | 低 |
| **#13 compliance-3layer** | terminal_metadata JSON 字段为 L3 拦截扩展点；本 feature 仅写 budget_dimension key，#13 追加 compliance_block_reason key | 低 |
| **#14 e2e-rollout** | daily aggregate Redis 替换 / daemon cron 接入 / R2 精确化 | 低 |

---

## §7 Migration 执行顺序（dev 上线步骤，pending 用户）

1. `cp migrations/20260521_140000_agent_billing_source_type_admin_test.sql` → dev 服务器
2. SSH dev：`mysql -u root -p < 20260521_140000_agent_billing_source_type_admin_test.sql`
3. 看 pre_check 行数 = 0 → 跑 ALTER → 看 post_check 行 = 1（chk_ct_source_type 存在）
4. 同步：`20260521_140100_agent_run_terminal_metadata.sql` 加列
5. AutoMigrate 在服务启动时建 credit_admin_test_grant 表（GORM 走）
6. 验证：`SHOW CREATE TABLE credit_transaction\G` 看 CHECK constraint 含 admin_test
7. 验证：`SHOW CREATE TABLE agent_run\G` 看 terminal_metadata 列

**Rollback 触发**：
- Pre-check 行数 ≠ 0 → 不跑 ALTER；先调查 invalid source_type
- prod 灾难（不在本 feature 范围）：跑 rollback SQL → revert CHECK → orphan rows 必须先 fix

---

## §8 S2 Open Questions（S3 前敲定）

1. **R2 estimator 复用 `budgetOperationMap` 还是新加 OpAgentRun？** 
   - **决策**：新加 `OpAgentRun Operation = "agent_run"` + `OpAgentTest Operation = "agent_test"` 到 `credit/types.go`，避免污染 budgetOperationMap 现有 12 个 SOP/Chatbot 入口。
   
2. **`isParentAccount` 判定**
   - **决策**：v1 简化为 `user.ParentUserID == nil`（即 user.parent_user_id IS NULL 的顶级账户）。后续 #14 决策是否引入 tenant_admin role / B2B grant 标志。
   
3. **PostToolCall tokens 从哪取**
   - **决策**：v1 简化 — `tokensFromCtxOrOutput` 优先读 ctx 内 token（aiservice adapter 已写）；若无，从 output JSON 解析 `usage.total_tokens`。两者皆无则跳过 RecordUsage（不阻塞）。
   
4. **`max_credits` R2 系数**
   - **决策**：v1 直接用 agent_definition.credit_cap_per_session 作为 MaxCredits（即 1:1）。不引入系数（R2 estimate 已经在 Reserve 阶段保守了）。
   
5. **terminal_metadata write 时机：hook 内 async vs runner.Run 末尾 sync**
   - **决策**：hook 内 async（避免阻塞 hook 返回）；runner.Run 末尾不重复 write。

---

## §9 S5 验证策略（reviewer P2-2 + 规则 10）

**策略**：TDD-only（biz 层 unit test + race detector + in-memory SQLite）。**不**走 Playwright E2E 或 gstack /qa。

**理由**：
- 本 feature 90% 后端 + DB schema，UI 改动是 #11 落地（学员侧）/ #10 落地（管理端）
- in-memory SQLite + GORM 可完整模拟 credit_admin_test_grant 行为（除 generated column / CHECK constraint）
- BudgetTracker 4 维触发可用 mock LLM + assertion 验证（无需真实 LLM）
- race detector 通过 `go test -race -count=1` 提供持久化保护

**S5 验收清单**：
- [ ] `go test -race -count=1 -timeout=120s ./internal/numind/biz/budget/...` PASS（含覆盖率 ≥ 80%）
- [ ] `go test -race -count=1 -timeout=120s ./internal/numind/biz/credit/...` 通过（不下降）
- [ ] `go test -race -count=1 -timeout=120s ./internal/numind/biz/agent/...` 通过（不下降）
- [ ] `go test -race -count=1 -timeout=120s ./internal/numind/store/...` 通过
- [ ] `task lint` clean
- [ ] migration 文件存在（双文件 ×3 = 6 个）含 rollback
- [ ] git grep 验证 "SOP/Chatbot Reserve 调用方零改动"（既有 callers 文件无 diff）

**回归保护**：TDD 测试永久留在 codebase；后续修改 budget/credit 模块时这些测试做回归保护。

---

## §10 风险登记（最新）

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| credit_transaction CHECK ALTER prod in-flight 写阻塞 | 低 | 几秒延迟 | MySQL 8 instant DDL；dev 验证 < 1s |
| agent_run.terminal_metadata prod 大表 ALTER 锁表 | 中 | 1-2 min 锁表 | MySQL 8 ALTER ADD COLUMN online；prod 操作前评估 row count |
| in-memory daily aggregate 多实例不一致 | 低 | 单用户日累计偶偏大 | v1 单实例；#14 引入 Redis |
| GORM SQLite generated column 行为不可控 | 中 | 测试套件启动失败 | `->;type:...` read-only tag + 测试用 Remaining() Go 计算 |
| ReconcileAgentTest topup 路径让积分意外 ↑ | 中 | 父账户超扣 | refund cap 到 used_amount；underestimate topup 同样从 grant 走 → 仍受 5000 上限保护 |
| HookAction enum 值 4 与第三方 mock/test 冲突 | 低 | 测试 panic | hooks_test.go 加显式覆盖 |

---

## §11 S2 reviewer 修订记录（P0/P1 fix log）

| ID | 来源 | 影响 | 修订 |
|---|---|---|---|
| S2-P0-1 | reviewer | CreditTransaction 没 Reference 字段 | 改用 Operation+BizRefType+BizRefID 三字段（实际 model）|
| S2-P0-2 | reviewer | CreditReservation.ReservedCents 不存在 | 全 spec 改 ReservedCredits（实际字段名）|
| S2-P0-3 | reviewer | creditService 字段名是 `store` 不是 `ds` | 全 spec 改 `s.store.DB()` |
| S2-P0-4 | reviewer | AgentDefinition.CreditCapPerSession 是 *uint 不是 int64 | LimitsFromAgentDef 加 nil 守护 + 显式 int64 转换 |
| S2-P1-1 | reviewer | IBudgetRunStore.UpdateTerminalMetadata 签名 vs IAgentRunStore 不一致 | 统一签名 `datatypes.JSON` |
| S2-P1-2 | reviewer | permission.WrapHooks 不透传 NarrationProvider/NarrationRunID | 本 feature S4 顺手 inline fix #6 wrap_hooks.go（属规则 §7 inline 修复，不单独立 task）|
| S2-P1-3 | reviewer | ErrAdminTestExhausted 在 errno 包没定义 | spec §4.6 明确加全局 errno.ErrAdminTestExhausted + 内部 sentinel 桥接 |
| S2-P1-4 | reviewer | DefaultAdminTestGrant int64 赋给 uint32 字段类型错 | 拆 uint32 (model 用) + int64 (API/Status 用) 两常量 |
| S2-P2-1 | reviewer | PostToolCall RecordUsage tokens 数据流 v1 不可达 | 见 §12 v1 限制说明 |
| S2-P2-2 | reviewer | state.go 头注释 "共 12 个" 过时 | S4 顺手改为 "共 13 个" |
| S2-P2-3 | reviewer | admin_test.go 绕 store 层惯例 | v1 接受（直接 DB.Tx），#14 TODO 迁移到 IAdminTestGrantStore |
| S2-P2-4 | reviewer | r2_estimator_test 缺 promptCharCount=0/负数 | S3 plan 显式列入 task |
| S2-P2-5 | reviewer | credit_admin_test_grant 没 migration SQL | S3 plan 加 task `20260521_140200_create_credit_admin_test_grant.sql` |

---

## §12 v1 限制说明（known gaps，#14 跟进）

1. **PostToolCall tokens 数据流不完整（reviewer P2-1）**：
   - PostToolCall hook 的 output 是 **tool 输出**（如 bash 命令 stdout），不是 LLM 响应
   - LLM token 用量在 `aiservice.Chat` 内 / Langfuse generation；目前不写到 PreToolCall/PostToolCall hook 可访问的 ctx
   - **v1 行为**：`RecordUsage` 在 v1 几乎永远拿到 tokens=0（除非将来 aiservice adapter 改成显式写 ctx）
   - **后果**：`DimMaxCredits` 在 v1 主要由 **Reserve 阶段保守估算** 把关（CheckAndEstimateBudget 已限制余额），PostToolCall 累加形同虚设
   - **TODO(#14)**：让 `aiservice.Chat` 在调用结束时把 token 数写 ctx；BudgetGate 在 PostToolCall 取 ctx token；或在 `aiservice` adapter 直接调 `BudgetTracker.RecordUsage`

2. **Daily aggregate 跨实例不一致（蓝本 §4.1.8）**：
   - in-memory cache + 30s lazy sync to DB
   - 多实例 prod 部署时单父账户日累计可能偶偏（一个实例 1500，另一个 1500 = 总 3000 ≠ 实际 2500）
   - **TODO(#14)**：Redis INCRBY 替换

3. **cron 调度未接入（蓝本 §4.3.8）**：
   - `AdminTestExpireDaemon` stub 函数 ready but 未挂调度
   - 月初新建 grant 依赖 lazy-create（Consume 内 OnConflict-DoNothing）；月底未用 grant 暂不真删
   - **TODO(#14)**：接 prod scheduler

4. **MaxTurnsPerRun 字段未引入 agent_definition**：
   - v1 走 DefaultLimits.MaxTurns=50；运营无法每个 Agent 配 turn 限制
   - **后续 feature**：决策是否加该字段

5. **migration 文件**：
   - `20260521_140000_*` (CHECK ALTER) / `20260521_140100_*` (agent_run ALTER) — 双 migration 双 rollback
   - `20260521_140200_create_credit_admin_test_grant.sql` — 创建表（reviewer P2-5）+ rollback
   - AutoMigrate 在 dev 服务启动时创建表；prod 上线手 SSH 跑 SQL（与 `project_dev_deploy_migration_gap` 一致）

