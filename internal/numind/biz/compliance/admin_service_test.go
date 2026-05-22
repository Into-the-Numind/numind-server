package compliance_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/compliance"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test DB helper
// ---------------------------------------------------------------------------

func newComplianceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	// SQLite-compat raw DDL: ComplianceRule uses CURRENT_TIMESTAMP(3) for MySQL ms precision.
	require.NoError(t, db.Exec(model.SQLiteCreateComplianceRuleDDL).Error)
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func newAdminService(t *testing.T) (*compliance.AdminService, *gorm.DB) {
	t.Helper()
	db := newComplianceTestDB(t)
	s := store.NewTestStore(db)
	cache := compliance.NewTTLCache(10, 5*time.Minute)
	svc := compliance.NewAdminService(s.Compliance(), cache)
	return svc, db
}

// ---------------------------------------------------------------------------
// TestAdminService_Create_Get_Happy
// ---------------------------------------------------------------------------

func TestAdminService_Create_Get_Happy(t *testing.T) {
	svc, _ := newAdminService(t)
	ctx := context.Background()

	active := true
	rule, err := svc.Create(ctx, compliance.CreateRequest{
		ParentUserID: 1,
		RuleType:     model.ComplianceRuleTypeForbidBrand,
		RuleText:     "CompetitorX",
		IsActive:     &active,
	})
	require.NoError(t, err)
	require.NotZero(t, rule.ID)
	assert.Equal(t, uint(1), rule.ParentUserID)
	assert.Equal(t, model.ComplianceRuleTypeForbidBrand, rule.RuleType)
	assert.Equal(t, "CompetitorX", rule.RuleText)
	assert.True(t, rule.IsActive)

	// Get should return same rule.
	fetched, err := svc.Get(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, rule.ID, fetched.ID)
	assert.Equal(t, "CompetitorX", fetched.RuleText)
}

// ---------------------------------------------------------------------------
// TestAdminService_Create_IsActiveFalse_DefaultTrueGotcha
// ---------------------------------------------------------------------------

// Verifies the default:true bool gotcha (database.md §6): when IsActive=false
// is explicitly set, the rule must be persisted as inactive, not flipped to
// true by GORM's DB default.
func TestAdminService_Create_IsActiveFalse_DefaultTrueGotcha(t *testing.T) {
	svc, db := newAdminService(t)
	ctx := context.Background()

	active := false
	rule, err := svc.Create(ctx, compliance.CreateRequest{
		ParentUserID: 2,
		RuleType:     model.ComplianceRuleTypeForbidPhrase,
		RuleText:     "secret",
		IsActive:     &active,
	})
	require.NoError(t, err)
	assert.False(t, rule.IsActive, "biz layer: IsActive should be false")

	// Verify directly in DB — not just the in-memory struct.
	var row model.ComplianceRule
	require.NoError(t, db.First(&row, rule.ID).Error)
	assert.False(t, row.IsActive, "DB row: IsActive should be false (no default:true flip)")
}

// ---------------------------------------------------------------------------
// TestAdminService_List_Filter
// ---------------------------------------------------------------------------

func TestAdminService_List_Filter(t *testing.T) {
	svc, _ := newAdminService(t)
	ctx := context.Background()

	active := true
	inactive := false

	// Insert 3 rules: 2 active, 1 inactive, across 2 parents.
	_, err := svc.Create(ctx, compliance.CreateRequest{ParentUserID: 10, RuleType: model.ComplianceRuleTypeForbidBrand, RuleText: "A", IsActive: &active})
	require.NoError(t, err)
	_, err = svc.Create(ctx, compliance.CreateRequest{ParentUserID: 10, RuleType: model.ComplianceRuleTypeForbidPhrase, RuleText: "B", IsActive: &inactive})
	require.NoError(t, err)
	_, err = svc.Create(ctx, compliance.CreateRequest{ParentUserID: 20, RuleType: model.ComplianceRuleTypeForbidBrand, RuleText: "C", IsActive: &active})
	require.NoError(t, err)

	// List all for parent 10 — expect 2.
	result, err := svc.List(ctx, compliance.ListOpts{ParentUserID: 10, Page: 1, PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(2), result.Total)

	// List active only for parent 10 — expect 1.
	isActive := true
	result, err = svc.List(ctx, compliance.ListOpts{ParentUserID: 10, IsActive: &isActive, Page: 1, PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(1), result.Total)
	assert.Equal(t, "A", result.Rules[0].RuleText)

	// List all no parent filter — expect 3.
	result, err = svc.List(ctx, compliance.ListOpts{Page: 1, PageSize: 20})
	require.NoError(t, err)
	assert.Equal(t, int64(3), result.Total)
}

// ---------------------------------------------------------------------------
// TestAdminService_Patch
// ---------------------------------------------------------------------------

func TestAdminService_Patch(t *testing.T) {
	svc, _ := newAdminService(t)
	ctx := context.Background()

	active := true
	rule, err := svc.Create(ctx, compliance.CreateRequest{ParentUserID: 5, RuleType: model.ComplianceRuleTypeForbidBrand, RuleText: "Before", IsActive: &active})
	require.NoError(t, err)

	newText := "After"
	newActive := false
	patched, err := svc.Patch(ctx, rule.ID, compliance.PatchRequest{
		RuleText: &newText,
		IsActive: &newActive,
	})
	require.NoError(t, err)
	assert.Equal(t, "After", patched.RuleText)
	assert.False(t, patched.IsActive)
}

// ---------------------------------------------------------------------------
// TestAdminService_Patch_NotFound
// ---------------------------------------------------------------------------

func TestAdminService_Patch_NotFound(t *testing.T) {
	svc, _ := newAdminService(t)
	ctx := context.Background()

	newText := "x"
	_, err := svc.Patch(ctx, 999999, compliance.PatchRequest{RuleText: &newText})
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrComplianceRuleNotFound)
}

// ---------------------------------------------------------------------------
// TestAdminService_Delete_InvalidatesCache
// ---------------------------------------------------------------------------

func TestAdminService_Delete_InvalidatesCache(t *testing.T) {
	svc, _ := newAdminService(t)
	ctx := context.Background()

	active := true
	rule, err := svc.Create(ctx, compliance.CreateRequest{ParentUserID: 7, RuleType: model.ComplianceRuleTypeForbidBrand, RuleText: "del", IsActive: &active})
	require.NoError(t, err)

	// Soft-delete.
	require.NoError(t, svc.Delete(ctx, rule.ID))

	// After soft-delete the rule still exists but is_active=false.
	fetched, err := svc.Get(ctx, rule.ID)
	require.NoError(t, err)
	assert.False(t, fetched.IsActive)
}

// ---------------------------------------------------------------------------
// TestAdminService_Delete_NotFound
// ---------------------------------------------------------------------------

func TestAdminService_Delete_NotFound(t *testing.T) {
	svc, _ := newAdminService(t)
	ctx := context.Background()

	err := svc.Delete(ctx, 999999)
	require.Error(t, err)
	assert.ErrorIs(t, err, errno.ErrComplianceRuleNotFound)
}
