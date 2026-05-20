# NDF S0 Requirement Card · `agent-mode-billing-integration`

**Track**：Standard
**Feature ID**：`agent-mode-billing-integration`（14-feature 分解 #12/14）
**起草日期**：2026-05-21
**起草人**：AI（autopilot）
**状态**：S0 草案
**依赖**：
- #2 `agent-mode-runtime-skeleton`（merged `45770bb5`）— LoopState / TerminalReason 含 `TerminalErrorMaxBudget` + LoopEvent `LoopEventErrorMaxBudget` + RunHooks 接口
- #5 `agent-mode-skill-system`（merged `e05498b6`）— HookActionRegistry race-safe；agent_definition 表（含 `daily_credit_cap` / `credit_cap_per_session` 字段，本 feature 读，不改）
- #6 `agent-mode-permission-pipeline`（merged `65e9d144`）— PermissionGate.Check 在 PreToolCall hook 内串行（本 feature 在 permission 之后追加 BudgetTracker）
- 既有 `credit_service` 与 `MembershipService.GetBalance`（5 SOT 表 + Reserve/Reconcile 两阶段）

**阻塞**：#11 `agent-mode-student-ux`（学员侧成本透明 UX 读 GET `/v1/credits/balance` 含 admin_test 池）/ #14 `agent-mode-e2e-rollout`（端到端验收）

---

## 1. 起因（Why now）

Agent 模式 14-feature 分解的 **#12/14** —— Billing Integration 是 Agent 模式的"成本与失控防线"。蓝本 §4.1.8 / §4.3.8 / §6.6 三个章节交付：

1. **失控保护 BudgetTracker（蓝本 §4.1.8 Layer 4）** — 单 Run 内 4 维（步数 / token / 时长 / 日累计积分）任一超限即 terminal。复用 #2 状态机 `TerminalErrorMaxBudget`。
2. **试聊配额 `admin_test`（蓝本 §4.3.8）** — 父账户每月 5000 试聊积分独立池，与三池（trial/subscription/booster）完全隔离。Agent Builder 试聊路径专用。
3. **`credit_transaction.source_type` CHECK constraint** — #5 明确 defer 到 #12：新增 `'admin_test'` 枚举值。
4. **学员积分透明（蓝本 §6.6 后端契约）** — GET `/v1/credits/balance` 返回三池 + admin_test 实时余额。前端 UX 在 #11 实现。
5. **Reserve / Reconcile 集成** — Agent 单轮 LLM 调用前 R2 估算 Reserve，调用后实际用量 Reconcile（多退少补）。复用既有 `credit_service.Reserve/Reconcile`。

**前 11 个 feature 完成度（#12 入场前提）**：
- ✓ #1 V3 + Phase0 ADR
- ✓ #2 Runtime skeleton（TerminalErrorMaxBudget 在 12 个 Terminal 中已是第 7 个）
- ✓ #3 Tool Registry（38 字段含 R2 估算输入字段：input_token_estimate / output_token_estimate）
- ✓ #4 Sandbox（沙箱内置工具的 token 用量记录）
- ✓ #5 Skill（agent_definition 表含 `daily_credit_cap` / `credit_cap_per_session`，本 feature 只读）
- ✓ #6 Permission Pipeline（PreToolCall hook 已建好；本 feature 在 permission 之后串行追加 BudgetTracker）
- ✓ #7 Memory / #8 Narration / #9 Compact（已 merged，与本 feature 无 PreToolCall hook 冲突）

**核心矛盾**：Agent 模式比 SOP/Chatbot 风险高出一个数量级 — LLM 决定下一轮工具调用 → 单次 run 可能跑 30 步 → 单步 1k token = 30k token = 几百积分。**没有失控保护**就是"用户余额一次任务烧光"。**没有试聊隔离**就是"父账户给自己开通学员账户配 100 元/月会员，反复试聊调问卷 = 一周烧光"。

---

## 2. 业务范围

> **关键术语对齐**：
> - 蓝本 §4.3.8 `source_type='admin_test'` ↔ Numind `credit_transaction.source_type` CHECK constraint 新增 `'admin_test'` 枚举值
> - 蓝本 `tenant_id` ↔ Numind `parent_user_id`（B2B2C 父账户，与 #5/#6 一致）
> - 蓝本 §4.1.8 `BudgetTracker` 4 维（MaxSteps / MaxCredits / MaxWallTime + 蓝本未拆但本 feature 拆出来的 MaxDailyCredits）

### In scope

#### 2.1 DB 层（1 张新表 + 1 个 CHECK constraint ALTER + 1 个 ALTER ADD COLUMN + 双 migration 文件）

**`credit_admin_test_grant` 表（B2B 父账户试聊独立配额）**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | BIGINT UNSIGNED PK AI | 主键 |
| parent_user_id | INT UNSIGNED NOT NULL INDEX | 父账户 user.id（独立账户也算父，即 parent_user_id = self.id） |
| granted_amount | INT UNSIGNED NOT NULL DEFAULT 5000 | 当月赠送积分（运营可调，默认 5000）|
| used_amount | INT UNSIGNED NOT NULL DEFAULT 0 | 当月已用 |
| remaining_amount | INT GENERATED ALWAYS AS (granted_amount - used_amount) STORED | 剩余（生成列，覆盖索引避免回表）|
| period_start | DATE NOT NULL | 当月起始日（YYYY-MM-01）|
| period_end | DATE NOT NULL | 当月最后一天（月底失效）|
| granted_at | DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP | 入账时间 |
| last_used_at | DATETIME NULL | 最近一次试聊扣费时间 |

**唯一约束**：`UNIQUE KEY uq_parent_period (parent_user_id, period_start)` —— 同父账户同月只允许一条 grant 记录（幂等键）。

**复合索引**：`idx_period_remaining (period_end, remaining_amount)` —— 月底失效扫描走覆盖索引。

**注释**：`COMMENT='配置者试聊独立测试配额（每月赠送 5000，月底作废，不累积）'`

> **蓝本 §8 第 11 表的 `tenant_id BIGINT NOT NULL` 字段在 Numind 实现中省略 — 用 `parent_user_id` 替代**。理由：Numind B2B2C 模型只有用户层级（`user.parent_user_id` 表示父子关系），不引入独立 tenant 概念。唯一约束相应改名为 `uq_parent_period`。所有蓝本中提到 tenant_id 的 SQL 都按此映射执行。

**`credit_transaction.source_type` CHECK constraint ALTER**

现有 CHECK 枚举（业务规则 §4）：`'trial' / 'subscription' / 'cycle' / 'booster' / 'admin' / 'system' / NULL（legacy 行）`

**migration 改动**：
```sql
-- UP
ALTER TABLE credit_transaction DROP CHECK chk_ct_source_type;
ALTER TABLE credit_transaction ADD CONSTRAINT chk_ct_source_type
  CHECK (source_type IN ('trial','subscription','cycle','booster','admin','system','admin_test') OR source_type IS NULL);

-- DOWN (rollback)
ALTER TABLE credit_transaction DROP CHECK chk_ct_source_type;
ALTER TABLE credit_transaction ADD CONSTRAINT chk_ct_source_type
  CHECK (source_type IN ('trial','subscription','cycle','booster','admin','system') OR source_type IS NULL);
```

> MySQL 8.0+ 支持 ALTER CHECK；SQLite（测试 in-memory）不强制 CHECK，无破坏。

**`agent_run.terminal_metadata` 字段 ALTER ADD COLUMN**

#2 落地的 `agent_run` 表当前字段（见 `internal/pkg/model/agent_run.go`）：`status / state_reason VARCHAR(50) / messages JSON / reservation_id / started_at / ended_at / compact_state JSON / compact_summary LONGTEXT / created_at / updated_at`。

`StateReason` size:50 太短且非结构化，无法承载 `budget_dimension` + 其他维度元数据。本 feature 新增 `terminal_metadata JSON` 字段：

```sql
-- UP
ALTER TABLE agent_run ADD COLUMN terminal_metadata JSON NULL COMMENT 'Terminal 时机的结构化元数据，如 budget_dimension'
  AFTER state_reason;

-- DOWN
ALTER TABLE agent_run DROP COLUMN terminal_metadata;
```

GORM model 同步加字段 `TerminalMetadata datatypes.JSON gorm:"type:json"`。本 feature 内只写 `budget_dimension`；#13 compliance / #14 e2e 可继续追加其他键。

#### 2.2 biz/budget 子包（新建）

```
internal/numind/biz/budget/
├── tracker.go             # BudgetTracker interface + 实现（持 4 维 limits + atomic counters）
├── dimensions.go          # 4 维 enum + 单维 check 方法
├── r2_estimator.go        # R2 估算 wrapper（复用 internal/pkg/pricing）
├── admin_test.go          # 试聊配额管理（Grant / Consume / Expire）
└── *_test.go              # 单测含 race detector
```

#### 2.3 biz/agent 集成（既有包改动）

- **`runner.go`** PreToolCall hook 区：在 #6 PermissionGate.Check 之后追加 `BudgetTracker.CanProceed`；任一维超限 → `HookActionBlockingStop` + 状态机 `LoopEventErrorMaxBudget`
- **`runner.go`** PostToolCall hook 区：调 `BudgetTracker.RecordUsage(ctx, runID, actualTokens)`
- **`runner.go`** Run 进入主循环前：每轮开始 `tracker.RecordStep()` + Check wall-clock
- **`hooks.go`** RunHooks 新增 `BudgetTracker` 字段 + `BudgetRunID` 字段（与 #8 Narration 模式一致）
- **`biz.go`** wire：新增 `WithBudgetTracker(t BudgetTracker) RunnerOption` —— 顶层 service Init 时注入
- **`helper.go`** AutoMigrate：新增 `&model.CreditAdminTestGrant{}`

#### 2.4 既有 credit_service 改造（最小化）

- `Reserve(req CreditReserveRequest)` 接受新字段 `SourceHint string`：`"agent_run"` / `"agent_test"`。`"agent_test"` 路径优先扣 `credit_admin_test_grant.remaining_amount`，扣完阻塞（**不 fallback 到三池**，避免误扣父账户正式积分）
- `Reconcile(req CreditReconcileRequest)` 同样按 SourceHint 走 admin_test 或三池
- `credit_transaction.source_type` 新增 `'admin_test'` 行（受 CHECK constraint 保护）
- **本 feature 不重写既有 Reserve/Reconcile 业务逻辑**，仅加 SourceHint 分支

#### 2.5 API 端点（学员侧 + 父账户侧合并）

- **扩展既有 `GET /v1/credits/balance`**（user_token）→ 返回结构追加 `admin_test_pool` 字段（仅父账户非零，子账户固定 0）
  ```json
  {
    "trial":         {"remaining": 120, "expires_at": "2026-05-23"},
    "subscription":  {"remaining": 1800, "current_cycle_end": "2026-06-01"},
    "booster":       [{"remaining": 600, "expires_at": "2026-08-19"}],
    "admin_test":    {"remaining": 4820, "period_end": "2026-05-31"}  // ← 新增
  }
  ```
- **不新增管理端端点**（管理端配额查询/调整 = #10 落地）
- **不新增 cron 端点**（月度刷新由独立 daemon 触发，本 feature 出 stub 函数 `budget.AdminTestExpireDaemon` 但**不接 cron 调度**，避免与 prod 调度耦合 —— `#14 e2e-rollout` 决策）

#### 2.6 失控保护四层

> 注：蓝本 §4.1.8 BudgetTracker 是三维（步数/积分/时长）；本 feature 拆出第 4 维"日累计积分上限"，理由 — agent_definition 已有 `daily_credit_cap` 字段 #5 落地，必须用上。

| 维度 | 来源 | 默认值 | terminal_reason | budget_dimension |
|---|---|---|---|---|
| **TurnCount**（步数）| `agent_definition.max_turns_per_run` 或默认 50 | 50 | `error_max_budget` | `max_turns` |
| **TokenCount**（积分）| `agent_definition.credit_cap_per_session × R2_系数` | 800 | `error_max_budget` | `max_credits` |
| **WallClock**（时长）| 默认 300s | 300s | `error_max_budget` | `max_wall_time` |
| **DailyCreditCap**（日累计）| `agent_definition.daily_credit_cap` 或默认 2000 | 2000 | `error_max_budget` | `max_daily_credits` |

`budget_dimension` 元数据写入 `agent_run.terminal_metadata` JSON 字段（本 feature ALTER ADD，见 §2.1）。

### Out of scope

- **prod 部署**（不部 prod，蓝本 #14 / 用户决定）
- **真实 LLM token 计算精确化**（v1 用 R2 估算 placeholder；精确化在 #14）
- **学员积分余额展示 UI**（#11 已 spawn 在 web-v3 worktree）
- **管理端积分配置 UI**（#10 已 spawn 在 admin-web worktree）
- **跨账户积分共享**（v2 待定）
- **月度 cron 调度真实接入**（v1 出 stub daemon 函数，调度接入留 #14）
- **23 个 Bash validator 完整扩展**（与 #6 一致，留 backlog）
- **Slack 财务告警接入**（运营手动监控）

---

## 3. Triage（Standard 5 条）

1. **DB schema 变更**：是（1 新表 `credit_admin_test_grant` + 1 CHECK constraint ALTER）✓
2. **新 API 端点**：否（端点字段扩展，controller + response struct 改动，**不**改 router.go）— 但仍需走 controller review，且响应字段扩展要保持向后兼容（旧字段不动，新字段可选）✓
3. **新外部服务集成**：否
4. **影响文件数**：>3（migration ×2 + model + store ×2 + 4 biz/budget files + 4 biz/budget *_test files + credit_service.go + runner.go + hooks.go + biz.go + helper.go + credit_controller.go + state.go = ≥17）✓
5. **高风险业务逻辑**：是（计费 = 高风险；本 feature 改 credit_transaction CHECK constraint + Reserve/Reconcile 新分支，全 prod 用户的计费路径都受影响 — 必须最严格 review）✓

**触发条件**：1 + 2 + 4 + 5 多条 → **Standard track**。

---

## 4. 业务目标 / 成功标准

1. **失控阻断**：单 Run 4 维任一超限 → 1s 内 terminal、`terminal_reason='error_max_budget'`、`budget_dimension` 字段写入 agent_run.terminal_metadata
2. **试聊隔离**：父账户在 Agent Builder Modal 内"试聊一下" → 扣 `credit_admin_test_grant`，不动 trial/subscription/booster 三池；admin_test 耗尽 → 阻塞试聊（不 fallback 到正式积分）
3. **零回归**：现有 SOP/Chatbot Reserve/Reconcile 完全不受影响；现有 6 种 source_type 行（NULL legacy + 6 已知）通过 CHECK constraint
4. **审计可查**：每次 Agent Run 终止于 `error_max_budget` 都有 `agent_run.terminal_metadata.budget_dimension` 可查
5. **R2 估算**：Reserve 阶段按 R2 估算预扣；Reconcile 阶段实际扣减 ≤ Reserve（多退少补，零负数余额）
6. **0 prod 影响**：6 条全守 — config_prod.yaml zero diff / 不打 git tag `v*` / 不 `/deploy-prod` / feature 分支 pre-push 拦截 / migration SQL 不在 dev/prod CI 自动跑 / 不动 prod 环境变量与服务器 SSH
7. **覆盖率**：biz/budget ≥ 80%；biz/credit（既有）不下降

---

## 5. 优先级与节奏

**优先级**：P0（蓝本 §4.1.8 失控保护是 Agent 模式的生死线 + §4.3.8 试聊配额是 B2B 模式的产品差异化）

**节奏（Standard track，估时 W11）**：
- S0 requirement card：本文件
- S1 proposal + PRD：技术方案 + 用户故事 + B2B 父账户 / 学员双视角 PRD
- S2 spec：详细 DB DDL + 接口签名 + R2 估算公式 + Reserve/Reconcile 分支决策树
- S3 plan：原子 task 拆分（M1-Mn）+ Wave 调度 + S5 验证策略
- S4 编码：实施 + per-task reviewer + P0/P1 全修
- S5 acceptance：覆盖率 / race detector / 0 prod 检查
- S6 ndf-done：merge develop + 清 worktree + deploy-checklist

---

## 6. 备注

**蓝本 §4.1.8 BudgetTracker 三维 vs 本 feature 四维差异说明**：

蓝本 §4.1.8 文字段 "四维预算" 但 Go struct 示例只给三维（MaxSteps / MaxCredits / MaxWallTime）— 蓝本本身有蓝图与 struct 不一致的偏差。本 feature 显式落实四维（增 DailyCreditCap），理由：
- `agent_definition.daily_credit_cap` 字段 #5 已落地（agent_definition 表），但**没有读取方**——本 feature 接入
- 三维都是"单 Run 内"上限；日累计是"跨 Run"上限，独立维度
- 学员可能在一天内连续触发多个 Agent Run，三维都不超的情况下，日累计仍可能爆雷

**#6 PreToolCall hook 区域 merge conflict 预期**：

#6 在 PreToolCall hook wrapper 内加了 `PermissionGate.Check`。本 feature 在同一 wrapper 内追加 `BudgetTracker.CanProceed`。串行顺序：

```
permission.Check (deny 短路) → budget.CanProceed (deny 短路) → base hook (启动容器 / 工具实际调用)
```

理由：permission deny 在前 = 即使预算超也不暴露权限内部状态；budget deny 在后 = 允许 audit 已用积分。

S6 手动 merge develop 时这块**必有 conflict**，已在 prompt 中预告。

**`credit_transaction.source_type` CHECK constraint 改动声明**：

#5 明确"零 source_type 枚举改动"，把 `admin_test` 值留到 #12。本 feature **唯一一次** ALTER 该 CHECK constraint。MySQL 8.0+ 支持 ALTER；SQLite 测试容器不强制 CHECK，测试无影响。Rollback 完整给出（DROP + ADD 旧枚举）。

**0 prod 红线**：
- config_prod.yaml 不动
- 不 `/deploy-prod`
- 不 `git tag v*`
- feature/* 分支 pre-push hook 拦
- migration SQL 文件不在 dev/prod CI 自动跑（per `project_dev_deploy_migration_gap`），上线前用户手 SSH 跑

---

**完成本 Card 后**：标记 S0 done，进 S1。

