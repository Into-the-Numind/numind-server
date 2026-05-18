# Legacy 计费体系彻底废弃

## 来源
- 提出人：zchen27@tulane.edu（产品负责人）
- 提出日期：2026-05-18
- 触发事件：trial_grant 表读路径被 `isEffectiveLegacy` / `HasActiveMembership` 错路由绕过，导致 prod 用户 200 积分不可用。Hotfix `credits-trial-grant-bypass-fix` (server v2.1.22 + web-v3 v1.0.22) 已止损，但 legacy 残留代码仍是 19 处隐患源。

## 需求描述
> "legacy 是已经被抛弃的体系，不应该再用了，不管什么情况，都不应再用 legacy 的体系，而 trial grant 是一定需要去读取的"

把 legacy_tier 计费体系（基于 `user.user_tier` + `monthly_sop_runs` 次数制）从代码、数据、API 响应、前端展示、测试中**全量移除**。新制 credits（基于 `trial_grant` + `credit_cycle` + `user_booster_balance` 三池 SOT）是唯一计费体系。

### 范围内（必须完成）

**S — 代码删除（按 hotfix audit 已列 19 处）：**
1. `biz/credit/credit_service.go`: 删 `isEffectiveLegacy()`、`legacyTierImpl` struct 及所有方法（CheckAndEstimate / Reserve / GetBalance / buildLegacyBalance）；`creditService` 各 dispatch 方法（CheckAndEstimate / Reserve / GetBalance / CheckAndEstimateBudget）直接调 credits 实现，不再分发
2. `biz/credit/credit.go`: `CanPerformAIOperation` 删 legacy 分支（SOP-isLegacy + CanRunSOP 路径），保留 credits 余额预检
3. `biz/sop/sop.go`: hotfix 已删 HasActiveMembership 二次拦截；S4 内同时清理相关 log 字段（user_tier / monthly_sop_runs 等）
4. `biz/customer/customer.go:171/230/270`: 三处 `GetRemainingSOPRuns()` 调用删除（ListSubUsers / GetSubUserDetail / GetCustomerStatistics 响应字段同步删）
5. `controller/v1/user/get.go:88`: `GetCurrentUser` 响应里的 remaining_runs 删除
6. `biz/payment/payment.go:142`: `CreateBoosterOrder` 删 billing_mode==legacy_tier 的拒绝分支（所有用户都可买 booster）
7. `biz/credit/grant_membership.go:172`: GrantMembership Step A 的 billing_mode flip 删除（先决条件：所有 prod 用户已被批量 UPDATE 到 credits）
8. `biz/credit/credit_service_langfuse.go:40/189/203`: 删 BillingMode span 字段；`classifyDeductedFrom` 的 `none(legacy)` 分支删
9. `pkg/model/user.go`: 删 `CanRunSOP() / GetRemainingSOPRuns() / HasActiveMembership() / GetActualUserTier()`方法；删 UserTier* 常量

**D — 数据库 migration（S4 末尾追加）：**
10. `UPDATE user SET billing_mode='credits' WHERE billing_mode='legacy_tier'`（先决：审计 prod 数据）
11. 新建 migration: `ALTER TABLE user DROP COLUMN user_tier, DROP COLUMN tier_expires, DROP COLUMN monthly_sop_runs, DROP COLUMN monthly_reset_at, DROP COLUMN billing_mode`（最后一步，rollback SQL 同步产出）

**F — 前端：**
12. `numind-web-v3/src/components/credit/CreditBalanceCard.vue`: 删 `'legacy'` cardState 分支 + 模板；删 `legacyUsed` computed
13. `numind-web-v3/src/api/credits.ts`: 把 `QuotaBreakdown` 接口中 legacy 字段（remaining_runs / monthly_limit / billing_mode）标 deprecated 或删；`BalanceDTO` 作为唯一余额类型
14. `numind-web-v3/src/stores/credits.ts`: `billingMode` computed 删；`displayState` 的 `'legacy'` 分支删
15. `numind-admin-web` 同步：admin 端子用户列表 / 客户统计的 remaining_runs 字段对应 UI 删

**T — 测试：**
16. 删除 legacy 专用测试用例（credit_service_test.go 中 isEffectiveLegacy 用例 / user_billing_mode_test.go / legacyTierImpl 测试文件）
17. 双制测试改成 credits-only：`credit_service_boundary_test.go` / `credit_service_reserve_test.go` 等保留但删 legacy 路径断言

### 范围外（不在本次 feature）
- credit_account 老表 DROP（已有独立 cleanup 任务跟踪）
- credit_package 表 DROP（在 membership-credits-redesign 跟踪）

## 业务目标
- **零隐患**：彻底消除 legacy 路径被错误触发导致用户余额读不到的可能性（本次 hotfix 已揭示一次，类似隐患 19 处）
- **架构清晰**：one billing path = credits SOT (trial_grant + credit_cycle + user_booster_balance)，移除"有效 legacy 过渡用户"这个无意义概念
- **代码减脂**：估算可删 ~600 行代码 + ~10 个测试文件

## 优先级
高 — 上一次 hotfix 已触发用户报障，残留路径继续在生产运行就是定时炸弹。

## Triage
- **推荐轨道：Standard**
- 分类理由：
  1. 数据库 schema 变更：**是**（DROP 4 列 + UPDATE 列值）
  2. 新增 API 端点：否
  3. 新外部服务集成：否
  4. 影响文件数：**>3**（≥ 19 个）
  5. 高风险业务逻辑：**是**（credit 扣减分发、SOP 权限、payment、grant flow 全在范围内）
- 人类决定：**Standard 已确认**（2026-05-18，用户答复"走正常的 track"）

## S0 决策清单（用户已答）
| 决策 | 选项 |
|---|---|
| DB 字段最终怎么处理 | 清数据 + 最终 DROP（含 billing_mode 本身） |
| API 字段 remaining_runs 怎么办 | 直接删，同步前端 |
| 测试策略 | 删 legacy 专用，双制测试改成 credits-only |
| PR 拆分 | 分 3-4 个 sub-task 递次提交 |

## 拟定 sub-task 拆分（S3 plan 再细化）
- **Task 1: Core dispatch 清理**（biz/credit/credit_service.go + biz/credit/credit.go + sop.go log）
  - 影响：Phase A + B，删 isEffectiveLegacy / legacyTierImpl / CanRunSOP runtime 调用
  - 风险：高（核心扣费路径），需要双制测试改写
- **Task 2: 边界 caller 清理**（customer.go + user/get.go + payment.go + grant_membership.go + observability spans）
  - 影响：Phase D + E + F
  - 风险：中（响应字段变化触及前端），需要前后端 PR 同步
- **Task 3: 前端清理**（CreditBalanceCard / credits.ts / stores/credits.ts + admin-web 同步）
  - 影响：Phase C 收尾 + admin-web
  - 风险：低（视觉层）
- **Task 4: Schema DROP migration**（user 表 5 列 + 数据审计 + rollback SQL）
  - 影响：DB
  - 风险：高（不可逆），prod 执行前必备完整 backup + 验证脚本
  - 触发条件：Task 1-3 已 prod 稳定运行 ≥ 1 周

## 备注
- Hotfix `credits-trial-grant-bypass-fix` 已修 isEffectiveLegacy 的 fallback + CreditBalanceCard 的 trial 行 + sop.go 的 TrialRemain sum，已 prod 上 v2.1.22 / v1.0.22。本 feature 是 hotfix 的后续清理工程化。
- Audit 数据来源：本次 session 的 Explore subagent 报告（path:line 全量罗列在对话历史）。
- `user_tier` 字段除了 legacy 计费外没有其他业务用途（确认：仅 SOP 权限 + 余额次数 + Booster gating），可以安全 DROP。如 S1 调研发现其他用途，回退到 S0 重新评估。
