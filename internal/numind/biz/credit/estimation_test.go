package credit_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// newEstimationTestDB builds a test DB with the tables EstimateCredits needs:
// credit_estimation_coefficient + pricing_rule.
func newEstimationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&model.CreditEstimationCoefficient{},
		&model.PricingRule{},
	))
	return db
}

// seedCoefficient inserts one CreditEstimationCoefficient row.
func seedCoefficient(t *testing.T, db *gorm.DB, provider, modelName, op string,
	char2tok, compPrompt, buffer float64, version uint, active bool) *model.CreditEstimationCoefficient {
	t.Helper()
	row := &model.CreditEstimationCoefficient{
		Provider:              provider,
		Model:                 modelName,
		Operation:             op,
		CharToTokenRatio:      char2tok,
		CompletionPromptRatio: compPrompt,
		SafetyBufferPct:       buffer,
		Version:               version,
		IsActive:              active,
	}
	require.NoError(t, db.Create(row).Error)
	return row
}

// seedPricingRule inserts a flat-billing pricing_rule row.
func seedPricingRule(t *testing.T, db *gorm.DB, serviceType, provider, modelName string,
	inputPerM, outputPerM float64) *model.PricingRule {
	t.Helper()
	rule := &model.PricingRule{
		ServiceType:        serviceType,
		Provider:           provider,
		Model:              modelName,
		BillingMode:        "flat",
		FlatUnit:           "call",
		InputPricePerMTok:  inputPerM,
		OutputPricePerMTok: outputPerM,
		IsActive:           true,
	}
	require.NoError(t, db.Create(rule).Error)
	return rule
}

// --- Task C.5: EstimateCredits R2 formula ---

// TestEstimateCredits_FormulaCalc verifies the R2 formula on a golden example:
// promptChars=1000, char2tok=1.5, comp_prompt=0.5, buffer=0.2
// input price = 2 yuan / Mtok, output price = 8 yuan / Mtok
// → estimatedPromptTokens = ceil(1000*1.5) = 1500
// → estimatedCompletionTokens = ceil(1500*0.5) = 750
// → costCents = round( (1500/1e6 * 2 + 750/1e6 * 8) * 100 ) = round( (3000 + 6000)/1e6 * 100 ) = round(0.9) = 1
// Wait that's tiny. Let me use bigger price: 200 and 800:
// costCents = round( (1500/1e6 * 200 + 750/1e6 * 800) * 100 ) = round( (0.3 + 0.6) * 100 ) = 90
// estimatedCredits = ceil(90 * 1.2) = 108
func TestEstimateCredits_FormulaCalc(t *testing.T) {
	db := newEstimationTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	biz := credit.NewEstimationBiz(ds, calc)

	seedCoefficient(t, db, "ali", "qwen-turbo", "sop_run", 1.5, 0.5, 0.2, 1, true)
	seedPricingRule(t, db, "llm_chat", "ali", "qwen-turbo", 200, 800)

	credits, coefID, err := biz.EstimateCredits(context.Background(),
		credit.OpSopRun, 1000, "qwen-turbo", "ali")
	require.NoError(t, err)
	assert.Greater(t, coefID, uint64(0), "coef id must be populated")

	// Expected: ceil(ceil(1500)*1.5 pre-safety → pricing applied → safety ceil)
	// promptTokens = 1500
	// completionTokens = 750 (ceil of 1500*0.5)
	// cost cents from pricing.CalculateCost = round( (1500*200 + 750*800)/1e6 * 100 )
	//   = round( (300000 + 600000)/1e6 * 100 ) = round(0.9 * 100) = round(90) = 90
	// after safety buffer: ceil(90 * 1.2) = 108
	assert.EqualValues(t, 108, credits, "formula result should be 108 credits")
}

// TestEstimateCredits_CeilRounding verifies that fractional tokens are ceil'd
// (conservative reservation). 1 char × 1.5 = 1.5 → ceil(1.5)=2 tokens.
func TestEstimateCredits_CeilRounding(t *testing.T) {
	db := newEstimationTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	biz := credit.NewEstimationBiz(ds, calc)

	// Use buffer=0.25 (binary-exact, so no float drift when multiplied by 200).
	seedCoefficient(t, db, "ali", "qwen-turbo", "sop_run", 1.5, 0.5, 0.25, 1, true)
	seedPricingRule(t, db, "llm_chat", "ali", "qwen-turbo", 1000000, 0)

	// 1 char × 1.5 = 1.5 → ceil = 2 tokens
	// completion = ceil(2 × 0.5) = 1 token
	// cost cents = round( (2 * 1e6 + 1 * 0)/1e6 * 100 ) = round(2 * 100) = 200
	// safety = ceil(200 * 1.25) = 250
	credits, _, err := biz.EstimateCredits(context.Background(),
		credit.OpSopRun, 1, "qwen-turbo", "ali")
	require.NoError(t, err)
	assert.EqualValues(t, 250, credits)
}

// TestEstimateCredits_ClampsToAtLeastOne verifies the floor behavior:
// when cost rounds to 0 cents for very short prompts, the estimate is
// clamped to 1 credit so Reserve's "estimated > 0" sanity check doesn't
// reject legitimate short-prompt SOP runs (prod data shows ~30% nodes
// have prompt < 200 chars).
func TestEstimateCredits_ClampsToAtLeastOne(t *testing.T) {
	db := newEstimationTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	biz := credit.NewEstimationBiz(ds, calc)

	// Cheap pricing: 1¥/MTok input, 2¥/MTok output. Buffer 0.3.
	seedCoefficient(t, db, "volc", "deepseek-v3-2-251201", "sop_run", 1.5, 0.5, 0.3, 1, true)
	seedPricingRule(t, db, "llm_chat", "volc", "deepseek-v3-2-251201", 1, 2)

	// promptChars=100 → promptTokens=150, completionTokens=75
	// costYuan = (150/1e6 * 1) + (75/1e6 * 2) = 0.000300 yuan
	// costCents = round(0.0300) = 0
	// estimated (pre-floor) = ceil(0 * 1.3) = 0
	// After floor: 1
	credits, _, err := biz.EstimateCredits(context.Background(),
		credit.OpSopRun, 100, "deepseek-v3-2-251201", "volc")
	require.NoError(t, err)
	assert.EqualValues(t, 1, credits,
		"short prompt that rounds to 0¢ should clamp to 1 credit so Reserve doesn't reject")

	// promptChars=0 → still 0 (no clamp for zero-length; no real LLM call)
	creditsZero, _, err := biz.EstimateCredits(context.Background(),
		credit.OpSopRun, 0, "deepseek-v3-2-251201", "volc")
	require.NoError(t, err)
	assert.EqualValues(t, 0, creditsZero,
		"zero-length prompt should not be clamped (no LLM call will happen)")
}

// TestEstimateCredits_FallbackLookup verifies that when the exact
// (provider, model, operation) is NOT seeded but the global fallback row
// (”, ”, ”) IS seeded, the fallback is returned.
func TestEstimateCredits_FallbackLookup(t *testing.T) {
	db := newEstimationTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	biz := credit.NewEstimationBiz(ds, calc)

	// Only seed the global fallback (the Track G seed shipped this row).
	fallback := seedCoefficient(t, db, "", "", "", 1.5, 0.5, 0.3, 1, true)
	seedPricingRule(t, db, "llm_chat", "volc", "deepseek-v3-2-251201", 200, 800)

	credits, coefID, err := biz.EstimateCredits(context.Background(),
		credit.OpSopRun, 1000, "deepseek-v3-2-251201", "volc")
	require.NoError(t, err, "should fall back to global row")
	assert.Equal(t, fallback.ID, coefID, "must return the global fallback coefficient id")
	assert.Greater(t, credits, int64(0))
}

// TestEstimateCredits_NotFound verifies that when neither an exact match nor
// a global fallback exists, ErrCoefficientNotFound is returned.
func TestEstimateCredits_NotFound(t *testing.T) {
	db := newEstimationTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	biz := credit.NewEstimationBiz(ds, calc)

	// No coefficients at all.
	_, _, err := biz.EstimateCredits(context.Background(),
		credit.OpSopRun, 1000, "qwen-turbo", "ali")
	require.Error(t, err)
	assert.True(t, errors.Is(err, credit.ErrCoefficientNotFound),
		"expected ErrCoefficientNotFound, got %v", err)
}

// TestEstimateCredits_InactiveCoefficientIgnored verifies that is_active=0
// rows are skipped by the lookup.
func TestEstimateCredits_InactiveCoefficientIgnored(t *testing.T) {
	db := newEstimationTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	biz := credit.NewEstimationBiz(ds, calc)

	// Inactive exact match — should be ignored.
	seedCoefficient(t, db, "ali", "qwen-turbo", "sop_run", 1.5, 0.5, 0.2, 1, false)
	// No active fallback either.
	_, _, err := biz.EstimateCredits(context.Background(),
		credit.OpSopRun, 100, "qwen-turbo", "ali")
	require.Error(t, err)
	assert.True(t, errors.Is(err, credit.ErrCoefficientNotFound))
}

// TestEstimateCredits_PrefersExactOverFallback verifies that when both an
// exact match AND a global fallback exist, the exact match wins.
func TestEstimateCredits_PrefersExactOverFallback(t *testing.T) {
	db := newEstimationTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	biz := credit.NewEstimationBiz(ds, calc)

	// Exact match with buffer=0.1 (small).
	exact := seedCoefficient(t, db, "ali", "qwen-turbo", "sop_run", 1.5, 0.5, 0.1, 1, true)
	// Global fallback with buffer=0.5 — would give a different answer.
	seedCoefficient(t, db, "", "", "", 1.5, 0.5, 0.5, 1, true)
	seedPricingRule(t, db, "llm_chat", "ali", "qwen-turbo", 200, 800)

	_, coefID, err := biz.EstimateCredits(context.Background(),
		credit.OpSopRun, 1000, "qwen-turbo", "ali")
	require.NoError(t, err)
	assert.Equal(t, exact.ID, coefID,
		"exact match must win over global fallback")
}

// TestEstimateCredits_PricingRuleMissing verifies that when the coefficient
// exists but no pricing_rule matches, the error surfaces (not
// ErrCoefficientNotFound).
func TestEstimateCredits_PricingRuleMissing(t *testing.T) {
	db := newEstimationTestDB(t)
	ds := store.NewTestStore(db)
	calc := pricing.NewCalculator(ds.Billing())
	biz := credit.NewEstimationBiz(ds, calc)

	seedCoefficient(t, db, "ali", "qwen-turbo", "sop_run", 1.5, 0.5, 0.2, 1, true)
	// No pricing_rule seeded.

	_, _, err := biz.EstimateCredits(context.Background(),
		credit.OpSopRun, 100, "qwen-turbo", "ali")
	require.Error(t, err)
	assert.False(t, errors.Is(err, credit.ErrCoefficientNotFound),
		"pricing miss should not be ErrCoefficientNotFound")
}

// --- Task C.6: UpdateCoefficient with version + retry ---

func TestUpdateCoefficient_InsertsNewVersion(t *testing.T) {
	db := newEstimationTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewEstimationBiz(ds, pricing.NewCalculator(ds.Billing()))

	// Seed v1 active
	seedCoefficient(t, db, "ali", "qwen-turbo", "sop_run", 1.5, 0.5, 0.2, 1, true)

	// Operator admin updates — insert v2 active, demote v1.
	newCoef := &model.CreditEstimationCoefficient{
		Provider:              "ali",
		Model:                 "qwen-turbo",
		Operation:             "sop_run",
		CharToTokenRatio:      1.4,
		CompletionPromptRatio: 0.6,
		SafetyBufferPct:       0.25,
		ChangeReason:          "beta calibration",
		UpdatedBy:             "admin@example.com",
	}
	newID, err := biz.UpdateCoefficient(context.Background(), newCoef)
	require.NoError(t, err)
	assert.Greater(t, newID, uint64(0))

	// v1 should be demoted to inactive.
	var v1 model.CreditEstimationCoefficient
	require.NoError(t, db.Where("provider = ? AND model = ? AND operation = ? AND version = ?",
		"ali", "qwen-turbo", "sop_run", 1).First(&v1).Error)
	assert.False(t, v1.IsActive, "old v1 must be demoted")

	// v2 is active.
	var v2 model.CreditEstimationCoefficient
	require.NoError(t, db.First(&v2, newID).Error)
	assert.True(t, v2.IsActive)
	assert.EqualValues(t, 2, v2.Version, "version auto-increments to 2")
	assert.Equal(t, "beta calibration", v2.ChangeReason)
}

// TestUpdateCoefficient_FirstInsert verifies insert when no prior version
// exists (version starts at 1).
func TestUpdateCoefficient_FirstInsert(t *testing.T) {
	db := newEstimationTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewEstimationBiz(ds, pricing.NewCalculator(ds.Billing()))

	newCoef := &model.CreditEstimationCoefficient{
		Provider: "volc", Model: "glm-4-7", Operation: "sop_chat",
		CharToTokenRatio: 1.5, CompletionPromptRatio: 0.5, SafetyBufferPct: 0.3,
	}
	id, err := biz.UpdateCoefficient(context.Background(), newCoef)
	require.NoError(t, err)

	var row model.CreditEstimationCoefficient
	require.NoError(t, db.First(&row, id).Error)
	assert.EqualValues(t, 1, row.Version)
	assert.True(t, row.IsActive)
}

// Sanity check that our math matches the manual calculation in the doc
// header for future test maintainers.
func TestEstimateCredits_ManualChecksum(t *testing.T) {
	// Rebuild the math step-by-step to guard against future regressions.
	promptChars := 1000.0
	char2tok := 1.5
	compPrompt := 0.5
	inputPrice := 200.0
	outputPrice := 800.0
	buffer := 0.2

	promptTokens := math.Ceil(promptChars * char2tok)                                // 1500
	completionTokens := math.Ceil(promptTokens * compPrompt)                         // 750
	costYuan := (promptTokens*inputPrice + completionTokens*outputPrice) / 1_000_000 // 0.9
	costCentsRoundTrip := int64(math.Round(costYuan * 100))                          // 90
	expected := int64(math.Ceil(float64(costCentsRoundTrip) * (1 + buffer)))         // 108

	assert.EqualValues(t, 108, expected,
		"doc-header math should stay in sync with the formula")
}
