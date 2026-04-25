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
	found, err := s.FindReadySummary(ctx, owner10, "sop_session", "sess-abc", "hash-xyz")
	require.NoError(t, err)
	require.NotNil(t, found, "should find a summary for owner=10")
	assert.Equal(t, sum10.ID, found.ID, "must return the owner=10 row, not owner=20")
	assert.Equal(t, "summary for owner 10", found.SummaryText)

	// Lookup with owner=20 must return the owner=20 row.
	found20, err := s.FindReadySummary(ctx, owner20, "sop_session", "sess-abc", "hash-xyz")
	require.NoError(t, err)
	require.NotNil(t, found20)
	assert.Equal(t, sum20.ID, found20.ID, "must return the owner=20 row")

	// Lookup with a non-existent owner must return nil / not-found error.
	_, err = s.FindReadySummary(ctx, uint(999), "sop_session", "sess-abc", "hash-xyz")
	assert.Error(t, err, "non-existent owner+scope+hash should return an error")
}

// TestContextBudgetStore_GetActiveTokenProfile_FiltersFallback verifies that
// GetActiveTokenProfile with IsFallback=false only matches non-fallback rows, and
// IsFallback=true only matches fallback rows, even when both exist for the same
// (provider, model, service_type) key. (P2-B fix)
func TestContextBudgetStore_GetActiveTokenProfile_FiltersFallback(t *testing.T) {
	db := newContextBudgetTestDB(t)
	s := NewContextBudgetStore(db)
	ctx := context.Background()

	profileJSON := datatypes.JSON([]byte(`{"avg_chars_per_token":4.0}`))

	// Insert an exact (non-fallback) active row directly, bypassing SaveTokenProfileVersion
	// to keep is_fallback=false explicit.
	exact := &model.TokenEstimationProfile{
		Provider:              "volc",
		Model:                 "deepseek-v3",
		ServiceType:           "llm_chat",
		ModelFamily:           "deepseek",
		ProfileJSON:           profileJSON,
		SafetyMultiplier:      1.15,
		CalibrationMultiplier: 1.0,
		IsFallback:            false,
		Version:               1,
		IsActive:              true,
		ChangeReason:          "exact",
		UpdatedBy:             "test",
	}
	require.NoError(t, db.Create(exact).Error)

	// Insert a fallback active row for the same key dimensions.
	fallback := &model.TokenEstimationProfile{
		Provider:              "volc",
		Model:                 "deepseek-v3",
		ServiceType:           "llm_chat",
		ModelFamily:           "deepseek",
		ProfileJSON:           profileJSON,
		SafetyMultiplier:      1.30,
		CalibrationMultiplier: 1.0,
		IsFallback:            true,
		Version:               1,
		IsActive:              true,
		ChangeReason:          "fallback",
		UpdatedBy:             "test",
	}
	require.NoError(t, db.Create(fallback).Error)
	// Correct GORM default:true gotcha for IsFallback if needed (IsFallback=true is non-zero, OK).

	// Exact lookup (IsFallback=false) must return the exact row.
	got, err := s.GetActiveTokenProfile(ctx, TokenProfileLookupKey{
		Provider:    "volc",
		Model:       "deepseek-v3",
		ServiceType: "llm_chat",
		IsFallback:  false,
	})
	require.NoError(t, err)
	assert.Equal(t, exact.ID, got.ID, "IsFallback=false should return non-fallback row")
	assert.False(t, got.IsFallback, "returned row must have IsFallback=false")

	// Fallback lookup (IsFallback=true) must return the fallback row.
	gotFB, err := s.GetActiveTokenProfile(ctx, TokenProfileLookupKey{
		Provider:    "volc",
		Model:       "deepseek-v3",
		ServiceType: "llm_chat",
		IsFallback:  true,
	})
	require.NoError(t, err)
	assert.Equal(t, fallback.ID, gotFB.ID, "IsFallback=true should return fallback row")
	assert.True(t, gotFB.IsFallback, "returned row must have IsFallback=true")
}

// TestContextBudgetStore_MultipleActiveProfilesReturnsNewest verifies that when
// two active rows exist for the same key (data integrity anomaly), GetActiveTokenProfile
// returns the newest one (highest version / id). (P2-A fix)
func TestContextBudgetStore_MultipleActiveProfilesReturnsNewest(t *testing.T) {
	db := newContextBudgetTestDB(t)
	s := NewContextBudgetStore(db)
	ctx := context.Background()

	profileJSON := datatypes.JSON([]byte(`{"avg_chars_per_token":4.0}`))

	// Insert two active rows directly, bypassing SaveTokenProfileVersion
	// to simulate the anomaly (both active = data integrity issue).
	older := &model.TokenEstimationProfile{
		Provider:              "ali",
		Model:                 "qwen-turbo",
		ServiceType:           "llm_chat",
		ModelFamily:           "qwen",
		ProfileJSON:           profileJSON,
		SafetyMultiplier:      1.10,
		CalibrationMultiplier: 1.0,
		IsFallback:            false,
		Version:               1,
		IsActive:              true,
		ChangeReason:          "v1",
		UpdatedBy:             "test",
	}
	require.NoError(t, db.Create(older).Error)

	newer := &model.TokenEstimationProfile{
		Provider:              "ali",
		Model:                 "qwen-turbo",
		ServiceType:           "llm_chat",
		ModelFamily:           "qwen",
		ProfileJSON:           profileJSON,
		SafetyMultiplier:      1.20,
		CalibrationMultiplier: 1.05,
		IsFallback:            false,
		Version:               2,
		IsActive:              true, // anomaly: v1 was not deactivated
		ChangeReason:          "v2",
		UpdatedBy:             "test",
	}
	require.NoError(t, db.Create(newer).Error)
	// newer has a higher ID and higher version — should be returned first by ORDER BY version DESC, id DESC.

	got, err := s.GetActiveTokenProfile(ctx, TokenProfileLookupKey{
		Provider:    "ali",
		Model:       "qwen-turbo",
		ServiceType: "llm_chat",
		IsFallback:  false,
	})
	require.NoError(t, err)
	assert.Equal(t, newer.ID, got.ID, "should return newest (higher version) active row")
	assert.Equal(t, uint(2), got.Version, "returned row must be version 2")
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

// TestContextBudgetStore_SaveTokenProfile_DoesNotDeactivateOtherFallbackKind verifies
// the F1 fix: when (provider, model, service_type) has both a fallback=true and a
// fallback=false active row, saving a new fallback=false version must NOT deactivate
// the fallback=true row.
func TestContextBudgetStore_SaveTokenProfile_DoesNotDeactivateOtherFallbackKind(t *testing.T) {
	db := newContextBudgetTestDB(t)
	s := NewContextBudgetStore(db)
	ctx := context.Background()

	profileJSON := datatypes.JSON([]byte(`{}`))

	// Directly insert two active rows: one is_fallback=false, one is_fallback=true.
	exact := &model.TokenEstimationProfile{
		Provider: "volc", Model: "deepseek-v3", ServiceType: "llm_chat",
		SafetyMultiplier: 1.15, CalibrationMultiplier: 1.0,
		ProfileJSON: profileJSON,
		Version:     1, IsActive: true, IsFallback: false,
	}
	fallbackRow := &model.TokenEstimationProfile{
		Provider: "volc", Model: "deepseek-v3", ServiceType: "llm_chat",
		SafetyMultiplier: 1.30, CalibrationMultiplier: 1.0,
		ProfileJSON: profileJSON,
		Version:     1, IsActive: true, IsFallback: true,
	}
	require.NoError(t, db.Create(exact).Error)
	require.NoError(t, db.Create(fallbackRow).Error)

	// Save a new fallback=false version — must only deactivate the existing fallback=false row.
	saved, err := s.SaveTokenProfileVersion(ctx, SaveTokenProfileInput{
		Provider: "volc", Model: "deepseek-v3", ServiceType: "llm_chat",
		SafetyMultiplier: 1.20, CalibrationMultiplier: 1.0,
		ProfileJSON: profileJSON,
		IsFallback:  false,
	})
	require.NoError(t, err)
	require.True(t, saved.IsActive)
	require.False(t, saved.IsFallback)
	assert.Equal(t, uint(2), saved.Version, "version should increment from fallback=false v1")

	// The fallback=true row must remain active (F1 fix).
	var fallbackRefreshed model.TokenEstimationProfile
	require.NoError(t, db.First(&fallbackRefreshed, fallbackRow.ID).Error)
	assert.True(t, fallbackRefreshed.IsActive, "fallback=true row must remain active after saving fallback=false")

	// The original fallback=false v1 row must be deactivated.
	var exactRefreshed model.TokenEstimationProfile
	require.NoError(t, db.First(&exactRefreshed, exact.ID).Error)
	assert.False(t, exactRefreshed.IsActive, "fallback=false v1 must be deactivated")
}

// TestContextBudgetStore_PatchEvent_EmptyPatchIsNoop verifies that calling PatchEvent
// with an all-nil EventPatch returns nil error and performs no UPDATE (short-circuit).
func TestContextBudgetStore_PatchEvent_EmptyPatchIsNoop(t *testing.T) {
	db := newContextBudgetTestDB(t)
	s := NewContextBudgetStore(db)
	ctx := context.Background()

	uid := uint(7)
	event := &model.ContextBudgetEvent{
		UserID:               &uid,
		Operation:            "sop_run",
		TaskID:               "task-noop",
		Provider:             "volc",
		Model:                "deepseek-v3",
		ContextWindow:        128000,
		MaxOutputTokens:      2048,
		ReservedOutputTokens: 256,
		FixedOverheadTokens:  128,
		SafeRatio:            0.85,
		SafeInputBudget:      109184,
		EstimatedBefore:      30000,
		EstimatedAfter:       30000,
		Status:               "ok",
	}
	require.NoError(t, s.CreateEvent(ctx, event))

	// Apply an empty patch — all fields nil.
	err := s.PatchEvent(ctx, event.ID, EventPatch{})
	require.NoError(t, err, "empty patch must not return an error")

	// Read back: all fields must be unchanged.
	var row model.ContextBudgetEvent
	require.NoError(t, db.First(&row, event.ID).Error)
	assert.Equal(t, "ok", row.Status, "status must remain unchanged after empty patch")
	assert.Equal(t, "task-noop", row.TaskID, "task_id must remain unchanged after empty patch")
}

// TestContextBudgetStore_FindReadySummary_StatusFiltering verifies that summaries
// with status != 'ready' are not returned by FindReadySummary.
func TestContextBudgetStore_FindReadySummary_StatusFiltering(t *testing.T) {
	db := newContextBudgetTestDB(t)
	s := NewContextBudgetStore(db)
	ctx := context.Background()

	ownerID := uint(55)
	fragIDs := datatypes.JSON([]byte(`[10,20]`))

	// Insert a summary with status='pending' (not 'ready').
	pendingSummary := &model.ContextSummary{
		UserID:            55,
		OwnerUserID:       &ownerID,
		ScopeType:         "sop_session",
		ScopeID:           "sess-filter",
		SourceHash:        "hash-filter",
		SourceFragmentIDs: fragIDs,
		SummaryText:       "pending summary",
		Status:            "pending",
	}
	require.NoError(t, s.UpsertSummary(ctx, pendingSummary))
	require.NotZero(t, pendingSummary.ID)

	// FindReadySummary must return not-found because status != 'ready'.
	_, err := s.FindReadySummary(ctx, ownerID, "sop_session", "sess-filter", "hash-filter")
	require.Error(t, err, "pending summary must not be returned by FindReadySummary")
	assert.ErrorContains(t, err, "not found", "error must indicate record not found")

	// Now update status to 'ready' and verify it is now found.
	require.NoError(t, db.Model(pendingSummary).UpdateColumn("status", "ready").Error)

	found, err := s.FindReadySummary(ctx, ownerID, "sop_session", "sess-filter", "hash-filter")
	require.NoError(t, err, "ready summary must be found")
	require.NotNil(t, found)
	assert.Equal(t, pendingSummary.ID, found.ID, "must return the same row after status update to ready")
}
