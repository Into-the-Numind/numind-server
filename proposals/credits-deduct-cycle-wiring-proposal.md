# Credits 扣减切换到 credit_cycle / user_booster_balance — 提案 + PRD

> **状态**：S1 (S2 spec 前的最后锁定)
> **NDF 版本**：1.1
> **创建日期**：2026-05-14
> **来自**：S0 requirement card (`requirements/credits-deduct-cycle-wiring.md`) + S0 reviewer subagent + user D2/D4 拍板

---

## §1 方案概述 [客户可见]

### 一句话描述

修复 P0 prod bug：所有按新流程开通会员的用户运行 SOP / chatbot / salesrag 都被错误拦截"积分不足"。把内部 Reserve/Reconcile 体系从读写老 `credit_package` 表，切换到读写新 `credit_cycle` + `user_booster_balance` + `trial_grant` 表（也就是 `/v1/credits/balance` 端点显示的那套数据源），让"显示的余额"和"扣减时检查的余额"是同一份数据。

### 用户视角后果

- 修复后：在期会员能正常跑 SOP / chatbot / salesrag，跑完会看到 `cycle_remaining` 真实下降
- 不影响：老用户（legacy_tier）路径行为完全不变
- 不影响：前端任何代码、API 响应格式

### 范围明确

- ✅ `creditsImpl.GetBalance` / `Reserve` / `Reconcile`（credits-mode 分支）
- ✅ `CanPerformAIOperation`（SOP/salesrag 多个入口的早期 guard）
- ✅ `refundToItems` / `refundOneItem`（per-item refund 路由）
- ✅ `MembershipService` 新增 tx-aware deduct/refund 接口
- ✅ `credit_reservation_item` schema 加 source_type + source_id 列
- ✅ STALE-LIE 注释修正
- ❌ 不删 credit_package / credit_account 表（legacy_tier 仍用，保留只读）
- ❌ 不改前端代码
- ❌ 不改 grant / fulfillOrder / booster 购买路径（已经写新表）
- ❌ 不清理 parent_grant 死代码（独立 housekeeping）

---

## §2 报价与周期 [客户可见]

- **预估工作量**：CC 4-7 h | 人类 1-2 工作日
- **风险等级**：高（动 Reserve/Reconcile 直接关系到扣费正确性）
- **交付时间线**：
  - S2 spec：1-2 h
  - S3 plan：30 min
  - S4 实施 + per-task two-stage review：2-4 h
  - S5 dev 验证：30 min
- **缓解风险**：
  - S2 spec 强制设计单 tx 原子性
  - S4 per-task review 双轨（spec compliance + code quality）
  - S5 dev 必须覆盖 admin / E2E_Booster_Child / Free / legacy_tier 4 种 fixture
  - 老 reservation 兼容（dispatch by item.source_type IS NULL）

---

## §3 决策锁定（D1-D6 全部锁定）

### D1: Tx-aware deduct/refund 接口设计 ✅ LOCKED

**问题**：`MembershipService.DeductCredits`（cycle.go:127）自开 tx，`creditsImpl.Reserve`（credit_service.go:499）也开 tx，MySQL 不支持真 nested tx。

**决议**：在 `MembershipService` 暴露 tx-aware 变体，让 caller 在自己的 outer tx 内调用：

```go
// 新接口
func (s *MembershipService) DeductCreditsTx(
    ctx context.Context,
    tx *gorm.DB,
    userID uint64,
    amount int64,
    now time.Time,
) (*DeductResult, error)

func (s *MembershipService) RefundCreditsTx(
    ctx context.Context,
    tx *gorm.DB,
    userID uint64,
    source DeductSource,    // "cycle" | "booster" | "trial"
    sourceID int64,          // credit_cycle.id / user_booster_balance.id / trial_grant.id
    amount int64,
    now time.Time,
) error

// DeductResult — 返回给 Reserve 写 item
type DeductResult struct {
    CycleDelta   int64
    BoosterDelta int64
    TrialDelta   int64
    // 详细分摊（用于 Reserve 写 credit_reservation_item 行）
    Items []DeductItem  // (source_type, source_id, amount)
}

type DeductItem struct {
    SourceType string  // "cycle" | "booster" | "trial"
    SourceID   int64
    Amount     int64
}
```

老 `DeductCredits()` 保留为 wrapper（内部开 tx 后调用 tx 版本），向后兼容现有 caller。

### D2: Reconcile 时 booster 已过期，退款 fallback ✅ LOCKED

**用户拍板**：用户体验优先，边缘场景概率极低（booster 90 天有效期）。

**Fallback 顺序**：
1. 退到当前 active booster 池（即使不是原批次）— 通过 `user_booster_balance.credits_remaining` 加回
2. 都过期 / 都用完 → 退到月度积分 `credit_cycle.credits_remaining`（如果当前 cycle 仍 active）
3. cycle 也无 active → 写 `membership_event` 台账记"would-refund X but no destination"，**金额丢失**（不静默失败：用户能在管理端查到）

### D3: 扣减优先级 ✅ LOCKED

**固化** `trial > cycle > booster`：
- Trial 优先（限定期 3 天 + 限定额度，"用掉算赚到"）
- Cycle 次之（月度续费的核心积分池）
- Booster 最后（永不过期 / 按 expires_at FIFO）

不做配置化（YAGNI），spec 注释说明决策原因。

### D4: credit_reservation_item 架构 ✅ LOCKED — 方案 A（精准版）

**用户拍板**：选精准版（代码多 30% 但不会退错），不选按比例分摊。

**Schema 变更**（migration）：

```sql
ALTER TABLE credit_reservation_item
  ADD COLUMN source_type VARCHAR(20) NULL COMMENT 'cycle/booster/trial; NULL = legacy old-path',
  ADD COLUMN source_id BIGINT UNSIGNED NULL COMMENT 'FK to credit_cycle.id / user_booster_balance.id / trial_grant.id depending on source_type',
  ADD INDEX idx_cri_source (source_type, source_id);
-- package_id 列保留 nullable（legacy reservation 仍用）
```

**Dispatch 规则**：
- `source_type IS NULL` → 走 legacy refund 路径（item.package_id → credit_package）
- `source_type IS NOT NULL` → 走 new refund 路径（按 source 分支到 RefundCreditsTx）

### D5: HasActiveMembership() 审计 ✅ COMPLETE

**审计结果**：`user.go:101 HasActiveMembership()` 仅读 in-memory `u.UserTier`（user_tier 字段），不查新表。对纯 credits-mode 用户安全（user_tier='free' → 返回 false → 走 credits 分支）。

**已知边缘情况**（不阻塞本 feature）：legacy 用户 billing_mode 切到 credits 后，如 user_tier 字段仍存活，`isEffectiveLegacy` 可能错误地路由到 legacy 分支。**Migration 边缘情况**，本 feature 不处理，写入 `risks` 表。

### D6: In-flight 老 reservation 兼容 ✅ LOCKED

**Dispatch 规则**（同 D4）：通过 `credit_reservation_item.source_type` 区分老/新 reservation：

- `source_type IS NULL`（migration 前的 reservation）→ `refundOneItem` 走老逻辑（读 credit_package via package_id，写 credit_package）
- `source_type IS NOT NULL`（migration 后的 reservation）→ `refundOneItem` 走新逻辑（调用 `MembershipService.RefundCreditsTx`）

Migration 后所有老 reservation 行 source_type 默认 NULL，refund 时仍走老路径 → 不破坏 in-flight 状态。

---

## §4 实施粒度（S3 plan 预览）

预估 8-10 个 task，分两批：

### Phase 1: 基建（4 tasks）

- **T1** Migration: credit_reservation_item 加 source_type + source_id + index
- **T2** Model: `credit_reservation_item.go` 加新字段 + GORM 标签
- **T3** Membership service tx-aware 接口: `DeductCreditsTx` + `RefundCreditsTx` + `DeductResult` 类型
- **T4** Membership store: `cycle_store.go` / `booster_store.go` / `trial_store.go` 暴露 tx-aware deduct/refund 方法

### Phase 2: Credit service 切换（4-5 tasks）

- **T5** `creditsImpl.GetBalance` 改读 `MembershipService.GetBalance`（credits 分支）
- **T6** `ICreditBiz.CanPerformAIOperation` 改读 `MembershipService.GetBalance`（credits 分支）
- **T7** `creditsImpl.Reserve` 改调 `DeductCreditsTx`，写 item 行（含 source_type / source_id）
- **T8** `creditsImpl.Reconcile` / `refundToItems` / `refundOneItem` dispatch（NULL → 老路径，否则走新）
- **T9** 注释修正 + dead helper 标记 + S5 验证策略 doc（NDF Rule 10）

每个 task 单独 commit + per-task two-stage review (spec compliance + code quality)。

---

## §5 风险表

| 风险 | 等级 | 缓解 |
|------|------|------|
| 单 tx 内 deduct + insert reservation 任一失败 | P0 | T7 实现强制单 tx；unit test 覆盖 partial failure |
| Migration 期间 in-flight reservation 错路由 | P1 | T1 migration 加 IS NULL → 老 path dispatch 默认 |
| Legacy_tier 用户路径回归 | P1 | T5/T6/T7 严格 `if user.BillingMode == credits` 分支，老 path 不动；S5 用 legacy 账号验证 |
| Booster 过期边缘退款丢失 | P2 | D2 锁定 fallback，写 ledger 不静默 |
| HasActiveMembership migration 边缘 | P3 | D5 文档化，不阻塞本 feature |
| 测试 fixture E2E_Booster_Child DB state 不准 | P2 | T9 S5 验证策略文档明确 DB state 检查 SQL |
| Dev CI Docker Hub 偶发超时 | P3 | 上轮经验：SSH mirror pull 后 CI rerun |

---

## §6 与 membership-balance-read-path 关系

| Feature | 修了什么 | 没修什么 |
|---------|---------|---------|
| `membership-balance-read-path`（上轮，S6） | `/v1/credits/balance` + `/v1/users/me` + `/v1/customers/sub-users` 显示路径 | 内部 Reserve/Reconcile / pre-check |
| **本 feature** | 内部 Reserve/Reconcile / pre-check / CanPerformAIOperation | （其它都不动） |

合起来 = credits-mode 体系完整接通。Prod 部署顺序：必须先上本 feature 再上 read-path（避免显示对但跑不了），或者两个一起上。**推荐两个一起 tag prod**（已 dev 验证 read-path，本 feature dev 验证后一起 release）。

---

## §7 验收（高层 — 详 S2 spec）

- ✅ Dev admin (cycle=2000) 跑 SOP，运行后 `cycle_remaining` 降到 ~1500-1800（按 estimate 浮动）
- ✅ Dev admin 跑 100 个 token 的小 SOP，Reconcile 后 cycle 退还多扣的部分
- ✅ Dev E2E_Booster_Child (trial=200, booster=600, sub=0) 跑 SOP，按 trial → booster 顺序扣减
- ✅ Dev free 用户跑 SOP → ErrInsufficientCredits（cycle=0, booster=0, trial=0）
- ✅ Dev legacy_tier 用户跑 SOP → 老路径行为不变
- ✅ `task lint` + `task test`（含 race + coverage）全过
- ✅ 关键代码注释 STALE-LIE 已修正
- ✅ Manifest stage S4 → S5 → S6 完整记录
