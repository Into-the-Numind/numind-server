# NDF S3 Task Plan · `agent-mode-billing-integration`

**Track**：Standard
**Feature ID**：`agent-mode-billing-integration`（14-feature 分解 #12/14）
**起草日期**：2026-05-21
**状态**：S3 草案
**前置 stage**：S2 通过（commit `17ef3a5c`）

---

## §1 Task 总览

13 个原子 task（M1-M13），分为 6 个 Wave（最大并行度 = 3 个 implementer agent 同 Wave）。

| Task | Wave | 范围 | 估时 | LOC 估 | 测试要求 | 依赖 |
|---|---|---|---|---|---|---|
| **M1** | W1 | Migration SQL ×3 双文件 | 1h | ~150 | pre-check SQL | 无 |
| **M2** | W1 | model.CreditAdminTestGrant + agent_run.TerminalMetadata 字段 + 单测 | 1h | ~80 | go test in-memory SQLite | M1（参考 schema）|
| **M3** | W1 | biz/budget/types.go + errno.go + dimensions.go + 单测 | 1h | ~120 | go test | 无 |
| **M4** | W2 | agent/hooks.go HookActionBudgetExceeded=4 + agent/state.go LoopEventErrorMaxBudget + Transition case + 单测 | 1h | ~50 | go test | 无（不影响 M1-M3）|
| **M5** | W2 | errno.ErrAdminTestExhausted 加全局 errno + biz/budget admin_test.go Consume/Refund/Status 实现 + 单测 | 3h | ~350 | go test -race + in-memory SQLite | M2, M3 |
| **M6** | W2 | biz/budget/r2_estimator.go + 单测（含 promptCharCount=0/负数 边界）| 1h | ~80 | go test | M3 |
| **M7** | W3 | biz/budget/tracker.go (BudgetTracker interface + in-memory impl with 4-dim atomic counters) + 单测 -race | 3h | ~350 | go test -race | M3, M4 |
| **M8** | W3 | store.IAgentRunStore.UpdateTerminalMetadata 接口 + 实现 + 单测 | 1h | ~50 | go test | M2 |
| **M9** | W4 | biz/budget/gate.go BudgetGate + WrapHooks 装饰器 + 单测 | 2h | ~250 | go test -race | M4, M7, M8 |
| **M10** | W4 | biz/credit 改造 — ReserveAgentTest / ReconcileAgentTest / GetBalance 扩展 AdminTestPool / SetAdminTestConsumer 注入 + 单测 | 2h | ~300 | go test -race + in-memory SQLite | M5 |
| **M11** | W5 | runner.go WithBudgetTracker option + Run 集成 (Start/Close + ad → limits) + 单测 + #6 顺手补丁 permission.WrapHooks 透传 NarrationProvider/RunID | 2h | ~150 | go test -race | M4, M7, M9 |
| **M12** | W5 | biz.go wire — budget.NewTracker + NewAdminTestConsumer + NewBudgetGate + hooks chain 嵌套（budget → permission）+ creditSvc.SetAdminTestConsumer + helper.go AutoMigrate | 1.5h | ~100 | 编译通过（无独立单测，集成测试覆盖）| 全部 M1-M11 |
| **M13** | W6 | S5 acceptance doc — race detector / 覆盖率 / 0 prod / 4 维触发 e2e 用例证据 | 1h | ~200 | — | M12 |

**总工时**：约 20.5h（自主推进，不严格按时间表）

**LOC 估算**：~2,250 行 production + tests（含 spec 中所有方法实现）

---

## §2 Wave 分组（并行 Tier 协议）

### W1 — 基础工件（3 task 并行，Tier 3 disjoint check）

| Task | 文件归属 |
|---|---|
| **M1** | `migrations/20260521_140000_agent_billing_source_type_admin_test.sql` `migrations/20260521_140000_agent_billing_source_type_admin_test_rollback.sql` `migrations/20260521_140100_agent_run_terminal_metadata.sql` `migrations/20260521_140100_agent_run_terminal_metadata_rollback.sql` `migrations/20260521_140200_create_credit_admin_test_grant.sql` `migrations/20260521_140200_create_credit_admin_test_grant_rollback.sql` |
| **M2** | `internal/pkg/model/credit_admin_test_grant.go` `internal/pkg/model/credit_admin_test_grant_test.go` `internal/pkg/model/agent_run.go` (Edit only: add TerminalMetadata field) `internal/pkg/model/agent_run_test.go` (Edit only: add TerminalMetadata test case) |
| **M3** | `internal/numind/biz/budget/types.go` `internal/numind/biz/budget/types_test.go` `internal/numind/biz/budget/errno.go` `internal/numind/biz/budget/dimensions.go` `internal/numind/biz/budget/dimensions_test.go` |

**Disjoint check 验证**：M1 全在 `migrations/`，M2 全在 `internal/pkg/model/`，M3 全在 `internal/numind/biz/budget/`。零交集 → Tier 3 安全。

注意：M2 改 `agent_run.go` 既有文件 + `agent_run_test.go` 是 `Edit`（不是 Write），与 M1 / M3 不交集。

### W2 — 接口与状态机（3 task 并行）

| Task | 文件归属 |
|---|---|
| **M4** | `internal/numind/biz/agent/hooks.go` (Edit: 加 HookActionBudgetExceeded + map case) `internal/numind/biz/agent/hooks_test.go` (Edit: 加测试) `internal/numind/biz/agent/state.go` (Edit: 加 LoopEventErrorMaxBudget + Transition case + 头注释 12→13 修正) `internal/numind/biz/agent/state_test.go` (Edit: 加测试) |
| **M5** | `internal/pkg/errno/credits.go` (Edit: 加 ErrAdminTestExhausted) `internal/numind/biz/budget/admin_test.go` `internal/numind/biz/budget/admin_test_test.go` |
| **M6** | `internal/numind/biz/budget/r2_estimator.go` `internal/numind/biz/budget/r2_estimator_test.go` |

**Disjoint check**：
- M4 → `biz/agent/hooks.go`、`hooks_test.go`、`state.go`、`state_test.go` 共 4 文件
- M5 → `pkg/errno/credits.go`、`biz/budget/admin_test.go`、`biz/budget/admin_test_test.go` 共 3 文件
- M6 → `biz/budget/r2_estimator.go`、`biz/budget/r2_estimator_test.go` 共 2 文件

零交集 → Tier 3 安全。

### W3 — 核心实现（2 task 并行）

| Task | 文件归属 |
|---|---|
| **M7** | `internal/numind/biz/budget/tracker.go` `internal/numind/biz/budget/tracker_test.go` |
| **M8** | `internal/numind/store/agent_run.go` (Edit: 加 IAgentRunStore.UpdateTerminalMetadata + impl) `internal/numind/store/agent_run_test.go` (Edit: 加测试) |

**Disjoint check**：M7 在 `biz/budget/`，M8 在 `store/`。零交集。

### W4 — Gate + credit 改造（2 task 并行）

| Task | 文件归属 |
|---|---|
| **M9** | `internal/numind/biz/budget/gate.go` `internal/numind/biz/budget/gate_test.go` `internal/numind/biz/budget/gate_wrap_hooks_test.go` (BudgetGate.WrapHooks 装饰器专属测试文件 — 注意：不是 wrap_hooks.go 独立源文件，WrapHooks 是 gate.go 上的方法) |
| **M10** | `internal/numind/biz/credit/credit_service.go` (Edit) `internal/numind/biz/credit/credit_service_admin_test_test.go` (新建) `internal/numind/biz/credit/types.go` (Edit: 加 AdminTestPoolView + BalanceBreakdown.AdminTestPool) `internal/numind/biz/credit/balance_admin_test_test.go` (新建) |

**Disjoint check**：M9 全在 `biz/budget/`，M10 全在 `biz/credit/`。零交集。

### W5 — Runner + Wire（2 task 串行）

> **M11 必须在 M12 之前**：因为 M12 wire 调用 M11 的 `WithBudgetTracker` option。

| Task | 文件归属 |
|---|---|
| **M11** | `internal/numind/biz/agent/runner.go` (Edit) `internal/numind/biz/agent/runner_budget_test.go` (新建) `internal/numind/biz/permission/wrap_hooks.go` (Edit: S2-P1-2 顺手 inline fix — narration 字段透传) `internal/numind/biz/permission/wrap_hooks_test.go` (Edit: 测试 narration 透传) — **验收**: M11 commit 前必须 `go test ./internal/numind/biz/permission/... ./internal/numind/biz/agent/...` PASS（确认 P1-2 顺手补丁不破坏 #6 既有验收）|
| **M12** | `internal/numind/biz/agent/biz.go` (Edit: wire 含 budget chain 嵌套) `internal/numind/helper.go` (Edit: AutoMigrate 加 CreditAdminTestGrant — 精确路径) |

W5 内 M11 → M12 **串行 dispatch**（前后依赖）。

### W6 — Acceptance doc（1 task）

| Task | 文件归属 |
|---|---|
| **M13** | `docs/superpowers/qa/2026-05-21-agent-mode-billing-integration-s5-acceptance.md` |

---

## §3 每个 Task 详细 spec（implementer agent 直接读）

### M1 — Migration SQL ×3 双文件

**目标**：创建 6 个 SQL 文件（3 UP + 3 DOWN）。

**文件清单**：
1. `20260521_140000_agent_billing_source_type_admin_test.sql` (UP) — credit_transaction CHECK ALTER 加 'admin_test'
2. `20260521_140000_agent_billing_source_type_admin_test_rollback.sql` (DOWN) — 还原 6 个枚举
3. `20260521_140100_agent_run_terminal_metadata.sql` (UP) — agent_run ADD COLUMN terminal_metadata JSON
4. `20260521_140100_agent_run_terminal_metadata_rollback.sql` (DOWN) — DROP COLUMN
5. `20260521_140200_create_credit_admin_test_grant.sql` (UP) — CREATE TABLE credit_admin_test_grant（含生成列 + 唯一/复合索引）
6. `20260521_140200_create_credit_admin_test_grant_rollback.sql` (DOWN) — DROP TABLE

每个 UP 文件须含：
- 文件头注释（YYYY-MM-DD / feature / ticket）
- Pre-check SQL（如适用 — 检查无违反新约束的行）
- 实际 ALTER / CREATE
- Post-check SQL（如适用 — 验证约束已生效）

**spec 引用**：`docs/superpowers/specs/2026-05-21-agent-mode-billing-integration-design.md` §2.1 / §2.2 / §2.3

**验收**：
- `mysql --version 8` 执行无 error
- `git diff` 显示只新增 6 个 SQL 文件（不动既有 migration）

### M2 — model.CreditAdminTestGrant + agent_run.TerminalMetadata + 测试

**目标**：
- 新建 `internal/pkg/model/credit_admin_test_grant.go`（含 struct + Remaining() 方法 + TableName）
- Edit `internal/pkg/model/agent_run.go` 加 `TerminalMetadata datatypes.JSON` 字段（AFTER state_reason 顺序无 GORM tag 强制；但代码顺序保持 state_reason 后 messages 前）
- 单测：`credit_admin_test_grant_test.go` — Create / Remaining() / TableName / GORM tag 反射
- 单测：`agent_run_test.go` Edit 加 TerminalMetadata 字段断言

**spec 引用**：spec §2.1（GORM model 完整定义）/ §2.3（agent_run model 改动）

**关键 GORM tag**（spec §2.1，**reviewer P2-4 修正**）：
- `RemainingAmount` 字段 `gorm:"column:remaining_amount;->;type:int GENERATED ALWAYS AS (CAST(granted_amount AS SIGNED) - CAST(used_amount AS SIGNED)) STORED;index:idx_period_remaining,priority:2"` (read-only + 复合索引第 2 列)
- `UniqueIndex:uq_parent_period,priority:1` 在 ParentUserID + `,priority:2` 在 PeriodStart
- `PeriodEnd` 字段 `gorm:"column:period_end;type:date;not null;index:idx_period_remaining,priority:1"` — 复合索引 idx_period_remaining 第 1 列是 period_end（与 SQL DDL `INDEX idx_period_remaining (period_end, remaining_amount)` 对齐）
- `PeriodStart` 字段 不带 idx_period_remaining 标签（只在唯一索引 uq_parent_period）

**Remaining() 方法**：
```go
func (g *CreditAdminTestGrant) Remaining() int64 {
    return int64(g.GrantedAmount) - int64(g.UsedAmount)
}
```

**验收**：
- `go test ./internal/pkg/model/...` PASS
- TerminalMetadata datatypes.JSON 字段存在
- AutoMigrate 在 in-memory SQLite 成功建表（即使 generated column 被 SQLite 忽略）

### M3 — biz/budget types.go + errno.go + dimensions.go + 测试

**目标**：建 budget 包基础类型与 4 维 enum。

**类型**：
- `Dimension` string type + 4 const（DimMaxTurns / DimMaxCredits / DimMaxWallTime / DimMaxDailyCredits）
- `Limits` struct（4 字段）
- `Snapshot` struct
- `AdminTestStatus` struct
- `BudgetExceededDetail` struct
- `DefaultLimits()` 函数
- `LimitsFromAgentDef(*model.AgentDefinition) Limits` 函数（nil 守护 + *uint 解引用）
- `DefaultAdminTestGrant uint32 = 5000`
- `DefaultAdminTestGrantInt64 int64 = 5000`
- sentinel errors: `ErrAdminTestExhausted` `ErrBudgetExceeded`

**测试**：
- `dimensions_test.go` — DefaultLimits 值正确 / LimitsFromAgentDef nil / *uint 解引用零值兜底 / 非零覆盖
- `types_test.go` — DefaultAdminTestGrant 值正确

**spec 引用**：spec §4.1 / §4.2 / §4.6

### M4 — agent/hooks.go HookAction 4 + state.go LoopEvent 19 + Transition case

**目标**：扩 enum + 状态机 case，不改既有 0-3 / 12 → 13 行为。

**Edits**：
- `hooks.go`：
  - 加 `HookActionBudgetExceeded HookAction = iota` (4)
  - `HookActionRegistry` 注释末尾加 `4=BudgetExceeded`
  - `HookActionToLoopEvent` 加 `case HookActionBudgetExceeded: return LoopEventErrorMaxBudget`
- `state.go`：
  - 头注释 "共 12 个" → "共 13 个"（reviewer S2-P2-2 fix）
  - LoopEvent 加 `LoopEventErrorMaxBudget // 19 — NEW (#12)`
  - Transition 加 `case LoopEventErrorMaxBudget: s.TerminalReason = TerminalErrorMaxBudget; return TerminalErrorMaxBudget, "", true`

**测试**（**reviewer P2-1 修正**：state_test.go 已存在，本 task 用 Edit 加测试，不新建 state_budget_test.go）：
- `hooks_test.go` (Edit)：
  - `TestHookActionToLoopEvent_BudgetExceeded` — 输入 HookActionBudgetExceeded 返回 LoopEventErrorMaxBudget
  - `TestHookActionRegistry_BudgetExceeded` — Record + LastAction 一致
- `state_test.go` (Edit — 已确认文件存在 internal/numind/biz/agent/state_test.go)：
  - `TestTransition_ErrorMaxBudget` — LoopEventErrorMaxBudget → TerminalErrorMaxBudget

**spec 引用**：spec §4.8

### M5 — errno.ErrAdminTestExhausted + biz/budget/admin_test.go

**目标**：
1. `internal/pkg/errno/credits.go` Edit 加：
   ```go
   ErrAdminTestExhausted = &Errno{HTTP: 429, Code: "Credits.AdminTestExhausted", Message: "本月测试配额已用完，请等待下月刷新"}
   ```
2. 新建 `biz/budget/admin_test.go`：
   - `AdminTestConsumer` interface（Consume / Refund / Status）
   - `adminTestConsumer` 实现（持 store.IStore）
   - `currentMonthBoundaries(now) (start, end time.Time)`
   - `daysUntil(target, now time.Time) int`
   - **关键 import**（reviewer P2-2 提醒）：
     - `gorm.io/gorm/clause`（OnConflict / Locking）
     - `errors`（errors.Is）
     - `gorm.io/gorm`（ErrRecordNotFound）
     - `numind-server/internal/numind/store`
     - `numind-server/internal/pkg/model`
3. `biz/budget/admin_test_test.go` 全测：
   - Consume 首次（lazy-create grant）成功 + INSERT credit_transaction
   - Consume 第二次（grant 存在）累加 used_amount
   - Consume exhausted → ErrAdminTestExhausted
   - Refund cap 到 used_amount（不会 < 0）
   - Refund 双重调用 idempotent
   - Status 无 grant 返回 default 5000
   - Status 有 grant 返回真实值
   - 并发 Consume：10 goroutine 同 parent → at most 5000 总额（race-safe）

**spec 引用**：spec §4.4

### M6 — biz/budget/r2_estimator.go

**目标**：R2 wrapper + 单测。

**实现** (spec §4.5)：
- `EstimateAgentTurn(ctx, pc pricing.ICalculator, provider, model string, promptCharCount, completionEstimate int) (*AgentTurnEstimate, error)`
- promptCharCount=0 / <0 → clamp 到 1
- completionEstimate <= 0 → 默认 500
- pc==nil → 报错 `EstimateAgentTurn: pricing calculator is nil`

**测试** (reviewer S2-P2-4)：
- promptCharCount=0 → estPromptTokens=1
- promptCharCount=-10 → estPromptTokens=1
- promptCharCount=10000 → estPromptTokens=5000
- completionEstimate=0 → DefaultCompletionEstimate(500)
- pc=nil → error

### M7 — biz/budget/tracker.go BudgetTracker in-memory impl + 单测 -race

**目标**：核心 BudgetTracker（4 维 atomic counter + RWMutex per-Run state）。

**实现** (spec §4.1)：
- `BudgetTracker` interface
- `NewTracker(store IBudgetStore) BudgetTracker`（store 可为 nil — v1 dev 用 nil）
- `budgetTracker` struct + state map keyed by runID
- `dailyAggregateCache` — in-process cache + 30s lazy sync（v1 简化，store 可 nil 时不持久化）
- 4 维 CanProceed 实现：
  - turn count >= MaxTurns
  - creditUsed >= MaxCredits
  - elapsed >= MaxWallTime
  - dailyCreditUsed >= MaxDailyCredits
- TODO(#14) 注释 — Redis INCRBY 替换

**测试** (race-safe)：
- Start/Close 状态隔离（10 个 runID 独立 state）
- RecordStep 累加（10 次 → TurnCount=10）
- RecordUsage 累加
- CanProceed 4 维独立 trip — 每维独立测
- Snapshot 一致性（concurrent read 不会读到部分写）
- Close 后再 Start 同 runID OK（state 重置）
- IBudgetStore=nil 兜底（无持久化但不 panic）
- 并发 Run 测：50 个 runID 并行 Start/RecordStep/CanProceed/Close（`-race` 验证）

**spec 引用**：spec §4.1

### M8 — store.IAgentRunStore.UpdateTerminalMetadata + 实现 + 测试

**目标**：在既有 `IAgentRunStore` 接口加方法。

**Edits** (spec §2.3)：
- `internal/numind/store/agent_run.go` 加 `UpdateTerminalMetadata(ctx context.Context, id uint64, metadata datatypes.JSON) error`
- 实现：`db.WithContext(ctx).Model(...).Where("id = ?", id).Update("terminal_metadata", metadata)`
- RowsAffected 0 时报错

**测试**：
- 写 + 读：写 metadata → Get(run) → JSON 字段反序列化等于 input
- 不存在 id → error "no row matched"
- JSON 内容（{"budget_dimension": "max_turns", "used": 51, "limit": 50}）反序列化正确

### M9 — biz/budget/gate.go BudgetGate + WrapHooks + 测试

**目标**：核心装饰器（PreToolCall + PostToolCall）。

**实现** (spec §4.3)：
- `BudgetGate` struct（tracker / adminConsumer / runStore）
- `NewBudgetGate(...)` 工厂
- `WrapHooks(base *agent.RunHooks) *agent.RunHooks` — 装饰
- `writeTerminalMetadata(ctx, runID, dim, detail)` async — go func
- `forwardPre` / `forwardPost` 兜底
- 关键：**保留 NarrationProvider / NarrationRunID** 透传（base 来的字段）

**测试** (race-safe)：
- PreToolCall allow 路径 — 调 base.PreToolCall（mocked）
- PreToolCall exceeded — Record BudgetExceeded + writeTerminalMetadata async（waitgroup 等 goroutine 完成）+ 不调 base
- PostToolCall — base 先调 + RecordUsage 后调 + base 错误透传
- nil base 兜底
- 并发 PreToolCall 不 race
- WrapHooks 返回的 hooks NarrationProvider/RunID 与 base 一致（透传不丢失）

### M10 — biz/credit 改造 (ReserveAgentTest / ReconcileAgentTest / GetBalance 扩展)

**目标**：在 ICreditService 加 2 方法 + GetBalance 末尾扩展。

**Edits** (spec §4.7)：
- `credit_service.go`：
  - `ICreditService` 接口加 2 方法
  - `creditService` struct 加 `adminConsumer budget.AdminTestConsumer` 字段
  - `SetAdminTestConsumer(c budget.AdminTestConsumer)` setter
  - `ReserveAgentTest(...)` 实现（调 adminConsumer.Consume + INSERT credit_reservation）
  - `ReconcileAgentTest(...)` 实现（compute refund / topup，UPDATE reservation）
  - `GetBalance` 末尾追加 AdminTestPool 填充（仅 isParentAccount 时）
- `types.go`：
  - `BalanceBreakdown` 末尾加 `AdminTestPool *AdminTestPoolView`
  - `AdminTestPoolView` struct

**测试**：
- `credit_service_admin_test_test.go`：
  - ReserveAgentTest 成功路径 — credit_reservation 写入 + credit_transaction (source_type=admin_test) 写入
  - ReserveAgentTest exhausted → errno.ErrAdminTestExhausted
  - ReconcileAgentTest refund 路径（actual < reserved → Refund）
  - ReconcileAgentTest topup 路径（actual > reserved → Consume more）
  - ReconcileAgentTest 双重 → error
  - SetAdminTestConsumer setter 替换 OK
- `balance_admin_test_test.go`：
  - GetBalance 父账户附加 AdminTestPool（granted/used/remaining/period_end/days_to_expire）
  - GetBalance 子账户为 nil
  - adminConsumer nil → 跳过 admin_test 填充（不阻塞）
  - adminConsumer.Status error → warn 不阻塞

**spec 引用**：spec §4.7

### M11 — runner.go WithBudgetTracker + Run 集成 + permission.WrapHooks 顺手补丁

**目标**：
1. `runner.go`：
   - 加 `WithBudgetTracker(t budget.BudgetTracker) RunnerOption`
   - `agentRunner` 加 `budgetTracker budget.BudgetTracker` 字段
   - **关键 reviewer P0-1 修正**：现有 `runner.go:222-246` 内 `ad` 变量在 `if req.AgentDefinitionID > 0` 块内用 `:=` 声明，块外不可见。M11 实施需要：
     - 在 `if req.AgentDefinitionID > 0` 块外先 `var ad *model.AgentDefinition`
     - 块内 `ad, err = r.skillStore.GetByIDIncludeInactive(...)` 改用 `=`（不是 `:=`）
     - 在块**外**（块结束后）插入 budget tracker 集成：
       ```go
       // budget tracker init — limits from ad (may be nil if AgentDefinitionID==0)
       if r.budgetTracker != nil {
           limits := budget.LimitsFromAgentDef(ad)  // ad 可为 nil → 走 DefaultLimits
           r.budgetTracker.Start(ctx, run.ID, limits)
           defer r.budgetTracker.Close(run.ID)
       }
       ```
     - **注意**：`budget.LimitsFromAgentDef(nil)` 已经在 M3 实现 nil 守护（返回 DefaultLimits），无需在调用方判断。
2. `permission/wrap_hooks.go` 顺手 fix (S2-P1-2)：
   - return struct 加 `NarrationProvider: base.NarrationProvider if base != nil else nil` 透传
   - 同样加 `NarrationRunID`
3. `permission/wrap_hooks_test.go` Edit：加测试验证透传 (Base hooks 含 NarrationProvider/RunID → permission.WrapHooks 返回也含)

**测试**：
- `runner_budget_test.go`：
  - WithBudgetTracker(nil) — Run 内跳过 Start/Close（nil 兜底）
  - WithBudgetTracker(tracker) + ad != nil — Start 用 limits / defer Close 调用
  - WithBudgetTracker(tracker) + ad == nil — 跳过（防 nil deref）
- `permission/wrap_hooks_test.go` 加：
  - Base hooks 含 NarrationProvider/RunID → permission.WrapHooks 返回也含

### M12 — biz.go wire

**目标**：在 biz/agent/biz.go 处装配整个 hooks chain（嵌套）。

**Edits**：
- `biz/agent/biz.go` (or 主 numind.go wire 处)：
  ```go
  budgetTracker := budget.NewTracker(nil)
  adminConsumer := budget.NewAdminTestConsumer(s)
  budgetGate := budget.NewBudgetGate(budgetTracker, adminConsumer, agentRunStoreAsBudgetRunStore(s.AgentRuns()))
  
  sandboxHooks := sandboxMgr.AsRunHooks()
  budgetWrapped := budgetGate.WrapHooks(sandboxHooks)
  permWrapped := permission.WrapHooks(budgetWrapped, permGate)
  
  runner := agent.NewAgentRunner(s.AgentRuns(), toolRegistry,
      agent.WithDefaultHooks(permWrapped),
      ...
      agent.WithBudgetTracker(budgetTracker),
  )
  
  creditSvc.SetAdminTestConsumer(adminConsumer)
  ```
- `helper.go` AutoMigrate 加 `&model.CreditAdminTestGrant{}`
- `arsAdapter` helper 在 biz.go 内 private（spec §4.9）

**测试**：依赖编译通过 + 后续 M13 acceptance e2e 覆盖。

### M13 — S5 acceptance doc

**目标**：`docs/superpowers/qa/2026-05-21-agent-mode-billing-integration-s5-acceptance.md`

**内容**：
- DoD 14 项打勾验证（spec §6）
- race detector 命令 + 输出
- 覆盖率（biz/budget overall + 子包）
- 4 维真实触发 e2e 用例（mock 触发条件 + 状态机 transition 验证）
- 0 prod 6 条清单
- 与 #6 PreToolCall 区域 merge conflict resolve 说明（M12 wire 处）
- known gaps (§12) 列入 TODO

---

## §4 S5 验证策略（NDF Rule 10 — S3 必含）

**策略**：TDD-only（biz 层 unit test + race detector + in-memory SQLite）

**理由**：
1. 本 feature 90% 后端 + DB schema，UI 改动是 #11/#10 落地
2. in-memory SQLite + GORM 可完整模拟 credit_admin_test_grant 业务路径
3. BudgetTracker 4 维触发由 mock LLM + assertion 验证（无需真实 LLM 调用）
4. race detector 通过 `go test -race -count=1` 提供持久化保护
5. 持久化测试代码 = 后续 budget/credit 模块修改时的回归保护

**关键 user path**（M13 acceptance doc 验证）：
1. 父账户首次试聊：Reserve (lazy-create grant) → Reconcile (refund cap)
2. 父账户连续试聊耗尽：第 N 次 Consume → ErrAdminTestExhausted
3. 学员触发 4 维超限：mock LLM → CanProceed exceeded → state.Transition → TerminalErrorMaxBudget + terminal_metadata.budget_dimension
4. 学员查看余额：GetBalance 父账户附加 AdminTestPool / 子账户 nil
5. 现有 SOP/Chatbot Reserve 调用方无 diff：`git grep` 既有 callers 文件

**不走** Playwright E2E（无前端改动）/ gstack /qa（前端在 #11）。

**回归保护**：TDD 测试永久留 codebase。本 feature 关键测试 = 后续 7 个未启动 feature（#13 #14）的回归 safety net。

---

## §5 风险（最新）

| 风险 | 缓解 |
|---|---|
| 多 implementer agent 并行 commit race | 每 Wave dispatch 前显式列文件归属 + ndf-check-disjoint 程序化验证 |
| M11 改 #6 permission wrap_hooks.go 触发 #6 测试回归 | M11 implementer 必须跑 `go test ./internal/numind/biz/permission/...` 确认 PASS |
| M12 wire 集成失败（hooks chain 顺序错） | M12 implementer 必须跑 `go test ./internal/numind/biz/agent/...` 含 runner_*_test.go 验证 |
| credit_transaction CHECK ALTER 与既有数据冲突 | M1 pre-check SQL 必跑（在 in-memory SQLite 静默 OK，dev 上线时手 SSH 跑前看 0 行） |
| SQLite generated column 行为 | M2 测试用 Remaining() Go 计算（不依赖 DB） |
| budget 包反向 import credit 风险 | M5 / M7 / M9 implementer 须避免 `import "numind-server/.../biz/credit"`；S4 后跑 `go vet ./...` 验证 |
| HookAction enum 值 4 与既有 race-safe atomic.Int32 注释 | M4 同步改注释；不引入运行时差异 |

---

## §6 Bug-from-Customer 复现测试要求

**不适用**：本 feature 是 14-feature 分解中的 #12，非 bug-from-customer。S4 commit 链无需以 `test(qa):` 开头。

---

## §7 提交规范

每个 task 完成后 commit：
- 标题：`feat(agent-billing): MN 描述` / `test(agent-billing): MN 描述` / `fix(agent-billing): MN 描述`
- trailer：`Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>`
- 一个 task 对应 1-2 个 commit（impl 1 + test 1 可分，亦可合一）

---

## §8 S3 reviewer 关注点

S3 reviewer 应验证：

1. **Wave 分组的 disjoint check 是否真的零交集**（M1-M3 / M4-M6 / M7-M8 / M9-M10）
2. **M5 admin_test 测试 case 是否覆盖 race**（并发 Consume）
3. **M7 BudgetTracker 4 维测试是否各自独立**（不依赖其他维度）
4. **M11 顺手补丁 permission.WrapHooks 是否在范围内**（属规则 §7 inline 修复，不破坏 #6 既有验收）
5. **S5 验证策略是否合理**（TDD 是否足够 — 无前端改动场景 TDD 是对的）
6. **Bug-from-customer 不适用声明正确**
7. **每个 task 文件归属表与 spec 文件路径一致**
8. **task 原子性**（每 task 完成后系统可编译运行）

