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

// TestGetPricingRule_PrefersExactOverGlobalFallback_EvenWhenGlobalIDLower
// is the regression guard for the cost_cents underbilling bug (dev usage_record
// id=352-363, 2026-04-20/21). The original SQL used
// Order(gorm.Expr("CASE ... END", args...)) which GORM v2 silently drops when
// chained with First(), leaving just `ORDER BY id LIMIT 1`. When a global
// fallback row (provider='', model='') gets a lower id than the model-specific
// row — the common case, because the fallback was seeded earlier than per-model
// aihubmix rows — GetPricingRule returned the fallback and every aihubmix call
// billed at ¥3/¥10 instead of ¥21.60/¥108.
//
// The fix moves priority selection to Go, so this test both pins the "exact
// wins over global" contract and makes sure a future SQL rewrite does not
// silently drop CASE again.
func TestGetPricingRule_PrefersExactOverGlobalFallback_EvenWhenGlobalIDLower(t *testing.T) {
	db := newBillingTestDB(t)
	s := newBillingStore(db)

	// Insert global fallback FIRST so it gets the lower id.
	global := &model.PricingRule{
		ServiceType:        "llm_chat",
		Provider:           "",
		Model:              "",
		BillingMode:        "flat",
		FlatUnit:           "call",
		InputPricePerMTok:  3.0,
		OutputPricePerMTok: 10.0,
		IsActive:           true,
	}
	require.NoError(t, s.CreatePricingRule(context.Background(), global))

	// Insert exact-match row AFTER — gets higher id.
	exact := &model.PricingRule{
		ServiceType:        "llm_chat",
		Provider:           "aihubmix",
		Model:              "claude-sonnet-4-6",
		BillingMode:        "flat",
		FlatUnit:           "call",
		InputPricePerMTok:  21.6,
		OutputPricePerMTok: 108.0,
		IsActive:           true,
	}
	require.NoError(t, s.CreatePricingRule(context.Background(), exact))

	require.Less(t, global.ID, exact.ID,
		"test setup invariant: global id must be lower than exact id")

	got, err := s.GetPricingRule(context.Background(), "llm_chat", "aihubmix", "claude-sonnet-4-6")
	require.NoError(t, err)
	assert.Equal(t, exact.ID, got.ID,
		"must return the exact (aihubmix, claude-sonnet-4-6) rule, not the lower-id global fallback")
	assert.Equal(t, 21.6, got.InputPricePerMTok)
	assert.Equal(t, 108.0, got.OutputPricePerMTok)
}

// TestGetPricingRule_FallsBackToProviderWildcardThenGlobal verifies the full
// three-level fallback chain when no exact row exists.
func TestGetPricingRule_FallsBackToProviderWildcardThenGlobal(t *testing.T) {
	db := newBillingTestDB(t)
	s := newBillingStore(db)

	// Only global + provider-wildcard exist; no exact (provider, model) row.
	require.NoError(t, s.CreatePricingRule(context.Background(), &model.PricingRule{
		ServiceType: "llm_chat", Provider: "", Model: "",
		BillingMode: "flat", FlatUnit: "call",
		InputPricePerMTok: 3.0, OutputPricePerMTok: 10.0, IsActive: true,
	}))
	providerWildcard := &model.PricingRule{
		ServiceType: "llm_chat", Provider: "volc-ark", Model: "",
		BillingMode: "flat", FlatUnit: "call",
		InputPricePerMTok: 1.0, OutputPricePerMTok: 2.0, IsActive: true,
	}
	require.NoError(t, s.CreatePricingRule(context.Background(), providerWildcard))

	// Provider match beats global.
	got, err := s.GetPricingRule(context.Background(), "llm_chat", "volc-ark", "any-unknown-model")
	require.NoError(t, err)
	assert.Equal(t, providerWildcard.ID, got.ID, "provider wildcard should beat global fallback")

	// Unknown provider falls through to global.
	got, err = s.GetPricingRule(context.Background(), "llm_chat", "unknown-provider", "any-model")
	require.NoError(t, err)
	assert.Equal(t, "", got.Provider)
	assert.Equal(t, "", got.Model)
	assert.Equal(t, 3.0, got.InputPricePerMTok)
}
