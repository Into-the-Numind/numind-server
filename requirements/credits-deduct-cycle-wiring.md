# Credits 扣减切换到 credit_cycle / user_booster_balance（credits-deduct-cycle-wiring）

## 来源

- 提出人：项目负责人（P0 prod incident 第二次延烧）
- 提出日期：2026-05-14
- 前置 feature：`membership-balance-read-path`（S6，dev 已部署；修了 read 显示）
- 历史规划：`docs/superpowers/plans/2026-04-29-membership-credits-redesign-plan.md` Task 16 cleanup（只覆盖 cron 删除 + helper 替换，未具体设计 reserve/reconcile 切表 wiring）
- 触发场景：上一轮修复后用户运行 SOP 仍报"积分不足"，但 `/v1/credits/balance` 显示 cycle 2000 + booster 600 = 2600

---

## 需求描述

**核心诉求：** 让所有 `billing_mode=credits` 用户能正确从 `credit_cycle.credits_remaining` + `user_booster_balance.credits_remaining` 池子扣减 SOP / chatbot / salesrag 运行积分，而不是从老 `credit_package` 表（该表对新用户为空）。

**现状盘点（三方 reviewer subagent 共识，2026-05-14）：**

| 路径 | 现实读 / 写 | 应该读 / 写 |
|------|-----------|-----------|
| SOP `CreateRun` early guard | `creditSvc.GetBalance` → `GetQuotaBreakdown` 读 `credit_package` | 读 `credit_cycle.credits_remaining` + `user_booster_balance` |
| SOP `ExecuteNode` precise pre-check | `CheckAndEstimate` → 同上 | 同上 |
| Chatbot pre-check (`ContextBudgetCredits` middleware) | `CheckAndEstimateBudget` → `s.credits.GetBalance` → 同上 | 同上 |
| Salesrag pre-check | `CheckAndEstimate` → 同上 | 同上 |
| Reserve（预扣，所有路径） | `creditsImpl.Reserve` → `DeductCreditsTx` → `GetActivePackagesForUpdate` + `UpdatePackage` 都写 `credit_package` | 写 `credit_cycle.credits_remaining` + `user_booster_balance`，按优先级 FIFO（trial > cycle > booster） |
| Reconcile（对账退/补） | `reconcileWithTokens` → `DeductCreditsTx` / `refundToItems` 写 `credit_package` | 同上，分摊回原扣减池 |
| **真正写新表的 path** | `MembershipService.DeductCredits` 写 `credit_cycle` + `user_booster_balance` | 已实现但**没有任何 caller**（`cycle.go:97-101` 自承 Task 16 未完成） |

**Smoking gun：** `store/credit.go:372-384` 注释承诺 "subscription/trial 还由 GrantOrRenewSubscription / GrantTrial 双写到老表保持向后兼容"。三方 reviewer grep 验证：**这两个函数完全不写 credit_package**。注释 STALE-LIE，承诺的双写从未实现。

**生产影响：** 任何通过新 grant 路径（`POST /v1/users/children/:id/grant-membership`）开通会员的 credits-mode 用户，都不能跑 SOP / chatbot / salesrag。dev 上的 admin 账号也复现（cycle 2000 但 Reserve 时 SubRemain=0）。Wendy（prod user 427）就算上线 read-path fix 也会立刻撞这个 bug。

---

## 业务目标

| 目标 | 衡量 |
|------|------|
| credits-mode 用户能正确扣减 cycle + booster 积分 | dev 上跑 1 次 SOP 后，`/v1/credits/balance` 的 `cycle_remaining` 下降 ≈ 估算值 |
| Pre-check 不再误判 ErrInsufficientCredits | admin 账号（cycle 2000）能成功提交 SOP 运行，不被 pre-check 拦截 |
| Reserve/Reconcile 与 cycle / booster 联动 | `credit_reservation` 行记录的 `source` 字段反映实际扣减池（trial/cycle/booster） |
| Legacy_tier 用户路径完全不动 | `billing_mode='legacy_tier'` 用户跑 SOP 行为 100% 与本 feature 上线前一致 |
| 修补 STALE-LIE 注释 | `store/credit.go:375-379` 注释要么删除（如不再被读）要么改写真实行为 |

---

## 优先级

**P0（最高）** — 所有 credits-mode 用户跑不了任何付费操作。上一轮 read-path fix 只是让"显示"正确，运行入口仍 100% 阻塞。

---

## 范围

### In Scope

1. **`creditsImpl.GetBalance`**（`internal/numind/biz/credit/credit_service.go:934`）：credits-mode 用户改读 `MembershipService.GetBalance`（返回 trial_remaining + cycle_remaining + booster_usable）；legacy 路径不变。
2. **`ICreditBiz.CanPerformAIOperation`**（`internal/numind/biz/credit/credit.go:56-78`）：credits-mode 用户走 `MembershipService.GetBalance` 判断池子余额，不再读 `credit_account.balance`。**S0 reviewer P0-1 补漏**：此函数被 sop.go:670 / sop.go:2290 / sales_rag.go:146/722/807/902/1059 多处调用，是 SOP 早期 guard + chat / salesrag 直检入口。原 card 漏掉这条路径。
3. **`creditsImpl.Reserve`**（`credit_service.go:444`）：credits-mode 用户改调用 `MembershipService.DeductCreditsTx`（**新增 tx-aware 变体，见 D1**）扣减 estimated_credits，FIFO 优先级见 D3；同时写 `credit_reservation` 行 + 必要的 source-typed item 行（schema 见 D4）；legacy 路径不变。
4. **`creditsImpl.Reconcile`** / `reconcileWithTokens`（`credit_service.go:670`）：基于 reservation 分摊多退少补 — 退到原扣减池；补则继续 `DeductCreditsTx`；legacy 路径不变。
5. **`refundToItems` / `refundOneItem`**（`credit_service.go:842-889`）：**S0 reviewer P0-2 补漏**：当前实现 `tx.First(&pkg, item.PackageID)` 读 `credit_package` 再 `tx.Save`，credits-mode 用户需替换为按 item 类型（cycle/booster/trial）路由到对应新表的 +回操作。该函数需重写，不只是 schema 变更。
6. **`creditsImpl.CheckAndEstimate` / `CheckAndEstimateBudget`**（`credit_service.go:166, 385`）：read path 跟随 GetBalance 自动修。
7. **`store/credit.go:372-384` STALE-LIE 注释**：用真实双系统说明替换（或全删）。
8. **`credit_reservation` + `credit_reservation_item` schema**：**S0 reviewer P0-3 升级**：新增字段方案在 D4 决策；最低限度需要让 item 行能区分 source pool（credit_cycle / user_booster_balance / trial_grant）而不是 credit_package FK。需 migration。
9. **`MembershipService` 扩展 tx-aware deduct + refund 接口**：当前 `DeductCredits` 自开 tx（cycle.go:127），需新增 `DeductCreditsTx(ctx, tx, ...)` 和 `RefundCreditsTx(ctx, tx, source, amount)` 让 Reserve/Reconcile 在外层 tx 内调用。**S0 reviewer P1-1**。
10. **测试覆盖**：
    - Unit test 覆盖 `creditsImpl.Reserve` 各 billing_mode 分支
    - Unit test 覆盖 Reconcile 多退少补对 cycle / booster / trial 的分摊
    - Unit test 覆盖 `CanPerformAIOperation` credits-mode 分支
    - Integration test：admin (cycle=2000, booster=0) 跑 SOP → Reserve 应从 cycle 扣 estimated；Reconcile 后 cycle.credits_remaining 反映实际 token 量
    - Integration test：E2E_Booster_Child（trial 200 + booster 600，sub=0）跑 SOP，按 trial → booster FIFO 扣，trial 耗尽后切 booster — **DB state 准备文档化（S0 reviewer P2-1）：trial_grant.expires_at > now AND trial_grant.credits_remaining > 0；user_booster_balance.credits_remaining > 0；subscription 表无行**
    - Regression：legacy_tier 用户跑 SOP 行为不变

### Out of Scope（明确不做）

- ❌ 删除 `credit_package` 表 / `credit_account` 表（保留只读，legacy_tier 仍用；后续单独 feature 决定 DROP 时机）
- ❌ 删除 `creditsImpl.deductCreditsTxFull`（保留供 legacy_tier 走，避免连锁修改）
- ❌ `parent_grant/grant.go` 死代码清理（独立 feature，已在 audit 中标注）
- ❌ 改变前端 `/v1/credits/balance` 响应结构（已是新字段，本 feature 后端实现也用相同结构）
- ❌ Booster 购买路径改造（`fulfillOrder` 已经直接写 `user_booster_balance`，无需改）
- ❌ Grant 路径改造（已经只写新表，无需改）
- ❌ `parent_grant/grant.go` 死代码删除（已确认 unreachable，遗留作未来 housekeeping）

### 范围中关键的"模糊地带"，S1 需要锁定

- **D1：Reserve 事务边界 — Tx-aware deduct/refund 必须** — **S0 reviewer P1-1 升级为 near-certain blocker**：`MembershipService.DeductCredits`（cycle.go:127）自开 tx，`creditsImpl.Reserve`（credit_service.go:499）也开 tx，MySQL 不支持真正的 nested tx（GORM `.Transaction()` 不默认用 savepoint）。S1 必须锁定：在 `MembershipService` 暴露 `DeductCreditsTx(ctx, tx *gorm.DB, ...)` 和 `RefundCreditsTx(ctx, tx, ...)` 变体，让 Reserve 在自己的 outer tx 内调用。这是结构性 refactor 而非 spec 决策。
- **D2：Reconcile 时 booster 已过期怎么办** — 反对账要把 cycle 扣的退回 cycle 没问题；但如果当时从某个 booster 批次扣了 100，Reconcile 时该 booster 批次已 expires_at 过去，钱退到哪里？提议：退到当前 active booster 池；如果 booster 池为空就退到 cycle；如果都空就丢失（写 ledger 但不退）。S1 决策。
- **D3：trial → cycle → booster 优先级是否固化** — 当前 spec 说 trial 优先，但实际 trial.credits_remaining 用完后应该自动转到 cycle？S1 锁定。
- **D4：`credit_reservation` + `credit_reservation_item` 架构选择** — **S0 reviewer P0-3 升级**：原 card 提议只在 parent row 加 `cycle_delta` / `booster_delta` / `trial_delta`，但 `credit_reservation_item.package_id` 当前 FK 到 `credit_package`，refund 时按 item 路由。两个互斥选项需 S1 锁定：
  - **方案 A**：让 item 变成 source-typed 行（新增 `source_type` enum + `source_id` 通用 FK，不再硬绑 credit_package）。refund 仍走 per-item routing。改动大但更"对"。
  - **方案 B**：仅在 parent row 加三个 delta 字段，废弃 item 路由（item 表保留只读用于 old-path reservations）。Refund 时按比例分摊回三个池子。改动小但精度差。
- **D5：`HasActiveMembership()` 实现审计** — **S0 reviewer P1-2**：`isEffectiveLegacy` 路由用 `user.HasActiveMembership()`（credit_service.go:78）。需 S1 验证此 helper 是否读 user model 字段（user_tier / tier_expires）还是新表（subscription）。如果读旧字段，credits-mode 用户能正确走 credits 分支，但需文档化。
- **D6：In-flight 老 reservation 兼容** — **S0 reviewer P2-2**：本 feature 上线时，可能存在已 reserved 但未 reconcile 的老路径 reservation 行（item.package_id → credit_package）。Reconcile 实现必须分支：if items.source_type IS NULL → 走老 refund 路径（写 credit_package）；else → 走新路径（写 cycle / booster / trial）。Migration 后老行的 source_type 默认 NULL。S1 锁定 dispatch 规则。

---

## 不在范围（避免延伸）

- 不改 billing_mode 切换逻辑（legacy_tier → credits 是另外的 feature）
- 不改 R2 估算逻辑（estimate.go 输出的 estimated_credits 直接消费）
- 不改 fulfillOrder（booster 购买后已 +到 user_booster_balance）
- 不改 grant flow（subscription / trial_grant 已写新表）
- 不改前端任何代码（balance endpoint 响应结构不变）

---

## 风险

| 风险 | 影响 | 缓解 |
|------|------|------|
| Reserve 事务边界不正确导致部分扣减 | 用户被扣 cycle 但 reservation 没创建（钱丢） | S2 spec 强制要求单 tx 内完成 cycle 扣减 + reservation insert |
| Reconcile 退回逻辑错误 | 多扣未退或重复退 | Unit test 覆盖所有 4 种 cycle/booster 组合 + delta>0/<0/=0 矩阵 |
| Legacy_tier 路径回归 | 老用户也跑不了 SOP | 修改前用 grep 找出 legacy_tier 测试 fixture，S5 用 legacy 账号验证 |
| credit_reservation schema 变更 | 历史数据兼容 | 新字段默认 NULL，老行不影响（迁移幂等，新行才填） |
| Booster 过期但有未对账 reservation | 退款逻辑不知道往哪退 | S1 D2 决策，文档化退款 fallback 顺序 |

---

## S1 决策清单（必须在进 S2 之前锁定）

- **D1：Tx-aware deduct/refund 接口设计**（P1-1 升级）— `MembershipService` 暴露什么 tx 变体？签名？
- **D2：Reconcile 时 booster 已过期 fallback 顺序**
- **D3：扣减优先级（trial → cycle → booster）固化 vs 配置化**
- **D4：item-level source-typed routing (方案 A) vs parent-level proportional refund (方案 B)**（P0-3）
- **D5：`HasActiveMembership()` 实现审计 + 文档化**（P1-2）
- **D6：In-flight 老 reservation 的 dispatch 规则**（P2-2）

---

## 验收（高层）

- ✅ Dev: admin 账号能成功提交 1 个 SOP 运行，运行后 `/v1/credits/balance` 的 `cycle_remaining` 下降合理值
- ✅ Dev: E2E_Booster_Child（trial+booster）跑 SOP，扣减按 trial → booster 顺序生效
- ✅ Dev: free 用户（无 sub 无 trial 无 booster）跑 SOP 仍正确返回 ErrInsufficientCredits
- ✅ Legacy_tier 用户跑 SOP 行为不变（quota 检查 + monthly_sop_runs 增量）
- ✅ Reconcile 多退少补：tokens 实际 < 估算 → cycle 退回；tokens 实际 > 估算 → cycle 继续扣
- ✅ 注释 STALE-LIE 已修正
- ✅ `task lint` + `task test` 全过

---

## 时间预估

- S0 → S1：30 min（决策 + PRD）
- S1 → S2：1-2 h（spec design）
- S2 → S3：30 min（task breakdown）
- S3 → S4：2-4 h（implementation，按 task 分批两阶段 review）
- S4 → S5：30 min（dev 验证）
- 全程：4-7 h CC，~1-2 个工作日人类时间

---

## 影响范围（grep 验证）

```
grep -rln "GetQuotaBreakdown\|deductCreditsTxFull\|DeductCreditsTx\|creditsImpl\." internal/
```

预期会涉及（修改）：
- `internal/numind/biz/credit/credit_service.go` — creditsImpl.GetBalance / Reserve / Reconcile 分支
- `internal/numind/biz/credit/credit.go` — deductCreditsTxFull legacy_tier 路径标注
- `internal/numind/biz/membership/cycle.go` — 移除 Task 16 TODO 注释
- `internal/numind/biz/membership/service.go` — 可能新增 ReserveCredits 方法（如果 DeductCredits 不够用）
- `internal/numind/store/membership/cycle_store.go` — 新增 ReserveTx 或类似
- `internal/numind/store/credit.go` — STALE-LIE 注释修正
- `migrations/YYYYMMDD_credit_reservation_pool_delta.sql` — schema 变更
- 测试：5-8 个测试文件
