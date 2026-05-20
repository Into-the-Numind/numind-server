package budget

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// AdminTestConsumer governs the credit_admin_test_grant pool used by parent
// users in Agent Builder Modal "试聊" path.
//
// Implementation notes:
//   - Lazy-create grant row on first Consume per parent per month.
//   - Concurrent Consume of same parent serializes on uq_parent_period UNIQUE KEY
//   - explicit SELECT ... FOR UPDATE row lock.
//   - Refund never goes below used_amount=0 (cap to current used_amount).
//   - All writes go through a single tx — Consume / Refund are atomic.
//
// TODO(#14): migrate to IAdminTestGrantStore in store/ layer to follow project
// convention; v1 directly walks DB.Tx for simplicity.
type AdminTestConsumer interface {
	// Consume reserves `amount` from the parent's current-month grant.
	// Returns ErrAdminTestExhausted when remaining < amount.
	Consume(ctx context.Context, parentUserID uint, amount int64) (txID uint64, err error)

	// Refund decreases used_amount for the given parent / txID.
	// refundAmount is capped to current used_amount (never below 0).
	Refund(ctx context.Context, parentUserID uint, txID uint64, refundAmount int64) error

	// Status returns the current-month grant state.
	// Returns default (Granted=5000, Used=0) if no grant row exists yet.
	Status(ctx context.Context, parentUserID uint, now time.Time) (*AdminTestStatus, error)
}

type adminTestConsumer struct {
	s store.IStore
}

// NewAdminTestConsumer constructs a GORM-backed AdminTestConsumer.
func NewAdminTestConsumer(s store.IStore) AdminTestConsumer {
	return &adminTestConsumer{s: s}
}

func (c *adminTestConsumer) Consume(ctx context.Context, parentUserID uint, amount int64) (uint64, error) {
	if amount <= 0 {
		return 0, fmt.Errorf("AdminTestConsumer.Consume: amount must be > 0, got %d", amount)
	}
	var txID uint64
	err := c.s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		periodStart, periodEnd := currentMonthBoundaries(time.Now().UTC())

		// 1. Lazy-create grant row (idempotent via uq_parent_period)
		grant := &model.CreditAdminTestGrant{
			ParentUserID:  parentUserID,
			GrantedAmount: DefaultAdminTestGrant,
			UsedAmount:    0,
			PeriodStart:   periodStart,
			PeriodEnd:     periodEnd,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "parent_user_id"}, {Name: "period_start"}},
			DoNothing: true,
		}).Create(grant).Error; err != nil {
			return fmt.Errorf("AdminTestConsumer.Consume: create grant: %w", err)
		}

		// 2. Lock and re-fetch the (possibly pre-existing) row
		var locked model.CreditAdminTestGrant
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("parent_user_id = ? AND period_start = ?", parentUserID, periodStart).
			First(&locked).Error; err != nil {
			return fmt.Errorf("AdminTestConsumer.Consume: lock grant: %w", err)
		}

		// 3. Check remaining
		if locked.Remaining() < amount {
			return ErrAdminTestExhausted
		}

		// 4. Update used_amount + last_used_at
		now := time.Now()
		newUsed := uint32(int64(locked.UsedAmount) + amount)
		if err := tx.Model(&locked).Updates(map[string]any{
			"used_amount":  newUsed,
			"last_used_at": now,
		}).Error; err != nil {
			return fmt.Errorf("AdminTestConsumer.Consume: update grant: %w", err)
		}

		// 5. INSERT credit_transaction (source_type='admin_test')
		sourceType := "admin_test"
		rec := &model.CreditTransaction{
			UserID:     parentUserID,
			Amount:     -amount,
			SourceType: &sourceType,
			SourceID:   nil, // admin_test 池没有外键 ID
			Operation:  "agent_test_reserve",
			BizRefType: "admin_test",
			BizRefID:   fmt.Sprintf("grant_%d", locked.ID),
		}
		if err := tx.Create(rec).Error; err != nil {
			return fmt.Errorf("AdminTestConsumer.Consume: insert tx: %w", err)
		}
		txID = rec.ID
		return nil
	})
	return txID, err
}

func (c *adminTestConsumer) Refund(ctx context.Context, parentUserID uint, txID uint64, refundAmount int64) error {
	if refundAmount <= 0 {
		return nil // nothing to refund
	}
	return c.s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Fetch original tx
		var orig model.CreditTransaction
		if err := tx.Where("id = ?", txID).First(&orig).Error; err != nil {
			return fmt.Errorf("AdminTestConsumer.Refund: fetch tx %d: %w", txID, err)
		}
		if orig.UserID != parentUserID {
			return fmt.Errorf("AdminTestConsumer.Refund: tx user mismatch (expected %d, got %d)", parentUserID, orig.UserID)
		}
		if orig.SourceType == nil || *orig.SourceType != "admin_test" {
			return fmt.Errorf("AdminTestConsumer.Refund: tx source_type not admin_test")
		}

		// 2. Lock current-month grant
		periodStart, _ := currentMonthBoundaries(time.Now().UTC())
		var grant model.CreditAdminTestGrant
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("parent_user_id = ? AND period_start = ?", parentUserID, periodStart).
			First(&grant).Error; err != nil {
			return fmt.Errorf("AdminTestConsumer.Refund: lock grant: %w", err)
		}

		// 3. Cap refund to current used_amount
		cap := int64(grant.UsedAmount)
		if refundAmount > cap {
			refundAmount = cap
		}
		if refundAmount <= 0 {
			return nil // nothing to refund (capped to 0)
		}

		// 4. Update used_amount
		newUsed := uint32(int64(grant.UsedAmount) - refundAmount)
		if err := tx.Model(&grant).Update("used_amount", newUsed).Error; err != nil {
			return fmt.Errorf("AdminTestConsumer.Refund: update grant: %w", err)
		}

		// 5. INSERT credit_transaction (+refund)
		sourceType := "admin_test"
		rec := &model.CreditTransaction{
			UserID:     parentUserID,
			Amount:     refundAmount,
			SourceType: &sourceType,
			SourceID:   nil,
			Operation:  "agent_test_refund",
			BizRefType: "admin_test",
			BizRefID:   fmt.Sprintf("tx_%d", txID),
		}
		return tx.Create(rec).Error
	})
}

func (c *adminTestConsumer) Status(ctx context.Context, parentUserID uint, now time.Time) (*AdminTestStatus, error) {
	periodStart, periodEnd := currentMonthBoundaries(now.UTC())
	var grant model.CreditAdminTestGrant
	err := c.s.DB().WithContext(ctx).
		Where("parent_user_id = ? AND period_start = ?", parentUserID, periodStart).
		First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// No grant yet this month — treat as freshly granted defaults
		return &AdminTestStatus{
			Granted:      DefaultAdminTestGrantInt64,
			Used:         0,
			Remaining:    DefaultAdminTestGrantInt64,
			PeriodStart:  periodStart,
			PeriodEnd:    periodEnd,
			DaysToExpire: daysUntil(periodEnd, now),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("AdminTestConsumer.Status: %w", err)
	}
	return &AdminTestStatus{
		Granted:      int64(grant.GrantedAmount),
		Used:         int64(grant.UsedAmount),
		Remaining:    grant.Remaining(),
		PeriodStart:  grant.PeriodStart,
		PeriodEnd:    grant.PeriodEnd,
		DaysToExpire: daysUntil(grant.PeriodEnd, now),
	}, nil
}

// currentMonthBoundaries returns the first and last day of the month containing
// now (UTC). Both are time.Date values with H=M=S=0.
func currentMonthBoundaries(now time.Time) (start, end time.Time) {
	y, m, _ := now.Date()
	start = time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
	end = start.AddDate(0, 1, -1) // last day of this month
	return
}

// daysUntil returns the number of days between now and target (rounded down).
// Returns 0 if target < now.
func daysUntil(target, now time.Time) int {
	d := target.Sub(now) / (24 * time.Hour)
	if d < 0 {
		return 0
	}
	return int(d)
}
