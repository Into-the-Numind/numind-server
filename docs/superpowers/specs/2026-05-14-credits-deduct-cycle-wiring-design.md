# Credits 扣减切换到 credit_cycle / user_booster_balance — S2 Spec

**Feature ID:** `credits-deduct-cycle-wiring`
**Track:** Standard (P0 prod accelerated)
**Repos:** numind-server only
**Date:** 2026-05-14
**Approach:** D4-A (item-level source-typed routing)

---

## §0 背景与边界

S1 proposal `proposals/credits-deduct-cycle-wiring-proposal.md` 已锁定全部 D1-D6。

**核心目标**：让 `creditsImpl.Reserve` / `Reconcile` / `CheckAndEstimate` / `CheckAndEstimateBudget` / `CanPerformAIOperation` 对 credits-mode 用户走 `MembershipService.DeductCreditsTx` / `RefundCreditsTx` 操作新表 `credit_cycle` + `user_booster_balance` + `trial_grant`，让 read+write 配套。Legacy_tier 路径保留不变。

**关键不变量**：
- INV-1：Reserve 时 deduct + reservation insert 在同一 outer tx，要么全成要么全失败
- INV-2：Reconcile 时 refund 按 item.source_type 路由（NULL=老路径写 credit_package，否则新路径写新表）
- INV-3：D3 优先级 `trial > cycle > booster` 固化。**Booster 是 per-user 单行聚合**（`user_booster_balance` 一个 user_id 一行 `credits_remaining int64`），不存在 per-batch FIFO；所有 booster 扣减都从该单行扣。"按 expires_at FIFO"原措辞误读，已删除。
- INV-4：D2 Refund fallback：原 source 不可用 → 当前 active `user_booster_balance` 行（如 credits_remaining 仍 > 0）→ 当前 active `credit_cycle` 行 → 写 `membership_event` ledger 不退（金额"丢失"但可追溯）
- INV-5：Legacy_tier 用户路径完全不变（任何 `if user.BillingMode == credits` 分支都不影响 legacy 分支）
- **INV-6（S2 reviewer P0-1 补漏）**：`creditsImpl` struct 需新增 `membershipSvc *membership.MembershipService` 字段；`NewCreditService` 签名需加 `membershipSvc` 参数；所有 caller (`router.go` 等) 同步注入

---

## §1 数据模型变更

### 1.1 Migration forward: `migrations/20260514_120000_credit_reservation_item_add_source.sql`

```sql
-- D4: 给 credit_reservation_item 加 source_type / source_id 列，让每个 item 行能精确路由
-- 到 cycle / booster / trial 三张新表。老 reservation 行 source_type 默认 NULL，refund 时仍走
-- 老路径（item.package_id → credit_package）。

ALTER TABLE credit_reservation_item
    ADD COLUMN source_type VARCHAR(20) NULL COMMENT 'cycle/booster/trial; NULL = legacy old-path' AFTER package_id,
    ADD COLUMN source_id BIGINT UNSIGNED NULL COMMENT 'FK to credit_cycle.id / user_booster_balance.id / trial_grant.id depending on source_type' AFTER source_type,
    ADD INDEX idx_cri_source (source_type, source_id);

-- package_id 仍保留 (nullable since legacy)，新行 source_type 不为 NULL 时 package_id 应为 NULL
```

### 1.2 Migration rollback: `migrations/20260514_120000_credit_reservation_item_add_source_rollback.sql`

```sql
-- 仅用于灾难恢复。在迁移后产生的"新路径 reservation"会失去 refund 路由信息。
-- 仅当确认无任何 in-flight 新路径 reservation 时执行。

ALTER TABLE credit_reservation_item
    DROP INDEX idx_cri_source,
    DROP COLUMN source_id,
    DROP COLUMN source_type;
```

### 1.3 Model: `internal/pkg/model/credit_reservation.go`

```go
type CreditReservationItem struct {
    ID            uint64    `gorm:"primaryKey;autoIncrement" json:"id"`
    ReservationID uint64    `gorm:"not null;index" json:"reservation_id"`
    PackageID     *uint64   `gorm:"column:package_id" json:"package_id,omitempty"`   // legacy
    SourceType    *string   `gorm:"column:source_type;size:20;index:idx_cri_source,priority:1" json:"source_type,omitempty"` // "cycle" | "booster" | "trial"
    SourceID      *uint64   `gorm:"column:source_id;index:idx_cri_source,priority:2" json:"source_id,omitempty"`
    Amount        int64     `gorm:"not null" json:"amount"`
    CreatedAt     time.Time `gorm:"autoCreateTime" json:"created_at"`
}
```

**Why pointer types**：legacy 行 `source_type=NULL`，新行 `package_id=NULL`。pointer 让两种状态可区分（用 `if item.SourceType != nil` 路由）。

---

## §2 MembershipService 新接口

### 2.1 DeductSource 枚举

```go
// internal/numind/biz/membership/types.go (新文件)

type DeductSource string

const (
    DeductSourceTrial   DeductSource = "trial"
    DeductSourceCycle   DeductSource = "cycle"
    DeductSourceBooster DeductSource = "booster"
)
```

### 2.2 DeductionResult + DeductItem（在已有类型上扩展，S2 reviewer P0-3 修正）

**已有类型** `cycle.go:22-29 DeductionResult{FromTrial, FromCycle, FromBooster}` 保留不变以避免破坏现有 caller。

**仅新增 Items 字段**（向后兼容增量）：

```go
// internal/numind/biz/membership/cycle.go — 在已有 DeductionResult 上加 Items

type DeductionResult struct {
    FromTrial   int64       // 既有
    FromCycle   int64       // 既有
    FromBooster int64       // 既有
    Items       []DeductItem // 新增 — 详细分摊，按扣减顺序记录
}

type DeductItem struct {
    SourceType DeductSource  // "trial" | "cycle" | "booster"
    SourceID   uint64         // trial_grant.id / credit_cycle.id / user_booster_balance.user_id
    Amount     int64
}
```

**注**：booster `SourceID` 用 `user_id`（user_booster_balance 表的 PK 是 user_id 而非自增 id，per INV-3）。

**注 #2**：S1 D1 文档把 sourceID 写成 `int64` 是 typo，统一以 `uint64` 为准（与 model 字段 `*uint64` 一致）。

### 2.3 DeductCreditsTx — tx-aware 扣减

```go
// internal/numind/biz/membership/cycle.go

// DeductCreditsTx 在 caller 提供的 tx 内扣减 amount 积分，按 trial > cycle > booster 优先级
// 分摊（D3 INV-3）。返回详细分摊用于 caller 写 credit_reservation_item。
//
// Caller 责任：保证 tx 已开启且未 commit。本函数仅写不 commit。
//
// 失败语义：amount 超过总余额时返回 ErrInsufficientCredits，tx 应由 caller rollback。
//
// 锁顺序（沿用现有 cycle.go:113 实现，避免死锁）：trial_grant → credit_cycle → user_booster_balance。
func (s *MembershipService) DeductCreditsTx(
    ctx context.Context,
    tx *gorm.DB,
    userID uint64,
    amount int64,
    now time.Time,
) (*DeductionResult, error) {
    // 1. 按固定锁顺序读余额（FOR UPDATE）
    // 2. 按 D3 优先级分摊：trial 先扣 → 满 cycle 后扣 → booster 单聚合行最后扣（无 per-batch）
    // 3. 余额不足整体 return ErrInsufficientCredits（不部分扣）
    // 4. 逐池 Update（trial_grant.credits_remaining / credit_cycle.credits_remaining / user_booster_balance.credits_remaining），写 membership_event
    // 5. 返回 DeductionResult{FromTrial, FromCycle, FromBooster, Items}，Items 含 1-3 个条目（按实际扣减池数）
}
```

### 2.4 RefundCreditsTx — tx-aware 退款

```go
// internal/numind/biz/membership/cycle.go

// RefundCreditsTx 在 caller 提供的 tx 内把 amount 积分退回指定 source。
// 如果原 source 不可用（cycle 已切到下个周期 / booster 批次已过期），按 D2 fallback：
//   active booster → active cycle → ledger 丢失（写 event 但不退）。
//
// 返回值：实际退回的目标（用于 caller 审计）；错误仅在数据库异常时返回。
func (s *MembershipService) RefundCreditsTx(
    ctx context.Context,
    tx *gorm.DB,
    userID uint64,
    source DeductSource,
    sourceID uint64,
    amount int64,
    now time.Time,
) (refundedTo DeductSource, refundedID uint64, refundedAmount int64, err error)
```

### 2.5 Wrapper: 老 DeductCredits 保持向后兼容

老签名 `DeductCredits(ctx, userID, amount) (*DeductionResult, error)` 不变（无 `now` 参数，无新 Items 字段）—— 改写为 wrapper，内部调 tx 版本：

```go
// cycle.go - 老 DeductCredits 改写为 wrapper（签名不变）
func (s *MembershipService) DeductCredits(ctx context.Context, userID uint64, amount int64) (*DeductionResult, error) {
    var result *DeductionResult
    err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        r, e := s.DeductCreditsTx(ctx, tx, userID, amount, time.Now().UTC())
        result = r
        return e
    })
    return result, err
}
```

**注**：老 caller 不感知 `Items` 字段，但拿到的 `*DeductionResult` 含新字段，零破坏。

---

## §3 creditsImpl 改造

### 3.1 GetBalance（read path）

```go
// credit_service.go

func (c *creditsImpl) GetBalance(ctx context.Context, user *model.User) (*BalanceBreakdown, error) {
    // credits-mode 用户：从 MembershipService 读
    if c.membershipSvc != nil {
        view, err := c.membershipSvc.GetBalance(ctx, uint64(user.ID), time.Now().UTC())
        if err != nil {
            return nil, fmt.Errorf("creditsImpl.GetBalance via membership: %w", err)
        }
        return &BalanceBreakdown{
            SubTotal:      view.CycleRemaining,           // cycle 当 sub
            SubRemain:     view.CycleRemaining,
            BoosterTotal:  view.BoosterUsable,            // 受冻结状态影响
            BoosterRemain: view.BoosterUsable,
            TrialRemain:   view.TrialRemaining,           // 新字段，BalanceBreakdown 需加
            // expires_at fields ...
        }, nil
    }
    // fallback：旧路径（理论上不会走到这里，但保留以防注入失败）
    return c.legacyGetBalance(ctx, user)
}
```

**Why keep legacyGetBalance as fallback**：避免 membershipSvc nil 注入时的硬失败，留 graceful degradation。

### 3.2 Reserve（write path）

```go
// credit_service.go:444

func (c *creditsImpl) Reserve(ctx context.Context, user *model.User, op Operation, in EstimationInput) (*Reservation, error) {
    pre, err := c.CheckAndEstimate(ctx, user, op, in)
    if err != nil {
        return nil, err
    }
    if !pre.Sufficient {
        return nil, ErrInsufficientCredits
    }

    var reservation *Reservation
    err = c.store.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
        // 1. 在 outer tx 内调 DeductCreditsTx
        result, e := c.membershipSvc.DeductCreditsTx(
            ctx, tx, uint64(user.ID), pre.EstimatedCredits, time.Now().UTC(),
        )
        if e != nil {
            return e  // tx rollback
        }

        // 2. 写 credit_reservation 行
        reservationRow := &model.CreditReservation{
            UserID:          user.ID,
            Operation:       string(op),
            EstimatedCredits: pre.EstimatedCredits,
            Status:          model.ReservationStatusReserved,
            // ...
        }
        if e := tx.Create(reservationRow).Error; e != nil {
            return e
        }

        // 3. 按 result.Items 写 credit_reservation_item 行
        for _, item := range result.Items {
            itemRow := &model.CreditReservationItem{
                ReservationID: reservationRow.ID,
                SourceType:    strPtr(string(item.SourceType)),
                SourceID:      uintPtr(item.SourceID),
                Amount:        item.Amount,
                // PackageID 为 nil（新路径）
            }
            if e := tx.Create(itemRow).Error; e != nil {
                return e
            }
        }

        reservation = toReservationDTO(reservationRow)
        return nil
    })
    return reservation, err
}
```

### 3.3 Reconcile / refundOneItem dispatch（S2 reviewer P0-2 修正）

**已确认实际签名**（`credit_service.go:846`）：`refundOneItem(ctx, tx, userID uint, item CreditReservationItem, amount int64) → (int64, error)`。

`item` 本身不带 UserID — userID 由 caller 从 parent reservation 传入。本 spec 沿用该签名，新代码读 `userID` 参数：

```go
// credit_service.go:846

func (c *creditsImpl) refundOneItem(ctx context.Context, tx *gorm.DB, userID uint, item model.CreditReservationItem, amount int64) (int64, error) {
    // D6 dispatch：source_type 为 nil 说明是老路径 reservation
    if item.SourceType == nil {
        return c.refundOneItemLegacy(ctx, tx, userID, item, amount)  // 走老路径：写 credit_package
    }
    // 新路径：调 MembershipService.RefundCreditsTx
    refundedTo, refundedID, refundedAmt, err := c.membershipSvc.RefundCreditsTx(
        ctx, tx,
        uint64(userID),
        membership.DeductSource(*item.SourceType),
        *item.SourceID,
        amount,
        time.Now().UTC(),
    )
    if err != nil {
        return 0, fmt.Errorf("refundOneItem new path: %w", err)
    }
    _ = refundedTo  // for audit logging if needed
    _ = refundedID
    return refundedAmt, nil
}
```

### 3.4 CanPerformAIOperation 改造（S0 reviewer P0-1）

```go
// credit.go:56

func (b *creditBiz) CanPerformAIOperation(ctx context.Context, user *model.User, op Operation) error {
    if user.BillingMode == model.BillingModeCredits && b.membershipSvc != nil {
        // credits-mode 用户：通过 MembershipService.GetBalance 判断
        view, err := b.membershipSvc.GetBalance(ctx, uint64(user.ID), time.Now().UTC())
        if err != nil {
            return fmt.Errorf("CanPerformAIOperation: %w", err)
        }
        total := view.TrialRemaining + view.CycleRemaining + view.BoosterUsable
        if total <= 0 {
            return ErrInsufficientCredits
        }
        return nil
    }
    // legacy_tier 用户：保留原 credit_account.balance 读路径
    bal, err := b.ds.Credits().GetBalance(ctx, user.ID)
    if err != nil {
        return fmt.Errorf("CanPerformAIOperation: legacy: %w", err)
    }
    if bal <= 0 {
        return ErrInsufficientCredits
    }
    return nil
}
```

---

## §4 实施顺序 (S3 plan 预览，详 plan 文件)

S3 plan 会拆 **10 个 task**（S2 reviewer P0-1 补 T0 wiring task），按以下顺序：

0. **T0 — creditsImpl 注入 MembershipService**（P0-1 修复）：`NewCreditService` 签名加 `membershipSvc`；`creditsImpl` struct 加字段；router.go / 测试 caller 同步注入
1. **T1 — Migration + Model**：credit_reservation_item 加 source_type / source_id 列 + index
2. **T2 — DeductSource enum + 扩展 DeductionResult**：types.go + 在已有 DeductionResult 加 Items 字段（不破坏旧 callers）
3. **T3 — DeductCreditsTx 实现**：cycle.go 内核，沿用既有锁顺序
4. **T4 — RefundCreditsTx 实现**：含 D2 fallback 链
5. **T5 — DeductCredits wrapper**：保持向后兼容，老签名不变
6. **T6 — creditsImpl.GetBalance 改造 + BalanceBreakdown +TrialRemain 字段**（P2-4）：read path；BalanceBreakdown 加 TrialRemain，CheckAndEstimate/CheckAndEstimateBudget 的 total 求和都加 TrialRemain（P1-2 修复）
7. **T7 — CanPerformAIOperation 改造**：SOP/salesrag 早期 guard，credits-mode 分支
8. **T8 — creditsImpl.Reserve 改造**：tx 内 deduct + item insert（含 source_type/source_id）
9. **T9 — refundOneItem dispatch + STALE-LIE 注释删除 + S5 validation strategy doc**：注释具体改法（P2-3）：删除 `store/credit.go:372-384` 整段 STALE-LIE 注释，加 1 行真实指向新路径的说明

每个 task atomic（独立可编译 / 独立 commit / per-task two-stage review）。

---

## §5 测试策略（详 S3 plan §S5 validation strategy）

### Unit test 必覆盖（go test ./...）

- `TestDeductCreditsTx_TrialOnly`：trial 200 → 扣 100 → trial 100
- `TestDeductCreditsTx_TrialOverflow`：trial 200 → 扣 300 → trial 0 + cycle -100
- `TestDeductCreditsTx_CycleOnly`：no trial → cycle 2000 → 扣 100 → cycle 1900
- `TestDeductCreditsTx_BoosterMultiBatch`：2 个 booster 批次按 expires_at FIFO
- `TestDeductCreditsTx_Insufficient`：total < amount → ErrInsufficientCredits, no partial deduct
- `TestRefundCreditsTx_OriginalActive`：原 source 还 active → 退回原处
- `TestRefundCreditsTx_BoosterExpired_FallbackCycle`：D2 fallback 链
- `TestRefundCreditsTx_AllExpired_Ledger`：写 event 不退
- `TestReserve_AtomicTxFailure`：deduct 成 / reservation insert 失败 → 整体 rollback
- `TestRefundOneItem_LegacyDispatch`：source_type 为 NULL → 走老路径

### Integration test（dev 环境）

- Admin (cycle=2000) 跑 1 个 SOP, 估算 100 → Reserve 后 cycle=1900；Reconcile token 实际 80 → cycle 回补 20 → cycle=1920
- E2E_Booster_Child (trial=200, booster=600) 跑 SOP 估算 250 → trial 200 + booster 50 扣，trial=0, booster=550
- Free 用户跑 SOP → 即返 ErrInsufficientCredits
- Legacy_tier 用户跑 SOP → 走老 path，行为不变

### Regression

- `task test` 全 pass，coverage ≥ 80% on changed packages
- E2E `membership-credits-redesign.spec.ts` Path 1/6 (parent-only) 仍 pass

---

## §6 现有 caller 影响清单

```
$ grep -rln "creditsImpl\|creditService\." internal/numind/biz/ | head
internal/numind/biz/sop/sop.go            ← CreateRun / ExecuteNode 多次调用，预期行为不变
internal/numind/biz/salesrag/salesrag.go  ← acquireSalesragCredits 等，预期行为不变
internal/pkg/aiservice/middleware/context_budget.go ← chatbot middleware，预期行为不变
```

**调用者不改动**：API 签名不变，只是内部分支路由不同。

---

## §7 风险表（与 S1 proposal 同步 + spec 级细化）

| 风险 | 缓解 |
|------|------|
| DeductCreditsTx 内 cycle/booster 行 FOR UPDATE 死锁 | 按固定顺序 lock（先 trial_grant，后 credit_cycle，后 user_booster_balance），所有 caller 遵守同序 |
| Reserve outer tx 持有时间长 | T8 实现确保 estimate 计算在 tx 外完成，tx 内只做 deduct + insert |
| Refund fallback 链导致非预期 cycle 增长 | D2 写 event ledger 含原 source 信息，便于审计与对账 |
| 老 reservation in-flight 与新代码混用 | D6 dispatch 按 source_type IS NULL；老 path 代码保留不变 |
| Legacy_tier 用户被错路由 | 每个改造点严格 `if user.BillingMode == BillingModeCredits` 分支；S5 用 legacy 账号验证 |

---

## §8 范围外（明确不做）

- 不删 credit_package / credit_account / credit_transaction 表（legacy_tier 仍用）
- 不删 `deductCreditsTxFull` / `refundOneItemLegacy`（legacy 分支保留）
- 不改 grant / fulfillOrder / booster 购买路径
- 不改前端代码
- 不清理 parent_grant 死代码
- 不优化 R2 估算逻辑

---

## §9 S3 plan 准备就绪

S3 task plan 会按本 spec §4 顺序拆分，每个 task 独立 atomic：明确验收 / 不依赖未提交代码 / 完成后系统可编译。S5 validation strategy 单独 task（NDF Rule 10）。
