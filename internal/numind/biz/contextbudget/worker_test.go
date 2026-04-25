package contextbudget

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newWorkerTestDB creates an isolated in-memory SQLite DB for worker tests.
// It includes context_summary and context_budget_event tables.
func newWorkerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.TokenEstimationProfile{},
		&model.ContextBudgetPolicy{},
		&model.ContextSummary{},
		&model.ContextBudgetEvent{},
	))
	return db
}

// workerStubCompressor is a test double that returns a fixed fragment or an error.
// Named separately from biz_test.go's stubCompressor to avoid redeclaration.
type workerStubCompressor struct {
	result contextbudget.ContextFragment
	err    error
}

func (s *workerStubCompressor) Compress(_ context.Context, _ []contextbudget.ContextFragment, _ int) (contextbudget.ContextFragment, error) {
	if s.err != nil {
		return contextbudget.ContextFragment{}, s.err
	}
	return s.result, nil
}

// buildMinimalJob creates a SummaryJob with the given ownerUserID for tests.
func buildMinimalJob(ownerUserID uint, scopeType, scopeID, sourceHash string) SummaryJob {
	return SummaryJob{
		UserID:            ownerUserID,
		OwnerUserID:       ownerUserID,
		ScopeType:         scopeType,
		ScopeID:           scopeID,
		SourceHash:        sourceHash,
		SourceFragmentIDs: []string{"frag-1", "frag-2"},
		Fragments: []contextbudget.ContextFragment{
			{
				ID:          "frag-1",
				Role:        contextbudget.RoleDurable,
				Source:      contextbudget.SourceAssistant,
				ContentType: contextbudget.ContentText,
				Content:     "This is a long assistant message that needs compression.",
				Importance:  5,
			},
			{
				ID:          "frag-2",
				Role:        contextbudget.RoleDurable,
				Source:      contextbudget.SourceUser,
				ContentType: contextbudget.ContentText,
				Content:     "This is a long user message that also needs compression.",
				Importance:  5,
			},
		},
		Operation: "chatbot_chat",
	}
}

// processJobSync is a test helper to run a job synchronously by enqueuing then processing.
func processJobSync(w *SummaryWorker, job SummaryJob) {
	ctx := context.Background()
	w.processJob(ctx, job)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestSummaryWorker_UpsertsReadySummaryByOwnerScopeAndHash verifies that a successful
// compression results in a context_summary row with status='ready', the correct
// owner_user_id, and non-empty summary_text.
func TestSummaryWorker_UpsertsReadySummaryByOwnerScopeAndHash(t *testing.T) {
	db := newWorkerTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	successFrag := contextbudget.ContextFragment{
		ID:          "summary-frag-1",
		Role:        contextbudget.RoleDurable,
		Source:      contextbudget.SourceInternal,
		ContentType: contextbudget.ContentSummary,
		Content:     "Compressed summary of the conversation.",
		Importance:  8,
	}
	compressor := &workerStubCompressor{result: successFrag}

	worker := NewSummaryWorker(cbStore, compressor, WorkerOptions{
		QueueSize: 10,
	})

	ownerUserID := uint(10)
	job := buildMinimalJob(ownerUserID, "sop_run", "run-abc123", "hash-xyz-789")

	// Process the job synchronously (bypass Run goroutine for deterministic testing).
	processJobSync(worker, job)

	// Assert: context_summary row exists with correct fields.
	var row model.ContextSummary
	err := db.Where("scope_type = ? AND scope_id = ? AND source_hash = ?",
		"sop_run", "run-abc123", "hash-xyz-789").First(&row).Error
	require.NoError(t, err, "expected context_summary row to be persisted")

	require.NotNil(t, row.OwnerUserID, "owner_user_id must not be null")
	assert.Equal(t, ownerUserID, *row.OwnerUserID, "owner_user_id must match job.OwnerUserID")
	assert.Equal(t, "sop_run", row.ScopeType)
	assert.Equal(t, "run-abc123", row.ScopeID)
	assert.Equal(t, "hash-xyz-789", row.SourceHash)
	assert.Equal(t, "ready", row.Status, "status must be 'ready' on success")
	assert.NotEmpty(t, row.SummaryText, "summary_text must be non-empty")
	assert.Empty(t, row.ErrorMessage, "error_message must be empty on success")
}

// TestSummaryWorkerFailureStoresFailedSummaryWithoutBlockingCaller verifies that
// when the compressor returns an error, the worker persists a 'failed' summary
// without blocking the caller.
func TestSummaryWorkerFailureStoresFailedSummaryWithoutBlockingCaller(t *testing.T) {
	db := newWorkerTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	compressor := &workerStubCompressor{err: errors.New("LLM call timed out")}

	worker := NewSummaryWorker(cbStore, compressor, WorkerOptions{
		QueueSize: 10,
	})

	ownerUserID := uint(20)
	job := buildMinimalJob(ownerUserID, "chat_session", "session-999", "hash-fail-test")

	// Track timing: caller should not be blocked.
	start := time.Now()
	processJobSync(worker, job)
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 5*time.Second, "processJob should return quickly (no blocking)")

	// Assert: context_summary row exists with status='failed'.
	var row model.ContextSummary
	err := db.Where("scope_type = ? AND scope_id = ? AND source_hash = ?",
		"chat_session", "session-999", "hash-fail-test").First(&row).Error
	require.NoError(t, err, "expected context_summary row to be persisted even on failure")

	assert.Equal(t, "failed", row.Status, "status must be 'failed' on compression error")
	assert.NotEmpty(t, row.ErrorMessage, "error_message must be set on failure")
	require.NotNil(t, row.OwnerUserID, "owner_user_id must be set even on failure")
	assert.Equal(t, ownerUserID, *row.OwnerUserID)
}

// TestSummaryWorkerFailureAlsoPatchesEventStatus verifies that when compression fails
// and the job carries a non-zero EventID, the worker patches the corresponding
// context_budget_event row to status='failed' and error_code='compression_failed'
// (spec §5.4 acceptance condition).
func TestSummaryWorkerFailureAlsoPatchesEventStatus(t *testing.T) {
	db := newWorkerTestDB(t)
	cbStore := store.NewContextBudgetStore(db)
	ctx := context.Background()

	// Create a real event row so we have a valid ID to patch.
	uid := uint(30)
	event := &model.ContextBudgetEvent{
		UserID:    &uid,
		Operation: "chatbot_chat",
		Provider:  "volc",
		Model:     "deepseek-v3",
		Status:    "pending",
	}
	require.NoError(t, cbStore.CreateEvent(ctx, event))
	require.NotZero(t, event.ID)

	compressor := &workerStubCompressor{err: errors.New("LLM unavailable")}

	worker := NewSummaryWorker(cbStore, compressor, WorkerOptions{
		QueueSize: 10,
	})

	ownerUserID := uint(30)
	job := buildMinimalJob(ownerUserID, "chat_session", "session-event-patch", "hash-event-patch")
	job.EventID = event.ID // associate the event

	processJobSync(worker, job)

	// Assert: context_summary row has status='failed'.
	var summaryRow model.ContextSummary
	err := db.Where("scope_type = ? AND scope_id = ? AND source_hash = ?",
		"chat_session", "session-event-patch", "hash-event-patch").First(&summaryRow).Error
	require.NoError(t, err, "expected context_summary row to be persisted")
	assert.Equal(t, "failed", summaryRow.Status)

	// Assert: event row was patched to status='failed' and error_code='compression_failed'.
	var eventRow model.ContextBudgetEvent
	require.NoError(t, db.First(&eventRow, event.ID).Error)
	assert.Equal(t, "failed", eventRow.Status,
		"context_budget_event.status must be 'failed' when compression fails")
	assert.Equal(t, "compression_failed", eventRow.ErrorCode,
		"context_budget_event.error_code must be 'compression_failed'")
}

// TestSummaryWorkerDoesNotLookupSummaryWithoutOwnerUserID verifies that a job
// with OwnerUserID=0 is rejected defensively to prevent cross-tenant pollution.
func TestSummaryWorkerDoesNotLookupSummaryWithoutOwnerUserID(t *testing.T) {
	db := newWorkerTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	successFrag := contextbudget.ContextFragment{
		ID:      "summary-frag",
		Content: "should never be stored",
	}
	compressor := &workerStubCompressor{result: successFrag}

	worker := NewSummaryWorker(cbStore, compressor, WorkerOptions{
		QueueSize: 10,
	})

	// Job with OwnerUserID=0 — invalid input.
	job := SummaryJob{
		UserID:      0, // zero = no real user
		OwnerUserID: 0,
		ScopeType:   "sop_run",
		ScopeID:     "run-should-not-save",
		SourceHash:  "hash-zero-owner",
		Fragments: []contextbudget.ContextFragment{
			{
				ID:      "frag-1",
				Content: "some content",
			},
		},
		Operation: "sop_run",
	}

	processJobSync(worker, job)

	// Assert: no context_summary rows were written (cross-tenant protection).
	var count int64
	db.Model(&model.ContextSummary{}).
		Where("scope_id = ?", "run-should-not-save").
		Count(&count)
	assert.Equal(t, int64(0), count,
		"worker must not write any summary when OwnerUserID=0 (cross-tenant protection)")
}
