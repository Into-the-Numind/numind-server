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

type userBoosterBalanceStore struct {
	db *gorm.DB
}

var _ IUserBoosterBalanceStore = (*userBoosterBalanceStore)(nil)

// NewUserBoosterBalanceStore constructs an IUserBoosterBalanceStore backed by db.
func NewUserBoosterBalanceStore(db *gorm.DB) IUserBoosterBalanceStore {
	return &userBoosterBalanceStore{db: db}
}

// Get returns the booster balance for userID, or (nil, nil) if not found.
func (s *userBoosterBalanceStore) Get(ctx context.Context, userID uint64) (*model.UserBoosterBalance, error) {
	var bal model.UserBoosterBalance
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&bal).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("booster.Get: %w", err)
	}
	return &bal, nil
}

// GetForUpdate returns the booster balance using SELECT FOR UPDATE inside tx.
// Returns (nil, nil) if not found.
func (s *userBoosterBalanceStore) GetForUpdate(ctx context.Context, tx *gorm.DB, userID uint64) (*model.UserBoosterBalance, error) {
	var bal model.UserBoosterBalance
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ?", userID).Take(&bal).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("booster.GetForUpdate: %w", err)
	}
	return &bal, nil
}

// Increment adds delta to credits_remaining for userID via an upsert.
// If the row does not exist it is created with credits_remaining = delta.
// Concurrent Increment calls on the same userID are serialised at the DB level.
func (s *userBoosterBalanceStore) Increment(ctx context.Context, tx *gorm.DB, userID uint64, delta int64) error {
	now := time.Now().UTC()
	err := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"credits_remaining": gorm.Expr("credits_remaining + ?", delta),
			"updated_at":        now,
		}),
	}).Create(&model.UserBoosterBalance{
		UserID:           userID,
		CreditsRemaining: delta,
		UpdatedAt:        now,
	}).Error
	if err != nil {
		return fmt.Errorf("booster.Increment: %w", err)
	}
	return nil
}

// Decrement subtracts delta from credits_remaining for userID.
// The biz layer is responsible for verifying sufficient balance before calling.
// Returns an error if the row does not exist.
func (s *userBoosterBalanceStore) Decrement(ctx context.Context, tx *gorm.DB, userID uint64, delta int64) error {
	res := tx.WithContext(ctx).Model(&model.UserBoosterBalance{}).
		Where("user_id = ?", userID).
		Updates(map[string]any{
			"credits_remaining": gorm.Expr("credits_remaining - ?", delta),
			"updated_at":        time.Now().UTC(),
		})
	if res.Error != nil {
		return fmt.Errorf("booster.Decrement: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("booster.Decrement: no row found for user_id=%d", userID)
	}
	return nil
}
