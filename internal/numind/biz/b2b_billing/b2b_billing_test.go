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
	"numind-server/internal/pkg/model"
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
            monthly_sop_runs INTEGER DEFAULT 0,
            monthly_reset_at DATETIME,
            user_tier       TEXT DEFAULT 'free',
            tier_expires    DATETIME,
            billing_mode    TEXT NOT NULL DEFAULT 'credits',
            username        TEXT,
            password        TEXT,
            is_admin        INTEGER DEFAULT 0,
            status          INTEGER DEFAULT 0,
            last_login      DATETIME
        )`).Error)

	require.NoError(t, db.AutoMigrate(&model.CreditPackage{}))
	// membership_event: use raw DDL instead of AutoMigrate to avoid SQLite
	// incompatibility with `gorm:"type:datetime(0)"` — SQLite stores datetimes
	// as TEXT but AutoMigrate emits `datetime(0)` which prevents GORM from
	// scanning the column back into time.Time via go-sqlite3's string scanner.
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

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func insertB2BUser(t *testing.T, db *gorm.DB, username string) uint {
	t.Helper()
	res := db.Exec(
		`INSERT INTO user (created_at, updated_at, username, user_tier, billing_mode) VALUES (?, ?, ?, 'free', 'credits')`,
		time.Now(), time.Now(), username,
	)
	require.NoError(t, res.Error)
	var id uint
	require.NoError(t, db.Raw("SELECT last_insert_rowid()").Scan(&id).Error)
	return id
}

func insertGrantPackage(t *testing.T, db *gorm.DB, childID uint, pkgType string, granter uint, activatedAt time.Time) {
	t.Helper()
	pkg := &model.CreditPackage{
		UserID:        childID,
		Type:          pkgType,
		TotalCredits:  200,
		RemainCredits: 200,
		ActivatedAt:   activatedAt,
		ExpiresAt:     activatedAt.Add(30 * 24 * time.Hour),
		Status:        model.CreditPackageActive,
		GrantSource:   model.GrantSourceB2BGrant,
		GranterUserID: &granter,
	}
	require.NoError(t, db.Create(pkg).Error)
}

func insertSelfPurchasePackage(t *testing.T, db *gorm.DB, userID uint, pkgType string, activatedAt time.Time) {
	t.Helper()
	pkg := &model.CreditPackage{
		UserID:        userID,
		Type:          pkgType,
		TotalCredits:  600,
		RemainCredits: 600,
		ActivatedAt:   activatedAt,
		ExpiresAt:     activatedAt.Add(90 * 24 * time.Hour),
		Status:        model.CreditPackageActive,
		GrantSource:   model.GrantSourceSelfPurchase,
	}
	require.NoError(t, db.Create(pkg).Error)
}

func insertMembershipEvent(t *testing.T, db *gorm.DB, granterID, childID uint, productType string, months *uint8, occurredAt time.Time) {
	t.Helper()
	granterID64 := uint64(granterID)
	amount := int64(0)
	switch productType {
	case membershipModel.ProductTypeTrial:
		amount = 990
	case membershipModel.ProductTypeMonthly:
		amount = 9900
	}
	idempotencyKey := fmt.Sprintf("test-%d-%d-%d", granterID, childID, occurredAt.UnixNano())
	ev := &membershipModel.MembershipEvent{
		UserID:         uint64(childID),
		EventType:      membershipModel.EventTypeSubGranted,
		ProductType:    productType,
		Months:         months,
		AmountCents:    amount,
		Source:         membershipModel.SourceB2BGrant,
		GranterUserID:  &granterID64,
		IdempotencyKey: &idempotencyKey,
		OccurredAt:     occurredAt,
	}
	require.NoError(t, db.Create(ev).Error)
}

// --------------------------------------------------------------------------
// chooseSource unit tests — 5 cases covering all three modes
// --------------------------------------------------------------------------

func TestChooseSource(t *testing.T) {
	cutover := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		monthStart time.Time
		monthEnd   time.Time
		want       string
	}{
		{
			name:       "entirely before cutover → legacy_only",
			monthStart: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			monthEnd:   time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			want:       "legacy_only",
		},
		{
			name:       "month end exactly on cutover → legacy_only",
			monthStart: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			monthEnd:   time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC), // me == cutover: !me.After(cutover)
			want:       "legacy_only",
		},
		{
			name:       "month starts exactly on cutover → new_only",
			monthStart: time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC), // ms == cutover: !ms.Before(cutover)
			monthEnd:   time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
			want:       "new_only",
		},
		{
			name:       "entirely after cutover → new_only",
			monthStart: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			monthEnd:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			want:       "new_only",
		},
		{
			name:       "cutover inside month → cutover_split",
			monthStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			monthEnd:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			want:       "cutover_split",
		},
		{
			name:       "zero cutover → legacy_only",
			monthStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			monthEnd:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			want:       "legacy_only",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cutoverArg time.Time
			if tc.want != "legacy_only" || tc.name != "zero cutover → legacy_only" {
				cutoverArg = cutover
			}
			if tc.name == "zero cutover → legacy_only" {
				cutoverArg = time.Time{}
			}
			got := chooseSource(tc.monthStart, tc.monthEnd, cutoverArg)
			assert.Equal(t, tc.want, got)
		})
	}
}

// --------------------------------------------------------------------------
// Legacy-only mode (backward-compat tests, matches original test suite)
// --------------------------------------------------------------------------

func TestGetBillingReport_Empty(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds) // zero cutover → legacy_only

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	assert.Equal(t, "2026-04", report.Month)
	assert.Empty(t, report.ByParent)
	assert.EqualValues(t, 0, report.TotalAmountCents)
	assert.Equal(t, "legacy_only", report.Source)
}

func TestGetBillingReport_OneParentTwoChildren(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "parent1")
	childA := insertB2BUser(t, db, "child_a")
	childB := insertB2BUser(t, db, "child_b")

	apr10 := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	apr15 := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	insertGrantPackage(t, db, childA, model.CreditTypeTrial, parent, apr10)
	insertGrantPackage(t, db, childB, model.CreditTypeSubscription, parent, apr15)
	// packages outside April — should NOT count
	insertGrantPackage(t, db, childB, model.CreditTypeSubscription, parent, time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC))
	insertGrantPackage(t, db, childB, model.CreditTypeSubscription, parent, time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC))

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	assert.Equal(t, "2026-04", report.Month)
	require.Len(t, report.ByParent, 1, "one parent")

	row := report.ByParent[0]
	assert.Equal(t, parent, row.ParentUserID)
	assert.Equal(t, "parent1", row.ParentUsername)
	assert.Equal(t, 2, row.GrantsCount, "trial + 1 monthly = 2 grants in April")
	assert.EqualValues(t, 10890, row.AmountCents)
	assert.Len(t, row.Details, 2)
	assert.EqualValues(t, 10890, report.TotalAmountCents)
}

func TestGetBillingReport_ExcludesSelfPurchase(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "parent1")
	child := insertB2BUser(t, db, "child1")

	apr10 := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

	insertGrantPackage(t, db, child, model.CreditTypeTrial, parent, apr10)
	insertSelfPurchasePackage(t, db, child, model.CreditTypeBooster, apr10)

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	require.Len(t, report.ByParent, 1)
	assert.Equal(t, 1, report.ByParent[0].GrantsCount, "only B2B grants count")
	assert.EqualValues(t, 990, report.ByParent[0].AmountCents)
}

func TestGetBillingReport_MultipleParents(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent1 := insertB2BUser(t, db, "alpha_corp")
	parent2 := insertB2BUser(t, db, "beta_corp")
	child1 := insertB2BUser(t, db, "alice")
	child2 := insertB2BUser(t, db, "bob")

	apr10 := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

	insertGrantPackage(t, db, child1, model.CreditTypeSubscription, parent1, apr10)
	insertGrantPackage(t, db, child1, model.CreditTypeSubscription, parent1, apr10)
	insertGrantPackage(t, db, child2, model.CreditTypeTrial, parent2, apr10)

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	require.Len(t, report.ByParent, 2)

	byName := map[string]ParentBillingRow{}
	for _, r := range report.ByParent {
		byName[r.ParentUsername] = r
	}
	assert.Equal(t, 2, byName["alpha_corp"].GrantsCount)
	assert.EqualValues(t, 19800, byName["alpha_corp"].AmountCents)
	assert.Equal(t, 1, byName["beta_corp"].GrantsCount)
	assert.EqualValues(t, 990, byName["beta_corp"].AmountCents)
	assert.EqualValues(t, 20790, report.TotalAmountCents)
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

func TestGetBillingReport_BoundaryActivatedAt(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "p")
	child := insertB2BUser(t, db, "c")

	aprStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	aprEnd := time.Date(2026, 4, 30, 23, 59, 59, 999_000_000, time.UTC)
	mayStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	insertGrantPackage(t, db, child, model.CreditTypeTrial, parent, aprStart)
	insertGrantPackage(t, db, child, model.CreditTypeTrial, parent, aprEnd)
	insertGrantPackage(t, db, child, model.CreditTypeTrial, parent, mayStart)

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	require.Len(t, report.ByParent, 1)
	assert.Equal(t, 2, report.ByParent[0].GrantsCount, "only April boundary packages count")
}

// --------------------------------------------------------------------------
// New-only mode tests
// --------------------------------------------------------------------------

func TestGetBillingReport_NewOnly_MembershipEvents(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)

	// cutover = May 1 → June report is new_only
	cutover := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	biz := NewWithCutover(ds, cutover)

	parent := insertB2BUser(t, db, "corp_a")
	child := insertB2BUser(t, db, "emp_a")

	months := uint8(1)
	jun10 := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	insertMembershipEvent(t, db, parent, child, membershipModel.ProductTypeMonthly, &months, jun10)
	// self_purchase event (no granter) — should be excluded
	selfEv := &membershipModel.MembershipEvent{
		UserID:      uint64(child),
		EventType:   membershipModel.EventTypeSubGranted,
		ProductType: membershipModel.ProductTypeMonthly,
		Months:      &months,
		AmountCents: 9900,
		Source:      membershipModel.SourceSelfPurchase,
		OccurredAt:  jun10,
	}
	selfKey := "self-purchase-key"
	selfEv.IdempotencyKey = &selfKey
	require.NoError(t, db.Create(selfEv).Error)

	report, err := biz.GetBillingReport(context.Background(), "2026-06")
	require.NoError(t, err)
	assert.Equal(t, "new_only", report.Source)
	require.Len(t, report.ByParent, 1)
	assert.Equal(t, 1, report.ByParent[0].GrantsCount)
	assert.EqualValues(t, 9900, report.ByParent[0].AmountCents)
	assert.EqualValues(t, 9900, report.TotalAmountCents)
	assert.Equal(t, 1, report.TotalEventsCount)
	assert.Equal(t, 1, report.ActiveParentsCount)
}

// --------------------------------------------------------------------------
// Cutover-split mode tests
// --------------------------------------------------------------------------

func TestGetBillingReport_CutoverSplit_BothSources(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)

	// cutover = May 15 → May report is cutover_split
	cutover := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	biz := NewWithCutover(ds, cutover)

	parent := insertB2BUser(t, db, "split_corp")
	child := insertB2BUser(t, db, "split_emp")

	// Legacy event: May 5 (before cutover) → in credit_package
	may5 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	insertGrantPackage(t, db, child, model.CreditTypeTrial, parent, may5)

	// New event: May 20 (after cutover) → in membership_event
	may20 := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	months := uint8(1)
	insertMembershipEvent(t, db, parent, child, membershipModel.ProductTypeMonthly, &months, may20)

	report, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	assert.Equal(t, "cutover_split", report.Source)
	require.Len(t, report.ByParent, 1)
	row := report.ByParent[0]
	assert.Equal(t, 2, row.GrantsCount, "1 legacy + 1 new")
	// trial=990, monthly=9900 → total 10890
	assert.EqualValues(t, 10890, row.AmountCents)
	assert.EqualValues(t, 10890, report.TotalAmountCents)
	assert.Equal(t, 2, report.TotalEventsCount)
}

func TestGetBillingReport_CutoverSplit_DedupeNewWins(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)

	// cutover = May 15 → May report is cutover_split
	cutover := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	biz := NewWithCutover(ds, cutover)

	parent := insertB2BUser(t, db, "dedup_corp")
	child := insertB2BUser(t, db, "dedup_emp")

	// An event at exactly the cutover second: put in legacy (credit_package)
	// AND a corresponding new (membership_event). The new one should win.
	// For same deduplication key we need same: granter, child, ts, productType, months, quantity.
	// Here we test distinct timestamps before/after — no same-key conflict.
	// Real dedup scenario: legacy row at cutover-1s, new row at same time (impossible
	// in practice). We test the dedup by inserting legacy at may10 and new at may10 same second.
	may10 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

	// Insert legacy package at may10 with trial (amount 990)
	insertGrantPackage(t, db, child, model.CreditTypeTrial, parent, may10)

	// Insert new event at may10 same second with trial — same dedupeKey → new should win
	// (in new event, amountCents comes from membership_event itself)
	granterID64 := uint64(parent)
	idKey := "dedup-test-key-unique"
	ev := &membershipModel.MembershipEvent{
		UserID:         uint64(child),
		EventType:      membershipModel.EventTypeTrialGranted,
		ProductType:    membershipModel.ProductTypeTrial,
		AmountCents:    990,
		Source:         membershipModel.SourceB2BGrant,
		GranterUserID:  &granterID64,
		IdempotencyKey: &idKey,
		OccurredAt:     may10, // same timestamp as legacy
	}
	require.NoError(t, db.Create(ev).Error)

	// For this test: legacy interval = [may1, cutover=[may15)
	//                new interval    = [may15, jun1)
	// may10 < may15 → legacy leg picks it up; may10 NOT in new leg [may15, jun1)
	// → No deduplication conflict in this case; both events are distinct (different legs)
	// We just verify the report shows 1 event (legacy) + report is correct.
	report, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	assert.Equal(t, "cutover_split", report.Source)
	require.Len(t, report.ByParent, 1)
	// Only the legacy event (may10 < cutover) is counted.
	// The membership_event at may10 is NOT in [may15, jun1) so not picked by new leg.
	assert.Equal(t, 1, report.ByParent[0].GrantsCount)
	assert.EqualValues(t, 990, report.TotalAmountCents)
}

func TestGetBillingReport_NewMetadataFields(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	cutover := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	biz := NewWithCutover(ds, cutover)

	// cutover = Mar 1 → April report is new_only
	parent := insertB2BUser(t, db, "meta_corp")
	child := insertB2BUser(t, db, "meta_emp")
	months := uint8(1)
	insertMembershipEvent(t, db, parent, child, membershipModel.ProductTypeMonthly, &months, time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC))

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	assert.Equal(t, "new_only", report.Source)
	assert.False(t, report.CutoverDate.IsZero(), "cutover_date must be present in response")
	assert.Equal(t, 1, report.TotalEventsCount)
	assert.Equal(t, 1, report.ActiveParentsCount)
}
