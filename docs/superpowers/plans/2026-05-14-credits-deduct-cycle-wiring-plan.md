# Credits 扣减切换 — S3 Task Plan

**Feature ID:** `credits-deduct-cycle-wiring`
**Spec:** `docs/superpowers/specs/2026-05-14-credits-deduct-cycle-wiring-design.md`
**Date:** 2026-05-14
**Track:** Standard (P0 prod accelerated)

---

## §0 Plan 概览

10 个 atomic task + 1 个 S5 validation strategy doc。每个 task：
- 独立可编译（commit 后 `go build ./...` PASS）
- 独立 commit（per-task two-stage review 后 commit）
- Per-task review：spec compliance subagent + code quality subagent

预估实施时间：3-5 小时 CC，~1-2 天人类。

依赖图：

```
T0 (wiring) ──┬─→ T2 (types) ──→ T3 (DeductCreditsTx) ──→ T4 (RefundCreditsTx) ──→ T5 (wrapper)
              │                                                                          │
T1 (migration) ──→ T6 (GetBalance) ──→ T7 (CanPerformAIOperation) ─────────────────────┘
                                                                                          │
                                                              ↓                            │
                                                          T8 (Reserve) ─→ T9 (refund + cleanup)
                                                                                          │
                                                                                          ↓
                                                                              T10 (validation doc)
```

---

## §1 Tasks

### T0 — creditsImpl 注入 MembershipService

**Goal:** `NewCreditService` 接收 `membershipSvc`，`creditsImpl` struct 加字段，所有 caller 同步注入。本任务**不改任何业务逻辑**，仅基础设施。

**Files:**
- `internal/numind/biz/credit/credit_service.go`：`NewCreditService` 加 `membershipSvc *membership.MembershipService` 参数；`creditsImpl` 加字段；`creditService` 也加字段（注入到 credits 分支）
- `internal/numind/router.go`：`NewCreditService(...)` 调用处加 `membershipSvc` 参数
- 测试文件：搜 `NewCreditService(` 在 `_test.go` 中的调用，全部加 `nil`（让现有 legacy-only 测试不破）

**Acceptance:** `go build ./...` PASS；`task lint` PASS；`go test ./internal/numind/biz/credit/...` PASS（不应有新行为变化）。

**Commit msg:** `feat(credit): wire MembershipService into creditsImpl (T0/10)`

---

### T1 — Migration: credit_reservation_item +source_type +source_id

**Goal:** Schema 加 2 个 nullable 列 + 复合索引，老 reservation 行不影响。

**Files:**
- 新建 `migrations/20260514_120000_credit_reservation_item_add_source.sql`（forward）
- 新建 `migrations/20260514_120000_credit_reservation_item_add_source_rollback.sql`（rollback）
- `internal/pkg/model/credit_reservation.go`：CreditReservationItem struct 加 `SourceType *string` + `SourceID *uint64`，GORM 标签 + JSON 标签

**Acceptance:** Migration SQL 语法正确；GORM model 与 SQL schema 一致；`go build ./...` PASS。

**Commit msg:** `feat(schema): credit_reservation_item add source_type/source_id (T1/10)`

---

### T2 — DeductSource enum + 扩展 DeductionResult.Items

**Goal:** 类型基础：DeductSource 枚举 + DeductItem struct + 在已有 DeductionResult 加 Items 字段。

**Files:**
- `internal/numind/biz/membership/types.go`（新建）：`DeductSource` enum + `DeductItem` struct
- `internal/numind/biz/membership/cycle.go`：在 line 22-29 的 `DeductionResult` 加 `Items []DeductItem`

**Acceptance:** `go build ./...` PASS；老 caller 不感知 `Items` 字段但能拿到（zero-value nil slice）；测试 PASS。

**Commit msg:** `feat(membership): add DeductSource enum + DeductionResult.Items (T2/10)`

---

### T3 — DeductCreditsTx 实现

**Goal:** 在 cycle.go 加 `DeductCreditsTx(ctx, tx, userID, amount, now)`，沿用既有 alphabetical lock 顺序，按 trial > cycle > booster 优先级分摊，返回 `DeductionResult.Items` 详细列表。

**Files:**
- `internal/numind/biz/membership/cycle.go`：新增 `DeductCreditsTx`
- `internal/numind/biz/membership/cycle_test.go`：覆盖测试（trial-only / cycle-only / booster-only / trial-overflow / multi-pool / insufficient / lock-order）

**Acceptance:** Unit test 覆盖 7 个 case，全 PASS；`task lint` PASS。

**Commit msg:** `feat(membership): implement DeductCreditsTx (T3/10)`

---

### T4 — RefundCreditsTx 实现（含 D2 fallback）

**Goal:** 在 cycle.go 加 `RefundCreditsTx(ctx, tx, userID, source, sourceID, amount, now)`，原 source 仍 active 则退回原处；否则 fallback 链：active booster → active cycle → ledger 丢失。

**Files:**
- `internal/numind/biz/membership/cycle.go`：新增 `RefundCreditsTx`，返回 `(refundedTo DeductSource, refundedID uint64, refundedAmount int64, err error)`
- `internal/numind/biz/membership/cycle_test.go`：覆盖 fallback 链测试

**Acceptance:** 单元测试覆盖 4 个 case：original-active / booster-expired-fallback-cycle / all-expired-ledger / unknown-source-error。

**Commit msg:** `feat(membership): implement RefundCreditsTx with D2 fallback chain (T4/10)`

---

### T5 — 老 DeductCredits → wrapper

**Goal:** 老签名 `DeductCredits(ctx, userID, amount)` 改写为 wrapper，内部自开 tx 调 `DeductCreditsTx`。签名不变，向后兼容。

**Files:**
- `internal/numind/biz/membership/cycle.go`：改写老 `DeductCredits` 函数体（移除原 deduct 逻辑，改成 tx wrapper）；移除 `cycle.go:97-101` 的 Task 16 TODO 注释

**Acceptance:** 现有调用 `DeductCredits` 的所有点（如果有）行为不变；`task test` PASS。

**Commit msg:** `refactor(membership): DeductCredits → wrapper for DeductCreditsTx (T5/10)`

---

### T6 — creditsImpl.GetBalance + BalanceBreakdown +TrialRemain

**Goal:** GetBalance read path 切到 MembershipService；BalanceBreakdown 加 TrialRemain 字段；CheckAndEstimate + CheckAndEstimateBudget 的 `total = SubRemain + BoosterRemain` 改成含 TrialRemain。

**Files:**
- `internal/numind/biz/credit/types.go`：BalanceBreakdown 加 `TrialRemain int64`
- `internal/numind/biz/credit/credit_service.go:934`：`creditsImpl.GetBalance` 改 read 路径（credits-mode 走 MembershipService）
- `internal/numind/biz/credit/credit_service.go:212, 401`：CheckAndEstimateBudget + CheckAndEstimate 的 total 求和加 TrialRemain

**Acceptance:** 单元测试覆盖：credits-mode user (cycle=2000, trial=200, booster=600) → total=2800；legacy_tier user 行为不变。

**Commit msg:** `feat(credit): GetBalance reads MembershipService for credits-mode (T6/10)`

---

### T7 — CanPerformAIOperation credits-mode 分支

**Goal:** SOP/salesrag 早期 guard 不再读 credit_account。签名 `(bool, string)` 不变。

**Files:**
- `internal/numind/biz/credit/credit.go:56`：加 credits-mode 分支调 MembershipService.GetBalance；老 legacy 路径抽到 `canPerformAIOperationLegacy` helper

**Acceptance:** 单元测试覆盖 credits-mode trial / cycle / booster / 0-balance 4 个 case；legacy_tier 完全不变。

**Commit msg:** `feat(credit): CanPerformAIOperation reads MembershipService for credits-mode (T7/10)`

---

### T8 — creditsImpl.Reserve 切到 DeductCreditsTx

**Goal:** Reserve write path 在 outer tx 内调 DeductCreditsTx，按 result.Items 写 credit_reservation_item 行（含 source_type/source_id），加 observability log。

**Files:**
- `internal/numind/biz/credit/credit_service.go:444`：`creditsImpl.Reserve` credits-mode 分支
- `internal/numind/biz/credit/credit_service.go`：observability log 加 `log.C(ctx).Infow("reserve: credits-mode new path", ...)`

**Acceptance:** 单元测试覆盖：
- Reserve 100 from cycle=2000 → cycle=1900, item count=1
- Reserve 250 from trial=200+cycle=2000 → trial=0+cycle=1950, item count=2
- Reserve 5000 from cycle=2000 (不够) → ErrInsufficientCredits, tx rollback, cycle 不变
- legacy_tier user → 走老 Reserve 路径不变

**Commit msg:** `feat(credit): Reserve writes via DeductCreditsTx for credits-mode (T8/10)`

---

### T9 — refundOneItem dispatch + STALE-LIE 注释 + cleanup

**Goal:** Reconcile 退款按 item.SourceType 路由（NULL → 老路径，否则 → RefundCreditsTx）；删除 store/credit.go:372-384 整段 STALE-LIE 注释，加 1 行真实指向。

**Files:**
- `internal/numind/biz/credit/credit_service.go:846`：`refundOneItem` dispatch 分支
- `internal/numind/biz/credit/credit_service.go`：原 refund 逻辑抽到 `refundOneItemLegacy` helper
- `internal/numind/store/credit.go:372-384`：删除 STALE-LIE 注释，改成简短真实说明：`// 仅服务 legacy_tier 用户。credits-mode 用户的余额在 credit_cycle / user_booster_balance / trial_grant 表，由 MembershipService 管理。`

**Acceptance:** 单元测试覆盖：
- Refund item with source_type=NULL → 走 legacy 路径（写 credit_package）
- Refund item with source_type="cycle" → 走 new 路径（写 credit_cycle via RefundCreditsTx）
- 注释 grep 确认 STALE-LIE 删除

**Commit msg:** `feat(credit): refundOneItem dispatch by source_type + remove STALE-LIE (T9/10)`

---

### T10 — S5 validation strategy doc（NDF Rule 10 强制）

**Goal:** 把 S5 验证策略写成独立 .md 文件。

**Files:**
- 新建 `docs/superpowers/specs/2026-05-14-credits-deduct-cycle-wiring-validation-strategy.md`

**内容（详 S2 spec §5）：**
1. **验证方式**：单元测试 + dev curl + Playwright E2E（沿用现有 spec）
2. **理由**：
   - 单元测试覆盖 DeductCreditsTx / RefundCreditsTx 算法正确性
   - Curl 直接验证 dev backend `/v1/credits/balance` + 跑 SOP 后再查
   - Playwright 跑 membership-credits-redesign.spec.ts 验证现有用户路径不破
   - **不选 gstack /qa**：本 feature 后端逻辑改造，无 UI 变化
3. **关键用户路径（10 条）**：
   - P1：Admin (cycle=2000) 跑 1 个 SOP → 应成功
   - P2：Admin Reconcile 后 cycle_remaining 反映实际 token 量
   - P3：E2E_Booster_Child (trial+booster) 跑 SOP → trial 先扣
   - P4：E2E_Frozen_Child (sub 过期 + booster) 跑 SOP → 应失败（booster frozen）
   - P5：E2E_Free_Child 跑 SOP → ErrInsufficientCredits
   - P6：Legacy_tier 用户跑 SOP → 老路径行为不变
   - P7：Reconcile token 实际 < 估算 → cycle 退回多扣部分
   - P8：Reconcile booster 批次过期场景 → D2 fallback 触发
   - P9：In-flight 老 reservation refund → 走老路径（source_type=NULL）
   - P10：并发 Reserve（同一 user 两个 SOP 同时跑）→ 锁顺序避免死锁

**Acceptance:** 文档完整含 3 要素（方式 / 理由 / 路径）；S3 reviewer 单独审查（NDF Rule 10）。

**Commit msg:** `docs(s5): credits-deduct-cycle-wiring validation strategy (T10/10)`

---

## §2 实施流程总结

1. 按顺序执行 T0 → T10
2. 每个 task：
   - implementer subagent 实施 + commit
   - spec compliance reviewer subagent
   - code quality reviewer subagent
   - 主控 verify commit + git status + 更新 manifest progress
3. 任何 P0 review finding：fix 后重新 review
4. 任何 P1 review finding：必须修
5. 任何 P2 finding：能现在修则现在修
6. 完整后 manifest stage S4 → S5 → S6 → S7

---

## §3 风险 + Mitigation

| 风险 | Mitigation |
|------|-----------|
| T0 改 NewCreditService 签名破坏现有测试 | T0 acceptance 强制要求测试加 nil 后 PASS |
| T3 锁顺序写反导致死锁 | T3 acceptance 强制要求与 cycle.go:113-116 完全一致 |
| T8 outer tx + DeductCreditsTx 内部嵌套 tx 冲突 | T3 强制 DeductCreditsTx 不开内部 tx，T8 强制传 outer tx |
| T9 dispatch 分支 panic on missing SourceID | T9 加 nil 检查 + 默认走 legacy |
| Dev CI Docker Hub 拉镜像超时 | 上轮经验：SSH mirror pull + CI rerun |

---

## §4 完成标准

- T0-T10 全 commit + per-task 两阶段 review PASS
- `task test` 全 pass (含 race + coverage)
- `task lint` exit 0
- manifest progress 10/10 reviewed
- S5 validation 在 dev backend 端到端通过
