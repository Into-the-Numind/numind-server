# Credits-System 数据一致性现状审计（S0 调研报告）

**日期**：2026-05-15
**关联 feature**：`credits-system-data-consistency`（S0）
**Prod 版本**：numind-server v2.1.19
**目的**：在决定 NDF 路径（单 tracker feature vs 多独立 feature）前，把 credits-system 当前的真实数据模型语义、新老表共存现状、6 个具体问题摸清楚。**本文档不含修复方案**，仅作为决策输入。

---

## §1 背景

2026-04-29 启动的 `membership-credits-redesign` feature 设计了 5 张新表（`subscription` / `trial_grant` / `credit_cycle` / `user_booster_balance` / `membership_event`）取代老的 `credit_package` 状态机。该 feature S4 编码 24/24 已完成、S4 已 merge develop（2026-05-14），但 **S5 验收未完成**（lint/test、dev DB 应用 migration 重跑 dry-run、Playwright E2E 实跑、Langfuse trace 回归均未完成）。

2026-05-14 P0 bug `credits-deduct-cycle-wiring` 揭示：grant 写 subscription + credit_cycle 新表，但 Reserve/Reconcile/GetBalance 仍读写 credit_package 老表。补丁式修复（commit v2.1.17/18/19）把 Reserve/Reconcile 切到 `MembershipService.DeductCreditsTx`，但**老表的写入路径未清理**——`RechargeWithOrderTx`（支付回调）、`GrantMembership`（B2B 帮开通）、`RechargeCredits`（admin 充值）仍在 INSERT credit_package。

结果：**新老两套表共存，数据持续 drift**。本文档摸清现状。

---

## §2 当前 credits-system 数据模型全景（16 张活表）

按角色分 6 层：

### Layer 1 账户聚合（2 张）
| 表 | prod 行数 | 角色 | 写入路径 | 读取路径 |
|---|---:|---|---|---|
| `credit_account` | 84 | 用户余额缓存（balance 字段） | `store/credit.go:62-83` GetOrCreateAccount（lazy）<br>`store/credit.go:266-272` UpdateBalance（delta） | `store/credit.go:87-96` GetBalance → biz/credit/credit.go:98,268 CanPerformAIOperation **fallback**（仅当 membershipSvc=nil） |
| `user_booster_balance` | 4 | booster 余额聚合（新设计 SOT） | `store/membership/user_booster_balance.go` Increment/Decrement | biz/membership/cycle.go DeductCredits 锁行；biz/membership/state.go GetMembershipStateBatch |

### Layer 2 积分来源（4 张）
| 表 | prod 行数 | 角色 | 写入路径 |
|---|---:|---|---|
| `credit_package` | 120 | **老 SOT，仍主动写** | biz/credit/credit.go:296 RechargeCredits（admin）<br>biz/credit/credit.go:326 RechargeWithOrderTx（支付回调）<br>biz/credit/grant_membership.go:136 GrantMembership（B2B）<br>biz/credit/credit.go:206 deductCreditsTxFull UpdatePackage（FIFO 扣减） |
| `subscription` | 58 | 新订阅生命周期主表 | biz/membership/subscribe.go GrantOrRenewSubscription |
| `trial_grant` | 33 | 新 trial 唯一 SOT | biz/membership/trial.go:130 GrantTrial |
| `credit_cycle` | 38 | 新月度 cycle 积分窗口 | biz/membership/cycle.go ensureCurrentCycle（lazy） |

### Layer 3 购买/事件（2 张）
| 表 | prod 行数 | 角色 |
|---|---:|---|
| `payment_order` | 28 | 支付订单（type ∈ {trial, subscription, yearly, booster}） |
| `membership_event` | 129 | 审计 log + idempotency 保护（unique key on idempotency_key）— **不冗余，保留** |

### Layer 4 扣减（3 张）
| 表 | prod 行数 | 角色 |
|---|---:|---|
| `credit_reservation` | 1650 | Reserve 阶段预扣 |
| `credit_reservation_item` | 1700 | Reserve 拆分到 package/source（含 source_type / source_id 字段） |
| `credit_transaction` | 3075 | **权威扣减 ledger**（1948 借项 / 42947 总扣减） |

### Layer 5 定价（4 张）
- `pricing_rule`(30) / `pricing_rule_tier`(16) — 价格规则（1 条硬 FK：`tier.rule_id → rule.id`）
- `credit_estimation_coefficient`(8) / `credit_user_type_config`(1) — 估算系数

### Layer 6 用量明细（1 张）
- `usage_record`(3274) — 每次 LLM 调用的 token 明细。`credits_deducted` 字段 SUM=0（死字段）

---

## §3 数据现象（prod 实测，2026-05-15）

> **重要订正（2026-05-15 深调）**：本节早期版本把全部 3 类现象统称"P0 drift"，但深入核查后发现这 4 个 subsection 实际是 **3 种不同性质** 的问题，不全是"数据不一致"：
> - **3.1 ubb 与 credit_package(booster) 不一致** → **设计意图，非 drift**（ubb 是 SOT，credit_package 已被新支付路径绕过）
> - **3.2 credit_account.balance 与 Σpackages 不一致** → **过期清理 gap**（账面字段未衰减，但 GetBalance 走 expires_at 判断正确）
> - **3.3 trial_grant vs credit_package(trial) 数量差 1** → **设计意图**（user 432 在 5/15 当天通过新路径开 trial，只写新表）
> - **3.4 usage_record.credits_deducted 全 0** → **死字段**（表本身活跃，21 字段中 20 个在用，只此 1 列无 reader）
>
> 看节内"修复后含义"段落了解处理方式。

### 3.1 user_booster_balance vs Σcredit_package(booster, active)

4 行中 **3 行 DRIFT**：

| user_id | ubb_balance | pkg_sum | delta | 解读 |
|---:|---:|---:|---:|---|
| 1 | 6000 | 0 | +6000 | ubb 记 6000 booster，但所有 booster 包都已 exhausted/expired → ubb 没被同步衰减 |
| 30 | 128 | 13 | +115 | ubb 滞后 |
| 280 | 0 | 0 | 0 | OK |
| 348 | 433 | 288 | +145 | ubb 滞后 |

**根因（订正）**：**不是 drift，是设计意图**。`payment.fulfillOrder` 已只走新表 `user_booster_balance.Increment`，**根本不写 credit_package(booster)**。`membership_event(booster_granted)` 事件查证：
- user 1：5/14 self_purchase ¥299 → 10×600=6000（合法）
- user 30：5/12 self_purchase 1 个 → 128（用掉 472）
- user 348：5/11 self_purchase 1 个 → 433（用掉 167）

**ubb 才是 booster SOT**。`credit_package(booster)` 仅剩 2 active + 1 exhausted 是历史残留。比较两表语义错误。

**修复后含义**：保留 ubb（spec §2.4 一致），T11 DROP credit_package 后语义自洽。

### 3.2 credit_account.balance vs Σcredit_package(active)

84 行中 **6 行 mismatch**：

| user_id | balance | pkg_sum | delta |
|---:|---:|---:|---:|
| 388 | 150 | 0 | +150（幽灵余额） |
| 392 | 150 | 0 | +150 |
| 280 | 31 | 0 | +31 |
| 382 | 125 | 150 | -25（反向） |
| 389 | 15 | 0 | +15 |
| 386 | 2 | 0 | +2 |

**根因（订正）**：**不是写入分裂 drift，是过期清理 gap**。user 388/392 是 user 30 的子账户，4/26 被 b2b_grant 开了 200 积分 trial 试用包，扣了 50 剩 150，**4/29 过期**。但 3 张表的余额字段（`credit_package.remain_credits` / `trial_grant.credits_remaining` / `credit_account.balance`）**都没被衰减为 0**——`credit_package.status` 字段虽然被 cron 标了 'exhausted'，但和 `remain_credits=150` 自相矛盾。

用户实际余额是 0（`MembershipService.GetBalance` 走 expires_at 判断，体验正确），账面字段只是好看不准。

**修复后含义**：T11 删 `credit_account.balance` 字段；过期判断走 expires_at，账面字段消失即不再误导。

### 3.3 trial_grant vs credit_package(trial)

| 维度 | trial_grant | credit_package(trial) |
|---|---:|---:|
| 行数 | 33 | 32（active 28 + exhausted 4）|
| Sum credits_remaining | 4160 | 4035（active+pending 3735，含 exhausted 4035）|

**根因（订正）**：**不是 drift，是新写路径已只写新表**。多的 1 行是 **user 432**（2026-05-15 当天通过 `/v1/users/children/.../grant-membership` 新路径被开 trial），新代码只 INSERT `trial_grant` 不再 INSERT `credit_package(trial)`。这正是 spec §3.3 设计意图。

**修复后含义**：trial_grant 是 trial SOT；T11 删 credit_package 后多/差立即消失。

### 3.4 usage_record.credits_deducted 死字段（注意：表 ≠ 字段）

- **表本身完全活跃**：7 天新写入 **1009 行**，最后写入 2026-05-15 18:07，每次 LLM/AI 调用都在写
- 写入入口：`internal/pkg/aiservice/middleware/billing.go:285`
- 读取入口：admin `/v1/admin/billing/records` + 用户端 `/v1/billing/usage` + `sop.go:1151` SOP 成本聚合
- 21 字段中 20 个活：prompt_tokens / completion_tokens / cost_cents / revenue_cents / metadata / task_id / unit / call_count 等
- **仅 `credits_deducted` 这一列**死：全表 3274 行 SUM=0，grep 无 reader
- 仅 `biz/credit/credit.go:250-254` 的 legacy DeductCredits 路径 UPDATE，新 MembershipService 路径不填该列

**修复后含义**：T10 操作精确为 `ALTER TABLE usage_record DROP COLUMN credits_deducted` —— 仅删 1 列，表保留。

---

## §4 P1 设计冗余（同份数据存两次）

| # | 冗余对 | 性质 | prod 证据 |
|---|---|---|---|
| 1 | `trial_grant` ↔ `credit_package(type='trial')` | 完全重叠：granted_at / expires_at / credits_remaining / source 都在 credit_package | 33 vs 32 行 drift 已证 |
| 2 | `user_booster_balance` ↔ Σ`credit_package(booster, active)` | 缓存表 vs 派生聚合 | 4 行表，根本无需缓存 |
| 3 | `credit_account.balance` ↔ Σ`credit_package(active)` | 缓存 vs 派生 | 84 行 6 个 drift |
| 4 | `usage_record.credits_deducted` ↔ `credit_transaction.amount` | 双写 vs 单写 | usage_record 全 0；credit_transaction 是权威源 |
| 5 | `membership_event` ↔ (`credit_package` + `payment_order` + `subscription`) | **不冗余**（审计 log + idempotency 唯一约束保护重入）| 129 行 audit 用途 — **保留** |

---

## §5 P2 关系不清

### 5.1 subscription / credit_cycle / credit_package 三角

- `subscription`(58) ↔ `credit_cycle`(38) ↔ `credit_package(subscription)`(50 active + 33 pending = 83)
- 38 vs 58：20 个 subscription 没 cycle —— credit_cycle 是 lazy-create，未到激活月或刚 grant 还没 cycle
- 50 active package vs 58 subscription：8 个 subscription 没对应 active package？或者 1:N？需要 spec 确认
- 33 pending package 全部对应 B2B 年卡 pending 段（1 active + 11 pending 段链）

### 5.2 硬 FK 缺失

prod 唯一硬 FK 是 `pricing_rule_tier.rule_id → pricing_rule.id`。其他都是软关联。最关键的 4 条建议补充：

| FK | ON DELETE |
|---|---|
| `credit_reservation_item.reservation_id → credit_reservation.id` | CASCADE |
| `credit_reservation_item.package_id → credit_package.id` | RESTRICT |
| `credit_transaction.package_id → credit_package.id` | RESTRICT |
| `credit_cycle.subscription_id → subscription.id` | CASCADE |

---

## §6 根因诊断：membership-credits-redesign Task 16 cleanup 未执行

`membership-credits-redesign` plan 的 Task 16 设计为：
> "切换 Reserve / Reconcile 到 MembershipService.DeductCredits，删除老 credit_package 的写入路径，用 cron 删除老表数据。"

实际执行情况（按 manifest 2026-04-30 S4 决策记录）：
- Task 16 在 S4 阶段被判定为 "0 改动需要"（manifest 决策 2026-04-30）
- 后来 2026-05-14 `credits-deduct-cycle-wiring` 才补做了部分 Task 16 内容（切 Reserve/Reconcile）
- **但老 credit_package 的 INSERT 路径完全没动**：
  - `RechargeWithOrderTx`（支付回调）仍创建 credit_package(type='subscription') ×N
  - `GrantMembership`（B2B 帮开通）仍创建 credit_package
  - `RechargeCredits`（admin 手动充值）仍创建 credit_package
  - `deductCreditsTxFull` 仍 UpdatePackage(remain_credits)
- 状态机 cron 仍跑（`ActivatePendingPackages` / `ExpireActivePackages`）

**结果**：write 写老表 + read 读新表 = 必然 drift。这是结构性问题，不是数据修复能解决的。

---

## §7 代码 caller 关键事实（来自 Explore subagent 调研）

| 表 | AutoMigrate | 仍被写入 | 仍被读取 | 与 membership-credits-redesign 关系 |
|---|:-:|:-:|:-:|---|
| credit_package | ✅ helper.go:257 | ✅（4 个入口）| ✅ | **应该被取代但未取代** |
| credit_account | ✅ | ✅ UpdateBalance | ✅ fallback path | balance 字段被 MembershipService.GetBalance 渐进替代 |
| trial_grant | ❌（独立 migration）| ✅ GrantTrial | ✅ DeductCredits | 新设计权威源 |
| user_booster_balance | ❌ | ✅ Increment/Decrement | ✅ | 新设计权威源 |
| usage_record.credits_deducted（字段）| - | ✅ legacy DeductCredits | ❌ 无 reader | 死字段 |

---

## §8 待决策清单

下列决策影响修复路径。**不预设答案**，等待人类拍板。

### D1 修复优先级
- D1a：先修 P0 数据 drift（校准 user_booster_balance / credit_account / trial_grant），再改结构
- D1b：先停 P0 drift 的 root cause（统一写入路径），再做表结构清理

### D2 老表下线策略
- D2a：彻底下线 credit_package，所有充值/扣减/状态机切到新表
- D2b：保留 credit_package 作为"账单粒度账册"（每月每包独立行，便于 B2B 月结），但作为 derived view 而非 SOT
- D2c：保留现状不动，仅修 P0 数据 drift

### D3 缓存表 (user_booster_balance / credit_account.balance) 策略
- D3a：彻底删（直接 Σ 派生计算，4-84 行规模不值得缓存）
- D3b：保留 + 写校准 cron / 写事务内同步逻辑
- D3c：作为只读视图（VIEW）替代实体表

### D4 trial_grant 与 credit_package(trial) 的二选一
- D4a：保留 trial_grant，删 credit_package(trial) 写入路径
- D4b：保留 credit_package(trial)，删 trial_grant 表
- D4c：保留两者，但明确 trial_grant 为 SOT，credit_package(trial) 是从 trial_grant 派生的查询视图

### D5 usage_record.credits_deducted 字段
- D5a：DROP COLUMN（最小风险）
- D5b：先设 deprecated 注释 + 停止写入路径，下个版本再 DROP

### D6 硬 FK 补充
- D6a：补 4 条关键 FK + 先清理孤儿数据
- D6b：保持软关联（GORM Preload 已足够，硬 FK 增加迁移风险）

### D7 NDF 路径选择
- D7a：**单 tracker feature**（本 feature `credits-system-data-consistency` 整体走 Standard，spec 一份、plan 一份、按 8-10 个 task 分阶段实现）
- D7b：**多独立 feature**（每个动作独立 feature 各自 NDF：删 credits_deducted 字段 / 删 user_booster_balance / 删 trial_grant / 加 FK / 重审 subscription 边界）
- D7c：**作为 membership-credits-redesign 的 S5/S6 扫尾推进**（reopen 该 feature 的 S4 plan 补 Task 16 真正落地）

---

## §9 推荐的下一步

1. **更新 DEPRECATED_FEATURES.md**：把 `usage_record.credits_deducted`、`credit_account.balance` 字段、潜在的 `credit_package(trial)` 子集登记为「待校准 / 计划下线」（§4 节）。这是无可争议的第一步，且与路径决策无关。
2. **用户阅读本 audit + manifest 注册的 S0 条目** → 拍板 D1-D7 决策 → 进 S1 Proposal。

如果用户选 D7c（合并到 membership-credits-redesign），本 feature 应取消，迁移到该 feature。

---

## §10 综合判断与锁定方案（2026-05-15 V2）

### 10.1 决策过程

- 3 个独立 sonnet reviewer 对原 V1 方案（5 task）全部 REJECT，共识 9 个 P0
- 用户拍板用户端「积分包历史」UI 自调研 → 确认不存在（CreditsView 不调 listPackages）
- prod 实测验证 user 1/30/348 booster 余额是合法 self_purchase + user 388/392 是过期清理 gap
- HTML 讲解图产出，用户 GO

### 10.2 决策清单最终结果

| 决策 | 锁定 | 备注 |
|---|---|---|
| **D1** 顺序 | 先拆 root cause → 校准 → 下线 | reviewer 共识 |
| **D2** credit_package | DROP 整张表 + 归档到 `legacy_credit_package_archive_20260515` | 含 7 年保留 COMMENT + README 表 |
| **D3** 缓存表 | **保留 user_booster_balance**（spec §2.4 SOT）；删 credit_account.balance 字段 | **反转 V1**（reviewer 3 Q8） |
| **D4** trial | trial_grant 为 SOT，credit_package(trial) 一并 DROP | T11 一次性 |
| **D5** 死字段 | DROP **COLUMN** usage_record.credits_deducted（**仅 1 列，表保留**）| 订正精确表述 |
| **D6** 硬 FK | 仅给新表加 FK：`cycle.subscription_id → subscription` / `reservation_item.reservation_id → credit_reservation` | polymorphic 不直接加，靠 source_type CHECK |
| **D7** NDF 路径 | **D7c — 并入 `membership-credits-redesign` 作为 plan extension** | 本 feature `credits-system-data-consistency` 取消并入 |

### 10.3 V2 锁定 task 树（12 task）

**Phase A 基础设施先行（解锁后续）**
- **T1** `credit_transaction` 加 `source_type` / `source_id` 列 + 从 `credit_package.type` 回填（前置：否则 DROP 后 3075 行 ledger 匿名化）

**Phase B 拆 4 个老写入路径（每个原子可独立 commit）**
- **T2** 删 admin Recharge：后端 controller + biz.RechargeCredits + admin_router 行 + admin-web `CreditUsersView.vue:217` 按钮 + API client（FE/BE 同步删，避免 404 bomb）
- **T3** 删 `internal/numind/controller/v1/parent_grant/` 死代码 package（grant.go 无路由注册；**不动 router.go:276 的 `/v1/users/children/.../grant-membership`**，那是活路径）
- **T4** 改写 `biz/credit/grant_membership.go` GrantMembership 实现：API 路径不变，内部从 `INSERT credit_package` 改为调 `MembershipService.Subscribe` / `GrantTrial`
- **T5** `RechargeWithOrderTx` 支付回调切完全到新表（subscription / membership_event / ubb）
- **T6** 删 legacy DeductCredits 链：sop.go:1825 + sales_rag.go:1170 切到 MembershipService → 删 `deductCreditsTxFull` / `GetActivePackagesForUpdate` / `UpdatePackage` / state cron stub

**Phase C 数据校准（删表前对账）**
- **T7** booster 余额逐用户对账：以 `membership_event(booster_granted)` 总额 + `credit_transaction` booster 扣减算法验证 ubb 数字。**user 1/30/348 已确认 self_purchase 合法，仅需 SQL 验算**
- **T8** ledger 校准（一次性 transaction）：以 `credit_transaction` 为 SOT 重建 trial_grant.credits_remaining / credit_cycle.credits_remaining；过期 trial 强制归 0；含 pre/post invariant + audit row 到 membership_event(idempotency_key='t8_calibration_20260515')

**Phase D 切剩余 readers**
- **T9** `biz/b2b_billing/b2b_billing.go` 切完全到 membership_event（删 spec §7 双口径 fallback）+ 删 `controller/v1/credit/listPackages` 死路由 + 删 `controller/v1/admin_credit/listPackagesByUser` 死路由

**Phase E 死字段/死表清理**
- **T10** `ALTER TABLE usage_record DROP COLUMN credits_deducted`（**仅删 1 列，表保留**）
- **T11** `CREATE TABLE legacy_credit_package_archive_20260515 AS SELECT * FROM credit_package`（含 COMMENT='保留 7 年与会计凭证同期' + README.md 字段语义说明） → 7 天 backup window → DROP TABLE credit_package + DROP COLUMN credit_account.balance + 清 GORM model + AutoMigrate 移除

**Phase F FK + 文档收尾**
- **T12** 加硬 FK：`cycle.subscription_id → subscription`（CASCADE）+ `reservation_item.reservation_id → credit_reservation`（CASCADE）+ 清孤儿 + 更新 [DEPRECATED_FEATURES.md](../../DEPRECATED_FEATURES.md) §3/§5 + 各 [CLAUDE.md](../CLAUDE.md) 引用

### 10.4 部署安全条款

每个 task 必须：
1. **forward + rollback 双 migration 脚本**，rollback 在 dev 实测一次再上 prod
2. **task 间部署间隔 ≥ 3 天**，监控 INSERT 0（confirm 旧路径无残余写入）才允许下一个 task
3. **MAINTENANCE_MODE=true 实测支付回调豁免**（T1/T10/T11 任何 schema 改动前）
4. **T11 archive 前先 mysqldump prod hot backup**，留 30 天

### 10.5 详细 plan 入口

`numind-server/docs/superpowers/plans/2026-05-15-membership-credits-redesign-cleanup-plan.md`
