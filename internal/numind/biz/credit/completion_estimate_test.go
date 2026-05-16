package credit_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/pkg/model"
)

// newCompletionEstimatorTestDB builds an isolated in-memory DB with
// usage_record. Each test gets its own DB instance so seed data does not
// bleed across tests (the package-wide "file::memory:?cache=shared" pattern
// would cause cross-test contamination here).
func newCompletionEstimatorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Use a unique DSN per test to guarantee isolation. file::memory:?cache=shared
	// shares state across all opens with the same name.
	db, err := gorm.Open(sqlite.Open("file:completion_est_"+t.Name()+"?mode=memory&cache=private"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.UsageRecord{}))
	return db
}

// seedUsageRecords inserts n usage records with the given completion_tokens
// and (provider, model). created_at is set to `daysAgo` days before now.
func seedUsageRecords(t *testing.T, db *gorm.DB, provider, modelName string, completionTokens int, daysAgo int, n int) {
	t.Helper()
	cutoff := time.Now().AddDate(0, 0, -daysAgo)
	for i := 0; i < n; i++ {
		row := &model.UsageRecord{
			UserID:           1,
			ServiceType:      "llm_chat",
			Provider:         provider,
			Model:            modelName,
			Operation:        "sop.text",
			PromptTokens:     1000,
			CompletionTokens: completionTokens,
			TotalTokens:      1000 + completionTokens,
			CostCents:        10,
			CreatedAt:        cutoff,
		}
		require.NoError(t, db.Create(row).Error)
	}
}

func TestCompletionEstimator_HappyPath_HistoricalAvg(t *testing.T) {
	db := newCompletionEstimatorTestDB(t)
	// Seed 50 records (above min_samples=30) with completion_tokens=1000,
	// 7 days ago (well within 30d window).
	seedUsageRecords(t, db, "dmxapi", "deepseek-v4-pro", 1000, 7, 50)

	est := credit.NewCompletionEstimator(db)
	tokens, hasData := est.Estimate(context.Background(), "dmxapi", "deepseek-v4-pro")

	require.True(t, hasData, "30+ samples should yield hasData=true")
	// avg = 1000, safety = 1.2 → expect 1200
	assert.Equal(t, 1200, tokens)
}

func TestCompletionEstimator_BelowMinSamples_ReturnsFallback(t *testing.T) {
	db := newCompletionEstimatorTestDB(t)
	// Only 10 records — below MinSamples=30 → should return (0, false).
	seedUsageRecords(t, db, "dmxapi", "claude-opus-4-7", 800, 7, 10)

	est := credit.NewCompletionEstimator(db)
	tokens, hasData := est.Estimate(context.Background(), "dmxapi", "claude-opus-4-7")

	assert.False(t, hasData, "10 samples is below MinSamples=30")
	assert.Equal(t, 0, tokens)
}

func TestCompletionEstimator_OutOfLookbackWindow_Excluded(t *testing.T) {
	db := newCompletionEstimatorTestDB(t)
	// Seed 100 records 60 days ago — outside the 30d window. Should be excluded.
	seedUsageRecords(t, db, "dmxapi", "gpt-5.5", 2000, 60, 100)

	est := credit.NewCompletionEstimator(db)
	tokens, hasData := est.Estimate(context.Background(), "dmxapi", "gpt-5.5")

	assert.False(t, hasData, "60d-old samples should be outside 30d window")
	assert.Equal(t, 0, tokens)
}

func TestCompletionEstimator_EmptyProviderOrModel_NoFallthroughQuery(t *testing.T) {
	db := newCompletionEstimatorTestDB(t)
	seedUsageRecords(t, db, "dmxapi", "deepseek-v4-pro", 1000, 7, 100)

	est := credit.NewCompletionEstimator(db)
	// Empty provider — must return (0, false) without querying (avoids accidental
	// cross-(provider, model) aggregation when the registry has not yet wired
	// the route at call time).
	tokens, hasData := est.Estimate(context.Background(), "", "deepseek-v4-pro")
	assert.False(t, hasData)
	assert.Equal(t, 0, tokens)

	tokens, hasData = est.Estimate(context.Background(), "dmxapi", "")
	assert.False(t, hasData)
	assert.Equal(t, 0, tokens)
}

func TestCompletionEstimator_CacheHit_DoesNotReQuery(t *testing.T) {
	db := newCompletionEstimatorTestDB(t)
	seedUsageRecords(t, db, "dmxapi", "deepseek-v4-pro", 1000, 7, 50)

	est := credit.NewCompletionEstimator(db)

	// First call — populates cache.
	tokens1, ok1 := est.Estimate(context.Background(), "dmxapi", "deepseek-v4-pro")
	require.True(t, ok1)
	require.Equal(t, 1200, tokens1)

	// Mutate DB after the cache is warmed: add 200 more rows with much higher
	// completion_tokens. If cache works, the second call returns the OLD value.
	seedUsageRecords(t, db, "dmxapi", "deepseek-v4-pro", 5000, 7, 200)

	tokens2, ok2 := est.Estimate(context.Background(), "dmxapi", "deepseek-v4-pro")
	require.True(t, ok2)
	assert.Equal(t, 1200, tokens2, "cache should return original value, not re-query the mutated table")
}

func TestCompletionEstimator_DifferentModelsCachedSeparately(t *testing.T) {
	db := newCompletionEstimatorTestDB(t)
	seedUsageRecords(t, db, "dmxapi", "deepseek-v4-pro", 1000, 7, 50)
	seedUsageRecords(t, db, "dmxapi", "claude-opus-4-7", 3000, 7, 50)

	est := credit.NewCompletionEstimator(db)
	t1, _ := est.Estimate(context.Background(), "dmxapi", "deepseek-v4-pro")
	t2, _ := est.Estimate(context.Background(), "dmxapi", "claude-opus-4-7")
	// Each model gets its own per-key cache entry — values must differ.
	assert.Equal(t, 1200, t1)
	assert.Equal(t, 3600, t2)
}

func TestCompletionEstimator_ZeroCompletionTokens_Excluded(t *testing.T) {
	db := newCompletionEstimatorTestDB(t)
	// All rows have completion_tokens=0 (e.g., requests that errored before
	// any output). The aggregate excludes these via WHERE completion_tokens > 0
	// to avoid pulling the mean down to ~0.
	seedUsageRecords(t, db, "dmxapi", "gemini-3.1-pro-preview", 0, 7, 50)

	est := credit.NewCompletionEstimator(db)
	tokens, hasData := est.Estimate(context.Background(), "dmxapi", "gemini-3.1-pro-preview")
	assert.False(t, hasData, "rows with completion_tokens=0 are filtered out")
	assert.Equal(t, 0, tokens)
}

func TestCompletionEstimator_Concurrent_NoRaceNoThunderingHerd(t *testing.T) {
	db := newCompletionEstimatorTestDB(t)
	seedUsageRecords(t, db, "dmxapi", "deepseek-v4-pro", 1000, 7, 50)

	est := credit.NewCompletionEstimator(db)

	// Fire N concurrent Estimate() calls on the same cold cache key. The
	// race detector (-race) flags any unsynchronised access; singleflight
	// + RWMutex should yield zero races. All callers should return the
	// same (1200, true) result.
	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]int, n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			v, _ := est.Estimate(context.Background(), "dmxapi", "deepseek-v4-pro")
			results[idx] = v
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		require.Equal(t, 1200, r, "goroutine %d got %d", i, r)
	}
}

func TestCompletionEstimator_NilDB_Panics(t *testing.T) {
	// Wiring bug must surface early: nil DB at construction must panic, not
	// silently return a useless estimator.
	defer func() {
		r := recover()
		require.NotNil(t, r, "NewCompletionEstimator(nil) must panic")
	}()
	_ = credit.NewCompletionEstimator(nil)
}
