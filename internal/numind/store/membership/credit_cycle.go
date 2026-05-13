package membership

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	model "numind-server/internal/pkg/model/membership"
)

type creditCycleStore struct {
	db *gorm.DB
}

var _ ICreditCycleStore = (*creditCycleStore)(nil)

// NewCreditCycleStore constructs an ICreditCycleStore backed by db.
func NewCreditCycleStore(db *gorm.DB) ICreditCycleStore {
	return &creditCycleStore{db: db}
}

// GetByUserAndStart returns the credit cycle for (userID, cycleStart), or (nil, nil).
func (s *creditCycleStore) GetByUserAndStart(ctx context.Context, tx *gorm.DB, userID uint64, cycleStart time.Time) (*model.CreditCycle, error) {
	var cycle model.CreditCycle
	err := tx.WithContext(ctx).
		Where("user_id = ? AND cycle_start = ?", userID, cycleStart).
		Take(&cycle).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("credit_cycle.GetByUserAndStart: %w", err)
	}
	return &cycle, nil
}

// GetByUserAndStartForUpdate is identical to GetByUserAndStart but runs
// SELECT FOR UPDATE inside tx.
func (s *creditCycleStore) GetByUserAndStartForUpdate(ctx context.Context, tx *gorm.DB, userID uint64, cycleStart time.Time) (*model.CreditCycle, error) {
	var cycle model.CreditCycle
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ? AND cycle_start = ?", userID, cycleStart).
		Take(&cycle).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("credit_cycle.GetByUserAndStartForUpdate: %w", err)
	}
	return &cycle, nil
}

// InsertOrIgnore inserts cycle; silently does nothing if (user_id, cycle_start)
// already exists (ON CONFLICT DO NOTHING). This provides idempotent cycle
// initialization.
func (s *creditCycleStore) InsertOrIgnore(ctx context.Context, tx *gorm.DB, cycle *model.CreditCycle) error {
	err := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "cycle_start"}},
		DoNothing: true,
	}).Create(cycle).Error
	if err != nil {
		return fmt.Errorf("credit_cycle.InsertOrIgnore: %w", err)
	}
	return nil
}

// Update saves all fields of cycle inside tx using Save (full update).
func (s *creditCycleStore) Update(ctx context.Context, tx *gorm.DB, cycle *model.CreditCycle) error {
	if err := tx.WithContext(ctx).Save(cycle).Error; err != nil {
		return fmt.Errorf("credit_cycle.Update: %w", err)
	}
	return nil
}

// DeleteExpired removes all credit cycles for userID whose cycle_end is before `before`.
func (s *creditCycleStore) DeleteExpired(ctx context.Context, tx *gorm.DB, userID uint64, before time.Time) error {
	err := tx.WithContext(ctx).
		Where("user_id = ? AND cycle_end < ?", userID, before).
		Delete(&model.CreditCycle{}).Error
	if err != nil {
		return fmt.Errorf("credit_cycle.DeleteExpired: %w", err)
	}
	return nil
}
