# legacy-system-deprecation — S2 技术设计

**Feature ID**: `legacy-system-deprecation`
**Track**: Standard
**Stage**: S2 (技术设计)
**作者**: AI (claude-opus-4-7)
**审阅人**: zchen27@tulane.edu
**日期**: 2026-05-18

## §0 上下文与关联

- 前置：hotfix `credits-trial-grant-bypass-fix`（已 prod 上 v2.1.22 / v1.0.22）
- 母 feature：`membership-credits-redesign`（其 Task 16 cleanup 的工程化收尾）
- Prod 现状（2026-05-18 直查）：246 用户全 `billing_mode=credits` + `user_tier=free`，legacy 代码路径全部死路径
- S0 需求卡：[requirements/legacy-system-deprecation.md](../../requirements/legacy-system-deprecation.md)
- S1 提案：[proposals/legacy-system-deprecation-proposal.md](../../proposals/legacy-system-deprecation-proposal.md)

## §1 设计原则

1. **One billing path = credits SOT**：删除后没有"过渡用户"概念，只有 credits 三池 (trial_grant + credit_cycle + user_booster_balance)
2. **零数据迁移**：prod 已 100% credits + free，不需要数据 UPDATE，只需 schema DROP
3. **可回滚边界**：代码改动可 git revert；schema DROP 提供完整 rollback SQL；不可逆操作前必须 prod backup
4. **append-only migration 历史**：不删除已运行过的 migration 文件，新增 DROP migration
5. **跨仓部署松耦合**：server 先（API 字段 omitempty），前端两仓 ≥1 天后；不要求 lock-step

## §2 8 项技术决策（S2 brainstorming 输出）

| # | 决策 | 选定方案 |
|---|---|---|
| 1 | API 字段 `remaining_runs` / `monthly_limit` | 直接从响应里移除（Go struct `omitempty` + 不再 set） |
| 2 | `admin_migration` controller | 整删（含路由 + admin-web MigrationsView 页面） |
| 3 | `IncrementSopRunCount` 方法 | 删调用方 + 删方法体（grep 找所有调用一并清） |
| 4 | `tier_change_log` 审计表 | 改名 `legacy_tier_change_log` + 停止写入（保留只读历史） |
| 5 | 2 个老 billing_mode migration SQL | 保留不动（append-only 历史） |
| 6 | doc 文件（CLAUDE.md / dev-env-setup / credits-system docs） | inline 改写，留锚点 `legacy_tier billing mode removed in 2026-05` |
| 7 | 跨仓部署顺序 | server 先 → 前端两仓 ≥1 天后部署 |
| 8 | Task 4 schema DROP 回滚 | 完整 ALTER ADD COLUMN rollback SQL + prod backup 双保险 |

## §3 架构差异（before / after）

### Before（hotfix v2.1.22 后的现状）

```
Request → CreditController.GetBalance
    ├─ if user.BillingMode == "credits" → getBalanceFromMembership → MembershipService ✓
    └─ else → creditSvc.GetBalance
                └─ if isEffectiveLegacy(user) → legacyTierImpl.GetBalance  ← 死路径但仍在
                └─ else → creditsImpl.GetBalance

Request → SOP CreateRun
    └─ creditSvc.GetBalance (含 isEffectiveLegacy 分发) + sop.go credits 预检 ✓

Request → CanPerformAIOperation
    ├─ if isEffectiveLegacy(user) → user.CanRunSOP()  ← 死路径
    └─ else → membershipSvc.GetBalance ✓
```

### After（feature 完成后）

```
Request → CreditController.GetBalance
    └─ getBalanceFromMembership → MembershipService（单路径）

Request → SOP CreateRun
    └─ creditSvc.GetBalance → creditsImpl.GetBalance → MembershipService

Request → CanPerformAIOperation
    └─ membershipSvc.GetBalance（单路径）
```

简化目标：**所有 dispatch 函数变成 ICreditService 接口的直接调用**，无 isEffectiveLegacy 分支。

## §4 API 契约差异

### `GET /v1/credits/balance`

**Before**（credits 用户响应；legacy 用户响应另一路径，本 feature 后不存在）：
```json
{
  "billing_mode": "credits",
  "membership_state": "free",
  "trial_remaining": 200,
  "cycle_remaining": 0,
  "booster_total": 0,
  "booster_usable": 0,
  "balance": 200,
  "sub_total": 0,
  "sub_remain": 0,
  "booster_remain": 0,
  "trial_expires_at": "2026-05-19T00:01:33Z"
}
```

**After**（移除 sub_total / sub_remain / booster_remain，保留新字段；同时移除 legacy 路径分支）：
```json
{
  "membership_state": "free" | "trial" | "pro",
  "trial_remaining": 200,
  "cycle_remaining": 0,
  "cycle_end": null,
  "booster_total": 0,
  "booster_usable": 0,
  "sub_expires_at": null,
  "trial_expires_at": "2026-05-19T00:01:33Z"
}
```

**变更详情**：
- 删字段：`billing_mode`（始终 credits 无意义）、`sub_total`、`sub_remain`、`booster_remain`、`balance`
- 保留：BalanceDTO 全字段

**前端兼容性**：CreditBalanceCard 已在 hotfix 中改为读 `trial_remaining`；本 feature Task 2 把 `sub_remain` 改成读 `cycle_remaining`、删 `balance` / `sub_total` 等显示。

### `GET /v1/user` (current user)

**Before**:
```json
{
  "id": 123,
  "phone": "...",
  "user_tier": "free",
  "tier_expires": null,
  "remaining_runs": 0,
  "monthly_limit": null,
  ...
}
```

**After**:
```json
{
  "id": 123,
  "phone": "...",
  ...
}
```

**删字段**：`user_tier`, `tier_expires`, `remaining_runs`, `monthly_limit`

### `GET /v1/customer/children/:id` + `ListSubUsers` + `GetCustomerStatistics`

同上，删 `remaining_runs` / `monthly_limit` 字段。

### `POST /v1/admin/migrate/billing-mode` 等 admin migration 端点

**整路由删除**（Task 2）。配套 admin-web MigrationsView 整页删。

### `POST /v1/admin/users/:id/tier` 等 admin tier 编辑端点

**Body 字段精简**：`controller/v1/admin_user/user.go:273-293` 中 user_tier / tier_expires / monthly_sop_runs / monthly_reset_at 4 字段移除。如果该 endpoint 整体后续只剩昵称/状态等字段，保留；否则一并删（S4 实施时根据残留字段判断）。

## §5 DB Schema 变更

### Task 4 DROP migration

文件：`numind-server/migrations/{YYYYMMDD}_{HHMMSS}_drop_legacy_user_columns.sql`

```sql
-- Task 4 of legacy-system-deprecation feature.
-- Preconditions:
--   1. server >= v{Task1-tag}; web-v3 >= v{Task2-tag}; admin-web >= v{Task2-tag}
--   2. Prod 7+ days no regression after Task 3 deploy
--   3. Prod DB full backup verified, restore drill passed in dev DB
--   4. Audit: SELECT COUNT(*) FROM user WHERE user_tier != 'free' OR tier_expires IS NOT NULL = 0

ALTER TABLE `user`
  DROP COLUMN `user_tier`,
  DROP COLUMN `tier_expires`,
  DROP COLUMN `monthly_sop_runs`,
  DROP COLUMN `monthly_reset_at`,
  DROP COLUMN `billing_mode`;

-- tier_change_log 改名（决策 4）
RENAME TABLE `tier_change_log` TO `legacy_tier_change_log`;
```

Rollback SQL：`{...}_drop_legacy_user_columns_rollback.sql`

```sql
-- Rollback: 恢复 5 个字段。注意：DROP 后字段数据丢失，rollback 仅恢复 schema，
-- 数据需从 backup 单独 restore（参见 §8.2 Rollback procedure）。

ALTER TABLE `user`
  ADD COLUMN `billing_mode` ENUM('legacy_tier','credits') NOT NULL DEFAULT 'credits',
  ADD COLUMN `monthly_sop_runs` INT DEFAULT 0,
  ADD COLUMN `monthly_reset_at` TIMESTAMP NULL DEFAULT NULL,
  ADD COLUMN `user_tier` VARCHAR(20) DEFAULT 'free',
  ADD COLUMN `tier_expires` TIMESTAMP NULL DEFAULT NULL;

-- 索引重建
ALTER TABLE `user`
  ADD INDEX `idx_user_billing_mode` (`billing_mode`),
  ADD INDEX `idx_user_tier` (`user_tier`);

-- 审计表改回
RENAME TABLE `legacy_tier_change_log` TO `tier_change_log`;
```

### 老 migration 文件处理（决策 5）

保留不动：
- `migrations/20260419_100000_add_billing_mode_to_user.sql`
- `migrations/20260419_100500_init_billing_mode_values.sql`
- `migrations/20260419_100500_init_billing_mode_values_rollback.sql`
- `migrations/add_tier_change_log.sql`

它们是历史 schema 演化记录，append-only。

## §6 sub-task 详细范围

### Task 1: 后端核心 dispatch 清理 (server only)

**修改文件**：
- `internal/numind/biz/credit/credit_service.go`
  - 删 `isEffectiveLegacy` 函数（line 87-105）
  - 删 `legacyTierImpl` struct + 4 个方法（line 296-358）
  - 删 `creditService.legacy` 字段（line 56）+ 初始化（line 82）
  - 简化 `CheckAndEstimate` / `Reserve` / `GetBalance` / `CheckAndEstimateBudget` 各 dispatch 方法为直接调用 `s.credits.*`，删 if 分支
- `internal/numind/biz/credit/credit.go`
  - `CanPerformAIOperation` 删 legacy 分支（line 65-73）+ 兜底 `b.ds.Credits().GetBalance` 分支（不再需要 fallback，membershipSvc 总是 wire）
- `internal/numind/biz/credit/credit_service_langfuse.go`
  - 删 BillingMode span 字段 / metadata（line 40, 189）
  - `classifyDeductedFrom` 删 `none(legacy)` 分支（line 203）
- `internal/numind/biz/sop/sop.go`
  - 删剩余 log 字段（已 hotfix 在 CreateRun 删 HasActiveMembership 分支；本次检查其它函数）

**测试**：
- 修改 `credit_service_test.go`：删 isEffectiveLegacy 测试用例；保留双制测试只跑 credits 路径
- 修改 `credit_service_boundary_test.go` / `credit_service_reserve_test.go`：删 legacy 边界用例
- 新增：smoke test 验证 credits 用户 GetBalance / CheckAndEstimate / Reserve 三路径都返回正确

**API 契约变化**：
- `GET /v1/credits/balance` 响应：删 `billing_mode` / `sub_total` / `sub_remain` / `booster_remain` / `balance`（5 字段）

**Deploy**: server 单独 tag，prod soak ≥1 周

---

### Task 2: 边界 caller + admin + 前端清理 (3 repos)

**numind-server 修改**：
- `internal/numind/biz/customer/customer.go`: 删 line 171 / 230 / 270 三处 `GetRemainingSOPRuns` 调用 + 删响应字段
- `internal/numind/controller/v1/user/get.go`: 删 line 64-88 中 user_tier / tier_expires / monthly_sop_runs / remaining_runs 4 字段
- `internal/numind/controller/v1/admin_user/user.go`: 删 line 273-293 中 4 个字段更新逻辑
- `internal/numind/controller/v1/admin_migration/migrations.go`: 整文件删 + 路由删除
- `internal/numind/biz/payment/payment.go`: 删 line 142 legacy_tier 拒绝分支
- `internal/numind/biz/credit/grant_membership.go`: 删 line 172 Step A flip billing_mode
- `internal/numind/store/customer.go`: 删 `IncrementSopRunCount` 方法（line 299-339）+ 所有调用方
- `internal/numind/router.go`: 删 admin_migration 路由注册

**numind-web-v3 修改**：
- `src/api/credits.ts`: 删 `QuotaBreakdown.remaining_runs` / `monthly_limit` / `billing_mode` 字段
- `src/stores/credits.ts`: 删 `billingMode` computed + `displayState` 中 `'legacy'` 分支（line 52, 85）
- `src/components/credit/CreditBalanceCard.vue`:
  - 删 `'legacy'` cardState 分支 + 整个 legacy 模板（line 27-37）
  - 删 `legacyUsed` computed（line 101-105）
  - cardState 判定中删 `billing_mode === 'legacy_tier'` 分支
  - 字段映射改：`sub_remain` → `cycle_remaining`，删 `sub_total` 显示
- `src/components/credit/BoosterPurchaseCard.vue`: 删 line 92 `billing_mode === 'legacy_tier'` 分支

**numind-admin-web 修改**：
- `src/api/credits.ts`: interface 去掉 `legacy_tier`
- `src/api/users.ts`: 删 `user_tier` 字段
- `src/views/CreditUsersView.vue`: 删 line 197-201 legacy banner
- `src/views/UsersView.vue`: 删 line 51 "等级" 表格列 + line 130 selectedTier + line 224-234 user_tier badge
- `src/views/MigrationsView.vue`: **整页删** + router 注册删

**测试**：
- 前端 type-check / lint
- Playwright admin/user 关键路径 smoke（登录、设置页、SOP 运行、客户管理）

**Deploy**: server tag (v2.1.x) + web-v3 tag (v1.0.y) + admin-web tag（同期或滞后 1 天）；prod soak ≥1 周

---

### Task 3: User model + tests 清理 (server only)

**numind-server 修改**：
- `internal/pkg/model/user.go`:
  - 删 `GetActualUserTier()` 方法（line 82-92）
  - 删 `HasActiveMembership()` 方法（line 101-102）
  - 删 `CanRunSOP()` 方法（line 107-152）
  - 删 `GetRemainingSOPRuns()` 方法（line 155-199）
  - 删 `IsInNewSOPMonth()` 方法（line 203-219）
  - 删常量 `UserTierFree` / `UserTierTrial` / `UserTierStandard` / `UserTierPremium`（line 50-54）
  - 删 `BillingModeLegacyTier` 常量（保留 `BillingModeCredits` 暂留，Task 4 一并删）
  - **保留** struct 字段（`UserTier`, `TierExpires`, `MonthlySopRuns`, `MonthlyResetAt`, `BillingMode`），Task 4 schema DROP 同时删
- 删 legacy 专用测试文件：
  - `internal/pkg/model/user_billing_mode_test.go`（整删）
  - `internal/numind/biz/credit/credit_service_test.go` 中 legacyTierImpl 相关用例
  - 删 `internal/numind/biz/credit/legacyTierImpl_test.go` 如存在
- 修复双制测试 → credits-only：
  - `credit_service_boundary_test.go` / `credit_service_reserve_test.go`：删 legacy 用例

**测试**：
- `task test` 全 PASS

**Deploy**: server 单独 tag，prod soak ≥3 天

---

### Task 4: Schema DROP migration (server only, DB 操作)

**前置（必须）**：
- Task 1-3 prod 已部署 ≥7 天，无新 regression
- 直查 prod 确认 0 用户 user_tier 非 free / billing_mode 非 credits
- Prod DB 完整 backup 已验证（dev DB 恢复 drill 通过）

**修改文件**：
- 新建 migration `migrations/{date}_drop_legacy_user_columns.sql`（§5 内容）
- 新建 rollback `migrations/{date}_drop_legacy_user_columns_rollback.sql`（§5 内容）
- `internal/pkg/model/user.go`: 删 struct 字段 (UserTier, TierExpires, MonthlySopRuns, MonthlyResetAt, BillingMode) + 删 `BillingModeCredits` 常量

**测试**：
- Migration dry-run 在 dev DB 上跑
- Rollback dry-run 也跑
- 全量 e2e Playwright + smoke

**Deploy**:
- server 单独 tag
- Migration 通过 GORM AutoMigrate 或单独 SQL 手动执行（看现有 release 流程）
- Prod 执行前 5 分钟预警 + admin 同步通告
- 执行后 SQL audit：`DESCRIBE user` 不再含 5 列

---

## §7 测试矩阵

| 场景 | Task 1 | Task 2 | Task 3 | Task 4 |
|---|---|---|---|---|
| credits 用户 GetBalance 返回三池 | ✓ | ✓ | ✓ | ✓ |
| credits 用户 SOP 运行扣减成功 | ✓ | ✓ | ✓ | ✓ |
| credits 用户 chatbot 流扣减成功 | ✓ | ✓ | ✓ | ✓ |
| trial-only 用户设置页显示积分 | ✓ | ✓ | ✓ | ✓ |
| B2B parent 给 child 开通会员 | ✓ | ✓ | ✓ | ✓ |
| Booster 自购订单创建 + 充值 | ✓ | ✓ | ✓ | ✓ |
| Admin 客户管理列表正确渲染 | - | ✓ | ✓ | ✓ |
| Admin 用户 tier 编辑 endpoint 404/部分字段去除 | - | ✓ | ✓ | ✓ |
| 老前端 client 调 GET /v1/credits/balance 不 crash | ✓ | ✓ | ✓ | ✓ |
| GET /v1/user 响应字段精简 | - | ✓ | ✓ | ✓ |
| DROP 后 user 表 DESCRIBE 验证 | - | - | - | ✓ |
| 回滚 SQL dry-run（仅 schema） | - | - | - | ✓ |

## §8 部署与回滚

### §8.1 部署顺序（每 task）

每 task 一个独立的 prod tag：

```
Task 1 → server v2.1.x (next patch)
  ↓ prod soak ≥1 week (smoke + Playwright + 用户报障监控)
Task 2 → server v2.1.y + web-v3 v1.0.z + admin-web v0.0.w (同期或滞后 1 天)
  ↓ prod soak ≥1 week
Task 3 → server v2.1.a
  ↓ prod soak ≥3 days (仅 user model + tests，运行时不变)
Task 4 → server v2.1.b + DB migration
  ↓ prod 执行前 5min 预警 + admin 同步通告
```

### §8.2 Rollback 程序

**代码层 (Task 1-3)**：
- 单 commit revert + 重 tag → CI 自动 deploy（用 hotfix 的 dockerproxy.net mirror 恢复路径如遇 GFW）

**Schema DROP (Task 4)**：
1. 检测 trigger：prod 业务侧出现 missing column 错误 OR DESCRIBE user 缺列
2. SSH prod，停 numind-server-prod 容器
3. 执行 rollback SQL：`mysql ... < {date}_drop_legacy_user_columns_rollback.sql`
4. 从 backup restore 5 列的历史数据（注意：本 feature 所有 prod 数据值都是 default，restore 数据等价于 default）
5. 部署 Task 4 之前的 server 版本（v2.1.b 之前的 tag）
6. 验证 healthz + 关键流冒烟

**全 feature 回滚**：所有 4 个 task 都有独立 git revert + tag 重发，可逐个回退。

## §9 跨仓部署松耦合的设计

**Why server 先**：server 加 `omitempty` 后旧字段从响应消失。老前端 client 解析 JSON 时该字段为 undefined / null，不 crash（前端代码用 `??` 兜底已经覆盖）。

**Why 前端不能先**：如果前端 v1.0.z 先部署，它会假设 server 不再返回 sub_remain / monthly_limit，但 server v2.1.x 之前的 tag 还在 set 这些字段。冗余但不破坏。但如果前端先删字段并改读 cycle_remaining，server 又还没改完，可能出现"两边都 0"的显示假象（虽然不是错，但 inconsistent）。

**最低耦合方案**：server tag 升级触发 prod 部署立即生效；前端两仓 ≥24h 后部署。

## §10 风险登记

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| Task 1 改 dispatch 引入 nil membershipSvc panic | 低 | 高 | wiring 已确认 prod 一直注入；增加 defensive nil-check |
| 老 client 调 /v1/credits/balance 期待 `balance` 字段崩溃 | 低 | 中 | 前端代码搜索 `balance.balance` 引用确认无；新前端用 `trial_remaining + cycle_remaining + booster_usable` 计算 |
| Task 2 admin_migration 删除后管理员需要紧急切 mode | 极低 | 中 | 0 prod 用户场景；如真需要可临时手 SQL UPDATE |
| Task 4 schema DROP 误删 → 业务流崩 | 极低 | 极高 | 前置 7 天 soak + DB backup + 执行 dry-run；rollback SQL 已就绪 |
| Cross-repo deploy 顺序错位（admin-web 先于 server） | 低 | 中 | 部署 checklist 明确顺序；CI 不并行触发 |
| 测试覆盖漏掉某条 legacy 路径仍在生产被触发 | 低 | 中 | grep audit 矩阵 + Playwright e2e 关键流 + prod log 监控 isEffectiveLegacy 调用计数（已无此函数后零调用） |

## §11 验收标准（最终）

- [ ] `git grep -rn "isEffectiveLegacy\|legacyTierImpl\|HasActiveMembership\|CanRunSOP\|GetRemainingSOPRuns\|GetActualUserTier\|IsInNewSOPMonth\|UserTierFree\|UserTierTrial\|UserTierStandard\|UserTierPremium\|BillingModeLegacyTier\|MonthlyResetAt\|TierExpires"` 在 numind-server 内零匹配（排除 archive / .md / migrations 历史 SQL）
- [ ] `git grep "user_tier\|tier_expires\|monthly_sop_runs\|monthly_reset_at"` 在 numind-server 代码内零匹配（Task 4 后）
- [ ] `git grep "'legacy_tier'\|'legacy'"` 三个仓库代码内零匹配
- [ ] `git grep "remaining_runs\|monthly_limit\|RemainingRuns\|MonthlyLimit"` 三个仓库代码内零匹配
- [ ] Admin UsersView 无"等级"列；CreditUsersView 无 legacy banner；MigrationsView 路由 404
- [ ] `task lint` + 三个 `npm run lint && npm run type-check` 全 PASS
- [ ] Prod `DESCRIBE user` 输出无 user_tier / tier_expires / monthly_sop_runs / monthly_reset_at / billing_mode 5 列
- [ ] 246 prod 用户 e2e Playwright 关键流（登录 / 设置页 / SOP / chatbot / B2B 开通 / Booster 充值）全 PASS

## §12 未决问题

- [ ] Task 2 admin_user `user.go:273-293` 删除 4 字段后，该 endpoint 是否完全没用？需 S3 plan 阶段 grep 调用方确认
- [ ] `legacy_tier_change_log` 表保留多久？建议 1 年后再评估 DROP；S3 plan 不动这条
- [ ] CLAUDE.md 中"双制并存"段落如何改写：单 PR 改还是多次小改？建议 Task 2 顺手做
