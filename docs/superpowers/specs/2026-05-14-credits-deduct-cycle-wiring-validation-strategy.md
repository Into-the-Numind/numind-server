# Credits 扣减切换 — S5 Validation Strategy

**Feature ID:** `credits-deduct-cycle-wiring`
**Date:** 2026-05-14
**Linked artifacts:** S3 plan T10 / S2 spec §5

---

## §1 验证方式 — 三层（L1/L2/L3）

| 层 | 工具 | 覆盖范围 | 触发时机 | 持久化保护？ |
|----|------|----------|----------|---------------|
| L1 单元测试 | `go test ./internal/numind/biz/credit/...` + `go test ./internal/numind/biz/membership/...` | 算法正确性：lock 顺序 / priority FIFO / D2 fallback / dispatch by source_type | CI on every push | ✅ 持久化 |
| L2 Dev backend curl | `curl + jq` on `$DEV_API_URL` 端点 | 端到端真实数据：admin / E2E_Booster_Child / E2E_Frozen_Child / E2E_Free_Child / 1 个 legacy_tier 测试号 | 部署 dev 后一次性 | ❌ 一次性，不产生测试代码 |
| L3 Playwright E2E | `e2e/membership-credits-redesign.spec.ts` Path 1 + 6 (parent-only) | 现有用户路径回归（确保 read-path feature 不破） | 本地 + CI 每次 push | ✅ 持久化 |

---

## §2 为什么这套组合（含选 vs 不选）

### 选用 L1 + L2 + L3

- **L1 unit** 覆盖算法正确性。新引入的 `DeductCreditsTx` / `RefundCreditsTx` / 老 wrapper / refund dispatch 四个核心函数都需要测。
- **L2 curl** 是端到端最直接验证 —— 因为本 feature 完全后端，无 UI 变化，前端 dogfood 无意义。
- **L3 Playwright** 起 regression-guard 角色，验证现有用户路径不破（Path 1 trial grant / Path 6 idempotency matrix）。本 feature **不写新 spec**。

### 明确不选 gstack /qa

- gstack /qa 是浏览器视觉 QA，本 feature 后端逻辑改造无 UI 变化，浏览器看不出差异。无价值。

### 回归保护诚实声明

- **L2 curl 是一次性验证**，不产生持久化测试代码。如未来修改本 feature 涉及的代码（cycle.go / credit_service.go / credit.go），需要重新跑 L2 才能验证不破坏。
- **L1/L3 是持久化保护**：CI 每次 push 自动跑。
- **未来回归保护缺口**：DeductCreditsTx / RefundCreditsTx 当前单元测试覆盖不完整（仅 transitive via wrapper）。Follow-up：在独立 feature 中补完 11 个 case 矩阵（见 §3 P9）。

---

## §3 关键用户路径（10 条，含层级标记）

| # | Path | 层级 | 验证步骤 |
|---|------|------|----------|
| **P1** | Admin (cycle=2000) 跑 1 个 SOP | L2 + L1 | 跑前 `curl /v1/credits/balance` cycle_remaining=2000；跑 SOP；跑后 cycle ≈ 2000 - estimated |
| **P2** | Admin Reconcile token < 估算 → 退回 | L1 unit | `TestReconcileWithTokens` （新 + 老路径都覆盖；新路径走 RefundCreditsTx） |
| **P3** | E2E_Booster_Child (trial=200, booster=600, sub=0) 跑 SOP → trial 先扣 | L2 + L1 | child 号 login + 跑 SOP，看 trial_grant.credits_remaining 先扣空（先满 200）再扣 user_booster_balance。**DB state 准备**：trial_grant.expires_at > now AND credits_remaining > 0；user_booster_balance.credits_remaining > 0；subscription 表无行。 |
| **P4** | E2E_Frozen_Child (sub 过期 + booster) 跑 SOP → 失败（INV-15 frozen） | L2 | child 号 login 跑 SOP 应返 Insufficient（subscription.expires_at < now AND trial_grant.expires_at < now AND user_booster_balance.credits_remaining > 0）|
| **P5** | E2E_Free_Child 跑 SOP → ErrInsufficientCredits | L2 | 直接 expect fail (subscription 无行 AND trial_grant 无行 AND user_booster_balance 无行) |
| **P6** | Legacy_tier 用户跑 SOP → 老路径不变 | L2 + L3 | 用 legacy 测试号验证行为同 baseline；Playwright Path 1 验证 parent grant 流程不破 |
| **P7** | Reconcile booster 批次过期 → D2 fallback 触发 | L1 unit | `TestRefundCreditsTx_BoosterExpired_FallbackCycle` (补在 follow-up) |
| **P8** | In-flight 老 reservation refund → 走老路径 (source_type=NULL) | L1 unit | `TestRefundOneItem_LegacyDispatch` (补在 follow-up) |
| **P9** | 并发 Reserve（同一 user）→ 锁顺序避免死锁 | L1 unit (sqlmock) | Lock order 测试（实际并发死锁测试成本高，sqlmock 验证 SQL 顺序即可。补在 follow-up） |
| **P10** | Migration 上线后老用户 in-flight reservation Reconcile → 不影响 | L2 | Dev MySQL 验证 migration 后老 row source_type IS NULL；新 row source_type 非 NULL |

---

## §4 Acceptance gate

进入 S6（合并 develop + dev 部署）前，必须：

- ✅ L1 unit tests 全 PASS：`task lint && task test`（已在 T0-T9 每个 task 验证）
- ✅ L2 curl P1/P3/P4/P5/P6/P10 都成功
- ✅ L3 Playwright Path 1 + 6 PASS
- ⚠️ 补 P7/P8/P9 单元测试列为 follow-up feature，不阻塞本次 prod 部署（合理因为这些都是 corner case，主路径已通过 P1-P6 验证）

进入 S7（prod tag）前，必须：

- ✅ Dev 上至少 24h 观察期无异常 log（grep `reserve: credits-mode new path`，看流量比例正常 + 无 ErrInsufficientCredits 反弹）
- ✅ Wendy (prod user 427) 的 cycle/booster/trial 状态与 admin (dev user 25) 在 dev 行为一致 (curl prod backend 校验)
- ✅ Rollback 流程演练：如 prod 异常，可 redeploy 上版本 image + run migration rollback

---

## §5 Rollback

如部署后发现严重问题：

1. **代码层**：redeploy 上一个 image (pmtmyaggy/numind-server:develop-0142bcd) — 这是 read-path 修复后的稳定版
2. **DB 层**：迁移 rollback 文件已写：`migrations/20260514_120000_credit_reservation_item_add_source_rollback.sql`。Run 仅当**确认无任何 in-flight 新路径 reservation**（grep `source_type IS NOT NULL` count = 0）
3. **半路径事故**：如新路径 reservation 已写但 reconcile 失败 — refund 卡死，可手工补：`UPDATE credit_cycle SET credits_remaining = credits_remaining + N WHERE user_id = X AND cycle_end > NOW()` 并写 membership_event 审计
