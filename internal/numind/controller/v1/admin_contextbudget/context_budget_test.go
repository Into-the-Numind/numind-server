package admin_contextbudget_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/biz/aiservice_admin"
	"numind-server/internal/numind/biz/contextbudget"
	"numind-server/internal/numind/controller/v1/admin_contextbudget"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice/registry"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Test DB helpers
// ---------------------------------------------------------------------------

func newTestDB(t *testing.T) *gorm.DB {
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
		&model.AIService{},
		&model.AIServiceRoute{},
		&model.AIServiceAuditLog{},
		&model.LLMProvider{},
		&model.TaskProfile{},
	), "auto-migrate")
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// newRouter builds a minimal Gin router with the context budget admin routes registered.
func newRouter(db *gorm.DB) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	cbStore := store.NewContextBudgetStore(db)
	cbBiz := contextbudget.New(cbStore, contextbudget.Options{})
	reg := registry.New(db)
	aiSvcBiz := aiservice_admin.New(reg, db)
	ctrl := admin_contextbudget.New(cbBiz, cbStore, aiSvcBiz, db)

	v1 := r.Group("/v1/admin")
	v1.GET("/context-budget/token-profiles", ctrl.ListTokenProfiles)
	v1.POST("/context-budget/token-profiles", ctrl.CreateTokenProfile)
	v1.PUT("/context-budget/token-profiles/:id", ctrl.UpdateTokenProfile)
	v1.DELETE("/context-budget/token-profiles/:id", ctrl.DeleteTokenProfile)
	v1.GET("/context-budget/token-profiles/history", ctrl.GetTokenProfileHistory)
	v1.GET("/context-budget/policies", ctrl.ListPolicies)
	v1.PUT("/context-budget/policies/:operation", ctrl.UpsertPolicy)
	v1.POST("/context-budget/preview", ctrl.Preview)
	v1.GET("/context-budget/events", ctrl.ListEvents)
	return r
}

// ---------------------------------------------------------------------------
// TestAdminContextBudgetRoutesAreRegistered
// ---------------------------------------------------------------------------

// TestAdminContextBudgetRoutesAreRegistered verifies that all 9 required
// context budget admin endpoints respond (not 404) when registered correctly.
func TestAdminContextBudgetRoutesAreRegistered(t *testing.T) {
	db := newTestDB(t)
	r := newRouter(db)

	paths := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/admin/context-budget/token-profiles", ""},
		{http.MethodPost, "/v1/admin/context-budget/token-profiles", `{"provider":"p","model":"m","service_type":"llm_chat","profile_json":{},"safety_multiplier":1.1,"calibration_multiplier":1.0}`},
		{http.MethodPut, "/v1/admin/context-budget/token-profiles/1", `{"profile_json":{},"safety_multiplier":1.2,"calibration_multiplier":1.0}`},
		{http.MethodDelete, "/v1/admin/context-budget/token-profiles/1", ""},
		{http.MethodGet, "/v1/admin/context-budget/token-profiles/history?provider=p&model=m&service_type=llm_chat", ""},
		{http.MethodGet, "/v1/admin/context-budget/policies", ""},
		{http.MethodPut, "/v1/admin/context-budget/policies/sop_run", `{"reserved_output_tokens":2048,"safe_ratio":0.85,"fixed_overhead_tokens":512,"soft_threshold_ratio":0.7,"hard_threshold_ratio":0.85}`},
		{http.MethodPost, "/v1/admin/context-budget/preview", `{"service_id":999,"operation":"sop_run","fixed_overhead_tokens":512,"reserved_output_tokens":2048,"safe_ratio":0.85}`},
		{http.MethodGet, "/v1/admin/context-budget/events", ""},
	}

	for _, p := range paths {
		t.Run(p.method+" "+p.path, func(t *testing.T) {
			var body *bytes.Reader
			if p.body != "" {
				body = bytes.NewReader([]byte(p.body))
			} else {
				body = bytes.NewReader(nil)
			}
			req := httptest.NewRequest(p.method, p.path, body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.NotEqual(t, http.StatusNotFound, w.Code, "endpoint %s %s should be registered", p.method, p.path)
		})
	}
}

// ---------------------------------------------------------------------------
// TestAdminContextBudgetTokenProfileHistoryUsesLookupQuery
// ---------------------------------------------------------------------------

// TestAdminContextBudgetTokenProfileHistoryUsesLookupQuery verifies that the
// history endpoint filters by provider/model/service_type query parameters and
// returns all rows (including inactive ones) sorted by version DESC.
func TestAdminContextBudgetTokenProfileHistoryUsesLookupQuery(t *testing.T) {
	db := newTestDB(t)
	r := newRouter(db)

	// Seed two versions for provider=volc, model=glm-4, service_type=llm_chat.
	profileJSON := mustJSON(t, map[string]interface{}{
		"method":                   "test",
		"message_overhead_tokens":  4,
		"fragment_overhead_tokens": 2,
		"classes": map[string]interface{}{
			"en": map[string]interface{}{"token_per_char": 0.25},
		},
		"safety_multiplier":      1.1,
		"calibration_multiplier": 1.0,
	})
	row1 := &model.TokenEstimationProfile{
		Provider: "volc", Model: "glm-4", ServiceType: "llm_chat",
		ProfileJSON: profileJSON, SafetyMultiplier: 1.1, CalibrationMultiplier: 1.0,
		Version: 1, IsActive: false,
	}
	row2 := &model.TokenEstimationProfile{
		Provider: "volc", Model: "glm-4", ServiceType: "llm_chat",
		ProfileJSON: profileJSON, SafetyMultiplier: 1.2, CalibrationMultiplier: 1.0,
		Version: 2, IsActive: true,
	}
	// A row for a different provider — must NOT appear in results.
	row3 := &model.TokenEstimationProfile{
		Provider: "ali", Model: "qwen-plus", ServiceType: "llm_chat",
		ProfileJSON: profileJSON, SafetyMultiplier: 1.1, CalibrationMultiplier: 1.0,
		Version: 1, IsActive: true,
	}
	require.NoError(t, db.Create(row1).Error)
	require.NoError(t, db.Create(row2).Error)
	require.NoError(t, db.Create(row3).Error)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/admin/context-budget/token-profiles/history?provider=volc&model=glm-4&service_type=llm_chat",
		nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data, ok := resp["data"].(map[string]interface{})
	require.True(t, ok, "data should be an object")
	list, ok := data["list"].([]interface{})
	require.True(t, ok, "data.list should be an array")
	assert.Len(t, list, 2, "should return exactly 2 rows for volc/glm-4/llm_chat")

	// Verify ordering: version DESC (row2=v2 first, row1=v1 second).
	first := list[0].(map[string]interface{})
	assert.Equal(t, float64(2), first["version"], "first row should be version 2")
}

// ---------------------------------------------------------------------------
// TestAdminContextBudgetPreviewReturnsBudgetMath
// ---------------------------------------------------------------------------

// TestAdminContextBudgetPreviewReturnsBudgetMath verifies that the preview
// endpoint delegates to biz.Preview and returns the computed safe_input_budget.
func TestAdminContextBudgetPreviewReturnsBudgetMath(t *testing.T) {
	db := newTestDB(t)
	r := newRouter(db)

	// Create an LLM service with capability_json carrying context_window=128000
	// and max_output_tokens=8192.
	svc := &model.AIService{
		ModelKey:    "test-model",
		DisplayName: "Test Model",
		ServiceType: "llm",
		IsActive:    true,
		CapabilityJSON: model.JSONMap{
			"context_window":    128000,
			"max_output_tokens": 8192,
		},
	}
	require.NoError(t, db.Create(svc).Error)

	body := map[string]interface{}{
		"service_id":             svc.ID,
		"operation":              "sop_run",
		"fixed_overhead_tokens":  512,
		"reserved_output_tokens": 4096,
		"safe_ratio":             0.85,
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/context-budget/preview",
		bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data, ok := resp["data"].(map[string]interface{})
	require.True(t, ok, "data should be object")
	assert.Equal(t, float64(128000), data["context_window"])
	assert.Equal(t, float64(8192), data["max_output_tokens"])
	// safe_input_budget = (128000 - 4096 - 512) * 0.85 = 104,617.6 → floor = 104617
	sib, ok := data["safe_input_budget"].(float64)
	require.True(t, ok, "safe_input_budget must be numeric")
	assert.Greater(t, sib, float64(0), "safe_input_budget must be positive")
	assert.True(t, data["valid"].(bool), "preview result should be valid")
}

// ---------------------------------------------------------------------------
// TestAdminContextBudgetEventsReturnsRecentMetadataOnly
// ---------------------------------------------------------------------------

// TestAdminContextBudgetEventsReturnsRecentMetadataOnly verifies that the
// events endpoint returns recent events without including prompt content.
func TestAdminContextBudgetEventsReturnsRecentMetadataOnly(t *testing.T) {
	db := newTestDB(t)
	r := newRouter(db)

	// Seed a few events.
	uid := uint(42)
	events := []*model.ContextBudgetEvent{
		{UserID: &uid, Operation: "sop_run", Provider: "volc", Model: "glm-4", Status: "ok",
			EstimatedBefore: 1000, EstimatedAfter: 900, SafeInputBudget: 100000},
		{UserID: &uid, Operation: "chatbot_chat", Provider: "ali", Model: "qwen-plus", Status: "compressed",
			EstimatedBefore: 2000, EstimatedAfter: 1500, SafeInputBudget: 80000},
	}
	for _, ev := range events {
		require.NoError(t, db.Create(ev).Error)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/context-budget/events", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data, ok := resp["data"].(map[string]interface{})
	require.True(t, ok, "data should be object")
	list, ok := data["list"].([]interface{})
	require.True(t, ok, "data.list should be an array")
	assert.GreaterOrEqual(t, len(list), 2, "should return at least 2 events")

	// Verify each event has metadata fields but not prompt content.
	for _, item := range list {
		ev := item.(map[string]interface{})
		// Must have ID and operation.
		assert.NotNil(t, ev["id"], "event must have id")
		assert.NotNil(t, ev["operation"], "event must have operation")
		// Must have token counts.
		assert.NotNil(t, ev["estimated_before"], "event must have estimated_before")
		// Must NOT have full prompt content or messages.
		_, hasPrompt := ev["prompt_content"]
		assert.False(t, hasPrompt, "event must not contain prompt_content")
		_, hasMessages := ev["messages"]
		assert.False(t, hasMessages, "event must not contain messages")
	}
}

// ---------------------------------------------------------------------------
// TestAIServiceLLMRequiresContextWindowAndMaxOutputTokens
// ---------------------------------------------------------------------------

// TestAIServiceLLMRequiresContextWindowAndMaxOutputTokens verifies that the
// aiservice_admin.CreateService biz validates context_window and max_output_tokens
// for llm service_type.
func TestAIServiceLLMRequiresContextWindowAndMaxOutputTokens(t *testing.T) {
	db := newTestDB(t)
	reg := registry.New(db)
	biz := aiservice_admin.New(reg, db)

	// Missing context_window → should fail.
	svcMissingCtx := &model.AIService{
		ModelKey:    "llm-missing-ctx",
		DisplayName: "LLM No Context",
		ServiceType: "llm",
		IsActive:    true,
		CapabilityJSON: model.JSONMap{
			"max_output_tokens": 4096,
			// context_window intentionally absent
		},
	}
	_, err := biz.CreateService(t.Context(), svcMissingCtx, 0, "test")
	assert.Error(t, err, "llm service without context_window should be rejected")

	// Missing max_output_tokens → should fail.
	svcMissingOut := &model.AIService{
		ModelKey:    "llm-missing-out",
		DisplayName: "LLM No Output",
		ServiceType: "llm",
		IsActive:    true,
		CapabilityJSON: model.JSONMap{
			"context_window": 128000,
			// max_output_tokens intentionally absent
		},
	}
	_, err = biz.CreateService(t.Context(), svcMissingOut, 0, "test")
	assert.Error(t, err, "llm service without max_output_tokens should be rejected")

	// max_output_tokens >= context_window → should fail.
	svcBadRatio := &model.AIService{
		ModelKey:    "llm-bad-ratio",
		DisplayName: "LLM Bad Ratio",
		ServiceType: "llm",
		IsActive:    true,
		CapabilityJSON: model.JSONMap{
			"context_window":    8192,
			"max_output_tokens": 8192, // equal — must be strictly less
		},
	}
	_, err = biz.CreateService(t.Context(), svcBadRatio, 0, "test")
	assert.Error(t, err, "llm service with max_output_tokens >= context_window should be rejected")

	// Valid llm service with both fields → should succeed.
	svcOK := &model.AIService{
		ModelKey:    "llm-valid",
		DisplayName: "LLM Valid",
		ServiceType: "llm",
		IsActive:    true,
		CapabilityJSON: model.JSONMap{
			"context_window":    128000,
			"max_output_tokens": 8192,
		},
	}
	created, err := biz.CreateService(t.Context(), svcOK, 0, "test")
	assert.NoError(t, err, "valid llm service should be created")
	assert.NotNil(t, created)

	// Non-LLM services (ocr/asr) should NOT require context_window.
	svcOCR := &model.AIService{
		ModelKey:    "ocr-valid",
		DisplayName: "OCR Valid",
		ServiceType: "ocr",
		IsActive:    true,
		CapabilityJSON: model.JSONMap{
			"image_formats": []interface{}{"jpg", "png"},
		},
	}
	createdOCR, errOCR := biz.CreateService(t.Context(), svcOCR, 0, "test")
	assert.NoError(t, errOCR, "ocr service without context_window should be accepted")
	assert.NotNil(t, createdOCR)
}

// ---------------------------------------------------------------------------
// TestAIServiceRejectsReservedOutputGreaterThanMaxOutputViaPolicyPreview
// ---------------------------------------------------------------------------

// TestAIServiceRejectsReservedOutputGreaterThanMaxOutputViaPolicyPreview
// verifies that Preview marks the result as invalid when
// reserved_output_tokens >= max_output_tokens.
func TestAIServiceRejectsReservedOutputGreaterThanMaxOutputViaPolicyPreview(t *testing.T) {
	db := newTestDB(t)
	r := newRouter(db)

	// Create service with max_output_tokens=4096.
	svc := &model.AIService{
		ModelKey:    "llm-small-output",
		DisplayName: "LLM Small Output",
		ServiceType: "llm",
		IsActive:    true,
		CapabilityJSON: model.JSONMap{
			"context_window":    32000,
			"max_output_tokens": 4096,
		},
	}
	require.NoError(t, db.Create(svc).Error)

	// Request preview with reserved_output_tokens=5000 > max_output_tokens=4096.
	body := map[string]interface{}{
		"service_id":             svc.ID,
		"operation":              "sop_run",
		"fixed_overhead_tokens":  512,
		"reserved_output_tokens": 5000, // > max_output_tokens
		"safe_ratio":             0.85,
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/context-budget/preview",
		bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// The endpoint should succeed at the HTTP level (200) but the result is invalid.
	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data, ok := resp["data"].(map[string]interface{})
	require.True(t, ok, "data should be object")
	// valid=false because reserved_output_tokens > max_output_tokens.
	valid, hasBool := data["valid"].(bool)
	assert.True(t, hasBool, "valid field should be boolean")
	assert.False(t, valid, "preview with reserved > max_output should be invalid")
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
