// Shared test helpers for the credit package's *_test.go files.
//
// Originally lived in credit_deduct_tx_test.go alongside the legacy DeductCredits
// chain tests. T6 (credits-cleanup) deleted that file but the helpers
// (newCreditTestDB, seedPackagesAndAccount) are still referenced by several
// *_test.go files in this package.
//
// T6 also added:
//   - membership-table DDL inside newCreditTestDB so credits-mode tests can
//     construct MembershipService and Reserve / Reconcile through the new
//     deduction path (MembershipService.DeductCreditsTx).
//   - seedPackagesAndAccount mirrors each credit_package row into the matching
//     new-tables row (subscription + credit_cycle, trial_grant, or
//     user_booster_balance) so that legacy seed data still exercises the new
//     deduction code.
//   - newCreditServiceWithMembership constructs a fully wired
//     ICreditService (MembershipService injected) for Reserve/Reconcile tests.
package credit_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/membership"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	membershipmodel "numind-server/internal/pkg/model/membership"
	"numind-server/internal/pkg/pricing"
)

// newCreditTestDB creates an isolated in-memory SQLite DB preloaded with the
// minimum set of tables required by the credit_service test suite (no
// credit_reservation tables — those use MySQL ENUM types which SQLite doesn't
// parse; the reserve test file augments the schema separately).
func newCreditTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Use a per-test unique named in-memory DB so that parallel tests do not share
	// schema state. The file: URI with mode=memory&cache=shared ensures all
	// connections within a single test see the same tables (unlike plain ":memory:"
	// where each pooled connection gets its own empty DB). ReplaceAll("/", "_")
	// guards against subtest names containing slashes breaking the SQLite URI.
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	// Force the pool to a single connection for test determinism — also sidesteps
	// SQLite's "database is locked" errors under parallel read/write.
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	// T11: CreditPackage removed from AutoMigrate — credit_package table was dropped.
	require.NoError(t, db.AutoMigrate(
		&model.CreditAccount{},
		&model.CreditTransaction{},
		&model.UsageRecord{},
	), "auto-migrate")

	// T6: add membership tables so MembershipService.DeductCreditsTx (the new
	// authoritative deduction path) has the schema it needs. We hand-roll the
	// DDL (rather than AutoMigrate) so the table shape matches the prod migrations.
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS subscription (
			id                     INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id                INTEGER NOT NULL UNIQUE,
			first_started_at       DATETIME NOT NULL,
			current_started_at     DATETIME NOT NULL,
			expires_at             DATETIME NOT NULL,
			total_months_purchased INTEGER NOT NULL,
			plan_type              TEXT NOT NULL DEFAULT 'monthly',
			cycle_credits          INTEGER NOT NULL DEFAULT 2000,
			source                 TEXT NOT NULL DEFAULT 'b2b_grant',
			granter_user_id        INTEGER,
			created_at             DATETIME NOT NULL,
			updated_at             DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS trial_grant (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id           INTEGER NOT NULL UNIQUE,
			granted_at        DATETIME NOT NULL,
			expires_at        DATETIME NOT NULL,
			credits_remaining INTEGER NOT NULL DEFAULT 200,
			source            TEXT NOT NULL DEFAULT 'b2b_grant',
			granter_user_id   INTEGER,
			created_at        DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS credit_cycle (
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
		`CREATE TABLE IF NOT EXISTS user_booster_balance (
			user_id            INTEGER PRIMARY KEY,
			credits_remaining  INTEGER NOT NULL DEFAULT 0,
			updated_at         DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS membership_event (
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
	}
	for _, stmt := range ddl {
		require.NoError(t, db.Exec(stmt).Error, "exec membership DDL")
	}
	return db
}

// seedPackagesAndAccount inserts a user, credit_account and membership table
// rows representing the given credit packages (trial / subscription / booster).
//
// T11 (credits-cleanup): credit_package table has been dropped. This helper no
// longer seeds credit_package rows. It seeds the three-pool SOT directly:
//   - CreditTypeTrial        → trial_grant
//   - CreditTypeSubscription → subscription + credit_cycle
//   - CreditTypeBooster      → user_booster_balance
//
// A CreditAccount row is still created (identity/status only; balance column dropped).
//
// The pkgs parameter uses model.CreditPackage as a convenient data carrier for
// the seed values (ExpiresAt, ActivatedAt, RemainCredits, Type). The credit_package
// table is NOT written.
func seedPackagesAndAccount(t *testing.T, db *gorm.DB, userID uint, pkgs []seedPackage) {
	t.Helper()

	// Ensure account row exists (balance column dropped in T11).
	acc := model.CreditAccount{UserID: userID, Status: "active"}
	require.NoError(t, db.Create(&acc).Error)

	// Seed membership tables directly (T11: credit_package is gone).
	if hasMembershipTables(db) {
		mirrorPackagesToMembershipTables(t, db, userID, pkgs)
	}
}

// seedPackage is a lightweight data carrier for seeding membership tables in tests.
// It replaces model.CreditPackage as the seed-data type after T11 dropped credit_package.
// TotalCredits is accepted for API compatibility with existing test literals but is
// not written to any table (the new schema doesn't have a "total" column per pool row).
type seedPackage struct {
	Type          string    // trial / subscription / booster  (model.CreditTypeTrial etc.)
	TotalCredits  int64     // ignored — retained for test-literal compatibility only
	RemainCredits int64     // credits_remaining in the pool row
	ActivatedAt   time.Time // subscription cycle start / trial granted_at
	ExpiresAt     time.Time // subscription expires_at / trial expires_at (ignored for booster)
}

// hasMembershipTables sniffs whether the subscription table exists. Used by
// seedPackagesAndAccount to silently skip mirroring on legacy test fixtures
// that predate the membership-tables schema.
func hasMembershipTables(db *gorm.DB) bool {
	var n int64
	err := db.Raw(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='subscription'`).Scan(&n).Error
	return err == nil && n > 0
}

// mirrorPackagesToMembershipTables seeds membership-table rows from seedPackage data
// so MembershipService.DeductCreditsTx can debit them. The aggregation rules:
//
//   - All subscription packages collapse into one `subscription` row plus one
//     `credit_cycle` row (windowed by the earliest ActivatedAt → latest ExpiresAt).
//     credits_remaining = sum of subscription RemainCredits.
//   - All trial packages collapse into one `trial_grant` row (credits_remaining =
//     sum of trial RemainCredits, expires_at = max ExpiresAt among trial).
//   - All booster packages collapse into one `user_booster_balance` row
//     (credits_remaining = sum of booster RemainCredits).
//
// Aggregation matches the new-schema invariant (one row per (user, pool type)).
// T11: parameter type changed from []model.CreditPackage to []seedPackage.
func mirrorPackagesToMembershipTables(t *testing.T, db *gorm.DB, userID uint, pkgs []seedPackage) {
	t.Helper()
	var (
		subRemain, trialRemain, boosterRemain int64
		subStart, subEnd                      time.Time
		subMonths                             int
		trialExpires                          time.Time
		hasSub, hasTrial, hasBooster          bool
	)
	for _, p := range pkgs {
		switch p.Type {
		case model.CreditTypeSubscription:
			subRemain += p.RemainCredits
			if !hasSub || p.ActivatedAt.Before(subStart) {
				subStart = p.ActivatedAt
			}
			if !hasSub || p.ExpiresAt.After(subEnd) {
				subEnd = p.ExpiresAt
			}
			subMonths++
			hasSub = true
		case model.CreditTypeTrial:
			trialRemain += p.RemainCredits
			if !hasTrial || p.ExpiresAt.After(trialExpires) {
				trialExpires = p.ExpiresAt
			}
			hasTrial = true
		case model.CreditTypeBooster:
			boosterRemain += p.RemainCredits
			hasBooster = true
		}
	}

	now := time.Now()

	if hasSub {
		sub := membershipmodel.Subscription{
			UserID:               uint64(userID),
			FirstStartedAt:       subStart,
			CurrentStartedAt:     subStart,
			ExpiresAt:            subEnd,
			TotalMonthsPurchased: subMonths,
			Source:               membershipmodel.SourceB2BGrant,
			CreatedAt:            now,
			UpdatedAt:            now,
		}
		require.NoError(t, db.Create(&sub).Error)

		cycle := membershipmodel.CreditCycle{
			UserID:           uint64(userID),
			SubscriptionID:   sub.ID,
			CycleStart:       subStart,
			CycleEnd:         subEnd,
			CreditsGranted:   int(subRemain), //nolint:gosec // test fixture: int64→int narrows safely
			CreditsRemaining: int(subRemain), //nolint:gosec // ditto
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		require.NoError(t, db.Create(&cycle).Error)
	}

	if hasTrial {
		tg := membershipmodel.TrialGrant{
			UserID:           uint64(userID),
			GrantedAt:        now.Add(-time.Hour),
			ExpiresAt:        trialExpires,
			CreditsRemaining: int(trialRemain), //nolint:gosec // test fixture: int64→int narrows safely
			Source:           membershipmodel.SourceB2BGrant,
			CreatedAt:        now,
		}
		require.NoError(t, db.Create(&tg).Error)
	}

	if hasBooster {
		ubb := membershipmodel.UserBoosterBalance{
			UserID:           uint64(userID),
			CreditsRemaining: boosterRemain,
			UpdatedAt:        now,
		}
		require.NoError(t, db.Create(&ubb).Error)
	}
}

// newCreditServiceWithMembership constructs an ICreditService with the
// MembershipService wired in (so Reserve/Reconcile reach the new authoritative
// deduction path). Tests that exercise legacy_tier-only paths can keep using
// NewCreditService(..., nil); tests that reach Reserve must call this helper.
func newCreditServiceWithMembership(ds store.IStore, db *gorm.DB, pc pricing.ICalculator) credit.ICreditService {
	msvc := membership.NewMembershipService(db)
	return credit.NewCreditService(ds, credit.NewCreditBiz(ds), pc, msvc)
}
