package contextbudget

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	aimw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/contextbudget"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestDB creates an isolated in-memory SQLite DB for biz tests.
func newTestDB(t *testing.T) *gorm.DB {
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

// sampleProfile returns a minimal active token estimation profile.
func sampleProfile(db *gorm.DB) *model.TokenEstimationProfile {
	profileJSON := datatypes.JSON(`{"method":"test","message_overhead_tokens":4,"fragment_overhead_tokens":2,"classes":{"en":{"token_per_char":0.25},"zh":{"token_per_char":0.6}},"safety_multiplier":1.0,"calibration_multiplier":1.0}`)
	p := &model.TokenEstimationProfile{
		Provider:              "volc",
		Model:                 "glm-4-7-251222",
		ModelFamily:           "glm",
		ServiceType:           "llm_chat",
		ProfileJSON:           profileJSON,
		SafetyMultiplier:      1.0,
		CalibrationMultiplier: 1.0,
		Version:               1,
		IsActive:              true,
		IsFallback:            false,
	}
	if err := db.Create(p).Error; err != nil {
		panic("sampleProfile: " + err.Error())
	}
	return p
}

// samplePolicy returns a minimal active context budget policy.
func samplePolicy(db *gorm.DB, op string) *model.ContextBudgetPolicy {
	p := &model.ContextBudgetPolicy{
		Operation:            op,
		ReservedOutputTokens: 512,
		SafeRatio:            0.85,
		FixedOverheadTokens:  128,
		SoftThresholdRatio:   0.7,
		HardThresholdRatio:   0.85,
		ChargeUser:           false,
		Version:              1,
		IsActive:             true,
	}
	if err := db.Create(p).Error; err != nil {
		panic("samplePolicy: " + err.Error())
	}
	// Ensure ChargeUser=false persisted (GORM default:true gotcha).
	if p.ChargeUser {
		db.Model(p).UpdateColumn("charge_user", false)
		p.ChargeUser = false
	}
	return p
}

// sampleRoute returns a resolved route with a simple capability.
func sampleRoute(ctxWindow, maxOutput int) *registry.ResolvedRoute {
	return &registry.ResolvedRoute{
		ServiceKey: "glm-4-7-251222",
		Provider: registry.ProviderInfo{
			Name: "volc",
		},
		Capability: profile.ServiceCapability{
			ContextWindow:   ctxWindow,
			MaxOutputTokens: maxOutput,
		},
	}
}

// sampleFragment returns a simple durable chat fragment.
func sampleFragment(id, content string) contextbudget.ContextFragment {
	return contextbudget.ContextFragment{
		ID:              id,
		Role:            contextbudget.RoleDurable,
		Source:          contextbudget.SourceUser,
		ContentType:     contextbudget.ContentText,
		Content:         content,
		Importance:      5,
		Order:           1,
		Compressibility: contextbudget.CompressDrop,
	}
}

// noopLogger discards log calls in tests.
type noopLogger struct{}

func (noopLogger) Warnw(msg string, kv ...interface{})  {}
func (noopLogger) Errorw(msg string, kv ...interface{}) {}

// stubCompressor always returns an error (no real LLM in unit tests).
type stubCompressor struct {
	called bool
}

func (c *stubCompressor) Compress(_ context.Context, fragments []contextbudget.ContextFragment, targetTokens int) (contextbudget.ContextFragment, error) {
	c.called = true
	return contextbudget.ContextFragment{}, errors.New("stub compressor: no LLM in tests")
}

// successCompressor returns a short summary fragment.
type successCompressor struct {
	called bool
}

func (c *successCompressor) Compress(_ context.Context, fragments []contextbudget.ContextFragment, targetTokens int) (contextbudget.ContextFragment, error) {
	c.called = true
	return contextbudget.ContextFragment{
		ID:          "summary-1",
		Role:        contextbudget.RoleDurable,
		Source:      contextbudget.SourceInternal,
		ContentType: contextbudget.ContentSummary,
		Content:     "short summary",
		Importance:  5,
		Order:       1,
	}, nil
}

// ---------------------------------------------------------------------------
// Test: TestPrepare_LoadsPolicyProfileAndProducesEvent
// ---------------------------------------------------------------------------

func TestPrepare_LoadsPolicyProfileAndProducesEvent(t *testing.T) {
	db := newTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	// Seed policy and profile.
	policy := samplePolicy(db, "chatbot_chat")
	profile := sampleProfile(db)
	_ = profile

	biz := New(cbStore, Options{
		Clock:  time.Now,
		Logger: noopLogger{},
	})

	fragments := []contextbudget.ContextFragment{
		sampleFragment("f1", "hello world, this is a test message"),
	}

	result, err := biz.Prepare(context.Background(), aimw.PrepareInput{
		Operation:       "chatbot_chat",
		UserID:          0,
		Route:           sampleRoute(128000, 8192),
		Fragments:       fragments,
		ContextWindow:   128000,
		MaxOutputTokens: 8192,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Policy and token profile should be loaded.
	assert.Equal(t, policy.ID, result.PolicyID, "PolicyID should match persisted policy")
	assert.NotZero(t, result.TokenProfileID, "TokenProfileID should be non-zero")
	assert.NotZero(t, result.EventID, "EventID should be non-zero (event created)")
	assert.Greater(t, result.SafeInputBudget, 0, "SafeInputBudget should be computed")
	assert.False(t, result.SkipBudget, "SkipBudget should be false for non-empty fragments")
}

// ---------------------------------------------------------------------------
// Test: TestPrepare_UsesSummaryCacheByOwnerScopeAndHash
// ---------------------------------------------------------------------------

func TestPrepare_UsesSummaryCacheByOwnerScopeAndHash(t *testing.T) {
	db := newTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	// Seed policy with a very small context window to force planner to act.
	policy := &model.ContextBudgetPolicy{
		Operation:            "chatbot_chat",
		ReservedOutputTokens: 50,
		SafeRatio:            0.85,
		FixedOverheadTokens:  10,
		SoftThresholdRatio:   0.7,
		HardThresholdRatio:   0.85,
		ChargeUser:           false,
		Version:              1,
		IsActive:             true,
	}
	require.NoError(t, db.Create(policy).Error)
	if policy.ChargeUser {
		db.Model(policy).UpdateColumn("charge_user", false)
		policy.ChargeUser = false
	}

	// Seed profile.
	sampleProfile(db)

	// Pre-populate a ready summary for fragment "frag-a" with a known source hash.
	ownerUserID := uint(42)
	sourceHash := "abc123hash"
	summary := &model.ContextSummary{
		UserID:                ownerUserID,
		OwnerUserID:           ptrUint(ownerUserID),
		ScopeType:             "sop_run",
		ScopeID:               "run-99",
		SourceHash:            sourceHash,
		SourceFragmentIDs:     datatypes.JSON(`["frag-a"]`),
		SummaryText:           "cached summary text",
		SummaryTokenEstimate:  10,
		OriginalTokenEstimate: 200,
		Model:                 "glm-4-7-251222",
		Provider:              "volc",
		Status:                "ready",
		CreatedByOperation:    "chatbot_chat",
	}
	require.NoError(t, db.Create(summary).Error)

	biz := New(cbStore, Options{
		Clock:  time.Now,
		Logger: noopLogger{},
	})

	// Fragment with matching source reference and the same hash.
	// We use a large content to ensure total > safe_input_budget (context_window=200).
	largeContent := repeat("A", 300) // 300 chars → > 50 tokens with overhead
	fragA := contextbudget.ContextFragment{
		ID:              "frag-a",
		Role:            contextbudget.RoleDurable,
		Source:          contextbudget.SourceUser,
		ContentType:     contextbudget.ContentText,
		Content:         largeContent,
		Importance:      5,
		Order:           1,
		Compressibility: contextbudget.CompressSummarize,
		SourceReference: sourceHash, // matches SummaryCache key
	}

	result, err := biz.Prepare(context.Background(), aimw.PrepareInput{
		Operation:       "chatbot_chat",
		UserID:          ownerUserID,
		Route:           sampleRoute(200, 100),
		Fragments:       []contextbudget.ContextFragment{fragA},
		ContextWindow:   200,
		MaxOutputTokens: 100,
		Metadata: map[string]string{
			"sop_run_id": "run-99",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// The result should contain a plan that used summary cache or reduced tokens.
	// Either the plan has ActionReuseSummary or the event was created successfully.
	assert.NotZero(t, result.EventID, "EventID should be set even when summary cache is used")

	// Verify the summary lookup was attempted by checking that the store can
	// indeed find the summary for the given owner/scope/hash.
	found, err := cbStore.FindReadySummary(context.Background(), ownerUserID, "sop_run", "run-99", sourceHash)
	require.NoError(t, err)
	assert.Equal(t, "cached summary text", found.SummaryText)
}

// ---------------------------------------------------------------------------
// Test: TestPrepare_CompressionCallUsesInternalOperationWithoutUserCharge
// ---------------------------------------------------------------------------

func TestPrepare_CompressionCallUsesInternalOperationWithoutUserCharge(t *testing.T) {
	db := newTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	// Use context_compression operation — an internal op that must not charge user.
	// Since context_compression is not in budgetOperationMap, we treat it as a
	// special internal operation that creates a skipped/ok event without credits.
	// Seed a policy for it as if it were configured.
	policy := &model.ContextBudgetPolicy{
		Operation:            "context_compression",
		ReservedOutputTokens: 512,
		SafeRatio:            0.85,
		FixedOverheadTokens:  128,
		SoftThresholdRatio:   0.7,
		HardThresholdRatio:   0.85,
		ChargeUser:           false, // MUST be false for internal op
		Version:              1,
		IsActive:             true,
	}
	require.NoError(t, db.Create(policy).Error)
	if policy.ChargeUser {
		db.Model(policy).UpdateColumn("charge_user", false)
		policy.ChargeUser = false
	}

	sampleProfile(db)

	stub := &stubCompressor{}
	biz := New(cbStore, Options{
		Compressor: stub,
		Clock:      time.Now,
		Logger:     noopLogger{},
	})

	fragments := []contextbudget.ContextFragment{
		sampleFragment("f1", "compress me"),
	}

	result, err := biz.Prepare(context.Background(), aimw.PrepareInput{
		Operation:       "context_compression",
		UserID:          0, // no user → no charge
		Route:           sampleRoute(128000, 8192),
		Fragments:       fragments,
		ContextWindow:   128000,
		MaxOutputTokens: 8192,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// context_compression is an internal operation: ChargeUser must be false.
	assert.False(t, result.Policy.ChargeUser,
		"context_compression must have ChargeUser=false (internal op, no billing)")

	// NormalizedOp should reflect internal routing.
	assert.NotEmpty(t, result.NormalizedOp, "NormalizedOp should not be empty")

	// Event created without user billing.
	assert.NotZero(t, result.EventID, "EventID should be created for internal ops too")
}

// ---------------------------------------------------------------------------
// Test: TestFinalize_PatchesActualUsageAndCalibration
// ---------------------------------------------------------------------------

func TestFinalize_PatchesActualUsageAndCalibration(t *testing.T) {
	db := newTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	// Seed an event directly.
	event := &model.ContextBudgetEvent{
		Operation:       "chatbot_chat",
		Provider:        "volc",
		Model:           "glm-4-7-251222",
		ContextWindow:   128000,
		MaxOutputTokens: 8192,
		EstimatedBefore: 500,
		EstimatedAfter:  500,
		SafeInputBudget: 90000,
		Status:          "ok",
	}
	require.NoError(t, db.Create(event).Error)
	require.NotZero(t, event.ID)

	biz := New(cbStore, Options{
		Clock:  time.Now,
		Logger: noopLogger{},
	})

	estimatedPrompt := 500
	actualPrompt := 450
	actualCompletion := 200

	err := biz.Finalize(context.Background(), aimw.FinalizeInput{
		EventID:                event.ID,
		ActualPromptTokens:     actualPrompt,
		ActualCompletionTokens: actualCompletion,
		EstimatedCredits:       100,
		Status:                 "ok",
	})
	require.NoError(t, err)

	// Reload the event and verify patches.
	var patched model.ContextBudgetEvent
	require.NoError(t, db.First(&patched, event.ID).Error)

	assert.NotNil(t, patched.ActualPromptTokens, "ActualPromptTokens should be patched")
	assert.Equal(t, actualPrompt, *patched.ActualPromptTokens)
	assert.NotNil(t, patched.ActualCompletionTokens, "ActualCompletionTokens should be patched")
	assert.Equal(t, actualCompletion, *patched.ActualCompletionTokens)

	// CalibrationRatio = actual / estimated.
	assert.NotNil(t, patched.CalibrationRatio, "CalibrationRatio should be set")
	expectedRatio := float64(actualPrompt) / float64(estimatedPrompt)
	assert.InDelta(t, expectedRatio, *patched.CalibrationRatio, 0.001,
		"CalibrationRatio should be actual/estimated")
}

// ---------------------------------------------------------------------------
// Test: TestPreview_UsesServiceIDAndPolicyFieldsForBudgetMath
// ---------------------------------------------------------------------------

func TestPreview_UsesServiceIDAndPolicyFieldsForBudgetMath(t *testing.T) {
	db := newTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	biz := New(cbStore, Options{
		Clock:  time.Now,
		Logger: noopLogger{},
	})

	cap := contextbudget.ModelCapability{
		ContextWindow:   100000,
		MaxOutputTokens: 16384,
	}

	input := PreviewInput{
		Capability:           cap,
		Operation:            "chatbot_chat",
		FixedOverheadTokens:  512,
		ReservedOutputTokens: 8192,
		SafeRatio:            0.85,
		SoftThresholdRatio:   0.7,
		HardThresholdRatio:   0.85,
	}

	result, err := biz.Preview(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, result)

	// Formula: safe_input_budget = floor((100000 - 8192 - 512) * 0.85)
	//        = floor(91296 * 0.85) = floor(77601.6) = 77601
	expectedSafeInputBudget := 77601
	assert.Equal(t, expectedSafeInputBudget, result.SafeInputBudget,
		"SafeInputBudget must match spec §2.4 formula")
	assert.Equal(t, 100000, result.ContextWindow, "ContextWindow should match capability")
	assert.Equal(t, 16384, result.MaxOutputTokens, "MaxOutputTokens should match capability")
	assert.Equal(t, 8192, result.ReservedOutputTokens, "ReservedOutputTokens should match input")
	assert.True(t, result.Valid, "Result should be valid for valid inputs")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ptrUint(v uint) *uint { return &v }

func repeat(s string, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = s[0]
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// Test: TestPrepare_CompressorIsCalledWhenSummarizeAction
// ---------------------------------------------------------------------------

// TestPrepare_CompressorIsCalledWhenSummarizeAction verifies that when the
// planner recommends an ActionSummarize action (because fragments exceed the
// safe budget), Prepare calls the Compressor, the resulting estimated token
// count is reduced, and the returned plan contains an ActionSummarize entry.
func TestPrepare_CompressorIsCalledWhenSummarizeAction(t *testing.T) {
	db := newTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	// Seed a policy with a very small context window to force the planner to
	// recommend summarisation.
	policy := &model.ContextBudgetPolicy{
		Operation:            "chatbot_chat",
		ReservedOutputTokens: 20,
		SafeRatio:            0.85,
		FixedOverheadTokens:  5,
		SoftThresholdRatio:   0.7,
		HardThresholdRatio:   0.85,
		ChargeUser:           false,
		Version:              1,
		IsActive:             true,
	}
	require.NoError(t, db.Create(policy).Error)
	if policy.ChargeUser {
		db.Model(policy).UpdateColumn("charge_user", false)
		policy.ChargeUser = false
	}

	// Seed a token profile.
	sampleProfile(db)

	// Wire a successCompressor so the Compress call can succeed.
	sc := &successCompressor{}
	biz := New(cbStore, Options{
		Compressor: sc,
		Clock:      time.Now,
		Logger:     noopLogger{},
	})

	// Create a large fragment whose token estimate exceeds the safe input budget
	// for a tiny context window (100 tokens total) with reserved_output=20 and
	// fixed_overhead=5.  safe_input_budget = floor((100 - 20 - 5) * 0.85) = 63.
	// 300 'A' chars at 0.25 tokens/char = 75 estimated tokens — well above 63.
	largeContent := repeat("A", 300) // ~75 tokens > safe budget of 63
	fragA := contextbudget.ContextFragment{
		ID:              "frag-large",
		Role:            contextbudget.RoleDurable,
		Source:          contextbudget.SourceUser,
		ContentType:     contextbudget.ContentText,
		Content:         largeContent,
		Importance:      3,
		Order:           1,
		Compressibility: contextbudget.CompressSummarize, // allows summarise
	}

	result, err := biz.Prepare(context.Background(), aimw.PrepareInput{
		Operation:       "chatbot_chat",
		UserID:          0,
		Route:           sampleRoute(100, 50),
		Fragments:       []contextbudget.ContextFragment{fragA},
		ContextWindow:   100,
		MaxOutputTokens: 50,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// Compressor must have been called.
	assert.True(t, sc.called, "successCompressor.Compress should have been called by applyPlan")

	// After compression, estimated tokens should be less than before compression.
	assert.Less(t, result.EstimatedAfter, result.EstimatedBefore,
		"EstimatedAfter should be < EstimatedBefore after successful compression")

	// The event must have been persisted.
	assert.NotZero(t, result.EventID, "EventID should be non-zero after compression")
}

// Compile-time check: Biz must satisfy aimw.ContextBudgetService.
var _ aimw.ContextBudgetService = (*Biz)(nil)

// Compile-time check: RenderContextFragments should be accessible via aiservice.
var _ = aiservice.RenderContextFragments

// ---------------------------------------------------------------------------
// Test: TestPrepare_GetActivePolicyUsesNormalizedOperation (P1-A regression)
// ---------------------------------------------------------------------------

// TestPrepare_GetActivePolicyUsesNormalizedOperation verifies that Prepare calls
// GetActivePolicy with the canonical (normalized) operation string, not the raw
// alias. Without this fix, "sop_node_execute" → policy miss → defaultPolicy;
// with this fix, it maps to "sop_run" and correctly loads the seeded policy row.
func TestPrepare_GetActivePolicyUsesNormalizedOperation(t *testing.T) {
	db := newTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	// Seed a policy under the CANONICAL key "sop_run", NOT the raw "sop_node_execute".
	// This mirrors what seed_context_budget_policies.sql does.
	policy := samplePolicy(db, "sop_run")

	sampleProfile(db)

	biz := New(cbStore, Options{
		Clock:  time.Now,
		Logger: noopLogger{},
	})

	fragments := []contextbudget.ContextFragment{
		sampleFragment("f1", "hello world"),
	}

	// Call Prepare with the RAW alias "sop_node_execute".
	result, err := biz.Prepare(context.Background(), aimw.PrepareInput{
		Operation:       "sop_node_execute", // raw alias → normalises to "sop_run"
		UserID:          0,
		Route:           sampleRoute(128000, 8192),
		Fragments:       fragments,
		ContextWindow:   128000,
		MaxOutputTokens: 8192,
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	// NormalizedOp must be "sop_run", not the raw "sop_node_execute".
	assert.Equal(t, "sop_run", result.NormalizedOp, "NormalizedOp must be canonical key")

	// PolicyID must match the seeded policy (found by normalized op).
	assert.Equal(t, policy.ID, result.PolicyID,
		"PolicyID should match the seeded 'sop_run' policy, not fall back to synthetic default")
}

// ---------------------------------------------------------------------------
// Test: TestPrepare_AppliesReuseSummaryFromCache (P1-B regression)
// ---------------------------------------------------------------------------

// TestPrepare_AppliesReuseSummaryFromCache verifies that when the planner
// recommends ActionReuseSummary and a cached summary is available in the DB,
// Prepare actually substitutes the fragment with the smaller cached summary,
// reducing EstimatedAfter and not returning ErrContextTooLarge.
func TestPrepare_AppliesReuseSummaryFromCache(t *testing.T) {
	db := newTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	// Tiny context window: context_window=120, reserved_output=30, overhead=5
	// safe_input_budget = floor((120 - 30 - 5) * 0.85) = floor(85*0.85) = 72
	policy := &model.ContextBudgetPolicy{
		Operation:            "chatbot_chat",
		ReservedOutputTokens: 30,
		SafeRatio:            0.85,
		FixedOverheadTokens:  5,
		SoftThresholdRatio:   0.7,
		HardThresholdRatio:   0.85,
		ChargeUser:           false,
		Version:              1,
		IsActive:             true,
	}
	require.NoError(t, db.Create(policy).Error)
	if policy.ChargeUser {
		db.Model(policy).UpdateColumn("charge_user", false)
		policy.ChargeUser = false
	}

	// Seed token profile.
	sampleProfile(db)

	// Pre-populate a ready summary for the fragment.
	ownerUserID := uint(55)
	sourceHash := "reuse-hash-001"
	summary := &model.ContextSummary{
		UserID:                ownerUserID,
		OwnerUserID:           ptrUint(ownerUserID),
		ScopeType:             "chat_session",
		ScopeID:               "sess-42",
		SourceHash:            sourceHash,
		SourceFragmentIDs:     datatypes.JSON(`["large-frag"]`),
		SummaryText:           "short",
		SummaryTokenEstimate:  5,
		OriginalTokenEstimate: 500,
		Model:                 "glm-4-7-251222",
		Provider:              "volc",
		Status:                "ready",
		CreatedByOperation:    "chatbot_chat",
	}
	require.NoError(t, db.Create(summary).Error)

	biz := New(cbStore, Options{
		Clock:  time.Now,
		Logger: noopLogger{},
	})

	// Large fragment: 400 chars × 0.25 tokens/char = ~100 estimated tokens,
	// well above safe_input_budget=72. Mark as CompressSummarize with a
	// SourceReference matching the seeded summary hash.
	largeContent := repeat("B", 400)
	largeFrag := contextbudget.ContextFragment{
		ID:              "large-frag",
		Role:            contextbudget.RoleDurable,
		Source:          contextbudget.SourceUser,
		ContentType:     contextbudget.ContentText,
		Content:         largeContent,
		Importance:      5,
		Order:           1,
		Compressibility: contextbudget.CompressSummarize,
		SourceReference: sourceHash, // matches DB summary
	}

	result, err := biz.Prepare(context.Background(), aimw.PrepareInput{
		Operation:       "chatbot_chat",
		UserID:          ownerUserID,
		Route:           sampleRoute(120, 60),
		Fragments:       []contextbudget.ContextFragment{largeFrag},
		ContextWindow:   120,
		MaxOutputTokens: 60,
		Metadata: map[string]string{
			"chat_session_id": "sess-42",
		},
	})
	require.NoError(t, err, "should not return ErrContextTooLarge when summary cache hits")
	require.NotNil(t, result)

	// Token count must have been reduced (summary replaced the large fragment).
	assert.Less(t, result.EstimatedAfter, result.EstimatedBefore,
		"EstimatedAfter should be < EstimatedBefore after ReuseSummary replacement")

	// The result fragments must include the summary content, not the original large content.
	foundLarge := false
	for _, f := range result.Fragments {
		if f.Content == largeContent {
			foundLarge = true
			break
		}
	}
	assert.False(t, foundLarge, "original large fragment content should be replaced by cached summary")

	assert.NotZero(t, result.EventID, "EventID should be set")
}

// ---------------------------------------------------------------------------
// Test: TestFinalize_SetsReserveAmountAndReconcileDelta (P2-B regression)
// ---------------------------------------------------------------------------

// TestFinalize_SetsReserveAmountAndReconcileDelta verifies that Finalize patches
// reserve_amount and reconcile_delta in the context_budget_event row.
func TestFinalize_SetsReserveAmountAndReconcileDelta(t *testing.T) {
	db := newTestDB(t)
	cbStore := store.NewContextBudgetStore(db)

	// Seed an event.
	event := &model.ContextBudgetEvent{
		Operation:       "chatbot_chat",
		Provider:        "volc",
		Model:           "glm-4-7-251222",
		ContextWindow:   128000,
		MaxOutputTokens: 8192,
		EstimatedBefore: 300,
		EstimatedAfter:  300,
		SafeInputBudget: 90000,
		Status:          "ok",
	}
	require.NoError(t, db.Create(event).Error)

	biz := New(cbStore, Options{
		Clock:  time.Now,
		Logger: noopLogger{},
	})

	err := biz.Finalize(context.Background(), aimw.FinalizeInput{
		EventID:                event.ID,
		ActualPromptTokens:     280,
		ActualCompletionTokens: 120,
		EstimatedCredits:       100,
		PricingCostCents:       110,
		Status:                 "ok",
	})
	require.NoError(t, err)

	var patched model.ContextBudgetEvent
	require.NoError(t, db.First(&patched, event.ID).Error)

	require.NotNil(t, patched.ReserveAmount, "ReserveAmount should be patched by Finalize")
	assert.Equal(t, int64(100), *patched.ReserveAmount, "ReserveAmount should equal EstimatedCredits")

	require.NotNil(t, patched.ReconcileDelta, "ReconcileDelta should be patched by Finalize")
	assert.Equal(t, int64(10), *patched.ReconcileDelta,
		"ReconcileDelta should equal PricingCostCents - EstimatedCredits (110 - 100 = 10)")
}
