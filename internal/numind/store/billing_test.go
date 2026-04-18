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

// newBillingTestDB creates an isolated in-memory SQLite DB for billing store tests.
func newBillingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	require.NoError(t, db.AutoMigrate(
		&model.PricingRule{},
	), "auto-migrate")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// TestCreatePricingRule_IsActiveFalse verifies that a PricingRule created with
// is_active=false is actually persisted as false. Regression for the GORM
// `default:true` gotcha: without the UpdateColumn fixup in CreatePricingRule,
// GORM v2 treats bool zero value (false) as "not set" and falls back to the DB
// default of true for model.PricingRule.IsActive.
func TestCreatePricingRule_IsActiveFalse(t *testing.T) {
	db := newBillingTestDB(t)
	s := newBillingStore(db)

	rule := &model.PricingRule{
		ServiceType: "llm",
		Provider:    "test-provider",
		Model:       "test-model",
		BillingMode: "flat",
		FlatUnit:    "call",
		IsActive:    false, // explicitly false
	}

	err := s.CreatePricingRule(context.Background(), rule)
	require.NoError(t, err)
	assert.NotZero(t, rule.ID, "rule should have been assigned an ID")

	assert.False(t, rule.IsActive,
		"returned rule should have is_active=false")

	// Double-check the actual DB row.
	var row model.PricingRule
	require.NoError(t, db.First(&row, rule.ID).Error)
	assert.False(t, row.IsActive,
		"DB row should persist is_active=false (not defaulted to true)")
}

// TestCreatePricingRule_IsActiveTrue verifies that the default (true) still works.
func TestCreatePricingRule_IsActiveTrue(t *testing.T) {
	db := newBillingTestDB(t)
	s := newBillingStore(db)

	rule := &model.PricingRule{
		ServiceType: "llm",
		Provider:    "test-provider-2",
		Model:       "test-model-2",
		BillingMode: "flat",
		FlatUnit:    "call",
		IsActive:    true,
	}

	err := s.CreatePricingRule(context.Background(), rule)
	require.NoError(t, err)
	assert.True(t, rule.IsActive, "rule with is_active=true should remain true")

	var row model.PricingRule
	require.NoError(t, db.First(&row, rule.ID).Error)
	assert.True(t, row.IsActive, "DB row should have is_active=true")
}
