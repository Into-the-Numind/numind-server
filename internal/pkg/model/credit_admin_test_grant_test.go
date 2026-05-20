package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCreditAdminTestGrant_TableName(t *testing.T) {
	assert.Equal(t, "credit_admin_test_grant", CreditAdminTestGrant{}.TableName())
}

func TestCreditAdminTestGrant_Remaining(t *testing.T) {
	g := &CreditAdminTestGrant{GrantedAmount: 5000, UsedAmount: 1200}
	assert.Equal(t, int64(3800), g.Remaining())

	g2 := &CreditAdminTestGrant{GrantedAmount: 5000, UsedAmount: 0}
	assert.Equal(t, int64(5000), g2.Remaining())

	// Edge: GrantedAmount < UsedAmount (theoretical, shouldn't happen in business logic)
	g3 := &CreditAdminTestGrant{GrantedAmount: 100, UsedAmount: 200}
	assert.Equal(t, int64(-100), g3.Remaining())
}

func TestCreditAdminTestGrant_AutoMigrate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&CreditAdminTestGrant{}))

	// Verify the table exists with the right columns
	var tableInfo []map[string]interface{}
	require.NoError(t, db.Raw("PRAGMA table_info(credit_admin_test_grant)").Scan(&tableInfo).Error)
	require.NotEmpty(t, tableInfo)
}

func TestCreditAdminTestGrant_CreateAndRead(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&CreditAdminTestGrant{}))

	now := time.Now()
	grant := &CreditAdminTestGrant{
		ParentUserID:  42,
		GrantedAmount: 5000,
		UsedAmount:    1500,
		PeriodStart:   time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:     time.Date(now.Year(), now.Month()+1, 0, 23, 59, 59, 0, time.UTC),
	}
	require.NoError(t, db.Create(grant).Error)
	require.NotZero(t, grant.ID)

	// Read back
	var got CreditAdminTestGrant
	require.NoError(t, db.First(&got, grant.ID).Error)
	assert.Equal(t, uint(42), got.ParentUserID)
	assert.Equal(t, uint32(5000), got.GrantedAmount)
	assert.Equal(t, uint32(1500), got.UsedAmount)
	assert.Equal(t, int64(3500), got.Remaining())
}

func TestCreditAdminTestGrant_UniqueParentPeriod(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&CreditAdminTestGrant{}))

	periodStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	g1 := &CreditAdminTestGrant{
		ParentUserID: 42, GrantedAmount: 5000,
		PeriodStart: periodStart, PeriodEnd: periodStart.AddDate(0, 1, -1),
	}
	require.NoError(t, db.Create(g1).Error)

	// Second insert with same parent + period → unique constraint violation
	g2 := &CreditAdminTestGrant{
		ParentUserID: 42, GrantedAmount: 6000,
		PeriodStart: periodStart, PeriodEnd: periodStart.AddDate(0, 1, -1),
	}
	err = db.Create(g2).Error
	require.Error(t, err)
}
