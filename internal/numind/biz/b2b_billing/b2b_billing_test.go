package b2b_billing

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	membershipModel "numind-server/internal/pkg/model/membership"
)

// --------------------------------------------------------------------------
// DB helpers
// --------------------------------------------------------------------------

func newB2BTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	db, err := gorm.Open(sqlite.Open(tmp+"/b2b_test.db?_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite file DB")

	require.NoError(t, db.Exec(`
        CREATE TABLE user (
            id              INTEGER PRIMARY KEY AUTOINCREMENT,
            created_at      DATETIME,
            updated_at      DATETIME,
            deleted_at      DATETIME,
            phone           TEXT,
            nickname        TEXT,
            avatar_url      TEXT,
            parent_user_id  INTEGER,
            total_sop_runs  INTEGER DEFAULT 0,
            username        TEXT,
            password        TEXT,
            is_admin        INTEGER DEFAULT 0,
            status          INTEGER DEFAULT 0,
            last_login      DATETIME
        )`).Error)

	// Legacy archive table — preserved for getLegacyEvents historical tooling
	// (not called by GetBillingReport post-T9).
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS legacy_credit_package_archive_20260515 (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id         INTEGER NOT NULL,
		type            TEXT NOT NULL,
		total_credits   INTEGER NOT NULL DEFAULT 0,
		remain_credits  INTEGER NOT NULL DEFAULT 0,
		status          TEXT NOT NULL DEFAULT 'active',
		grant_source    TEXT,
		granter_user_id INTEGER,
		activated_at    DATETIME,
		expires_at      DATETIME,
		created_at      DATETIME,
		updated_at      DATETIME,
		archived_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		archive_reason  TEXT NOT NULL DEFAULT 't11_drop_credit_package_20260515'
	)`).Error)

	// membership_event: raw DDL (SQLite incompatibility with `datetime(0)`).
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS membership_event (
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
	)`).Error)
	require.NoError(t, db.Exec(`CREATE INDEX IF NOT EXISTS idx_event_granter_occurred ON membership_event (granter_user_id, occurred_at)`).Error)

	// subscription: the new primary input for Rule A + Rule B (b2b-billing-rules-rewrite hotfix).
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS subscription (
		id                       INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id                  INTEGER NOT NULL UNIQUE,
		first_started_at         DATETIME NOT NULL,
		current_started_at       DATETIME NOT NULL,
		expires_at               DATETIME NOT NULL,
		total_months_purchased   INTEGER NOT NULL,
		source                   TEXT NOT NULL,
		granter_user_id          INTEGER,
		created_at               DATETIME NOT NULL,
		updated_at               DATETIME NOT NULL
	)`).Error)

	// trial_grant: input for the trial billing path.
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS trial_grant (
		id                  INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id             INTEGER NOT NULL UNIQUE,
		granted_at          DATETIME NOT NULL,
		expires_at          DATETIME NOT NULL,
		credits_remaining   INTEGER NOT NULL DEFAULT 200,
		source              TEXT NOT NULL,
		granter_user_id     INTEGER,
		created_at          DATETIME NOT NULL
	)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func insertB2BUser(t *testing.T, db *gorm.DB, username string) uint {
	t.Helper()
	res := db.Exec(
		`INSERT INTO user (created_at, updated_at, username) VALUES (?, ?, ?)`,
		time.Now(), time.Now(), username,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

// insertSubGrant inserts a complete b2b subscription grant: subscription row
// + matching sub_granted membership_event with a UUID-like idempotency key.
//
// Use this for "real" first-time grants (Rule A scenarios). The amount_cents
// stored on the event is whatever the caller passes — the new b2b_billing code
// recomputes amounts from product_type + months, so this value only affects
// legacy code paths (not the active report).
func insertSubGrant(t *testing.T, db *gorm.DB, granterID, childID uint, months int, grantedAt time.Time) {
	t.Helper()
	granterID64 := uint64(granterID)
	expiresAt := grantedAt.AddDate(0, months, 0)

	// subscription row
	res := db.Exec(
		`INSERT INTO subscription (user_id, first_started_at, current_started_at, expires_at,
			total_months_purchased, source, granter_user_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		childID, grantedAt, grantedAt, expiresAt, months,
		membershipModel.SourceB2BGrant, granterID64, grantedAt, grantedAt,
	)
	require.NoError(t, res.Error)

	// sub_granted event
	monthsU8 := uint8(months)
	key := fmt.Sprintf("uuid-real-%d-%d-%d", granterID, childID, grantedAt.UnixNano())
	ev := &membershipModel.MembershipEvent{
		UserID:         uint64(childID),
		EventType:      membershipModel.EventTypeSubGranted,
		ProductType:    membershipModel.ProductTypeMonthly,
		Months:         &monthsU8,
		AmountCents:    membershipModel.PriceForMonths(months),
		Source:         membershipModel.SourceB2BGrant,
		GranterUserID:  &granterID64,
		IdempotencyKey: &key,
		OccurredAt:     grantedAt,
	}
	require.NoError(t, db.Create(ev).Error)
}

// insertSubRenewal inserts a sub_renewed event AND bumps subscription state.
// Use this to simulate a parent topping up an existing user's subscription.
func insertSubRenewal(t *testing.T, db *gorm.DB, granterID, childID uint, monthsAdded int, occurredAt time.Time) {
	t.Helper()
	granterID64 := uint64(granterID)

	// Bump subscription.total_months_purchased + updated_at.
	// Tests don't assert on subscription.expires_at, so we skip the
	// expires_at recomputation here to keep the helper portable across
	// SQLite/MySQL date arithmetic differences.
	res := db.Exec(
		`UPDATE subscription
		 SET total_months_purchased = total_months_purchased + ?,
		     updated_at = ?
		 WHERE user_id = ?`,
		monthsAdded, occurredAt, childID,
	)
	require.NoError(t, res.Error)

	// sub_renewed event
	monthsU8 := uint8(monthsAdded)
	key := fmt.Sprintf("uuid-renew-%d-%d-%d", granterID, childID, occurredAt.UnixNano())
	ev := &membershipModel.MembershipEvent{
		UserID:         uint64(childID),
		EventType:      membershipModel.EventTypeSubRenewed,
		ProductType:    membershipModel.ProductTypeMonthly,
		Months:         &monthsU8,
		AmountCents:    membershipModel.PriceForMonths(monthsAdded),
		Source:         membershipModel.SourceB2BGrant,
		GranterUserID:  &granterID64,
		IdempotencyKey: &key,
		OccurredAt:     occurredAt,
	}
	require.NoError(t, db.Create(ev).Error)
}

// insertMigrationPlaceholderEvent inserts a sub_renewed (or sub_granted) event
// with the `migration-*` idempotency_key prefix that was used by the 4-30
// credit_package → membership_event migration script. These rows MUST be
// excluded from settlement billing.
func insertMigrationPlaceholderEvent(t *testing.T, db *gorm.DB, granterID, childID uint, months int, occurredAt time.Time, eventType string, migrationSeq int) {
	t.Helper()
	granterID64 := uint64(granterID)
	monthsU8 := uint8(months)
	key := fmt.Sprintf("migration-20260430-cp-%d", migrationSeq)
	ev := &membershipModel.MembershipEvent{
		UserID:         uint64(childID),
		EventType:      eventType,
		ProductType:    membershipModel.ProductTypeMonthly,
		Months:         &monthsU8,
		AmountCents:    0, // migration rows historically wrote 0
		Source:         membershipModel.SourceB2BGrant,
		GranterUserID:  &granterID64,
		IdempotencyKey: &key,
		OccurredAt:     occurredAt,
	}
	require.NoError(t, db.Create(ev).Error)
}

// insertTrialGrantRow inserts a trial_grant row + matching trial_granted event.
func insertTrialGrantRow(t *testing.T, db *gorm.DB, granterID, childID uint, grantedAt time.Time) {
	t.Helper()
	granterID64 := uint64(granterID)
	expiresAt := grantedAt.AddDate(0, 0, 3)

	res := db.Exec(
		`INSERT INTO trial_grant (user_id, granted_at, expires_at, credits_remaining, source, granter_user_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		childID, grantedAt, expiresAt, 200, membershipModel.SourceB2BGrant, granterID64, grantedAt,
	)
	require.NoError(t, res.Error)

	// trial_granted event (informational; new b2b_billing reads trial_grant table, not this event)
	key := fmt.Sprintf("uuid-trial-%d-%d-%d", granterID, childID, grantedAt.UnixNano())
	ev := &membershipModel.MembershipEvent{
		UserID:         uint64(childID),
		EventType:      membershipModel.EventTypeTrialGranted,
		ProductType:    membershipModel.ProductTypeTrial,
		AmountCents:    0,
		Source:         membershipModel.SourceB2BGrant,
		GranterUserID:  &granterID64,
		IdempotencyKey: &key,
		OccurredAt:     grantedAt,
	}
	require.NoError(t, db.Create(ev).Error)
}

// manuallyOverrideSubTotal simulates an operator manually adjusting
// subscription.total_months_purchased (e.g., the sandy case where the customer
// reported a mistake and the operator reduced 14 → 1 in the DB).
func manuallyOverrideSubTotal(t *testing.T, db *gorm.DB, childID uint, newTotal int, at time.Time) {
	t.Helper()
	res := db.Exec(
		`UPDATE subscription SET total_months_purchased = ?, updated_at = ? WHERE user_id = ?`,
		newTotal, at, childID,
	)
	require.NoError(t, res.Error)
}

// --------------------------------------------------------------------------
// chooseSource unit tests — always new_only post-T9
// --------------------------------------------------------------------------

func TestChooseSource(t *testing.T) {
	cutover := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		s    time.Time
		e    time.Time
		c    time.Time
	}{
		{"before cutover", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), cutover},
		{"after cutover", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), cutover},
		{"zero cutover", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Time{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, "new_only", chooseSource(tc.s, tc.e, tc.c))
		})
	}
}

// --------------------------------------------------------------------------
// PriceForMonths sanity (delegates to model constant; assert pricing tiers)
// --------------------------------------------------------------------------

func TestPriceForMonths_Tiers(t *testing.T) {
	cases := []struct {
		months int
		want   int64
	}{
		{1, 9900},    // ¥99
		{2, 19800},   // 2 × ¥99
		{6, 59400},   // 6 × ¥99
		{11, 108900}, // 11 × ¥99
		{12, 94900},  // ¥949 annual discount
		{0, 0},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("months=%d", tc.months), func(t *testing.T) {
			assert.EqualValues(t, tc.want, membershipModel.PriceForMonths(tc.months))
		})
	}
}

// --------------------------------------------------------------------------
// Empty / validation cases
// --------------------------------------------------------------------------

func TestGetBillingReport_Empty(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	assert.Equal(t, "2026-04", report.Month)
	assert.Empty(t, report.ByParent)
	assert.EqualValues(t, 0, report.TotalAmountCents)
	assert.Equal(t, "new_only", report.Source)
}

func TestGetBillingReport_InvalidMonth(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)
	for _, bad := range []string{"", "2026", "2026-4", "2026-13", "2026/04", "garbage"} {
		_, err := biz.GetBillingReport(context.Background(), bad)
		require.Error(t, err, "month=%q should be rejected", bad)
	}
}

// --------------------------------------------------------------------------
// Rule A: first-month subscriber
// --------------------------------------------------------------------------

func TestRuleA_NewMonthlySubscriber(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "user_moxiaopai")
	emma := insertB2BUser(t, db, "100Emma")

	may6 := time.Date(2026, 5, 6, 11, 43, 44, 0, time.UTC)
	insertSubGrant(t, db, parent, emma, 1, may6)

	r, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	require.Len(t, r.ByParent, 1)
	assert.Equal(t, 1, r.ByParent[0].GrantsCount)
	assert.EqualValues(t, 9900, r.ByParent[0].AmountCents, "1 month × ¥99")
}

func TestRuleA_NewAnnualSubscriber_HuiHui(t *testing.T) {
	// Real-world: 卉卉 (user 418) opened annual on 2026-05-11.
	// Migration created 1 sub_granted + 11 future sub_renewed placeholder events.
	// Expectation: May report shows ¥949 (one row) for her annual, NOT 12×¥99=¥1188,
	// and the 11 future placeholders do NOT contribute to any month's report.
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "user_moxiaopai")
	huihui := insertB2BUser(t, db, "100celine")

	may11 := time.Date(2026, 5, 11, 10, 25, 55, 0, time.UTC)

	// subscription says: 12-month annual
	res := db.Exec(`INSERT INTO subscription (user_id, first_started_at, current_started_at,
		expires_at, total_months_purchased, source, granter_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		huihui, may11, may11, may11.AddDate(1, 0, 0), 12,
		membershipModel.SourceB2BGrant, uint64(parent), may11, may11)
	require.NoError(t, res.Error)

	// All 12 events are migration placeholders (real prod state for 卉卉)
	insertMigrationPlaceholderEvent(t, db, parent, huihui, 1, may11, membershipModel.EventTypeSubGranted, 109)
	for i, monthOffset := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11} {
		insertMigrationPlaceholderEvent(t, db, parent, huihui, 1, may11.AddDate(0, monthOffset, 0), membershipModel.EventTypeSubRenewed, 110+i)
	}

	// May: should bill ¥949 (annual price, NOT 12 × ¥99)
	r, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	require.Len(t, r.ByParent, 1)
	assert.Equal(t, 1, r.ByParent[0].GrantsCount, "single grant detail row")
	assert.EqualValues(t, 94900, r.ByParent[0].AmountCents, "annual = ¥949 NOT 12 × ¥99")
	assert.Equal(t, 12, r.ByParent[0].Details[0].Months, "shows 12 months in detail")

	// June: should bill ¥0 (future placeholder rows ignored)
	r, err = biz.GetBillingReport(context.Background(), "2026-06")
	require.NoError(t, err)
	assert.Empty(t, r.ByParent, "June must NOT bill the annual placeholder")
	assert.EqualValues(t, 0, r.TotalAmountCents)

	// March 2027: also future placeholder month — should bill ¥0
	r, err = biz.GetBillingReport(context.Background(), "2027-03")
	require.NoError(t, err)
	assert.Empty(t, r.ByParent, "Mar 2027 must NOT bill the placeholder")
}

func TestRuleA_SandyManualOverride(t *testing.T) {
	// Real-world: sandy (user 435) opened annual + 2 monthly grants on 5-18 (14 months total),
	// but operator reduced subscription.total_months_purchased to 1 due to customer error report.
	// Expectation: report uses subscription.total = 1 → ¥99, ignoring the 3 events' 14-month sum.
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "user_moxiaopai")
	sandy := insertB2BUser(t, db, "100sandy")

	may18 := time.Date(2026, 5, 18, 11, 9, 4, 0, time.UTC)
	// Insert as if she got annual + 2 monthly tops
	insertSubGrant(t, db, parent, sandy, 12, may18)
	insertSubRenewal(t, db, parent, sandy, 1, may18.Add(time.Minute*1))
	insertSubRenewal(t, db, parent, sandy, 1, may18.Add(time.Minute*1+time.Second*14))
	// Operator override: total back to 1
	manuallyOverrideSubTotal(t, db, sandy, 1, may18.Add(time.Hour))

	r, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	require.Len(t, r.ByParent, 1)
	assert.Equal(t, 1, r.ByParent[0].GrantsCount, "manual override collapses to one row")
	assert.EqualValues(t, 9900, r.ByParent[0].AmountCents, "subscription.total=1 wins; ¥99 not ¥1147")
}

func TestRuleA_MixedMonthlyPlusAnnualSameMonth(t *testing.T) {
	// Hypothetical: parent grants 1 month, then 12-month annual in same month.
	// Expectation: per-event detail (sum matches total), so 2 detail rows: ¥99 + ¥949.
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "user_moxiaopai")
	user := insertB2BUser(t, db, "mixed_user")

	may1 := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	may2 := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)

	insertSubGrant(t, db, parent, user, 1, may1)
	insertSubRenewal(t, db, parent, user, 12, may2)

	r, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	require.Len(t, r.ByParent, 1)
	assert.Equal(t, 2, r.ByParent[0].GrantsCount, "two detail rows")
	assert.EqualValues(t, 9900+94900, r.ByParent[0].AmountCents, "¥99 + ¥949 = ¥1048 (annual pricing applied per-event)")
}

// --------------------------------------------------------------------------
// Rule B: cross-month renewal
// --------------------------------------------------------------------------

func TestRuleB_CrossMonthRenewal(t *testing.T) {
	// User granted 1 month on Apr 15, then renewed 2 months on May 10.
	// Expectation: April report = ¥99, May report = ¥198 (2-month renewal).
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "user_moxiaopai")
	user := insertB2BUser(t, db, "cross_month_user")

	apr15 := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	may10 := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)

	insertSubGrant(t, db, parent, user, 1, apr15)
	insertSubRenewal(t, db, parent, user, 2, may10)

	// April: Rule A fires → ¥99
	r, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	require.Len(t, r.ByParent, 1)
	assert.EqualValues(t, 9900, r.ByParent[0].AmountCents)

	// May: Rule B fires → ¥198
	r, err = biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	require.Len(t, r.ByParent, 1)
	assert.EqualValues(t, 19800, r.ByParent[0].AmountCents)
	assert.Equal(t, 2, r.ByParent[0].Details[0].Months)
}

func TestRuleB_OldAnnualUserNoActivityInMonth(t *testing.T) {
	// Real-world: 郭奕儿/刘丽 — annual granted April 29/30, then 4-30 migration script
	// generated future sub_renewed placeholders for May-March next year.
	// Expectation: ZERO billing in May for these users (placeholders ignored,
	// no real activity in May).
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "user_moxiaopai")
	guo := insertB2BUser(t, db, "100guo")

	apr29 := time.Date(2026, 4, 29, 19, 5, 1, 0, time.UTC)

	// Subscription: granted in April
	res := db.Exec(`INSERT INTO subscription (user_id, first_started_at, current_started_at,
		expires_at, total_months_purchased, source, granter_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		guo, apr29, apr29, apr29.AddDate(1, 0, 0), 12,
		membershipModel.SourceB2BGrant, uint64(parent), apr29, apr29.Add(time.Hour))
	require.NoError(t, res.Error)

	// Migration placeholder events: April sub_granted + 11 future sub_renewed
	insertMigrationPlaceholderEvent(t, db, parent, guo, 1, apr29, membershipModel.EventTypeSubGranted, 72)
	insertMigrationPlaceholderEvent(t, db, parent, guo, 1, apr29.AddDate(0, 1, 0), membershipModel.EventTypeSubRenewed, 73)
	insertMigrationPlaceholderEvent(t, db, parent, guo, 1, apr29.AddDate(0, 2, 0), membershipModel.EventTypeSubRenewed, 74)

	// April: Rule A → ¥949 (annual)
	r, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	require.Len(t, r.ByParent, 1)
	assert.EqualValues(t, 94900, r.ByParent[0].AmountCents, "April annual = ¥949")

	// May: Rule A doesn't fire (first_started_at = April); Rule B checked
	// (updated_at IS in May from a separate touch — but no real events)
	// → should be 0.
	// Bump updated_at to May to test Rule B path:
	require.NoError(t, db.Exec(
		`UPDATE subscription SET updated_at = ? WHERE user_id = ?`,
		time.Date(2026, 5, 13, 20, 31, 45, 0, time.UTC), guo).Error)

	r, err = biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	assert.Empty(t, r.ByParent, "May: no real events, placeholders ignored → ¥0")
}

func TestRuleB_AdminCalibrationOnlyNoRealRenewal(t *testing.T) {
	// User granted in April. In May, admin_calibration touches subscription
	// (e.g., to fix balance). No real sub_renewed event.
	// Expectation: May report = ¥0 for this user.
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "user_moxiaopai")
	user := insertB2BUser(t, db, "admincal_user")

	apr15 := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	may15 := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)

	insertSubGrant(t, db, parent, user, 1, apr15)
	// Bump updated_at into May, but no sub_renewed event written
	require.NoError(t, db.Exec(`UPDATE subscription SET updated_at = ? WHERE user_id = ?`, may15, user).Error)

	r, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	assert.Empty(t, r.ByParent, "admin calibration without real grant must not bill")
}

// --------------------------------------------------------------------------
// Trial path
// --------------------------------------------------------------------------

func TestTrial_BilledFromTrialGrantTable(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "user_moxiaopai")
	user := insertB2BUser(t, db, "trial_user")

	may10 := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
	insertTrialGrantRow(t, db, parent, user, may10)

	r, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	require.Len(t, r.ByParent, 1)
	assert.Equal(t, 1, r.ByParent[0].GrantsCount)
	assert.EqualValues(t, 990, r.ByParent[0].AmountCents, "trial = ¥9.9")
	assert.Equal(t, membershipModel.ProductTypeTrial, r.ByParent[0].Details[0].ProductType)
}

func TestTrial_ExcludedFromOtherMonth(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "p")
	user := insertB2BUser(t, db, "u")

	may10 := time.Date(2026, 5, 10, 10, 0, 0, 0, time.UTC)
	insertTrialGrantRow(t, db, parent, user, may10)

	r, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	assert.Empty(t, r.ByParent)
}

// --------------------------------------------------------------------------
// Mixed scenario integration
// --------------------------------------------------------------------------

func TestIntegrationMay2026_ProdLikeShape(t *testing.T) {
	// Simulates a slice of prod May 2026 state:
	//   - 5 new monthly users (Rule A monthly)
	//   - 1 new annual user (卉卉 pattern, Rule A annual)
	//   - 1 sandy-like user (Rule A with manual override)
	//   - 1 trial user
	//   - 1 prior-month user with no May activity (郭奕儿 pattern → ¥0)
	// Expected: parent total = 5 × ¥99 + ¥949 + ¥99 + ¥9.9 = 1543.9 = 154390 cents
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "user_moxiaopai")
	may := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)

	// 5 monthly users
	for i := 0; i < 5; i++ {
		u := insertB2BUser(t, db, fmt.Sprintf("monthly_%d", i))
		insertSubGrant(t, db, parent, u, 1, may.Add(time.Duration(i)*time.Hour))
	}

	// 1 annual user — migration-style data (1 real-looking sub_granted, with migration key, plus future placeholders)
	huihui := insertB2BUser(t, db, "100celine")
	may11 := time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC)
	res := db.Exec(`INSERT INTO subscription (user_id, first_started_at, current_started_at,
		expires_at, total_months_purchased, source, granter_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		huihui, may11, may11, may11.AddDate(1, 0, 0), 12,
		membershipModel.SourceB2BGrant, uint64(parent), may11, may11)
	require.NoError(t, res.Error)
	insertMigrationPlaceholderEvent(t, db, parent, huihui, 1, may11, membershipModel.EventTypeSubGranted, 109)
	for i := 0; i < 11; i++ {
		insertMigrationPlaceholderEvent(t, db, parent, huihui, 1, may11.AddDate(0, i+1, 0), membershipModel.EventTypeSubRenewed, 110+i)
	}

	// sandy
	sandy := insertB2BUser(t, db, "100sandy")
	may18 := time.Date(2026, 5, 18, 11, 9, 0, 0, time.UTC)
	insertSubGrant(t, db, parent, sandy, 12, may18)
	insertSubRenewal(t, db, parent, sandy, 1, may18.Add(time.Minute))
	insertSubRenewal(t, db, parent, sandy, 1, may18.Add(time.Minute*2))
	manuallyOverrideSubTotal(t, db, sandy, 1, may18.Add(time.Hour))

	// trial user
	trialU := insertB2BUser(t, db, "trial_x")
	insertTrialGrantRow(t, db, parent, trialU, may.Add(time.Hour*5))

	// prior-month annual user (郭奕儿)
	guo := insertB2BUser(t, db, "100guo")
	apr29 := time.Date(2026, 4, 29, 19, 5, 1, 0, time.UTC)
	res = db.Exec(`INSERT INTO subscription (user_id, first_started_at, current_started_at,
		expires_at, total_months_purchased, source, granter_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		guo, apr29, apr29, apr29.AddDate(1, 0, 0), 12,
		membershipModel.SourceB2BGrant, uint64(parent), apr29, apr29)
	require.NoError(t, res.Error)
	insertMigrationPlaceholderEvent(t, db, parent, guo, 1, apr29, membershipModel.EventTypeSubGranted, 72)
	insertMigrationPlaceholderEvent(t, db, parent, guo, 1, apr29.AddDate(0, 1, 0), membershipModel.EventTypeSubRenewed, 73)

	r, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	require.Len(t, r.ByParent, 1)

	expectedCents := int64(5*9900 + 94900 + 9900 + 990)
	assert.EqualValues(t, expectedCents, r.ByParent[0].AmountCents,
		"5 monthly + 1 annual + 1 sandy-override-monthly + 1 trial = ¥1543.90")
}

// --------------------------------------------------------------------------
// Edge: boundaries on month start/end
// --------------------------------------------------------------------------

func TestBoundary_FirstSecondOfMonth(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "p")
	user := insertB2BUser(t, db, "u")

	// first_started_at = exactly May 1 00:00:00 → should belong to May report
	may1 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	insertSubGrant(t, db, parent, user, 1, may1)

	r, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	require.Len(t, r.ByParent, 1)
	assert.EqualValues(t, 9900, r.ByParent[0].AmountCents)

	// And NOT to April report
	r, err = biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	assert.Empty(t, r.ByParent)
}

func TestBoundary_LastSecondOfMonth(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "p")
	user := insertB2BUser(t, db, "u")

	// first_started_at = May 31 23:59:59 → still in May
	mayEnd := time.Date(2026, 5, 31, 23, 59, 59, 0, time.UTC)
	insertSubGrant(t, db, parent, user, 1, mayEnd)

	r, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	require.Len(t, r.ByParent, 1)
}

// --------------------------------------------------------------------------
// GetBillingReportForParent tests
// --------------------------------------------------------------------------

// insertChildUser inserts a user whose parent_user_id is set (a non-parent / child account).
func insertChildUser(t *testing.T, db *gorm.DB, username string, parentID uint) uint {
	t.Helper()
	res := db.Exec(
		`INSERT INTO user (created_at, updated_at, username, parent_user_id) VALUES (?, ?, ?, ?)`,
		time.Now(), time.Now(), username, parentID,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

func TestGetBillingReportForParent_ScopedToParent(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parentA := insertB2BUser(t, db, "parentA")
	parentB := insertB2BUser(t, db, "parentB")
	childA := insertB2BUser(t, db, "childA")
	childB := insertB2BUser(t, db, "childB")

	may := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	insertSubGrant(t, db, parentA, childA, 1, may) // ¥99 by parentA
	insertSubGrant(t, db, parentB, childB, 3, may) // ¥297 by parentB

	r, err := biz.GetBillingReportForParent(context.Background(), "2026-05", parentA)
	require.NoError(t, err)
	assert.EqualValues(t, parentA, r.ParentUserID)
	assert.Equal(t, 1, r.GrantsCount, "parentA only sees own 1 grant")
	assert.EqualValues(t, 9900, r.TotalAmountCents)
	require.Len(t, r.Details, 1)
	assert.EqualValues(t, childA, r.Details[0].ChildUserID, "must NOT see parentB's child")
}

func TestGetBillingReportForParent_AmountMatchesAdmin(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "parent")
	c1 := insertB2BUser(t, db, "c1")
	c2 := insertB2BUser(t, db, "c2")
	may := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	insertSubGrant(t, db, parent, c1, 12, may)  // annual ¥949
	insertTrialGrantRow(t, db, parent, c2, may) // trial ¥9.9

	admin, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	require.Len(t, admin.ByParent, 1)

	self, err := biz.GetBillingReportForParent(context.Background(), "2026-05", parent)
	require.NoError(t, err)
	assert.Equal(t, admin.ByParent[0].AmountCents, self.TotalAmountCents, "口径必须一致")
	assert.Equal(t, admin.ByParent[0].GrantsCount, self.GrantsCount)
}

func TestGetBillingReportForParent_EmptyMonth(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)
	parent := insertB2BUser(t, db, "parent")

	r, err := biz.GetBillingReportForParent(context.Background(), "2026-05", parent)
	require.NoError(t, err)
	assert.EqualValues(t, 0, r.TotalAmountCents)
	assert.Equal(t, 0, r.GrantsCount)
	assert.NotNil(t, r.Details)
	assert.Len(t, r.Details, 0)
}

func TestGetBillingReportForParent_TrialAndMonthly(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)
	parent := insertB2BUser(t, db, "parent")
	c1 := insertB2BUser(t, db, "c1")
	c2 := insertB2BUser(t, db, "c2")
	may := time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
	insertSubGrant(t, db, parent, c1, 2, may)                  // 2 months ¥198
	insertTrialGrantRow(t, db, parent, c2, may.Add(time.Hour)) // trial ¥9.9

	r, err := biz.GetBillingReportForParent(context.Background(), "2026-05", parent)
	require.NoError(t, err)
	require.Len(t, r.Details, 2)
	assert.EqualValues(t, 19800+990, r.TotalAmountCents) // 2×¥99 + ¥9.9 = 20790 cents
	var sawTrial, sawMonthly bool
	for _, d := range r.Details {
		if d.ProductType == membershipModel.ProductTypeTrial {
			sawTrial = true
			assert.Equal(t, 0, d.Months)
		}
		if d.ProductType == membershipModel.ProductTypeMonthly {
			sawMonthly = true
			assert.Equal(t, 2, d.Months)
		}
	}
	assert.True(t, sawTrial && sawMonthly)
}

func TestGetBillingReportForParent_NotParentAccount(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)
	parent := insertB2BUser(t, db, "parent")
	child := insertChildUser(t, db, "child", parent)

	_, err := biz.GetBillingReportForParent(context.Background(), "2026-05", child)
	assert.ErrorIs(t, err, ErrNotParentAccount)
}

func TestGetBillingReportForParent_InvalidMonth(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)
	parent := insertB2BUser(t, db, "parent")
	for _, bad := range []string{"2026-13", "2026-1", "2026/05", "bad", ""} {
		_, err := biz.GetBillingReportForParent(context.Background(), bad, parent)
		assert.Error(t, err, "bad month %q must error", bad)
	}
}
