# NDF S1 Proposal + PRD · `agent-mode-billing-integration`

**Track**：Standard
**Feature ID**：`agent-mode-billing-integration`（14-feature 分解 #12/14）
**起草日期**：2026-05-21
**状态**：S1 草案
**前置 stage**：S0 通过（commit `9cb6f21a`）

---

## 1. 目标与背景

### 1.1 商业价值

Agent 模式（莫小派第三模态）相比 SOP/Chatbot **风险高出一个数量级**：

- **SOP**：单步骤已知 LLM 调用次数 → 一次 run 积分支出可预测（用户配 100 元/月 → 平均 50 次 SOP）
- **Chatbot**：单轮一次 LLM 调用 → 单条消息 ~10 积分
- **Agent**：单次 run 跑 N 轮（LLM 决定下一步）→ 单步 1k token = 30k token = 几百积分 = 用户一次任务烧光余额

**没有失控保护 = 用户感受不到产品价值就被反复扣费**。

**没有试聊隔离**：父账户配置 Agent 时反复调问卷试聊（蓝本 §4.3.8 描述这是核心 UX 流程），每次试聊就是真实 Agent 跑一次 = 反复扣父账户自己的正式订阅积分。父账户从"领域专家配置 Agent"变成"配置每烧自己的积分" = 强烈受骗感。

**蓝本 §4.3.8 明确**：父账户每月独立 5000 试聊积分（与三池隔离），月底失效不累积，进入 Agent Builder Modal 内"试聊"专用，**不允许 fallback 到正式积分**。

### 1.2 业务目标

- **失控阻断 1 秒内**：4 维任一超限 → terminal_reason='error_max_budget' + budget_dimension 元数据 P99 < 1s
- **试聊隔离 100%**：父账户 Agent Builder 试聊全走 `credit_admin_test_grant` 池；admin_test 耗尽 → 阻塞（不 fallback）
- **零回归 SOP/Chatbot**：现有 4 种 source_type（trial/subscription/cycle/booster）+ admin/system + NULL 全过 CHECK；现有 Reserve/Reconcile 调用方零代码改动
- **学员积分透明 1 个端点**：GET `/v1/credits/balance` 响应字段扩展 `admin_test_pool`（向后兼容）
- **R2 估算保守**：Reserve 总是 ≥ 实际，Reconcile 多退少补；零负数余额事故

### 1.3 技术目标

- biz/budget 子包覆盖率 ≥ 80%（plan 硬性）
- biz/agent / biz/credit / biz/membership 覆盖率不下降
- `go test -race ./...` PASS（atomic counter + 异步 admin_test consume race-safe）
- 4 维 BudgetTracker — 蓝本三维 + 本 feature 增 DailyCreditCap → 与 #5 落地的 agent_definition.daily_credit_cap 字段对齐
- `credit_transaction.source_type` CHECK constraint 唯一一次 ALTER（增 `'admin_test'`），不破现有 6 个枚举
- 0 prod 影响

---

## 2. 用户故事（User Stories）

### US-1：父账户在 Agent Builder 内试聊调问卷（试聊配额路径）

```
背景：父账户 P（user.id=42）配置一个"学生作业辅导 Agent"，问卷填完点保存
UX:   保存成功 → 顶部出现非阻塞提示条 "已发布。想试试效果？→ [立即试聊]"
点 [立即试聊] → 右侧滑出试聊对话窗 → 输入 "帮我看下这道数学题" → 发送

后端流：
1. credit_service.Reserve(req={SourceHint: "agent_test", UserID: 42, Op: OpAgentRun, ...})
2. budget.AdminTestConsume(ctx, parentUserID=42, R2_estimate=50)
   → 查 credit_admin_test_grant WHERE parent_user_id=42 AND period_start=2026-05-01
   → 看 remaining_amount = 4870 ≥ 50 → OK，预扣 50
   → INSERT credit_transaction (source_type='admin_test', amount=-50, ...)
   → UPDATE credit_admin_test_grant SET used_amount += 50 WHERE id=...
3. Agent 跑一轮 LLM → 实际用 38 积分
4. credit_service.Reconcile(rsvID=..., actualCost=38)
   → budget.AdminTestRefund(ctx, parentUserID=42, refund=12)
   → INSERT credit_transaction (source_type='admin_test', amount=+12, ...)
   → UPDATE credit_admin_test_grant SET used_amount -= 12

【关键不变量】：父账户 P 的正式订阅积分（cycle / booster / trial）此次试聊零变动。
```

**异常路径**：admin_test 耗尽（remaining_amount = 0）→ Reserve 返回 `ErrAdminTestExhausted` → 学员收到友好提示 "本月测试配额已用完，请等待下月 X 天后刷新"。**不 fallback 到正式积分**。

### US-2：学员触发 Agent 失控（4 维超限路径）

```
背景：学员 S（user.id=101，是父账户 P=42 下的子账户）让 Agent 写一份 5000 字文献综述
LLM 决定：连续 N 轮工具调用（搜索 → 阅读 → 总结 → ...）

Hook chain（PreToolCall 每轮触发）：
  permission.Check (#6) → allow（不在敏感 list）
  → budget.CanProceed (本 feature)
     检查 4 维：
       L1: turn_count = 51 >= 50 (default MaxTurns)  ❌
     → 返回 budget_dimension="max_turns"
  → registry.Record(HookActionBlockingStop)
  → sink ← BudgetExceededDetail{Dimension: "max_turns", UsedSteps: 51}

runner.Run 末尾：
  state.Transition(LoopEventErrorMaxBudget) → TerminalReason=TerminalErrorMaxBudget
  agent_run.terminal_metadata JSON: {"budget_dimension": "max_turns", "used_steps": 51, "limit": 50}
  agent_run.state_reason: "error_max_budget"

学员看到（#11 student-ux 渲染）：
  消息流末尾 narration: "任务执行步数达到上限（50 步），停止执行。
  已生成的内容已保存，可下载。下次任务建议简化为更小的子任务。"
```

类似路径 4 维都覆盖：
- max_turns（步数 / TurnCount）
- max_credits（单 Run 累计 token 折算积分 / TokenCount）
- max_wall_time（执行时长 / WallClock）
- max_daily_credits（日累计跨 Run 积分 / DailyCreditCap）

### US-3：学员查看当前积分余额（前端透明 UX）

```
背景：学员 S 想知道还剩多少积分
前端：进设置页 → 调 GET /v1/credits/balance

后端响应（兼容字段 + 新增 admin_test）：
{
  "code": 0,
  "data": {
    "billing_mode": "credits",
    "sub_remain": 1320,
    "sub_total": 2000,
    "sub_expires_at": "2026-06-15T00:00:00Z",
    "booster_remain": 600,
    "booster_total": 600,
    "trial_remain": 0,
    "admin_test_pool": null  // ← S 是子账户，admin_test 字段为 null
  }
}

父账户 P 看到自己的 balance：
{
  "data": {
    "billing_mode": "credits",
    "sub_remain": 1500,
    "sub_total": 2000,
    "booster_remain": 0,
    "trial_remain": 0,
    "admin_test_pool": {  // ← P 是父账户，admin_test 字段非 null
      "remaining": 4820,
      "granted": 5000,
      "period_end": "2026-05-31",
      "days_to_expire": 10
    }
  }
}
```

**前端兼容性**：admin_test_pool 字段是新增的，旧前端忽略不影响功能（向后兼容）。#11 在 student-ux 完成时显式渲染。

### US-4：财务月底对账（admin_test 池统计）

**本 feature 不交付**——管理端财务对账 UI 在 #10 落地。本 feature 仅出 stub daemon 函数 `budget.AdminTestExpireDaemon(ctx, today)` 用于：
- 月底标记当月 admin_test_grant.period_end < today 的 grant 记录为 expired（v1 用 last_used_at 列推断已用 → 暂不真删数据库行）
- 月初新建当月 grant 记录（5000 / 父账户）

**v1 不接 cron 调度** — 父账户第一次当月试聊时 lazy-create grant 记录（reviewer P1-4 fix）：

```go
// budget/admin_test.go (伪代码)
func (c *adminTestConsumer) Consume(ctx, parentUserID, amount) (txID, err) {
    periodStart, periodEnd := currentMonthBoundaries(time.Now())
    
    // Lazy-create grant row if not exists (race-safe via UNIQUE KEY uq_parent_period)
    grant, err := c.store.FindOrCreate(ctx, parent_user_id, periodStart, periodEnd,
        defaultGrantedAmount=5000)  // ON DUPLICATE KEY UPDATE granted_amount (idempotent)
    if grant.RemainingAmount < amount {
        return 0, ErrAdminTestExhausted
    }
    // 走原子 UPDATE used_amount + INSERT credit_transaction
    return c.tx(ctx, grant, amount)
}
```

`#14 e2e-rollout` 时由用户决策是否接入 prod scheduler 做 daemon 主动 expire / pre-grant；v1 lazy-create 完整覆盖正常路径，daemon 函数为冷储备。

---

## 3. 技术方案

### 3.1 整体架构

```
                ┌──────────────────────────────────────┐
                │ Agent Run 主流程（runner.Run）         │
                └──────────────┬────────────────────────┘
                               │
            每轮 PreToolCall   ▼
                ┌──────────────────────────────────────┐
                │  Hook chain（嵌套 wrapper）            │
                │  外层: permission.WrapHooks (#6)      │
                │   └─ permission.Check (deny 短路)     │
                │      ─┐                              │
                │       ▼ allow 透传                    │
                │  中层: budget.WrapHooks (本 feature)  │
                │   └─ budget.CanProceed (deny 短路)    │
                │      ─┐                              │
                │       ▼ allow 透传                    │
                │  内层: sandbox.AsRunHooks (#4)        │
                │   └─ 启动容器 / 工具实际执行             │
                └──────────────┬────────────────────────┘
                               │
            每轮 PostToolCall  ▼
                ┌──────────────────────────────────────┐
                │  逆顺序返回:                          │
                │  sandbox.PostToolCall → 关容器        │
                │  budget.PostToolCall  → RecordUsage  │
                │  permission.PostToolCall → 透传      │
                └──────────────────────────────────────┘
```

**串行顺序说明（与 #6 一致）**：

PreToolCall: `permission → budget → base`
- permission 在前：deny 时不暴露预算内部状态
- budget 在中：permission allow 后才检查预算（避免被 deny 工具消耗预算 check 开销）
- base 在后：sandbox 启动容器是最重的副作用

PostToolCall: `base → budget → permission`
- base 先：sandbox 关容器、写日志
- budget 后：从 output 提取实际 token → RecordUsage（这样 budget 拿到的是真实 token 数）
- permission 透传：post 阶段 permission 不决策

### 3.2 biz/budget 子包结构

```
internal/numind/biz/budget/
├── tracker.go             # BudgetTracker 接口 + 实现
├── tracker_test.go
├── dimensions.go          # 4 维 enum + 单维 Check 方法
├── dimensions_test.go
├── gate.go                # BudgetGate 顶层入口（持 tracker + admin_test consumer + audit）
├── gate_test.go
├── wrap_hooks.go          # WrapHooks(base, gate) 装饰器
├── wrap_hooks_test.go
├── r2_estimator.go        # R2 估算 wrapper（复用 internal/pkg/pricing）
├── r2_estimator_test.go
├── admin_test.go          # 试聊配额管理（Consume / Refund / Expire）
├── admin_test_test.go
├── errno.go               # ErrAdminTestExhausted / ErrBudgetExceeded
└── types.go               # CheckResult / BudgetExceededDetail / AdminTestStatus
```

**单元测试约束**：每个 `*.go` 文件必有对应 `_test.go`；race detector PASS；overall ≥ 80%。

### 3.3 核心接口签名

```go
// biz/budget/tracker.go
type BudgetTracker interface {
    // CanProceed 在 PreToolCall hook 内调用。
    // exceeded=true 时附带 dim 标识哪一维超限，附 detail JSON 写入 agent_run.terminal_metadata。
    CanProceed(ctx context.Context, runID uint64) (exceeded bool, dim Dimension, detail map[string]any)

    // RecordStep 在每轮 LLM 调用开始时调用（递增 turn count）。
    RecordStep(ctx context.Context, runID uint64)

    // RecordUsage 在 PostToolCall hook 内调用（递增 token / credit 维度）。
    // tokens 是实际 LLM API 返回的 prompt_tokens + completion_tokens。
    RecordUsage(ctx context.Context, runID uint64, tokens int)

    // Snapshot 取当前 4 维快照（用于审计 / 监控）。
    Snapshot(ctx context.Context, runID uint64) Snapshot

    // Start 在 Run 入口创建一个 BudgetState（4 维 limits + 起始时间）。
    Start(ctx context.Context, runID uint64, limits Limits)

    // Close 在 Run 退出时清理（无论是否超限）。
    Close(runID uint64)
}

// biz/budget/dimensions.go
type Dimension string
const (
    DimMaxTurns        Dimension = "max_turns"
    DimMaxCredits      Dimension = "max_credits"
    DimMaxWallTime     Dimension = "max_wall_time"
    DimMaxDailyCredits Dimension = "max_daily_credits"
)

type Limits struct {
    MaxTurns        int           // default 50
    MaxCredits      int64         // 从 agent_definition.credit_cap_per_session
    MaxWallTime     time.Duration // default 300s
    MaxDailyCredits int64         // 从 agent_definition.daily_credit_cap
}

// biz/budget/gate.go
type BudgetGate struct {
    tracker     BudgetTracker
    adminTest   AdminTestConsumer
    store       store.IAgentRunStore  // 写 terminal_metadata
    creditStore CreditPersistence     // 写 daily aggregate (跨 Run 累计)
}

func (g *BudgetGate) WrapHooks(base *agent.RunHooks) *agent.RunHooks {
    // 装饰器，参考 permission.WrapHooks
}

// biz/budget/admin_test.go
type AdminTestConsumer interface {
    // Consume 预扣（Reserve 阶段调用）。失败返回 ErrAdminTestExhausted。
    Consume(ctx context.Context, parentUserID uint, amount int64) (txID uint64, err error)
    // Refund 退还（Reconcile 阶段调用）。
    Refund(ctx context.Context, parentUserID uint, txID uint64, refund int64) error
    // Status 查当月 grant 状态（GetBalance 用）。
    Status(ctx context.Context, parentUserID uint, now time.Time) (*AdminTestStatus, error)
}

// biz/budget/types.go
type AdminTestStatus struct {
    Granted       int64
    Used          int64
    Remaining     int64
    PeriodStart   time.Time
    PeriodEnd     time.Time
    DaysToExpire  int
}

type BudgetExceededDetail struct {
    Dimension Dimension
    Used      int64  // 已用值（步数 / 积分 / 毫秒 / 日积分）
    Limit     int64  // 当前 limit
}
```

### 3.4 BudgetTracker 实现（in-memory + atomic counters）

**关键设计**：
- 单 Run 状态（turn count / credit / wall start time）全部 in-memory（`map[runID]*state` + RWMutex）
- 跨 Run 状态（daily credit aggregate）走 DB（新表 `agent_daily_credit_usage` 或复用 credit_transaction 聚合查询）
- 4 维 check 各自独立，任一超限即返回 exceeded=true

**为什么不走 DB**：单 Run 内 PreToolCall hook 每轮触发 → 走 DB 会增加 P50 延迟 50-100ms × N 轮 = 极差体验。in-memory 满足"P99 < 1s 失控阻断"目标。

**v1 跨 Run 日累计简化**：本 feature 用 in-process 缓存 + lazy DB sync（30s 刷新一次）。**多实例部署时跨实例不一致**。v1 接受这个 trade-off（dev 环境单实例 / prod 暂无多实例 Agent Run）。`#14 e2e-rollout` 决策是否引入 Redis。

**代码内 TODO 标注**（reviewer NOTE-3 fix）：在 tracker.go 实现中显式 `// TODO(#14): replace with Redis INCRBY if prod goes multi-instance`，避免 #14 时找不到口子。

**RecordStep 时序**（reviewer P1-3 fix）：BudgetTracker 接口区分两个时机：

- `RecordStep(ctx, runID)` 在 **runner.Run 主循环 LLM 调用前**调用（不走 hook，直接 runner.Run 内调）— 因为 PreToolCall hook 是工具调用前触发，**LLM 调用之间没有 hook**
- `CanProceed(ctx, runID)` 在 **PreToolCall hook 内**调用 — 比对 turn_count vs MaxTurns / credit_used vs MaxCredits / wall_time vs MaxWallTime / daily_credits vs MaxDailyCredits
- `RecordUsage(ctx, runID, tokens)` 在 **PostToolCall hook 内**调用 — 但实际 token 用量从 **LLM 响应**而非 tool output 拿到。本 feature 简化：PostToolCall 时若拿不到 token，从 ctx 取 prior LLM 调用的 token 数（# 7 memory + # 8 narration 已经在 ctx 写 token）

`max_turns` 维度：CanProceed 内判断 `state.TurnCount >= limits.MaxTurns`（不依赖 RecordStep 时序）；`RecordStep` 仅累加 TurnCount。

### 3.5 R2 估算

复用既有 `internal/pkg/pricing/pricing.go` 的 `pricing.ICalculator.CalculateCost(ctx, op, provider, model, promptTokens, completionTokens) (cents int64, err)`。

R2 估算函数（本 feature 新增 wrapper）：

```go
// biz/budget/r2_estimator.go
func EstimateAgentTurn(ctx context.Context, pc pricing.ICalculator,
    provider, model string, promptCharCount int) (int64, error) {
    // 1. 字符数 → token 估算（沿用 SOP/Chatbot R2 字符比例：1 token ≈ 2 中文字符 / 4 英文字符）
    estPromptTokens := promptCharCount / 2  // 保守估
    estCompletionTokens := 500              // Agent 默认 completion 上限

    // 2. 走 pricing 算 cost cents（== credits in this system）
    return pc.CalculateCost(ctx, "llm_chat", provider, model, estPromptTokens, estCompletionTokens)
}
```

**与既有 SOP/Chatbot 估算的区别**：
- SOP/Chatbot：每次调用前用 prompt 字符数估算 1 次
- Agent：单 Run 内 N 轮，每轮独立 R2 估算（multi-step accumulation）

### 3.6 credit_service 改造（最小化）

**改动点**：
1. **新增独立方法** `ReserveAgentTest` / `ReconcileAgentTest` 在 `creditsImpl`，**不污染** Reserve/Reconcile 主流程（reviewer P0-2 fix — 避免主路径分支判断 + 减少 SOP/Chatbot 回归面）
2. `Reservation` 加可选字段 `SourceHint string` 仅用于审计字段记录（**不**作为分支条件）
3. `creditsImpl.GetBalance` 内：扩展返回 `BalanceBreakdown` 加 `AdminTestPool *AdminTestStatus` 字段（父账户非 nil / 子账户 nil）— 详见 §3.6.1

**包依赖方向**（reviewer P0-2 fix — 防循环 import）：

```
budget ← credit         (credit.Reserve  在 SourceHint='agent_test' 时调 budget.AdminTestConsume)
budget → store          (budget 直接走 store 层写 credit_transaction / credit_admin_test_grant)
budget ↛ credit         (禁止 budget import credit 包；如需类型，单独 budget/types.go 定义)
```

`creditsImpl` 内通过依赖注入 `budget.AdminTestConsumer` 接口（接口在 budget 包定义）；具体实现 `budget.adminTestConsumer{store, db}` 直接走 store 层。

**接口签名**（新加在 credit_service.go ICreditService）：

```go
type ICreditService interface {
    // 既有方法不变
    ...

    // 新加（本 feature）— 试聊专用，源自 admin_test 池
    ReserveAgentTest(ctx context.Context, parentUser *model.User, estimated int64, idempotencyKey *string) (*Reservation, error)
    ReconcileAgentTest(ctx context.Context, reservationID uint64, actualCostCents int64) error
}
```

**调用链**：

```
controller/agent_run.go (src=agent_test) → biz/agent/runner.Run (req.SourceHint='agent_test')
   → credit.ReserveAgentTest(parentUser, estimated)
       → budget.AdminTestConsumer.Consume(ctx, parentUserID, estimated)
            → DB: lazy-create credit_admin_test_grant + UPDATE used_amount
            → DB: INSERT credit_transaction (source_type='admin_test', amount=-estimated)
       → INSERT credit_reservation (operation='agent_test', user_id=parent_user_id, idempotency_key=?)
       → return Reservation
   ...实际 LLM 调用...
   → credit.ReconcileAgentTest(rsvID, actualCost)
       → budget.AdminTestConsumer.Refund(ctx, parentUserID, txID, refund)
       → UPDATE credit_reservation SET status='reconciled', actual_cost_cents=?
```

**禁止破坏的不变量**：
- 既有 SOP/Chatbot 调用 Reserve / Reconcile / GetBalance 调用方代码零改动
- 既有 source_type 6 个枚举（trial/subscription/cycle/booster/admin/system）+ NULL 行 100% 不受 ALTER 影响
- 既有 BalanceBreakdown JSON 序列化字段名零改动（新增 `admin_test_pool` 是 optional，omitempty）

### 3.6.1 BalanceBreakdown 扩展（向后兼容）

reviewer P2-3 fix — US-3 JSON 示例必须与 types.go 现有 `BalanceBreakdown` 字段名对齐。

**S2 spec 明确**：本 feature 在 types.go `BalanceBreakdown` 末尾**追加** 字段，**不重构**：

```go
// types.go 改动（本 feature）
type BalanceBreakdown struct {
    BillingMode               string     `json:"billing_mode"`
    SubRemain                 int64      `json:"sub_remain"`
    SubTotal                  int64      `json:"sub_total"`
    SubExpiresAt              *time.Time `json:"sub_expires_at,omitempty"`
    BoosterRemain             int64      `json:"booster_remain"`
    BoosterTotal              int64      `json:"booster_total"`
    BoosterEarliestExpiresAt  *time.Time `json:"booster_earliest_expires_at,omitempty"`
    TrialRemain               int64      `json:"trial_remain"`
    TrialExpiresAt            *time.Time `json:"trial_expires_at,omitempty"`
    // ↓ 本 feature 新增 — 父账户非 nil，子账户 nil（omitempty 时 JSON 字段消失）
    AdminTestPool             *AdminTestPoolView `json:"admin_test_pool,omitempty"`
}

// AdminTestPoolView is the JSON view of admin_test grant for the parent user.
type AdminTestPoolView struct {
    Granted      int64  `json:"granted"`
    Used         int64  `json:"used"`
    Remaining    int64  `json:"remaining"`
    PeriodEnd    string `json:"period_end"`     // "YYYY-MM-DD"
    DaysToExpire int    `json:"days_to_expire"`
}
```

**前端兼容性测试**：现有 numind-web-v3 / numind-admin-web 不读 `admin_test_pool` → 序列化结果 JSON 多一个字段不影响解析。#11 落地时显式渲染。

### 3.7 集成点（runner.go / biz.go / helper.go）

**runner.go 改动**（最小化）：

```go
// 新增 RunnerOption
func WithBudgetTracker(t budget.BudgetTracker) RunnerOption { ... }

// agentRunner 新增字段
budgetTracker budget.BudgetTracker

// Run() 内插入位置（与 #7 memory / #8 narration / #9 compact 等并列）：
// 1.7. budget-integration: tracker.Start(runID, limits 从 agent_definition)
if r.budgetTracker != nil {
    limits := budget.LimitsFromAgentDef(ad)  // 读 daily_credit_cap / credit_cap_per_session
    r.budgetTracker.Start(ctx, run.ID, limits)
    defer r.budgetTracker.Close(run.ID)
}

// PreToolCall 已经在 biz.go 的 hooks chain 里被 budget.WrapHooks 包了，runner.Run 不需要改 chain
// PostToolCall 同理
```

**biz.go wire**（最小化）：

```go
// 现状：sandbox.AsRunHooks() → permission.WrapHooks(sandbox, permGate)
// 改为：sandbox.AsRunHooks() → budget.WrapHooks(sandbox, budgetGate) → permission.WrapHooks(...)
//
// 即先建 budget gate / tracker，再用 budget.WrapHooks 把 sandbox 包一层
// 然后再用 permission.WrapHooks 把整个 budget+sandbox 包一层
budgetTracker := budget.NewTracker(...)
budgetGate := budget.NewGate(budgetTracker, adminTestConsumer, store)
hooks1 := budget.WrapHooks(sandbox.AsRunHooks(), budgetGate)
hooks2 := permission.WrapHooks(hooks1, permGate)

runner := agent.NewAgentRunner(..., 
    agent.WithDefaultHooks(hooks2),
    agent.WithBudgetTracker(budgetTracker),  // 给 runner.Run Start/Close 用
)
```

**helper.go AutoMigrate**：

```go
return db.AutoMigrate(
    ...
    &model.CreditAdminTestGrant{},
)
```

### 3.8 状态机集成（state.go）

**现状审计**（S1 reviewer P0-1 修正）：
- `TerminalErrorMaxBudget` 在 state.go 第 18 行已存在 ✓
- **`LoopEventErrorMaxBudget` 不存在** ✗ — 现有 `LoopEventLLMErrMaxOutput`（第 56 行）注释是"LLM 输出超长重试后 terminal"，与预算超限**业务含义不同**

**本 feature 必须新增**：

```go
// state.go LoopEvent 加 (#12)
LoopEventErrorMaxBudget   // 19 — NEW (#12 agent-mode-billing-integration)
                          // BudgetTracker 4 维任一超限 → TerminalErrorMaxBudget

// state.go Transition switch 加 case
case LoopEventErrorMaxBudget:
    s.TerminalReason = TerminalErrorMaxBudget
    return TerminalErrorMaxBudget, "", true
```

**编译期不变量数组同步**：
- `[13]TerminalReason{}` 数组（state.go 行 37）不变 — TerminalErrorMaxBudget 已是第 11 个元素，TerminalPermissionDenied 第 13
- LoopEvent 总数 18 → 19（注释同步：`// 共 19 个事件，含 LoopEventErrorMaxBudget`）

**新加**：把 `agent_run.terminal_metadata` JSON 写入逻辑，在 runner.Run 末尾：

```go
// 末尾收 budget exceeded detail
if detail, ok := budgetGate.GetExceededDetail(run.ID); ok {
    meta, _ := json.Marshal(detail)
    r.runStore.UpdateTerminalMetadata(ctx, run.ID, json.RawMessage(meta))
}
```

→ store.IAgentRunStore 加方法 `UpdateTerminalMetadata(ctx, runID, json.RawMessage) error`。

### 3.9 失控保护熔断行为

| 维度 | 触发时机（CanProceed 内检测） | 行为 |
|---|---|---|
| max_turns | PreToolCall hook 内：`state.TurnCount >= limits.MaxTurns` | BudgetExceeded + sink send + terminal_metadata.dim |
| max_credits | PreToolCall hook 内：`state.CreditUsed >= limits.MaxCredits`（前一轮 PostToolCall 已 RecordUsage） | BudgetExceeded + terminal_metadata |
| max_wall_time | PreToolCall hook 内：`time.Since(startedAt) >= limits.MaxWallTime` | BudgetExceeded + terminal_metadata |
| max_daily_credits | PreToolCall hook 内：从 in-memory cache 查当日累计 | BudgetExceeded + terminal_metadata |

> reviewer P2-1 修订：max_credits 是 PreToolCall 时检测（用上一轮 PostToolCall 已记录的累计），不是 PostToolCall 立即 stop。1 轮延迟在 Agent 模式可接受。

**采用方案：新增 `HookActionBudgetExceeded=4` + `LoopEventErrorMaxBudget`**（与 #6 PermissionDenied 模式对称）

**理由**（比复用 BlockingStop 更优）：
- TerminalReason 精确为 `error_max_budget`（不是 `stop_hook_prevented`）
- `agent_run.terminal_metadata.budget_dimension` 可精确写入哪一维超限
- downstream（#13 compliance、#14 e2e）可 `WHERE state_reason='error_max_budget'` 精确查询
- 改动 3 处（hooks.go enum / state.go 加 case / HookActionToLoopEvent map），可控

```go
// hooks.go 改动（本 feature）
const (
    HookActionContinue        HookAction = iota // 0
    HookActionStop                              // 1
    HookActionBlockingStop                      // 2
    HookActionPermissionDeny                    // 3 — #6 落地
    HookActionBudgetExceeded                    // 4 — #12 本 feature 新增
)

// hooks.go atomic.Int32 注释同步更新（reviewer P1-1 fix）：
// 0=Continue 1=Stop 2=BlockingStop 3=PermissionDeny 4=BudgetExceeded

// HookActionToLoopEvent 加 case
case HookActionBudgetExceeded:
    return LoopEventErrorMaxBudget
```

**测试同步更新**：`hooks_test.go` 的 HookAction enum 边界测试、HookActionToLoopEvent 覆盖测试增加 BudgetExceeded 用例。

### 3.10 数据流：试聊（admin_test）完整路径

```
父账户 P (user.id=42) → Agent Builder UI → 试聊 → POST /v1/agent-run/create?source=agent_test
                                                              │
controller/v1/agent_run.go → biz/agent/runner.go              ▼
                                                  runner.Run(ctx, req={SourceHint:"agent_test"})
                                                              │
                          ┌───────────────────────────────────┴─────────┐
                          ▼                                              ▼
            credit_service.Reserve                             budget.Tracker.Start
            (with SourceHint=agent_test)                       (limits from agent_def)
                          │                                              │
                          ▼                                              │
            budget.AdminTestConsume(P, R2_estimate)                      │
            → check credit_admin_test_grant.remaining_amount             │
            → if ok: UPDATE used_amount; INSERT credit_transaction       │
              (source_type='admin_test')                                 │
            → if exhausted: return ErrAdminTestExhausted                 │
                          │                                              │
                          ▼                                              │
                  Hook chain (permission → budget → sandbox)             │
                          │ ◀─── budget.WrapHooks 每轮 check ────────────┘
                          ▼
            实际 LLM 调用 (aiservice.Chat) → 实际 tokens
                          │
                          ▼
            credit_service.Reconcile(rsvID, actualCost)
            → budget.AdminTestRefund(P, txID, refund)
            → UPDATE credit_admin_test_grant SET used_amount -= refund
            → INSERT credit_transaction (source_type='admin_test', amount=+refund)
```

---

## 4. 边界与不变量

### 4.1 不变量（绝不可破）

1. **既有 SOP/Chatbot Reserve/Reconcile 调用方零改动** — 不传 SourceHint 默认走 FIFO 三池
2. **现有 6 种 source_type + NULL 行 ALTER 后全过 CHECK**
3. **`agent_run.terminal_metadata` 字段 ALTER ADD COLUMN AFTER state_reason** 不影响既有行（NULL 默认）
4. **AdminTest 不 fallback 三池** — 耗尽就 ErrAdminTestExhausted，禁止误扣父账户正式积分
5. **0 prod**：6 条全守

### 4.2 设计取舍 / Trade-offs

| 取舍 | A 选项 | B 选项 | 选择 | 理由 |
|---|---|---|---|---|
| 跨 Run daily credit aggregate | in-memory cache + DB lazy sync | Redis Cluster | A | v1 单实例 / 复杂度优先 |
| HookAction for budget exceeded | 复用 BlockingStop | 新增 HookActionBudgetExceeded | B | 对称 + 元数据可区分 |
| admin_test 耗尽 fallback | fallback 到正式积分 | 阻塞试聊 | B（蓝本明示）| 防误扣 |
| period_start / period_end | 每月动态生成 | 写死 SQL CURDATE | 动态生成 | 测试可控 |
| terminal_metadata 写入 | runner.Run 末尾扫 budgetGate | hook 内直接写 DB | runner 末尾 | 减 DB 写次数 |
| budget v1 维度 | 蓝本 3 维 | 蓝本 3 维 + DailyCreditCap | 4 维 | agent_definition.daily_credit_cap 已存在必须接入 |

### 4.3 已知风险

- **风险 R-1**：in-memory daily aggregate 多实例不一致 → v1 接受（单实例 dev）；prod 多实例引入 Redis（#14 决策）
- **风险 R-2**：admin_test ALTER CHECK constraint 时 prod 有 in-flight Reserve 写入 → 阻塞写几秒（MySQL 8 ALTER CHECK 是 instant DDL，但保险起见 dev 部署前确认）
- **风险 R-3**：`credit_admin_test_grant` 生成列 (`remaining_amount`) SQLite 测试支持依赖版本（3.31+）— **采用方案**：GORM model 字段标 `gorm:"->;type:int as (granted_amount - used_amount) stored"` （`->;` 表示 read-only），AutoMigrate 在 SQLite 模式跳过 generated；测试代码不依赖 remaining_amount = granted_amount - used_amount 的 DB 计算，改用 model 的 `Remaining()` 方法（在 Go 代码计算）。S2 spec 明确 model + 测试 helper
- **风险 R-4**：terminal_metadata JSON 字段 prod 历史数据 NULL → 前端 #11 必须容错 null（已写在 contract 注释）

---

## 5. PRD 摘要（对接产品）

### 5.1 父账户视角

- **试聊体验**：保存 Agent → 顶部提示"想试试效果？立即试聊" → 进对话窗顶部标 "试聊模式 · 本月剩余 4,820 / 5,000 积分"
- **配额机制**：每月 1 号 5,000 积分自动到账；月底未用作废，不累积
- **耗尽行为**：弹出 Modal "本月测试配额已用完，下月 X 天后刷新。Tip: 如调整频繁，可联系运营提升额度"
- **正式积分零影响**：试聊不动 cycle / booster / trial

### 5.2 学员视角

- **任务前透明**：发送前显示预估积分消耗（# 11 落地，本 feature 后端契约就绪）
- **任务中透明**：status bar 每 5s 刷新已用积分 / 余额（# 11 落地）
- **失控保护**：4 维超限弹 Modal 二选一（继续 +200 积分 / 停止），UI 由 #11 实现

### 5.3 财务 / 运营视角

- **B2B 月度对账**：admin_test 池消耗不入 `b2b-billing-report` 输出（试聊积分 = 平台赠送 = 不向父账户收费）
- **运营调额**：通过修改 cron 任务的 granted_amount（v1 不接 cron，运营改 SQL UPDATE）
- **审计**：每次 admin_test consume / refund 都进 credit_transaction（source_type='admin_test'），可查可追

---

## 6. 验收标准（DoD）

- [ ] biz/budget 子包覆盖率 ≥ 80%
- [ ] `go test -race -count=1 -timeout=120s ./internal/numind/biz/budget/...` 输出全 PASS 留证（S5 acceptance doc 引用）
- [ ] credit_admin_test_grant 表 + agent_run.terminal_metadata 字段 + source_type CHECK ALTER 三个 migration 双文件全 ready（含 rollback）
- [ ] GET /v1/credits/balance 响应字段扩展 admin_test_pool 且向后兼容（旧字段不动）
- [ ] AgentRunner.Run 4 维任一超限 → TerminalErrorMaxBudget + terminal_metadata 写入
- [ ] AdminTestConsume / Refund 调通 credit_transaction (source_type='admin_test')
- [ ] 既有 SOP/Chatbot Reserve 调用方代码零改动（grep 全仓库验证）
- [ ] biz/agent / biz/credit / biz/membership 测试覆盖率不下降
- [ ] config_prod.yaml zero diff
- [ ] 不打 git tag / 不调 /deploy-prod
- [ ] 不动 prod SSH / prod 环境变量
- [ ] S5 acceptance doc 含 BudgetTracker 4 维 + admin_test 池 + R2 估算的端到端测试证据

---

## 7. S2 / S3 / S4 计划预览

- **S2 spec**：详 DB DDL + 全接口签名 + R2 估算公式 + Reserve/Reconcile 分支决策树 + 测试矩阵
- **S3 plan**：原子 task 拆 M1-M15+ + Wave 分配 + Tier 3 并行 disjoint check + S5 验证策略
- **S4 编码**：实施 + per-task spec/code 双 reviewer + P0/P1 全修
- **S5 acceptance**：覆盖率 / race detector / 0 prod 检查 / 4 维触发 e2e
- **S6 ndf-done**：手动 merge develop（与 #6 PreToolCall 区域 conflict resolve）+ deploy-checklist + worktree 清理

