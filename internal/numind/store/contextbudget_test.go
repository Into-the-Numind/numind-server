package store

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

// newContextBudgetTestDB creates an isolated in-memory SQLite DB for context budget store tests.
func newContextBudgetTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	require.NoError(t, db.AutoMigrate(
		&model.TokenEstimationProfile{},
		&model.ContextBudgetPolicy{},
		&model.ContextSummary{},
		&model.ContextBudgetEvent{},
	), "auto-migrate context budget tables")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// TestContextBudgetStore_SaveTokenProfileDeactivatesPriorActive verifies that
// saving a new token profile version deactivates all prior active rows with the
// same (provider, model, service_type) key, and the new row gets version=2 and
// is_active=true.
func TestContextBudgetStore_SaveTokenProfileDeactivatesPriorActive(t *testing.T) {
	db := newContextBudgetTestDB(t)
	s := NewContextBudgetStore(db)
	ctx := context.Background()

	profileJSON := datatypes.JSON([]byte(`{"avg_chars_per_token":4.0}`))

	// Save version 1.
	v1, err := s.SaveTokenProfileVersion(ctx, SaveTokenProfileInput{
		Provider:              "volc",
		Model:                 "deepseek-v3",
		ModelFamily:           "deepseek",
		ServiceType:           "llm_chat",
		ProfileJSON:           profileJSON,
		SafetyMultiplier:      1.15,
		CalibrationMultiplier: 1.0,
		ChangeReason:          "initial",
		UpdatedBy:             "test",
	})
	require.NoError(t, err)
	assert.Equal(t, uint(1), v1.Version, "first save should be version 1")
	assert.True(t, v1.IsActive, "first save should be active")

	// Save version 2.
	v2, err := s.SaveTokenProfileVersion(ctx, SaveTokenProfileInput{
		Provider:              "volc",
		Model:                 "deepseek-v3",
		ModelFamily:           "deepseek",
		ServiceType:           "llm_chat",
		ProfileJSON:           profileJSON,
		SafetyMultiplier:      1.20,
		CalibrationMultiplier: 1.05,
		ChangeReason:          "recalibrated",
		UpdatedBy:             "test",
	})
	require.NoError(t, err)
	assert.Equal(t, uint(2), v2.Version, "second save should be version 2")
	assert.True(t, v2.IsActive, "second save should be active")

	// Verify v1 is now inactive in the database.
	var v1row model.TokenEstimationProfile
	require.NoError(t, db.First(&v1row, v1.ID).Error)
	assert.False(t, v1row.IsActive, "prior active row (v1) must be deactivated after v2 save")

	// Verify exactly one active row exists.
	var activeCount int64
	require.NoError(t, db.Model(&model.TokenEstimationProfile{}).
		Where("provider = ? AND model = ? AND service_type = ? AND is_active = ?", "volc", "deepseek-v3", "llm_chat", true).
		Count(&activeCount).Error)
	assert.Equal(t, int64(1), activeCount, "only one active row should exist after versioned save")

	// GetActiveTokenProfile should return v2.
	got, err := s.GetActiveTokenProfile(ctx, TokenProfileLookupKey{
		Provider:    "volc",
		Model:       "deepseek-v3",
		ServiceType: "llm_chat",
	})
	require.NoError(t, err)
	assert.Equal(t, v2.ID, got.ID, "GetActiveTokenProfile should return v2")
	assert.InDelta(t, 1.20, got.SafetyMultiplier, 0.001)

	// Sub-test: ChargeUser=false GORM default:true gotcha protection for policies
	// (tested separately in TestContextBudgetStore_SavePolicyDeactivatesPriorActive).
}

// TestContextBudgetStore_SavePolicyDeactivatesPriorActive verifies that saving a
// new policy version deactivates the prior active row for the same operation,
// version numbers increment correctly, and the GORM default:true bool gotcha is
// handled for ChargeUser=false.
func TestContextBudgetStore_SavePolicyDeactivatesPriorActive(t *testing.T) {
	db := newContextBudgetTestDB(t)
	s := NewContextBudgetStore(db)
	ctx := context.Background()

	// Save version 1 with ChargeUser=true.
	v1, err := s.SavePolicyVersion(ctx, SavePolicyInput{
		Operation:            "sop_run",
		ReservedOutputTokens: 512,
		SafeRatio:            0.85,
		FixedOverheadTokens:  256,
		SoftThresholdRatio:   0.70,
		HardThresholdRatio:   0.85,
		ChargeUser:           true,
		ChangeReason:         "initial",
		UpdatedBy:            "admin",
	})
	require.NoError(t, err)
	assert.Equal(t, uint(1), v1.Version)
	assert.True(t, v1.IsActive)
	assert.True(t, v1.ChargeUser)

	// Save version 2 with ChargeUser=false (triggers GORM default:true gotcha).
	v2, err := s.SavePolicyVersion(ctx, SavePolicyInput{
		Operation:            "sop_run",
		ReservedOutputTokens: 1024,
		SafeRatio:            0.80,
		FixedOverheadTokens:  512,
		SoftThresholdRatio:   0.65,
		HardThresholdRatio:   0.80,
		ChargeUser:           false, // must be persisted as false, not defaulted to true
		ChangeReason:         "no charge for compression",
		UpdatedBy:            "admin",
	})
	require.NoError(t, err)
	assert.Equal(t, uint(2), v2.Version, "second save should increment version")
	assert.True(t, v2.IsActive)

	// GORM default:true gotcha: verify charge_user=false is actually persisted.
	var v2row model.ContextBudgetPolicy
	require.NoError(t, db.First(&v2row, v2.ID).Error)
	assert.False(t, v2row.ChargeUser, "ChargeUser=false must be persisted; GORM default:true gotcha must be handled")

	// Verify v1 is deactivated.
	var v1row model.ContextBudgetPolicy
	require.NoError(t, db.First(&v1row, v1.ID).Error)
	assert.False(t, v1row.IsActive, "prior active row (v1) must be deactivated")

	// Exactly one active row.
	var activeCount int64
	require.NoError(t, db.Model(&model.ContextBudgetPolicy{}).
		Where("operation = ? AND is_active = ?", "sop_run", true).
		Count(&activeCount).Error)
	assert.Equal(t, int64(1), activeCount, "only one active policy should exist")

	// GetActivePolicy should return v2.
	got, err := s.GetActivePolicy(ctx, "sop_run")
	require.NoError(t, err)
	assert.Equal(t, v2.ID, got.ID, "GetActivePolicy should return v2")
	assert.Equal(t, 1024, got.ReservedOutputTokens)
}

// TestContextBudgetStore_SummaryLookupRequiresOwnerScopeAndHash verifies that
// FindReadySummary enforces tenant isolation: two summaries with identical
// scope_type, scope_id, and source_hash but different owner_user_id must not
// cross over — querying with owner=10 must not return the owner=20 row.
func TestContextBudgetStore_SummaryLookupRequiresOwnerScopeAndHash(t *testing.T) {
	db := newContextBudgetTestDB(t)
	s := NewContextBudgetStore(db)
	ctx := context.Background()

	owner10 := uint(10)
	owner20 := uint(20)

	fragIDs := datatypes.JSON([]byte(`[1,2,3]`))

	// Insert summary for owner 10.
	sum10 := &model.ContextSummary{
		UserID:            10,
		OwnerUserID:       &owner10,
		ScopeType:         "sop_session",
		ScopeID:           "sess-abc",
		SourceHash:        "hash-xyz",
		SourceFragmentIDs: fragIDs,
		SummaryText:       "summary for owner 10",
		Status:            "ready",
	}
	require.NoError(t, s.UpsertSummary(ctx, sum10))
	require.NotZero(t, sum10.ID)

	// Insert summary for owner 20 — same scope+hash, different owner.
	sum20 := &model.ContextSummary{
		UserID:            20,
		OwnerUserID:       &owner20,
		ScopeType:         "sop_session",
		ScopeID:           "sess-abc",
		SourceHash:        "hash-xyz",
		SourceFragmentIDs: fragIDs,
		SummaryText:       "summary for owner 20",
		Status:            "ready",
	}
	require.NoError(t, s.UpsertSummary(ctx, sum20))
	require.NotZero(t, sum20.ID)

	// Sanity: IDs are distinct (two separate rows).
	assert.NotEqual(t, sum10.ID, sum20.ID, "distinct owners must produce distinct rows")

	// Lookup with owner=10 must return the owner=10 row.
	found, err := s.FindReadySummary(ctx, uint64(owner10), "sop_session", "sess-abc", "hash-xyz")
	require.NoError(t, err)
	require.NotNil(t, found, "should find a summary for owner=10")
	assert.Equal(t, sum10.ID, found.ID, "must return the owner=10 row, not owner=20")
	assert.Equal(t, "summary for owner 10", found.SummaryText)

	// Lookup with owner=20 must return the owner=20 row.
	found20, err := s.FindReadySummary(ctx, uint64(owner20), "sop_session", "sess-abc", "hash-xyz")
	require.NoError(t, err)
	require.NotNil(t, found20)
	assert.Equal(t, sum20.ID, found20.ID, "must return the owner=20 row")

	// Lookup with a non-existent owner must return nil / not-found error.
	_, err = s.FindReadySummary(ctx, 999, "sop_session", "sess-abc", "hash-xyz")
	assert.Error(t, err, "non-existent owner+scope+hash should return an error")
}

// TestContextBudgetStore_CreateAndPatchEvent verifies that an event can be created
// and subsequently patched with partial field updates (nil fields are left untouched).
func TestContextBudgetStore_CreateAndPatchEvent(t *testing.T) {
	db := newContextBudgetTestDB(t)
	s := NewContextBudgetStore(db)
	ctx := context.Background()

	uid := uint(42)
	event := &model.ContextBudgetEvent{
		UserID:               &uid,
		Operation:            "sop_run",
		TaskID:               "task-001",
		Provider:             "volc",
		Model:                "deepseek-v3",
		ContextWindow:        128000,
		MaxOutputTokens:      4096,
		ReservedOutputTokens: 512,
		FixedOverheadTokens:  256,
		SafeRatio:            0.85,
		SafeInputBudget:      108032,
		EstimatedBefore:      50000,
		EstimatedAfter:       40000,
		Status:               "ok",
	}

	// CreateEvent.
	require.NoError(t, s.CreateEvent(ctx, event))
	assert.NotZero(t, event.ID, "event should be assigned an ID")

	// PatchEvent: update some fields, leave others nil.
	actualPrompt := 52000
	actualCompletion := 3800
	reserveAmt := int64(1500)
	status := "reconciled"
	patch := EventPatch{
		ActualPromptTokens:     &actualPrompt,
		ActualCompletionTokens: &actualCompletion,
		ReserveAmount:          &reserveAmt,
		Status:                 &status,
		// ReconcileDelta, CalibrationRatio etc. intentionally left nil.
	}

	require.NoError(t, s.PatchEvent(ctx, event.ID, patch))

	// Read back and verify patched fields.
	var row model.ContextBudgetEvent
	require.NoError(t, db.First(&row, event.ID).Error)

	require.NotNil(t, row.ActualPromptTokens)
	assert.Equal(t, 52000, *row.ActualPromptTokens)
	require.NotNil(t, row.ActualCompletionTokens)
	assert.Equal(t, 3800, *row.ActualCompletionTokens)
	require.NotNil(t, row.ReserveAmount)
	assert.Equal(t, int64(1500), *row.ReserveAmount)
	assert.Equal(t, "reconciled", row.Status)

	// Non-patched fields must retain original values.
	assert.Equal(t, "sop_run", row.Operation)
	assert.Equal(t, "task-001", row.TaskID)
	assert.Equal(t, 50000, row.EstimatedBefore)
	assert.Nil(t, row.ReconcileDelta, "unpatched ReconcileDelta should remain nil")
}
