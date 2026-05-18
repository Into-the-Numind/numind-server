# legacy-system-deprecation — 提案

## §1 方案概述 [客户可见]

把 legacy_tier 计费体系（基于用户等级 + 次数制）从代码、API、UI、数据库 schema、文档中**全量移除**。新的 credits 三池体系（trial_grant + credit_cycle + user_booster_balance）成为唯一计费源。

**结论：风险比预期低**。Prod 数据审计显示 246 个用户全部已经是 `billing_mode=credits` + `user_tier=free`，所有 legacy 代码路径**都是死代码**。删除工作主要是工程清理，不涉及数据迁移。

**预期收益：**
- 60+ 处 legacy 残留代码消除，根除"过渡用户"误路由这类隐患（本月已触发 1 次 P0）
- one billing path = credits SOT，新人 onboarding + debug 心智负担大幅降低
- 估算可删 ~800 行代码 + ~8 个 admin 端 UI 元素 + 5 列 user 表字段

## §2 报价与周期 [客户可见]

- 预估工作量：**8-10 人天**（4 个 sub-task）
- 报价：N/A（内部 refactor）
- 交付时间线：
  - **Task 1** 后端核心 dispatch 清理 → 2026-05-21（dev 部署 + prod soak ≥1 周）
  - **Task 2** 后端边界 + admin + 前端清理 → 2026-05-28（prod soak ≥1 周）
  - **Task 3** User model + tests 清理 → 2026-06-04
  - **Task 4** Schema DROP migration → 2026-06-15（间隔 ≥1 周稳定后执行，含完整 rollback SQL + DB backup）

## §3 技术可行性 [AI 内部]

### Prod 数据现状（2026-05-18 直查 prod DB 结果）
| 指标 | 值 |
|---|---|
| 用户总数 | 246 |
| billing_mode='credits' | 246 |
| billing_mode='legacy_tier' | **0** |
| user_tier='free' | 246 |
| user_tier≠'free' | **0** |
| 有效 trial_grant | 2（均 credits 模式） |

含义：**legacy 代码路径全部死路径**。`isEffectiveLegacy` 已被 hotfix 简化为只检查 `BillingMode == legacy_tier`，当前永远返回 false。

### 现有功能复用
- credits 路径已经在 prod 稳定跑（v2.1.22 上线后）
- 三池 SOT 设计在 `membership-credits-redesign` feature 已 S5 验收
- Hotfix `credits-trial-grant-bypass-fix` 已经把入口分发条件统一到 `BillingMode==legacy_tier`，本 feature 是消除后续残留

### 技术风险
| 风险 | 评估 | 缓解方案 |
|---|---|---|
| 删除 user_tier 字段后 admin 端无法手动指派会员等级 | 中（业务流程影响） | B2B 开通会员现在走 GrantMembership → 写 credit_cycle，已经不依赖 user_tier。Admin UI 同步删 "等级" 列；如未来需要 tier 概念可基于 credits 配额重建 |
| Schema DROP 不可逆 | 高 | Task 4 单独跑、间隔 ≥1 周稳定运行；migration 含完整 rollback SQL；执行前 prod DB backup 落地 |
| admin_migration controller 删除后无法手工切 billing_mode | 低 | 该工具是过渡期产物，prod 已 100% credits 即可删除；如需保留 forensic 入口，可仅删除 UI 不删 API |
| 审计/历史表 `tier_change_log` 数据保留问题 | 低 | Task 4 同时迁移：表保留只读，列名添 `legacy_` 前缀，DDL DROP 仅针对 user 表 |
| 旧 client 仍读 remaining_runs 字段 | 低 | 字段返回 null 不破坏类型，前端 hotfix 同期 patch 兼容；admin-web 同步 deploy |
| Test suite 大幅返工 | 中（工作量增加 1-2 天） | 范围内已规划，删 legacy 专用测试 + 双制测试改 credits-only |

### 涉及仓库
- [x] **numind-server**: ~30 处代码 + 2 migration SQL（含新增 DROP migration）
- [x] **numind-web-v3**: ~7 处（CreditBalanceCard / credits.ts / stores/credits.ts / BoosterPurchaseCard）
- [x] **numind-admin-web**: ~5 处（UsersView 等级列 / CreditUsersView legacy banner / MigrationsView 整页删 / API interface）
- [ ] 文档：5 个 .md 文件需要更新

### AI 可观测性
- [ ] 涉及 LLM 调用：否
- N/A

## §4 产品需求定义 — PRD [AI 内部]

### 用户故事
- 作为**用户**，我希望在任何场景下都不再因 legacy 残留代码而看到错误的余额或被误判 "积分不足"
- 作为**开发者**，我希望 codebase 只有一条计费路径，新功能开发不再需要"双制兼容"心智
- 作为**管理员**，我希望客户管理界面只展示与 credits 体系相关的数据，不再有过期的"等级 / 月度次数"列

### 验收标准
- [ ] `git grep -rn "isEffectiveLegacy\|legacyTierImpl\|BillingModeLegacyTier\|HasActiveMembership\|CanRunSOP\|GetRemainingSOPRuns\|GetActualUserTier\|IsInNewSOPMonth\|UserTierFree\|UserTierTrial\|UserTierStandard\|UserTierPremium"` 在 numind-server 内零匹配（除 .md / archive）
- [ ] `git grep "user_tier\|tier_expires\|monthly_sop_runs\|monthly_reset_at"` 在 numind-server 内零匹配（DROP migration 后）
- [ ] `git grep "'legacy_tier'\|'legacy'"` 在三个仓库内零匹配（cardState / billing_mode interface / banner）
- [ ] `git grep "remaining_runs\|monthly_limit\|RemainingRuns\|MonthlyLimit"` 三个仓库零匹配
- [ ] Admin UsersView 不再有"等级"列；CreditUsersView 不再有 legacy_tier banner；MigrationsView 整页删除
- [ ] `task lint` PASS；`npm run lint && npm run type-check` PASS（前端两仓）
- [ ] Prod 部署后 `SELECT COUNT(*) FROM user WHERE billing_mode IS NULL` = 0；`DESCRIBE user` 不再包含 user_tier/tier_expires/monthly_sop_runs/monthly_reset_at/billing_mode 5 列
- [ ] 246 个 prod 用户的 trial / SOP / chatbot 流路径无 regression（curl smoke + Playwright）

### 边界情况
- **新用户注册**: 不再写 `user_tier=free`；只走 credits 路径（注册流程目前已默认 billing_mode=credits，仅需删 user_tier 默认值）
- **B2B 给子用户赠送会员**: GrantMembership Step A（flip billing_mode）删除；订阅写入仅 credit_cycle，不动 user_tier
- **Admin 端**: 删除"切换 billing_mode"工具 + "调整等级"工具；改为只读展示 credit_cycle / trial_grant / booster
- **降级/过期**: legacy 中"等级过期降级 free"逻辑消失，credits 体系自然处理（trial_grant.expires_at / credit_cycle.cycle_end）
- **历史审计**: `tier_change_log` 表保留只读，但停止写入；可考虑表名前缀 `legacy_`

### 权限规则
- 不变。三个 sub-task 不动权限模型。

### UI 行为规格
- **numind-web-v3 CreditBalanceCard**:
  - 删 `'legacy'` cardState 分支 + legacy 模板（"本月运行次数 / 无限"）
  - 删 `legacyUsed` computed
  - 保留 free / credits 两态
- **numind-web-v3 BoosterPurchaseCard**:
  - 删 `bal?.billing_mode === 'legacy_tier'` 兼容分支
- **numind-admin-web UsersView**:
  - 删 "等级" 表格列 + tier 编辑下拉
- **numind-admin-web CreditUsersView**:
  - 删 legacy_tier banner（"此用户为 billing_mode=legacy_tier"）
- **numind-admin-web MigrationsView**:
  - **整页删除** + 路由删除（仅 legacy 用户迁移工具，目标 0 用户）

### 设计/UX 备注
- 用户侧无感（设置页已经在 credits 模式下，移除的 legacy 分支用户从来看不到）
- Admin 侧"等级"列消失是可见变化；如需保留可视化，把"剩余 cycle 积分"列前置即可，已经有数据

## §5 sub-task 详细拆分（S3 plan 锁定）

### Task 1: 后端核心 dispatch 清理
**范围**:
- `biz/credit/credit_service.go`: 删 `isEffectiveLegacy` 函数 + 所有 6 个调用 site；删 `legacyTierImpl` struct + 4 个方法 + `legacy` 字段；`creditService` 各 dispatch 方法直接调 credits 实现
- `biz/credit/credit.go`: `CanPerformAIOperation` 删 legacy 分支
- `biz/credit/credit_service_langfuse.go`: 删 BillingMode span 字段 + `classifyDeductedFrom` 的 `none(legacy)` 分支
- `biz/sop/sop.go`: 已 hotfix；仅删剩余 log 字段

**风险**: 高（核心扣费路径）
**测试**: 双制测试改 credits-only；新增 smoke test
**部署**: 单独打 tag → prod soak ≥1 周再进 Task 2

### Task 2: 边界 caller + admin + 前端清理
**范围**:
- `biz/customer/customer.go:171/230/270`: 删 GetRemainingSOPRuns 调用 + 响应字段
- `controller/v1/user/get.go:88`: 删 remaining_runs 字段 + 关联读取
- `controller/v1/admin_user/user.go:273-293`: 删 user_tier/tier_expires/monthly_* 更新逻辑
- `controller/v1/admin_migration/migrations.go`: 整个 controller 删除（路由 + handler）
- `biz/payment/payment.go:142`: 删 legacy_tier 拒绝分支
- `biz/credit/grant_membership.go:172`: 删 Step A flip billing_mode
- `store/customer.go IncrementSopRunCount`: 删 monthly_sop_runs/monthly_reset_at 更新逻辑（保留方法签名，body 空操作或直接删调用）
- `numind-web-v3`: CreditBalanceCard 删 legacy 分支 + BoosterPurchaseCard 删 legacy 分支 + credits.ts 接口收敛
- `numind-admin-web`: UsersView 删等级列 + CreditUsersView 删 banner + MigrationsView 整页删 + API interface 收敛

**风险**: 中（前后端响应字段变化）
**测试**: 前端 type-check；Playwright admin/user 关键流冒烟
**部署**: server tag + 两个前端 tag 同期；prod soak ≥1 周

### Task 3: User model + tests 清理
**范围**:
- `pkg/model/user.go`: 删方法 (CanRunSOP / GetRemainingSOPRuns / HasActiveMembership / GetActualUserTier / IsInNewSOPMonth) + 常量 (UserTier*) ；**struct 字段保留**（等 Task 4 schema DROP 后再删）
- 删除 legacy 专用测试：`credit_service_test.go` 中 isEffectiveLegacy 用例 / `user_billing_mode_test.go` / `legacyTierImpl_test.go`
- 双制测试改 credits-only：`credit_service_boundary_test.go` / `credit_service_reserve_test.go`
- 删 numind-web-v3 / numind-admin-web 中相关 .spec.ts 用例

**风险**: 低（Task 1+2 已确保运行时不再调）
**测试**: go test all PASS
**部署**: server 单 tag；prod soak ≥3 天后进 Task 4

### Task 4: Schema DROP migration
**范围**:
- 新建 migration `numind-server/migrations/{date}_drop_legacy_user_columns.sql`:
  ```sql
  ALTER TABLE `user`
    DROP COLUMN user_tier,
    DROP COLUMN tier_expires,
    DROP COLUMN monthly_sop_runs,
    DROP COLUMN monthly_reset_at,
    DROP COLUMN billing_mode;
  ```
- 对应 rollback SQL（含 backfill credits default + tier='free' default）
- `pkg/model/user.go`: 删 struct 字段
- 旧 init/migration 文件标 deprecated 或删（20260419_*_billing_mode_*）

**风险**: 高（DROP 不可逆）
**前置**:
- Prod backup verified
- 跑 dry-run rollback SQL 验证
- Task 1-3 prod soak ≥7 天 + 三方 audit 无回归
**测试**: 全量 e2e Playwright + smoke
**部署**: 单独 tag；prod 执行前 5 分钟预警 + admin 同步通告

## §6 回退路径
- Task 1-3 代码层 revert：git revert 对应 commits + 重 tag
- Task 4 schema DROP：执行 rollback SQL（ALTER TABLE ADD COLUMN 把 5 列加回 + backfill default value），需注意 ENUM 类型 / NOT NULL 约束顺序

## §7 备注
- 本 feature 完成后，`membership-credits-redesign` 的 Task 16 cleanup 才算彻底完结。
- Documentation 更新（CLAUDE.md / docs/）放在 Task 2-3 内顺手做，不单列。
- Office-hours 产品挑战：考虑过"保留 user_tier 字段做未来 tier 扩展"，但 credits 三池本身已支持 tier 语义（cycle quota 即 tier），不需要额外字段。结论：彻底删，未来重建。
