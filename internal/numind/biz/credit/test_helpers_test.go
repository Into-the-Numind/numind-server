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

	require.NoError(t, db.AutoMigrate(
		&model.CreditAccount{},
		&model.CreditPackage{},
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

// seedPackagesAndAccount inserts a user, credit_account and a set of active
// credit_packages. Packages should be provided in FIFO order (earliest
// ExpiresAt first). Total balance is the sum of remain_credits.
//
// T6: each credit_package row is mirrored into the matching new-tables row so
// MembershipService.DeductCreditsTx (the new authoritative deduction path) can
// debit equivalent amounts. The mirror rules:
//
//   - CreditTypeTrial      → trial_grant (credits_remaining=remain, expires=ExpiresAt)
//   - CreditTypeSubscription → subscription + credit_cycle for the package's
//     [ActivatedAt, ExpiresAt] window (credits_remaining=remain)
//   - CreditTypeBooster    → user_booster_balance (credits_remaining=remain)
//
// Booster never expires in the new schema (per-user aggregate row), so booster
// package ExpiresAt is ignored during the mirror. Tests that depended on
// booster expiry no longer apply post-T6.
func seedPackagesAndAccount(t *testing.T, db *gorm.DB, userID uint, pkgs []model.CreditPackage) {
	t.Helper()

	// Ensure account row exists
	var total int64
	for _, p := range pkgs {
		total += p.RemainCredits
	}
	acc := model.CreditAccount{UserID: userID, Balance: total, Status: "active"}
	require.NoError(t, db.Create(&acc).Error)

	for i := range pkgs {
		pkgs[i].UserID = userID
		if pkgs[i].Status == "" {
			pkgs[i].Status = model.CreditPackageActive
		}
		require.NoError(t, db.Create(&pkgs[i]).Error)
	}

	// T6 mirror to new tables so MembershipService.DeductCreditsTx debits the
	// equivalent balance. If the membership tables aren't present (older test
	// fixture), silently skip — backwards compatibility for tests that don't
	// reach the deduction path.
	if !hasMembershipTables(db) {
		return
	}
	mirrorPackagesToMembershipTables(t, db, userID, pkgs)
}

// hasMembershipTables sniffs whether the subscription table exists. Used by
// seedPackagesAndAccount to silently skip mirroring on legacy test fixtures
// that predate the membership-tables schema.
func hasMembershipTables(db *gorm.DB) bool {
	var n int64
	err := db.Raw(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='subscription'`).Scan(&n).Error
	return err == nil && n > 0
}

// mirrorPackagesToMembershipTables copies the legacy credit_package seed data
// into the equivalent membership-tables rows so MembershipService.DeductCreditsTx
// can debit them. The aggregation rules:
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
func mirrorPackagesToMembershipTables(t *testing.T, db *gorm.DB, userID uint, pkgs []model.CreditPackage) {
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
