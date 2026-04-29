package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

// newCreditUserTypeTestDB creates an in-memory SQLite DB seeded with one
// credit_user_type_config row ('trial', 0.5, active).
func newCreditUserTypeTestDB(t *testing.T) (*gorm.DB, CreditStore) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.CreditUserTypeConfig{}))

	require.NoError(t, db.Create(&model.CreditUserTypeConfig{
		UserType:         "trial",
		CreditMultiplier: 0.5,
		Description:      "trial users half rate",
		IsActive:         true,
	}).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	return db, newCreditStore(db)
}

// TestUpdateUserTypeConfig_AllFieldsIncludingIsActiveFalse verifies the map-based
// Updates path correctly writes all three fields, including is_active=false (the
// GORM default:true gotcha would silently revert this with struct-based Updates).
func TestUpdateUserTypeConfig_AllFieldsIncludingIsActiveFalse(t *testing.T) {
	db, s := newCreditUserTypeTestDB(t)

	updates := map[string]interface{}{
		"credit_multiplier": 0.3,
		"description":       "updated description",
		"is_active":         false,
	}
	err := s.UpdateUserTypeConfig(context.Background(), "trial", updates)
	require.NoError(t, err)

	var row model.CreditUserTypeConfig
	require.NoError(t, db.Where("user_type = ?", "trial").First(&row).Error)
	assert.InDelta(t, 0.3, row.CreditMultiplier, 0.001)
	assert.Equal(t, "updated description", row.Description)
	assert.False(t, row.IsActive, "is_active=false must persist (GORM default:true gotcha guard)")
}

// TestUpdateUserTypeConfig_NotFound verifies that updating an unknown user_type
// returns gorm.ErrRecordNotFound so the controller can map it to a 404 response.
func TestUpdateUserTypeConfig_NotFound(t *testing.T) {
	_, s := newCreditUserTypeTestDB(t)

	err := s.UpdateUserTypeConfig(context.Background(), "nonexistent", map[string]interface{}{
		"credit_multiplier": 0.7,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestListUserTypeConfigs_ReturnsSeededRowsOrdered verifies that ListUserTypeConfigs
// returns all rows ordered by user_type ASC.
func TestListUserTypeConfigs_ReturnsSeededRowsOrdered(t *testing.T) {
	db, s := newCreditUserTypeTestDB(t)

	// Seed a second row out-of-order (alphabetically before 'trial').
	require.NoError(t, db.Create(&model.CreditUserTypeConfig{
		UserType:         "subscription",
		CreditMultiplier: 1.0,
		Description:      "subscription users normal rate",
		IsActive:         true,
	}).Error)

	rows, err := s.ListUserTypeConfigs(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	// ORDER BY user_type ASC: subscription < trial alphabetically.
	assert.Equal(t, "subscription", rows[0].UserType)
	assert.Equal(t, "trial", rows[1].UserType)
}
