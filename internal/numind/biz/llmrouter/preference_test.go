package llmrouter

import (
	"context"
	"testing"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ============================================================================
// llmrouter.SavePreference regression tests (Task 10, S3 P1-D + plan earlier)
//
// These tests lock down the thinking-flag semantics of SavePreference after the
// ai_service migration (Task 7a/7b reset intrinsic flags + explicit variants):
//
//   1. SavePreference accepts thinking-capable base models (not hard-rejected
//      post-migration when the row has supports_thinking=1 + thinking_only=1)
//   2. DeepSeek base (supports_thinking=1, thinking_only=0) no longer gets
//      force-promoted to thinking=1 when caller asks thinking=false
//   3. Gemini intrinsic (supports_thinking=1, thinking_only=1) is still
//      force-promoted to thinking=1 regardless of caller's thinking=false
// ============================================================================

// newPreferenceTestDB creates an isolated in-memory SQLite DB with the minimal
// schema SavePreference reads/writes against:
//   - ai_service (the AIService row for the candidate model)
//   - task_profile (binding resolver)
//   - task_profile_service (allowed role join)
//   - user_model_preference (target of Upsert)
func newPreferenceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	require.NoError(t, db.AutoMigrate(
		&model.AIService{},
		&model.TaskProfile{},
		&model.TaskProfileService{},
		&model.UserModelPreference{},
	), "auto-migrate")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// seedAllowedService inserts an ai_service row and binds it to the given
// Task Profile (creating the task_profile row if missing) under the "allowed"
// role so Router.isAllowedForFeature picks it up. Returns the seeded AIService.
func seedAllowedService(t *testing.T, db *gorm.DB, taskID string, svc *model.AIService) *model.AIService {
	t.Helper()

	// 1. Ensure ai_service row
	svc.IsActive = true
	require.NoError(t, db.Create(svc).Error, "create ai_service")
	// GORM default:true on IsActive write-back, we explicitly set above so no
	// is_active=false case to patch here.

	// 2. Ensure task_profile row
	var tp model.TaskProfile
	err := db.Where("task_id = ?", taskID).First(&tp).Error
	if err == gorm.ErrRecordNotFound {
		tp = model.TaskProfile{
			TaskID:      taskID,
			DisplayName: taskID,
			ServiceType: "llm",
		}
		require.NoError(t, db.Create(&tp).Error, "create task_profile")
	} else {
		require.NoError(t, err)
	}

	// 3. Bind service as "allowed" role
	binding := &model.TaskProfileService{
		TaskProfileID: tp.ID,
		ServiceID:     svc.ID,
		Role:          model.TaskProfileRoleAllowed,
	}
	require.NoError(t, db.Create(binding).Error, "create task_profile_service binding")

	return svc
}

// TestSavePreference_ThinkingVariantModel_Accepts verifies that after the
// Task 7a/7b migration, SavePreference no longer hard-rejects a
// thinking-capable row when caller opts in with thinking=true.
//
// Regression protection for preference.go:246 — the thinking validation
// previously rejected models that had supports_thinking=1 before the
// migration swept intrinsic flags correctly.
func TestSavePreference_ThinkingVariantModel_Accepts(t *testing.T) {
	db := newPreferenceTestDB(t)
	ds := store.NewTestStore(db)
	router := New(ds)

	// Seed: claude-sonnet-4-6-thinking with supports_thinking=1 + thinking_only=1.
	// is_thinking=false (base row, not a variant flagged-as-variant) so the
	// IsThinking gate at preference.go:237 does not reject.
	seedAllowedService(t, db, "chatbot.stream", &model.AIService{
		ModelKey:         "claude-sonnet-4-6-thinking",
		DisplayName:      "Claude Sonnet 4.6 Thinking",
		ServiceType:      "llm",
		SupportsThinking: true,
		ThinkingOnly:     true,
		IsThinking:       false,
	})

	ctx := context.Background()
	err := router.SavePreference(ctx, 42, "chatbot", "claude-sonnet-4-6-thinking", true)
	require.NoError(t, err, "SavePreference must accept a supports_thinking=1 model with thinking=true")

	// Assert: row persisted with thinking=1
	var pref model.UserModelPreference
	require.NoError(t, db.Where("user_id = ? AND feature = ?", 42, "chatbot").First(&pref).Error)
	assert.Equal(t, "claude-sonnet-4-6-thinking", pref.ModelKey)
	assert.True(t, pref.Thinking, "thinking flag should persist as true")
}

// TestSavePreference_ThinkingOnlyNoLongerForces_DeepSeek verifies S3 P1-D:
// after Task 7a migration reset DeepSeek's thinking_only from 1 → 0, saving
// thinking=false on DeepSeek base must NOT be force-promoted to thinking=1
// (preference.go:242 only forces when thinking_only=1).
func TestSavePreference_ThinkingOnlyNoLongerForces_DeepSeek(t *testing.T) {
	db := newPreferenceTestDB(t)
	ds := store.NewTestStore(db)
	router := New(ds)

	// Post-migration DeepSeek state: supports_thinking=1, thinking_only=0
	seedAllowedService(t, db, "sop.text", &model.AIService{
		ModelKey:         "deepseek-v3.2",
		DisplayName:      "DeepSeek V3.2",
		ServiceType:      "llm",
		SupportsThinking: true,
		ThinkingOnly:     false,
		IsThinking:       false,
	})

	ctx := context.Background()
	err := router.SavePreference(ctx, 101, "sop", "deepseek-v3.2", false)
	require.NoError(t, err)

	// Assert: thinking stays false (NOT force-promoted)
	var pref model.UserModelPreference
	require.NoError(t, db.Where("user_id = ? AND feature = ?", 101, "sop").First(&pref).Error)
	assert.Equal(t, "deepseek-v3.2", pref.ModelKey)
	assert.False(t, pref.Thinking,
		"post-migration DeepSeek (thinking_only=0) must NOT force-promote thinking=false → true")
}

// TestSavePreference_ThinkingOnlyStillForces_Gemini verifies S3 P1-D
// intrinsic-thinking semantic is preserved: Gemini (thinking_only=1) still
// force-promotes thinking=false → true via preference.go:242, because the
// provider cannot turn off its reasoning trace.
func TestSavePreference_ThinkingOnlyStillForces_Gemini(t *testing.T) {
	db := newPreferenceTestDB(t)
	ds := store.NewTestStore(db)
	router := New(ds)

	// Gemini intrinsic kept: supports_thinking=1, thinking_only=1
	seedAllowedService(t, db, "chatbot.stream", &model.AIService{
		ModelKey:         "gemini-3.1-pro-preview",
		DisplayName:      "Gemini 3.1 Pro Preview",
		ServiceType:      "llm",
		SupportsThinking: true,
		ThinkingOnly:     true,
		IsThinking:       false,
	})

	ctx := context.Background()
	err := router.SavePreference(ctx, 202, "chatbot", "gemini-3.1-pro-preview", false)
	require.NoError(t, err)

	// Assert: thinking is force-promoted to true despite caller's false
	var pref model.UserModelPreference
	require.NoError(t, db.Where("user_id = ? AND feature = ?", 202, "chatbot").First(&pref).Error)
	assert.Equal(t, "gemini-3.1-pro-preview", pref.ModelKey)
	assert.True(t, pref.Thinking,
		"Gemini (thinking_only=1) must force-promote thinking=false → true (intrinsic)")
}
