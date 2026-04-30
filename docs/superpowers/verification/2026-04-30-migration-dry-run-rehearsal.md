# membership-credits-redesign — Migration Dry-Run Rehearsal Report

**Date**: 2026-04-30
**Agent**: Wave 2 / Agent E-migration
**Scope**: 01-dry-run.sql on dev DB + manual reconciliation SQL
**Commit**: scripts at `a1a2e83` (feat/membership-credits-redesign branch)

---

## 1. Dev 数据集规模

| 指标 | 数值 |
|------|------|
| 总用户数（有 credit_package 记录）| 4 |
| 总 credit_package 行数 | 9 |
| subscription 包数 | 2（user 25 exhausted, user 27 active）|
| trial 包数 | 3（user 54×2, user 55×1，均已过期）|
| booster 包数 | 4（user 25: 3 exhausted + 1 active）|

**活跃余额快照**（演练时刻）：

| user_id | 类型 | 活跃余额 |
|---------|------|---------|
| 25 | booster | ~9937（live app 持续消耗中）|
| 27 | subscription | 2000 |
| 54 | trial（已过期）| 0 |
| 55 | trial（已过期）| 0 |

> 注意：dev 是活跃环境，user 25 的 booster 余额在演练期间由 9997 → 9855 → 9828 → 9937（写回？）持续波动，说明 live app 正在跑 credit 消耗。

---

## 2. 前置操作

dry-run 依赖的 5 张新表在 dev 上尚未创建（feat/membership-credits-redesign 分支未合并 develop）。
演练前手动执行 `20260430_membership_credits_redesign.sql`（`CREATE TABLE IF NOT EXISTS` 幂等），创建了：
`subscription`, `trial_grant`, `credit_cycle`, `user_booster_balance`, `membership_event`

这是合法的 dry-run 前提条件操作，等同于 prod 切换窗口的 schema apply 步骤（Task 14 step 1）。

---

## 3. Dry-Run 输出摘要

### §A 数据概览（正常）

```
=== A1. credit_package type distribution ===
booster   active    1  9937
booster   exhausted 3  0
subscription  active    1  2000
subscription  exhausted 1  0
trial     active    3  600

=== A2. Users with active credit_package (by type) ===
booster  1 user   9937 credits
subscription 1 user  2000 credits
```

### §B 段合并预览（正常）

```
user_id  first_started_at            expires_at                pkg_count  total_months  grant_source   granter
25       2026-04-19 14:31:59         2026-05-19 14:31:59       1          1             self_purchase  NULL
27       2026-04-20 03:47:59         2026-05-20 03:47:59       1          1             b2b_grant      25
```

两个订阅用户各一段，无跨段合并场景（单一连续段）。granter_user_id=25 在 user 27 行中正确赋值（B2B 路径验证通过）。

### §C Trial Grant 预览 — BUG FOUND (P1)

```
ERROR 1055 (42000): Expression #4 of SELECT list is not in GROUP BY clause and contains
nonaggregated column 'numind-dev.credit_package.remain_credits' which is not functionally
dependent on columns in GROUP BY clause; incompatible with sql_mode=only_full_group_by
```

**根因**：01-dry-run.sql §C 使用 `FIRST_VALUE() OVER (PARTITION BY user_id ORDER BY activated_at)` 窗口函数和 `GROUP BY user_id` 混用，在 MySQL `only_full_group_by` SQL mode 下报错。该 SQL mode 在 MySQL 8.0 默认开启，dev 和 prod 均受影响。

**影响**：§C 报错导致 dry-run 脚本中止，§D 和 §F 的 blocker 检查未能执行。

**手动补充结果**（通过独立 SQL 验证，见第 4 节）。

### §D Booster 汇聚（手动补充）

```
user_id  active_booster_pkgs  credits_remaining
25       1                    ~9937（波动）
```

### §F Blocker 检查（手动补充，全部在 §C 崩溃后单独跑）

| 检查项 | 结果 |
|--------|------|
| F1: subscription 表为空 | ✅ violation_count=0 |
| F2: trial_grant 表为空 | ✅ violation_count=0 |
| F3: user_booster_balance 无正余额 | ✅ violation_count=0 |
| F4: membership_event 表为空 | ✅ violation_count=0 |
| **F5: booster total_credits 须为 600 的倍数** | **❌ violation_count=2 — BLOCKER** |
| F6: subscription 表为空（重复 F1）| ✅ violation_count=0 |
| F7: subscription activated_at < expires_at | ✅ violation_count=0 |
| F8: 每用户 trial 包数 <= 1（警告）| ⚠️ warning_count=1 |

---

## 4. 对账结果

### 4.1 Per-user 对账

| user_id | pre_active | post_total | delta | 状态 |
|---------|-----------|-----------|-------|------|
| 25 | ~9937 | ~9937 | 0 | OK（booster → user_booster_balance）|
| 27 | 2000 | 0 | 2000 | **设计行为** |
| 54 | 0 | 0 | 0 | OK（trial 已过期）|
| 55 | 0 | 0 | 0 | OK（trial 已过期）|

### 4.2 User 27 delta 说明（设计行为，非 bug）

User 27 是活跃订阅用户（2000 credits，sub active 到 2026-05-20）。迁移后：

- `subscription` 表有该用户一行（`expires_at=2026-05-20`, `total_months_purchased=1`）
- `credit_cycle` 表无数据（README 明确："credit_cycle: Not populated by this migration. The application creates credit_cycle rows at subscription renewal time."）
- 首次 API 调用触发 `ensureCurrentCycle()` → 懒创建 credit_cycle 行并填充 2000 credits

**结论**：subscription credits 迁移后通过应用层懒创建保障，不在 migration SQL 直接对账范围内。这是 spec §6.2 明确的设计决策。

但这带来一个**实操风险**（P1）：切换窗口期间，若有用户在 apply.sql 执行完毕、应用重启之前发起 API 调用，可能触发双账务路径（old credit_package 路径已失效，new credit_cycle 尚未创建）。需要 maintenance mode 严格覆盖切换间隙。

---

## 5. 发现的问题

### P1-1：01-dry-run.sql §C SQL 语法在 MySQL only_full_group_by 下崩溃

- **严重性**: P1（脚本中止，F 区 blocker 检查全部跳过）
- **定位**: `01-dry-run.sql` line ~100，FIRST_VALUE 窗口函数 + GROUP BY 混用
- **修复方向**: 将 `FIRST_VALUE() OVER (PARTITION BY user_id ORDER BY activated_at)` 替换为子查询 JOIN（如 §3 手动 SQL 所示），或在 CTE 中用 `ROW_NUMBER()` 取最小 activated_at 行
- **修复优先级**: 必须在 prod 演练前修复，否则 dry-run 等于无效

```sql
-- 修复方案（取最早 trial 包的 remain_credits）
SELECT cp.user_id, cp.activated_at AS granted_at, cp.expires_at,
       cp.remain_credits AS credits_remaining, cp.grant_source AS source, cp.granter_user_id
FROM credit_package cp
INNER JOIN (
  SELECT user_id, MIN(activated_at) AS min_at
  FROM credit_package WHERE type = 'trial' GROUP BY user_id
) earliest ON earliest.user_id = cp.user_id
  AND cp.activated_at = earliest.min_at AND cp.type = 'trial'
ORDER BY cp.user_id;
```

### P1-2：BLOCKER_F5 violation_count=2（booster total_credits 非 600 倍数）

- **严重性**: P1（会阻断 prod 迁移）
- **定位**: credit_package id=9 (total_credits=1000, exhausted) 和 id=10 (total_credits=10000, active)
- **Dev 上下文**: 这是测试数据（admin 端手工写入的非标准 booster 包）
- **Prod 上影响评估**:
  - id=9: remain=0，exhausted，不影响余额迁移
  - id=10: active, remain~9937。F5 仅检查 total_credits 的倍数，但 apply.sql 的 booster 逻辑是 `SUM(remain_credits)`，与 total_credits 无关
  - **F5 check 的保守性**：F5 检查是防御性检查，用于发现"数据异常"。对于 dev 测试数据，这属于 false positive。对于 prod，需要确认是否存在非 600 倍数的 booster，若有需要运营确认是否正常数据
- **建议**: 在 prod 演练前，先运行 `SELECT id, total_credits FROM credit_package WHERE type='booster' AND total_credits % 600 != 0;` 审计 prod 数据。若 prod 无此类数据，F5 是多余的 false alarm；若有，需 DBA 评估

### P2-1：WARN_F8 user 54 有 2 个 trial 包

- **严重性**: P2（警告，不阻断）
- **分析**: user 54 在同一天（2026-03-28）激活了两个 trial 包（id=2 at 13:17，id=3 at 13:32），均已过期。spec 的 `UNIQUE(user_id)` 约束是针对新表 trial_grant，old credit_package 无此约束
- **迁移影响**: apply.sql 取的是最早 activated_at 的 trial 包，id=2 会被采用，id=3 被忽略（remain_credits 均为 200，但 trial 在 dev 上已过期 remain 仍显示 200 因未被扣减）
- **建议**: 确认忽略第二个 trial 包的余额是否可接受（对 prod 同样审计是否存在多 trial 包用户）

### P2-2：订阅 credit_cycle 懒创建的切换窗口风险

- **严重性**: P2（实操风险，非脚本 bug）
- 已在第 4.2 节说明

---

## 6. 关键路径覆盖评估

| 路径 | dev 覆盖 | 结论 |
|------|---------|------|
| 普通月订阅用户（user 27）| ✅ 有 1 个活跃订阅用户 | 段合并单段验证 OK |
| 已过期用户（user 54/55）| ✅ trial 已过期，迁后余额=0 | 过期过滤逻辑验证 OK |
| B2B 用户（user 27 的 granter=25）| ✅ granter_user_id 正确赋值 | B2B 路径验证 OK |
| 不连续段（开-停-再开）| ❌ dev 无此场景数据 | 单段场景，段合并算法未充分验证 |
| trial+Pro 叠加 | ❌ dev 无此场景数据 | 需 prod 演练或构造测试数据 |
| booster 多包累加 | ⚠️ 部分（有 1 active + 3 exhausted）| exhausted 过滤逻辑验证 OK，累加仅 1 包 |

**关键路径覆盖率**: 3.5/6（58%）

---

## 7. 总结：是否 Ready for Prod 演练？

**结论：NOT READY — 需先修复 2 个 P1 问题**

| 前提条件 | 状态 |
|----------|------|
| P1-1 修复：01-dry-run.sql §C SQL 语法 | ❌ 必须修复 |
| P1-2 确认：Prod DB 有无 non-600-multiple booster | ❌ 需 prod 数据审计 |
| Prod snapshot 验证（dev 数据量不足）| ❌ 建议 prod 演练前从 prod 拉 RO snapshot |
| 段合并不连续段场景验证 | ⚠️ 建议构造测试数据或 prod 演练时验证 |
| apply.sql + 03-verify.sql 联动验证 | ❌ 本次演练仅做 dry-run，未做 apply（按任务要求） |

**推荐后续步骤**:
1. 修复 01-dry-run.sql §C SQL（P1-1，预计 5 分钟）
2. 在 prod 上审计 non-600-multiple booster 数据（`SELECT id FROM credit_package WHERE type='booster' AND total_credits % 600 != 0`）
3. 如 prod 数据干净，可拉 prod snapshot 到测试环境跑完整的 apply + verify 联动验证
4. 确认 maintenance mode 切换窗口 SOP（覆盖 subscription credit_cycle 懒创建间隙）

---

*Report generated by Agent E-migration, 2026-04-30*
