// Package membership provides data-access interfaces and implementations for
// the membership/credits subsystem: subscriptions, trial grants, credit cycles,
// booster balances, and membership audit events.
package membership

import (
	"context"
	"time"

	"gorm.io/gorm"

	model "numind-server/internal/pkg/model/membership"
)

// IMembershipStore aggregates all membership-related store interfaces.
// Callers obtain a single gateway via store.IStore.Membership() rather than
// constructing each sub-store individually.
type IMembershipStore interface {
	Subscriptions() ISubscriptionStore
	TrialGrants() ITrialGrantStore
	CreditCycles() ICreditCycleStore
	BoosterBalances() IUserBoosterBalanceStore
	Events() IMembershipEventStore
}

// ISubscriptionStore manages user subscription rows (one per user, replaced on
// renewal/extension).
type ISubscriptionStore interface {
	// Get returns the subscription for userID, or (nil, nil) if not found.
	Get(ctx context.Context, userID uint64) (*model.Subscription, error)
	// GetForUpdate is identical to Get but runs SELECT FOR UPDATE inside tx.
	GetForUpdate(ctx context.Context, tx *gorm.DB, userID uint64) (*model.Subscription, error)
	// Create inserts a new subscription row inside tx.
	Create(ctx context.Context, tx *gorm.DB, sub *model.Subscription) error
	// Update saves all fields of sub inside tx.
	Update(ctx context.Context, tx *gorm.DB, sub *model.Subscription) error
	// HasActive reports whether userID has an unexpired subscription at now.
	HasActive(ctx context.Context, userID uint64, now time.Time) (bool, error)
}

// ITrialGrantStore manages trial grant rows (one per user, immutable after
// create; biz layer manages credits_remaining via Update).
type ITrialGrantStore interface {
	// Get returns the trial grant for userID, or (nil, nil) if not found.
	Get(ctx context.Context, userID uint64) (*model.TrialGrant, error)
	// GetForUpdate is identical to Get but runs SELECT FOR UPDATE inside tx.
	GetForUpdate(ctx context.Context, tx *gorm.DB, userID uint64) (*model.TrialGrant, error)
	// Create inserts a new trial grant inside tx. UNIQUE on user_id — callers
	// should check for duplicate error (MySQL 1062) and convert to
	// ErrTrialAlreadyGranted at the biz layer.
	Create(ctx context.Context, tx *gorm.DB, tg *model.TrialGrant) error
	// Update saves all fields of tg inside tx.
	Update(ctx context.Context, tx *gorm.DB, tg *model.TrialGrant) error
	// HasActive reports whether userID has a trial grant that has not yet expired.
	HasActive(ctx context.Context, userID uint64, now time.Time) (bool, error)
}

// ICreditCycleStore manages monthly credit cycle rows (one per user per
// billing cycle). The composite UNIQUE KEY (user_id, cycle_start) is
// enforced at the DB level; InsertOrIgnore leverages this for idempotency.
type ICreditCycleStore interface {
	// GetByUserAndStart returns the cycle for (userID, cycleStart), or (nil, nil).
	GetByUserAndStart(ctx context.Context, tx *gorm.DB, userID uint64, cycleStart time.Time) (*model.CreditCycle, error)
	// GetByUserAndStartForUpdate is identical but runs SELECT FOR UPDATE inside tx.
	GetByUserAndStartForUpdate(ctx context.Context, tx *gorm.DB, userID uint64, cycleStart time.Time) (*model.CreditCycle, error)
	// InsertOrIgnore inserts cycle; silently does nothing if (user_id, cycle_start)
	// already exists (ON CONFLICT DO NOTHING).
	InsertOrIgnore(ctx context.Context, tx *gorm.DB, cycle *model.CreditCycle) error
	// Update saves all fields of cycle inside tx.
	Update(ctx context.Context, tx *gorm.DB, cycle *model.CreditCycle) error
	// DeleteExpired removes all cycles for userID whose cycle_end is before `before`.
	DeleteExpired(ctx context.Context, tx *gorm.DB, userID uint64, before time.Time) error
}

// IUserBoosterBalanceStore manages a single booster-credit balance row per user
// (composite upsert pattern).
type IUserBoosterBalanceStore interface {
	// Get returns the balance row for userID, or (nil, nil) if not found.
	Get(ctx context.Context, userID uint64) (*model.UserBoosterBalance, error)
	// GetForUpdate is identical but runs SELECT FOR UPDATE inside tx.
	GetForUpdate(ctx context.Context, tx *gorm.DB, userID uint64) (*model.UserBoosterBalance, error)
	// Increment adds delta to credits_remaining; creates the row if it does not exist.
	Increment(ctx context.Context, tx *gorm.DB, userID uint64, delta int64) error
	// Decrement subtracts delta from credits_remaining (no floor guard at store
	// level — biz layer must check balance before calling).
	Decrement(ctx context.Context, tx *gorm.DB, userID uint64, delta int64) error
}

// IMembershipEventStore is an append-only audit log for membership lifecycle
// events. The idempotency_key UNIQUE index provides at-most-once semantics.
type IMembershipEventStore interface {
	// Create inserts a new event inside tx. UNIQUE on idempotency_key provides
	// DB-level deduplication; callers should check for duplicate errors.
	Create(ctx context.Context, tx *gorm.DB, event *model.MembershipEvent) error
	// GetByIdempotencyKey returns the event with the given key, or (nil, nil).
	GetByIdempotencyKey(ctx context.Context, key string) (*model.MembershipEvent, error)
	// QueryByGranterAndMonth returns all events attributed to granterID whose
	// occurred_at falls in the half-open interval [monthStart, monthEnd).
	QueryByGranterAndMonth(ctx context.Context, granterID uint64, monthStart, monthEnd time.Time) ([]model.MembershipEvent, error)
}

// ============================================================
// Aggregate store
// ============================================================

// membershipStore is the concrete aggregate satisfying IMembershipStore.
type membershipStore struct {
	db *gorm.DB
}

// NewMembershipStore constructs an IMembershipStore backed by db.
func NewMembershipStore(db *gorm.DB) IMembershipStore {
	return &membershipStore{db: db}
}

func (m *membershipStore) Subscriptions() ISubscriptionStore {
	return NewSubscriptionStore(m.db)
}

func (m *membershipStore) TrialGrants() ITrialGrantStore {
	return NewTrialGrantStore(m.db)
}

func (m *membershipStore) CreditCycles() ICreditCycleStore {
	return NewCreditCycleStore(m.db)
}

func (m *membershipStore) BoosterBalances() IUserBoosterBalanceStore {
	return NewUserBoosterBalanceStore(m.db)
}

func (m *membershipStore) Events() IMembershipEventStore {
	return NewMembershipEventStore(m.db)
}
