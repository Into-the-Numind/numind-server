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

type trialGrantStore struct {
	db *gorm.DB
}

var _ ITrialGrantStore = (*trialGrantStore)(nil)

// NewTrialGrantStore constructs an ITrialGrantStore backed by db.
func NewTrialGrantStore(db *gorm.DB) ITrialGrantStore {
	return &trialGrantStore{db: db}
}

// Get returns the trial grant for userID, or (nil, nil) if not found.
func (s *trialGrantStore) Get(ctx context.Context, userID uint64) (*model.TrialGrant, error) {
	var tg model.TrialGrant
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).Take(&tg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("trial_grant.Get: %w", err)
	}
	return &tg, nil
}

// GetForUpdate returns the trial grant for userID using SELECT FOR UPDATE inside tx.
// Returns (nil, nil) if not found.
func (s *trialGrantStore) GetForUpdate(ctx context.Context, tx *gorm.DB, userID uint64) (*model.TrialGrant, error) {
	var tg model.TrialGrant
	err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ?", userID).Take(&tg).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("trial_grant.GetForUpdate: %w", err)
	}
	return &tg, nil
}

// Create inserts a new trial grant inside tx. The DB UNIQUE constraint on
// user_id will surface as an error (MySQL error 1062); biz layer converts this
// to ErrTrialAlreadyGranted.
func (s *trialGrantStore) Create(ctx context.Context, tx *gorm.DB, tg *model.TrialGrant) error {
	if err := tx.WithContext(ctx).Create(tg).Error; err != nil {
		return fmt.Errorf("trial_grant.Create: %w", err)
	}
	return nil
}

// Update saves all changed fields of tg inside tx using Save (full update).
func (s *trialGrantStore) Update(ctx context.Context, tx *gorm.DB, tg *model.TrialGrant) error {
	if err := tx.WithContext(ctx).Save(tg).Error; err != nil {
		return fmt.Errorf("trial_grant.Update: %w", err)
	}
	return nil
}

// HasActive reports whether userID has a trial grant that has not yet expired at now.
func (s *trialGrantStore) HasActive(ctx context.Context, userID uint64, now time.Time) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&model.TrialGrant{}).
		Where("user_id = ? AND expires_at > ?", userID, now).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("trial_grant.HasActive: %w", err)
	}
	return count > 0, nil
}
