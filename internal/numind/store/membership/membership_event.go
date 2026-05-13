package membership

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	model "numind-server/internal/pkg/model/membership"
)

type membershipEventStore struct {
	db *gorm.DB
}

var _ IMembershipEventStore = (*membershipEventStore)(nil)

// NewMembershipEventStore constructs an IMembershipEventStore backed by db.
func NewMembershipEventStore(db *gorm.DB) IMembershipEventStore {
	return &membershipEventStore{db: db}
}

// Create inserts a new membership event inside tx. The DB UNIQUE constraint on
// idempotency_key provides at-most-once semantics; callers should treat a
// duplicate-key error as the event already having been recorded.
func (s *membershipEventStore) Create(ctx context.Context, tx *gorm.DB, event *model.MembershipEvent) error {
	if err := tx.WithContext(ctx).Create(event).Error; err != nil {
		return fmt.Errorf("membership_event.Create: %w", err)
	}
	return nil
}

// GetByIdempotencyKey returns the event with the given idempotency key, or
// (nil, nil) if not found.
func (s *membershipEventStore) GetByIdempotencyKey(ctx context.Context, key string) (*model.MembershipEvent, error) {
	var event model.MembershipEvent
	err := s.db.WithContext(ctx).Where("idempotency_key = ?", key).Take(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("membership_event.GetByIdempotencyKey: %w", err)
	}
	return &event, nil
}

// QueryByGranterAndMonth returns all events attributed to granterID whose
// occurred_at falls in the half-open interval [monthStart, monthEnd).
// Used for B2B monthly billing reports.
func (s *membershipEventStore) QueryByGranterAndMonth(ctx context.Context, granterID uint64, monthStart, monthEnd time.Time) ([]model.MembershipEvent, error) {
	var events []model.MembershipEvent
	err := s.db.WithContext(ctx).
		Where("granter_user_id = ? AND occurred_at >= ? AND occurred_at < ?", granterID, monthStart, monthEnd).
		Order("occurred_at ASC").
		Find(&events).Error
	if err != nil {
		return nil, fmt.Errorf("membership_event.QueryByGranterAndMonth: %w", err)
	}
	return events, nil
}
