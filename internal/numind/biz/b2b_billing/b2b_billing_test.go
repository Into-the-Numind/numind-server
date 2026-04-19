package b2b_billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

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

// ---------- Q1.4 GetBillingReport ----------

func TestGetBillingReport_Empty(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	assert.Equal(t, "2026-04", report.Month)
	assert.Empty(t, report.ByParent)
	assert.EqualValues(t, 0, report.TotalAmountCents)
}

func TestGetBillingReport_OneParentTwoChildren(t *testing.T) {
	db := newB2BTestDB(t)
	ds := store.NewTestStore(db)
	biz := New(ds)

	parent := insertB2BUser(t, db, "parent1")
	childA := insertB2BUser(t, db, "child_a")
	childB := insertB2BUser(t, db, "child_b")

	// April 2026: parent grants trial to A + monthly (3 packages) to B
	apr10 := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	apr15 := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

	insertGrantPackage(t, db, childA, model.CreditTypeTrial, parent, apr10)
	// 3 monthly packages activated at months 0,1,2 — but the billing month is
	// defined by activated_at. For B2B billing, we only count packages activated
	// in the report month. Seed: 1 active in April, rest fall outside.
	insertGrantPackage(t, db, childB, model.CreditTypeSubscription, parent, apr15)
	// Two other packages activated in May/June — should NOT count for April report
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
	// trial = 990, monthly (1 package) = 9900 → total 10890
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

	// B2B grant should be included
	insertGrantPackage(t, db, child, model.CreditTypeTrial, parent, apr10)
	// C-end self_purchase (booster) should NOT be included
	insertSelfPurchasePackage(t, db, child, model.CreditTypeBooster, apr10)

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	require.Len(t, report.ByParent, 1)
	assert.Equal(t, 1, report.ByParent[0].GrantsCount, "only B2B grants count, booster self_purchase excluded")
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

	// Build lookup
	byName := map[string]ParentBillingRow{}
	for _, r := range report.ByParent {
		byName[r.ParentUsername] = r
	}
	assert.Equal(t, 2, byName["alpha_corp"].GrantsCount)
	assert.EqualValues(t, 19800, byName["alpha_corp"].AmountCents) // 2 monthly * 9900
	assert.Equal(t, 1, byName["beta_corp"].GrantsCount)
	assert.EqualValues(t, 990, byName["beta_corp"].AmountCents) // 1 trial * 990
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

	// First millisecond of April
	aprStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	// Last millisecond of April (before May 1)
	aprEnd := time.Date(2026, 4, 30, 23, 59, 59, 999_000_000, time.UTC)
	// First moment of May (should be excluded from April report)
	mayStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	insertGrantPackage(t, db, child, model.CreditTypeTrial, parent, aprStart)
	insertGrantPackage(t, db, child, model.CreditTypeTrial, parent, aprEnd)
	insertGrantPackage(t, db, child, model.CreditTypeTrial, parent, mayStart)

	report, err := biz.GetBillingReport(context.Background(), "2026-04")
	require.NoError(t, err)
	require.Len(t, report.ByParent, 1)
	assert.Equal(t, 2, report.ByParent[0].GrantsCount, "only April boundary packages count")
}
