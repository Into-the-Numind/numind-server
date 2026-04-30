# Prod 数据只读审计报告

**日期**: 2026-04-30  
**审计员**: Wave 3 Agent J  
**目的**: 确认 Wave 2 dry-run 发现的两个潜在阻断（P1-2 booster 不整除、P2 多 trial 包）在 prod 是否实际存在  
**审计性质**: 纯只读 SELECT，SET TRANSACTION READ ONLY

---

## 一、审计结论摘要

| 项目 | Dev 状态 | Prod 状态 | 判定 |
|------|---------|---------|------|
| P1-2: booster total_credits % 600 != 0 | 2 条脏数据 | **0 条**（prod 无 booster 包） | dev 特有 false positive |
| P2: 用户拥有 >1 个 trial 包 | user 54 有 2 个 | **0 个**（每用户最多 1 trial） | dev 特有 false positive |

**结论：两个 BLOCKER 均为 dev 环境特有的测试数据问题，prod 完全干净。可以直接进行 prod apply。**

---

## 二、审计 1 — Booster total_credits 对齐性

### 查询 1a：汇总统计

```sql
SELECT
  COUNT(*) AS total_booster_count,
  SUM(CASE WHEN total_credits > 0 AND total_credits % 600 != 0 THEN 1 ELSE 0 END) AS misaligned_count,
  MIN(total_credits) AS min_tc,
  MAX(total_credits) AS max_tc
FROM credit_package
WHERE type = 'booster';
```

**结果**：

| total_booster_count | misaligned_count | min_tc | max_tc |
|--------------------|-----------------:|-------:|-------:|
| 0 | NULL | NULL | NULL |

**结论**：prod 完全没有 booster 类型的包（系统尚未开放 booster 购买功能）。BLOCKER_F5 在 prod 不触发。

### 查询 1b：明细（misaligned）

无结果，因为 booster 总数为 0。

---

## 三、审计 2 — 多 trial 包用户

```sql
SELECT user_id, COUNT(*) AS trial_count
FROM credit_package
WHERE type = 'trial'
GROUP BY user_id
HAVING COUNT(*) > 1
LIMIT 50;
```

**结果**：空集（0 行）

**结论**：prod 每个用户最多 1 个 trial 包，无多 trial 用户。WARN_F8（dev 中 user 54 有 2 个 trial）在 prod 不触发。

---

## 四、Prod 数据规模摸底

### 4.1 类型分布

prod 中 `credit_package` 共 **91 条**记录，分布如下：

| type | count |
|------|------:|
| subscription | 62 |
| trial | 29 |
| booster | **0** |

### 4.2 状态分布

| status | count | sum(remain_credits) |
|--------|------:|--------------------:|
| subscription / active | 40 | 67,228 |
| subscription / pending | 22 | 44,000 |
| trial / active | 25 | 3,418 |
| trial / exhausted | 4 | 300 |

**全局状态汇总**（所有类型）：

| total | active | pending | exhausted | expired | cancelled |
|------:|-------:|--------:|----------:|--------:|----------:|
| 91 | 65 | 22 | 4 | 0 | 0 |

### 4.3 用户数

- `credit_package` 中涉及的 distinct 用户：**68 人**

### 4.4 exhausted 包中 remain_credits > 0 的异常

4 个 exhausted trial 包中有 2 个 remain_credits 不为 0：

| id | user_id | remain_credits | expires_at |
|----|---------|---------------:|------------|
| 50 | 388 | 150 | 2026-04-29 |
| 54 | 392 | 150 | 2026-04-29 |

**分析**：这 2 个包已过期（expires_at 在今天之前），status 被标记为 exhausted 但 remain_credits 没有清零。这是过期 → exhausted 状态转换时的边界情况，apply.sql 使用 `WHERE status = 'active'` 过滤，这两条记录不会被纳入迁移计算，**不影响 apply**。建议 Wave 4 或后续 cleanup 时清零过期包的 remain_credits。

---

## 五、不连续段用户（段合并算法测试场景）

### 5.1 多段用户总览

有 2 个用户各持有 12 个 subscription 段（均为 B2B grant 的连续月度段链）：

| user_id | segment_count |
|---------|-------------:|
| 406 | 12 |
| 411 | 12 |

### 5.2 User 406 详情

12 个月度段，从 2026-04-29 起，连续到 2027-04-29：
- 1 个 **active** 段（当前月）
- 11 个 **pending** 段（未来月，激活链）
- 每段 total_credits = 2000，remain_credits = 2000
- grant_source = b2b_grant

**特征**：这是标准的 B2B 预付年度方案（12 个月段链），activated_at 严格首尾相接，是**连续段**，不是不连续段。段合并算法应将这 12 段视为一条完整的年度权益链。

### 5.3 User 411 详情

结构与 user 406 完全一致：12 个月度段（1 active + 11 pending），从 2026-04-30 起，到 2027-04-30。

**重要观察**：user 406 在 2027-01-29 → 2027-03-01 这段（id=82）有约 31 天间隔（跨越 2027 年 2 月），但 activated_at/expires_at 首尾相接，不是真正的不连续。apply.sql 按时间戳对齐，这条记录合并算法按正常处理即可。

---

## 六、Dry-run BLOCKER_F5 / WARN_F8 判定

| 告警 | dev 现象 | prod 判定 | 建议 |
|------|---------|---------|------|
| BLOCKER_F5（booster total_credits % 600 != 0） | dev 有 2 条测试包不整除 | **prod 无 booster，不触发** | 接受 dry-run 警告，直接 apply |
| WARN_F8（用户有 >1 trial 包） | dev user 54 有 2 个 trial | **prod 无此情况** | 接受 dry-run 警告，直接 apply |

---

## 七、推荐下一步

**可以直接进行 prod apply。** 无需 Wave 4 脏数据处理。

具体建议：

1. **直接 apply**：prod 数据干净，68 用户 91 条包，规模小，apply 风险极低。
2. **apply 重点关注**：user 406、user 411 各 12 段连续链的段合并结果是否正确（合并成 1 条年度条目 or 12 条月度条目，取决于 spec 设计）。
3. **后续 cleanup（非阻断）**：user 388、user 392 的 exhausted 包 remain_credits 非零，建议后续 cleanup 脚本清零（`UPDATE credit_package SET remain_credits=0 WHERE status='exhausted' AND remain_credits > 0`）。
4. **booster 上线前预检**：booster 功能上线时，补充 BLOCKER_F5 检查到 CI pipeline，确保 total_credits 强制为 600 整数倍。

---

## 附录：审计 SQL 完整清单

所有查询均为只读 SELECT，在 `numind-prod` 数据库执行，未修改任何数据。

```sql
-- 1. 表结构确认
DESCRIBE credit_package;

-- 2. 类型分布
SELECT type, COUNT(*) AS cnt FROM credit_package GROUP BY type ORDER BY cnt DESC;

-- 3. Audit 1: booster 对齐性
SELECT COUNT(*) AS total_booster_count,
  SUM(CASE WHEN total_credits > 0 AND total_credits % 600 != 0 THEN 1 ELSE 0 END) AS misaligned_count,
  MIN(total_credits) AS min_tc, MAX(total_credits) AS max_tc
FROM credit_package WHERE type = 'booster';

-- 4. Audit 2: 多 trial 包用户
SELECT user_id, COUNT(*) AS trial_count FROM credit_package
WHERE type = 'trial' GROUP BY user_id HAVING COUNT(*) > 1 LIMIT 50;

-- 5. 数据规模摸底
SELECT type, status, COUNT(*) AS package_count, SUM(remain_credits) AS sum_remain
FROM credit_package GROUP BY type, status ORDER BY type, status;

-- 6. distinct 用户数
SELECT COUNT(DISTINCT user_id) AS distinct_users FROM credit_package;

-- 7. 全状态分布
SELECT COUNT(*) AS total,
  SUM(CASE WHEN status='active' THEN 1 ELSE 0 END) AS active_count,
  SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END) AS pending_count,
  SUM(CASE WHEN status='exhausted' THEN 1 ELSE 0 END) AS exhausted_count,
  SUM(CASE WHEN status='expired' THEN 1 ELSE 0 END) AS expired_count,
  SUM(CASE WHEN status='cancelled' THEN 1 ELSE 0 END) AS cancelled_count
FROM credit_package;

-- 8. 多段订阅用户
SELECT user_id, COUNT(*) AS segment_count FROM credit_package
WHERE type = 'subscription' GROUP BY user_id HAVING COUNT(*) > 1 ORDER BY segment_count DESC LIMIT 50;

-- 9. User 406 段详情
SELECT id, type, status, total_credits, remain_credits, activated_at, expires_at, grant_source
FROM credit_package WHERE user_id = 406 AND type = 'subscription' ORDER BY activated_at;

-- 10. User 411 段详情
SELECT id, type, status, total_credits, remain_credits, activated_at, expires_at, grant_source
FROM credit_package WHERE user_id = 411 AND type = 'subscription' ORDER BY activated_at;

-- 11. exhausted 包余额检查
SELECT id, user_id, total_credits, remain_credits, status, activated_at, expires_at
FROM credit_package WHERE type = 'trial' AND status = 'exhausted' ORDER BY id;
```
