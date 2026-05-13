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

type subscriptionStore struct {
	db *gorm.DB
}

var _ ISubscriptionStore = (*subscriptionStore)(nil)

// NewSubscriptionStore constructs an ISubscriptionStore backed by db.
func NewSubscriptionStore(db *gorm.DB) ISubscriptionStore {
	return &subscriptionStore{db: db}
}

// Get returns the subscription for userID, or (nil, nil) if not found.
func (s *subscriptionStore) Get(ctx context.Context, userID uint64) (*model.Subscription, error) {
	var sub model.Subscription
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("subscription.Get: %w", err)
	}
	return &sub, nil
}

// GetForUpdate returns the subscription for userID using SELECT FOR UPDATE inside tx.
// Returns (nil, nil) if not found.
func (s *subscriptionStore) GetForUpdate(ctx context.Context, tx *gorm.DB, userID uint64) (*model.Subscription, error) {
	var sub model.Subscription
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ?", userID).Take(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("subscription.GetForUpdate: %w", err)
	}
	return &sub, nil
}

// Create inserts a new subscription row inside tx.
func (s *subscriptionStore) Create(ctx context.Context, tx *gorm.DB, sub *model.Subscription) error {
	if err := tx.WithContext(ctx).Create(sub).Error; err != nil {
		return fmt.Errorf("subscription.Create: %w", err)
	}
	return nil
}

// Update saves all changed fields of sub inside tx using Save (full update).
func (s *subscriptionStore) Update(ctx context.Context, tx *gorm.DB, sub *model.Subscription) error {
	if err := tx.WithContext(ctx).Save(sub).Error; err != nil {
		return fmt.Errorf("subscription.Update: %w", err)
	}
	return nil
}

// HasActive reports whether userID has an unexpired subscription at now.
func (s *subscriptionStore) HasActive(ctx context.Context, userID uint64, now time.Time) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.Subscription{}).
		Where("user_id = ? AND expires_at > ?", userID, now).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("subscription.HasActive: %w", err)
	}
	return count > 0, nil
}
