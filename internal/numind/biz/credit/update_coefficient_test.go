package credit_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/pricing"
)

// --- Task C.6: UpdateCoefficient concurrency retry ---

// TestUpdateCoefficient_ConcurrentRetrySucceeds fires two goroutines that
// both try to insert a new version for the same (provider, model, operation).
// The single-connection SQLite pool serialises them in practice, but both
// inserts must succeed (with different versions) — the retry logic handles
// the duplicate-key race.
func TestUpdateCoefficient_ConcurrentRetrySucceeds(t *testing.T) {
	db := newEstimationTestDB(t)
	ds := store.NewTestStore(db)
	biz := credit.NewEstimationBiz(ds, pricing.NewCalculator(ds.Billing()))

	// Seed initial v1 so both writers race to create v2.
	seedCoefficient(t, db, "ali", "qwen-turbo", "sop_run", 1.5, 0.5, 0.2, 1, true)

	const writers = 2
	var wg sync.WaitGroup
	ids := make([]uint64, writers)
	errs := make([]error, writers)

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			next := &model.CreditEstimationCoefficient{
				Provider: "ali", Model: "qwen-turbo", Operation: "sop_run",
				CharToTokenRatio:      1.4,
				CompletionPromptRatio: 0.6,
				SafetyBufferPct:       0.25,
				ChangeReason:          fmt.Sprintf("concurrent writer %d", idx),
				UpdatedBy:             "admin",
			}
			ids[idx], errs[idx] = biz.UpdateCoefficient(context.Background(), next)
		}(i)
	}
	wg.Wait()

	for i := 0; i < writers; i++ {
		require.NoError(t, errs[i], "writer %d failed", i)
		assert.Greater(t, ids[i], uint64(0), "writer %d got zero id", i)
	}
	assert.NotEqual(t, ids[0], ids[1], "each writer must insert a distinct row")

	// DB state: v1 demoted, v2+v3 both exist, only the latest is_active=1.
	var rows []model.CreditEstimationCoefficient
	require.NoError(t, db.Where("provider = ? AND model = ? AND operation = ?",
		"ali", "qwen-turbo", "sop_run").
		Order("version ASC").
		Find(&rows).Error)
	require.Len(t, rows, 3, "v1 + 2 concurrent = 3 rows total")

	var activeCount int
	for _, r := range rows {
		if r.IsActive {
			activeCount++
		}
	}
	assert.Equal(t, 1, activeCount, "exactly one row must be active after concurrent writes")

	// Versions monotonically increase 1, 2, 3 (not 1, 2, 2).
	assert.EqualValues(t, 1, rows[0].Version)
	assert.EqualValues(t, 2, rows[1].Version)
	assert.EqualValues(t, 3, rows[2].Version)
}
