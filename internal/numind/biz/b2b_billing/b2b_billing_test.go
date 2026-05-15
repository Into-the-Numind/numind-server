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
// chooseSource unit tests — T9: always new_only
// --------------------------------------------------------------------------

func TestChooseSource(t *testing.T) {
	// T9 cleanup: chooseSource always returns new_only regardless of input.
	cutover := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		monthStart time.Time
		monthEnd   time.Time
		cutoverArg time.Time
	}{
		{
			name:       "entirely before cutover → new_only (T9 simplified)",
			monthStart: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			monthEnd:   time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			cutoverArg: cutover,
		},
		{
			name:       "entirely after cutover → new_only",
			monthStart: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			monthEnd:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			cutoverArg: cutover,
		},
		{
			name:       "zero cutover → new_only (T9 simplified)",
			monthStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			monthEnd:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
			cutoverArg: time.Time{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseSource(tc.monthStart, tc.monthEnd, tc.cutoverArg)
			assert.Equal(t, "new_only", got, "T9: chooseSource always returns new_only")
		})
	}
}

// --------------------------------------------------------------------------
// Basic report tests (all use new_only / membership_event source after T9)
// --------------------------------------------------------------------------

func TestGetBillingReport_Empty(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds) // T9: always new_only regardless of cutover

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	assert.Equal(t, "2026-04", report.Month)
	assert.Empty(t, report.ByParent)
	assert.EqualValues(t, 0, report.TotalAmountCents)
	assert.Equal(t, "new_only", report.Source)
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
	months := uint8(1)

	// T9: all reads go to membership_event, not credit_package
	insertMembershipEvent(t, db, parent, childA, membershipModel.ProductTypeTrial, nil, apr10)
	insertMembershipEvent(t, db, parent, childB, membershipModel.ProductTypeMonthly, &months, apr15)
	// events outside April — should NOT count
	insertMembershipEvent(t, db, parent, childB, membershipModel.ProductTypeMonthly, &months, time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC))
	insertMembershipEvent(t, db, parent, childB, membershipModel.ProductTypeMonthly, &months, time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC))

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

	// T9: B2B grant via membership_event; self-purchase event (no granter) excluded
	insertMembershipEvent(t, db, parent, child, membershipModel.ProductTypeTrial, nil, apr10)
	// self_purchase event: no granter — getNewEvents skips rows with nil GranterUserID
	selfEv := &membershipModel.MembershipEvent{
		UserID:      uint64(child),
		EventType:   membershipModel.EventTypeSubGranted,
		ProductType: membershipModel.ProductTypeMonthly,
		AmountCents: 9900,
		Source:      membershipModel.SourceSelfPurchase,
		OccurredAt:  apr10,
	}
	selfKey := "self-purchase-excl-test"
	selfEv.IdempotencyKey = &selfKey
	require.NoError(t, db.Create(selfEv).Error)

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
	months := uint8(1)

	// T9: all via membership_event
	insertMembershipEvent(t, db, parent1, child1, membershipModel.ProductTypeMonthly, &months, apr10)
	insertMembershipEvent(t, db, parent1, child1, membershipModel.ProductTypeMonthly, &months, apr10.Add(time.Second)) // distinct key
	insertMembershipEvent(t, db, parent2, child2, membershipModel.ProductTypeTrial, nil, apr10)

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

func TestGetBillingReport_BoundaryOccurredAt(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "p")
	child := insertB2BUser(t, db, "c")

	aprStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	aprEnd := time.Date(2026, 4, 30, 23, 59, 59, 0, time.UTC) // within April
	mayStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	// T9: boundary test via membership_event occurred_at
	insertMembershipEvent(t, db, parent, child, membershipModel.ProductTypeTrial, nil, aprStart)
	insertMembershipEvent(t, db, parent, child, membershipModel.ProductTypeTrial, nil, aprEnd)
	insertMembershipEvent(t, db, parent, child, membershipModel.ProductTypeTrial, nil, mayStart)

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	require.Len(t, report.ByParent, 1)
	assert.Equal(t, 2, report.ByParent[0].GrantsCount, "only April boundary events count")
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
// T9 note: cutover_split mode removed — all months now use new_only.
// The following tests were the cutover_split tests; they now verify that
// even months spanning the former cutover boundary use new_only correctly.
// --------------------------------------------------------------------------

func TestGetBillingReport_FormerCutoverMonth_UsesNewOnly(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)

	// T9: even with a cutover date set, chooseSource always returns new_only.
	cutover := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	biz := NewWithCutover(ds, cutover)

	parent := insertB2BUser(t, db, "split_corp")
	child := insertB2BUser(t, db, "split_emp")

	// Only membership_event rows are read, regardless of cutover.
	may20 := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	months := uint8(1)
	insertMembershipEvent(t, db, parent, child, membershipModel.ProductTypeMonthly, &months, may20)

	report, err := biz.GetBillingReport(context.Background(), "2026-05")
	require.NoError(t, err)
	assert.Equal(t, "new_only", report.Source, "T9: cutover_split removed; always new_only")
	require.Len(t, report.ByParent, 1)
	assert.Equal(t, 1, report.ByParent[0].GrantsCount)
	assert.EqualValues(t, 9900, report.TotalAmountCents)
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
