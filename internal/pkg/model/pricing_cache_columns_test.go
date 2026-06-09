package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPricingRule_CachedInputColumns_AutoMigrate verifies the two new nullable
// cache-hit price columns exist after AutoMigrate (Task 2 / spec §4.3).
func TestPricingRule_CachedInputColumns_AutoMigrate(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.AutoMigrate(&PricingRule{}))

	for _, col := range []string{
		"cached_input_price_per_m_tok",
		"sell_cached_input_price_per_m_tok",
	} {
		assert.True(t, db.Migrator().HasColumn(&PricingRule{}, col),
			"column %s should exist after AutoMigrate", col)
	}
}

// TestPricingRule_CachedInputColumns_NullRoundTrip is the zero-regression control:
// an unseeded row reads NULL (nil pointer) on BOTH cached columns. NULL is the
// "not configured" sentinel — CalculateCostWithCache falls back to full input price.
func TestPricingRule_CachedInputColumns_NullRoundTrip(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.AutoMigrate(&PricingRule{}))

	row := PricingRule{
		ServiceType:           "llm_chat",
		Provider:              "ali",
		Model:                 "qwen-turbo",
		BillingMode:           "flat",
		FlatUnit:              "call",
		InputPricePerMTok:     0.3,
		OutputPricePerMTok:    0.6,
		SellInputPricePerMTok: 0.5,
		CreditMultiplier:      1.0,
		IsActive:              true,
		// CachedInputPricePerMTok / SellCachedInputPricePerMTok left nil on purpose.
	}
	require.NoError(t, db.Create(&row).Error)
	assert.NotZero(t, row.ID)

	var got PricingRule
	require.NoError(t, db.First(&got, row.ID).Error)
	assert.Nil(t, got.CachedInputPricePerMTok, "unseeded cost cached price must read NULL")
	assert.Nil(t, got.SellCachedInputPricePerMTok, "unseeded sell cached price must read NULL")
}

// TestPricingRule_CachedInputColumns_SetRoundTrip verifies a configured pair of
// cached prices persists and reads back exactly. Pointers so NULL vs 0 are
// distinguishable (0 would be a real "free cached input" price, NULL = fallback).
func TestPricingRule_CachedInputColumns_SetRoundTrip(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.AutoMigrate(&PricingRule{}))

	cachedCost := 1.4
	cachedSell := 1.4
	row := PricingRule{
		ServiceType:                 "llm_chat",
		Provider:                    "dmxapi",
		Model:                       "deepseek-v4-pro",
		BillingMode:                 "flat",
		FlatUnit:                    "call",
		InputPricePerMTok:           14.0,
		OutputPricePerMTok:          28.0,
		SellInputPricePerMTok:       14.0,
		CreditMultiplier:            1.0,
		IsActive:                    true,
		CachedInputPricePerMTok:     &cachedCost,
		SellCachedInputPricePerMTok: &cachedSell,
	}
	require.NoError(t, db.Create(&row).Error)

	var got PricingRule
	require.NoError(t, db.First(&got, row.ID).Error)
	require.NotNil(t, got.CachedInputPricePerMTok, "set cost cached price must read non-NULL")
	require.NotNil(t, got.SellCachedInputPricePerMTok, "set sell cached price must read non-NULL")
	assert.InDelta(t, 1.4, *got.CachedInputPricePerMTok, 0.0001)
	assert.InDelta(t, 1.4, *got.SellCachedInputPricePerMTok, 0.0001)
}

// TestUsageRecord_CachedPromptTokens_AutoMigrate verifies the observability
// column exists and defaults to 0 (additive, zero-regression on legacy rows).
func TestUsageRecord_CachedPromptTokens_AutoMigrate(t *testing.T) {
	db := newTestDB(t)
	require.NoError(t, db.AutoMigrate(&UsageRecord{}))

	assert.True(t, db.Migrator().HasColumn(&UsageRecord{}, "cached_prompt_tokens"),
		"column cached_prompt_tokens should exist after AutoMigrate")

	// A row created without setting CachedPromptTokens reads back 0 (not NULL).
	row := UsageRecord{
		UserID:      1,
		ServiceType: "llm_chat",
		Provider:    "dmxapi",
		Model:       "deepseek-v4-pro",
		Operation:   "sop_node_execute",
	}
	require.NoError(t, db.Create(&row).Error)

	var got UsageRecord
	require.NoError(t, db.First(&got, row.ID).Error)
	assert.Equal(t, 0, got.CachedPromptTokens, "legacy/unset cached_prompt_tokens must default to 0")
}
