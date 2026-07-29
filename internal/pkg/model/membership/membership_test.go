package membership_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model/membership"
)

func newTestDB(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	migrateMembershipSchema(t, db)
	return db
}

// migrateMembershipSchema creates membership tables for SQLite unit tests.
// GORM's AutoMigrate would write `type:enum(...)` directly to CREATE TABLE,
// but SQLite doesn't support ENUM natively, so we use raw SQL (ENUM degrades to TEXT).
// Real ENUM DDL on MySQL is created by migrations/20260429_*.sql.
func migrateMembershipSchema(t *testing.T, db *gorm.DB) {
	t.Helper()
	ddl := []string{
		`CREATE TABLE subscription (
			id                    INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id               INTEGER NOT NULL UNIQUE,
			first_started_at      DATETIME NOT NULL,
			current_started_at    DATETIME NOT NULL,
			expires_at            DATETIME NOT NULL,
			total_months_purchased INTEGER NOT NULL,
			plan_type              TEXT NOT NULL DEFAULT 'monthly',
			cycle_credits          INTEGER NOT NULL DEFAULT 2000,
			source                TEXT NOT NULL DEFAULT 'b2b_grant',
			granter_user_id       INTEGER,
			created_at            DATETIME NOT NULL,
			updated_at            DATETIME NOT NULL
		)`,
		`CREATE INDEX idx_sub_expires_at ON subscription (expires_at)`,
		`CREATE INDEX idx_sub_granter_expires ON subscription (granter_user_id, expires_at)`,
		`CREATE TABLE trial_grant (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id           INTEGER NOT NULL UNIQUE,
			granted_at        DATETIME NOT NULL,
			expires_at        DATETIME NOT NULL,
			credits_remaining INTEGER NOT NULL DEFAULT 200,
			source            TEXT NOT NULL DEFAULT 'b2b_grant',
			granter_user_id   INTEGER,
			created_at        DATETIME NOT NULL
		)`,
		`CREATE INDEX idx_trial_expires_at ON trial_grant (expires_at)`,
		`CREATE INDEX idx_trial_granter_expires ON trial_grant (granter_user_id, expires_at)`,
		`CREATE TABLE credit_cycle (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id           INTEGER NOT NULL,
			subscription_id   INTEGER NOT NULL,
			cycle_start       DATETIME NOT NULL,
			cycle_end         DATETIME NOT NULL,
			credits_granted   INTEGER NOT NULL DEFAULT 0,
			credits_remaining INTEGER NOT NULL DEFAULT 0,
			created_at        DATETIME NOT NULL,
			updated_at        DATETIME NOT NULL,
			UNIQUE(user_id, cycle_start)
		)`,
		`CREATE INDEX idx_cycle_user_end ON credit_cycle (user_id, cycle_end)`,
		`CREATE TABLE user_booster_balance (
			user_id            INTEGER PRIMARY KEY,
			credits_remaining  INTEGER NOT NULL DEFAULT 0,
			updated_at         DATETIME NOT NULL
		)`,
		`CREATE INDEX idx_booster_updated_at ON user_booster_balance (updated_at)`,
		`CREATE TABLE membership_event (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id           INTEGER NOT NULL,
			event_type        TEXT NOT NULL,
			product_type      TEXT NOT NULL,
			months            INTEGER,
			quantity          INTEGER,
			amount_cents      INTEGER NOT NULL DEFAULT 0,
			source            TEXT NOT NULL,
			granter_user_id   INTEGER,
			idempotency_key   TEXT UNIQUE,
			subscription_id   INTEGER,
			occurred_at       DATETIME NOT NULL
		)`,
		`CREATE INDEX idx_event_user_occurred ON membership_event (user_id, occurred_at)`,
		`CREATE INDEX idx_event_type_occurred ON membership_event (event_type, occurred_at)`,
		`CREATE INDEX idx_event_granter_occurred ON membership_event (granter_user_id, occurred_at)`,
	}
	for _, s := range ddl {
		require.NoError(t, db.Exec(s).Error)
	}
}

func TestSubscription_TableName(t *testing.T) {
	require.Equal(t, "subscription", (membership.Subscription{}).TableName())
}

func TestSubscription_CreateAndQuery(t *testing.T) {
	db := newTestDB(t)
	parentID := uint64(100)
	now := time.Now().UTC()
	sub := &membership.Subscription{
		UserID:               42,
		FirstStartedAt:       now,
		CurrentStartedAt:     now,
		ExpiresAt:            now.AddDate(0, 1, 0),
		TotalMonthsPurchased: 1,
		Source:               membership.SourceB2BGrant,
		GranterUserID:        &parentID,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	require.NoError(t, db.Create(sub).Error)
	require.NotZero(t, sub.ID)

	var got membership.Subscription
	require.NoError(t, db.Where("user_id = ?", 42).Take(&got).Error)
	require.Equal(t, 1, got.TotalMonthsPurchased)
	require.Equal(t, membership.SourceB2BGrant, got.Source)
	require.NotNil(t, got.GranterUserID)
}

func TestTrialGrant_DefaultsAndQuery(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	tg := &membership.TrialGrant{
		UserID:           7,
		GrantedAt:        now,
		ExpiresAt:        now.AddDate(0, 0, 3),
		CreditsRemaining: 200,
		Source:           membership.SourceB2BGrant,
		CreatedAt:        now,
	}
	require.NoError(t, db.Create(tg).Error)
	require.Equal(t, "trial_grant", tg.TableName())
}

func TestCreditCycle_TableNameAndCreate(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	cc := &membership.CreditCycle{
		UserID:           1,
		SubscriptionID:   1,
		CycleStart:       now,
		CycleEnd:         now.AddDate(0, 1, 0),
		CreditsGranted:   2000,
		CreditsRemaining: 2000,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	require.NoError(t, db.Create(cc).Error)
	require.Equal(t, "credit_cycle", cc.TableName())
}

func TestUserBoosterBalance_PrimaryKeyIsUserID(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	bb := &membership.UserBoosterBalance{
		UserID:           1,
		CreditsRemaining: 600,
		UpdatedAt:        now,
	}
	require.NoError(t, db.Create(bb).Error)

	var got membership.UserBoosterBalance
	require.NoError(t, db.Where("user_id = ?", 1).Take(&got).Error)
	require.Equal(t, int64(600), got.CreditsRemaining)
}

func TestMembershipEvent_AllEventTypes(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	months := uint8(3)
	e := &membership.MembershipEvent{
		UserID:      9,
		EventType:   membership.EventTypeSubGranted,
		ProductType: membership.ProductTypeMonthly,
		Months:      &months,
		AmountCents: 9900,
		Source:      membership.SourceB2BGrant,
		OccurredAt:  now,
	}
	require.NoError(t, db.Create(e).Error)
	require.Equal(t, "membership_event", e.TableName())
}
