// Package admin_credit_test exercises the estimation-coefficient CRUD HTTP
// handlers. Business concerns (SELECT FOR UPDATE + retry) are biz-layer tests;
// these focus on HTTP contract: filter whitelisting, pagination, error → status
// classification, and the envelope shape the admin UI parses.
package admin_credit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	creditbiz "numind-server/internal/numind/biz/credit"
	admin_credit "numind-server/internal/numind/controller/v1/admin_credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// stubs
// ---------------------------------------------------------------------------

// stubEstimationBiz is a minimal IEstimationBiz for handler tests.
type stubEstimationBiz struct {
	updateID  uint64
	updateErr error
}

func (s *stubEstimationBiz) EstimateCredits(_ context.Context, _ creditbiz.Operation, _ int, _, _ string) (int64, uint64, error) {
	panic("not used in tests")
}
func (s *stubEstimationBiz) UpdateCoefficient(_ context.Context, _ *model.CreditEstimationCoefficient) (uint64, error) {
	return s.updateID, s.updateErr
}

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_foreign_keys=on"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&model.CreditEstimationCoefficient{}))
	return db
}

// adminMiddleware mounts a test admin into the gin context. NewMemoryAdmin
// simulates the AdminAuthMiddleware's "current_user" shim.
func adminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		u := &model.User{Username: "alice", IsAdmin: true}
		u.ID = 42
		c.Set("current_user", u)
		c.Next()
	}
}

func newRouter(t *testing.T, ctrl *admin_credit.CoefficientController) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(adminMiddleware())
	r.GET("/v1/admin/estimation-coefficients", ctrl.ListCoefficients)
	r.GET("/v1/admin/estimation-coefficients/history", ctrl.ListCoefficientHistory)
	r.POST("/v1/admin/estimation-coefficients", ctrl.CreateCoefficient)
	r.PUT("/v1/admin/estimation-coefficients/:id", ctrl.UpdateCoefficient)
	r.DELETE("/v1/admin/estimation-coefficients/:id", ctrl.DeleteCoefficient)
	return r
}

// ---------------------------------------------------------------------------
// List tests
// ---------------------------------------------------------------------------

// TestListCoefficients_DefaultActive verifies is_active=1 is the implicit filter.
func TestListCoefficients_DefaultActive(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	require.NoError(t, db.Create(&model.CreditEstimationCoefficient{
		Provider: "ali", Model: "qwen-turbo", Operation: "sop_run",
		CharToTokenRatio: 1.5, CompletionPromptRatio: 0.5, SafetyBufferPct: 0.2,
		Version: 1, IsActive: true,
	}).Error)
	require.NoError(t, db.Create(&model.CreditEstimationCoefficient{
		Provider: "ali", Model: "qwen-turbo", Operation: "sop_run",
		CharToTokenRatio: 1.5, CompletionPromptRatio: 0.5, SafetyBufferPct: 0.2,
		Version: 2, IsActive: false,
	}).Error)

	ctrl := admin_credit.NewCoefficientController(&stubEstimationBiz{}, ds)
	r := newRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/estimation-coefficients", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                              `json:"code"`
		Data admin_credit.ListCoefficientsResp `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	assert.Equal(t, int64(1), env.Data.Total, "default only returns is_active=1")
	require.Len(t, env.Data.List, 1)
	assert.True(t, env.Data.List[0].IsActive)
}

// TestListCoefficients_AllFlag verifies is_active=all returns every row.
func TestListCoefficients_AllFlag(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	require.NoError(t, db.Create(&model.CreditEstimationCoefficient{
		Provider: "ali", Model: "qwen-turbo", Operation: "sop_run",
		CharToTokenRatio: 1.5, CompletionPromptRatio: 0.5, SafetyBufferPct: 0.2,
		Version: 1, IsActive: true,
	}).Error)
	require.NoError(t, db.Create(&model.CreditEstimationCoefficient{
		Provider: "ali", Model: "qwen-turbo", Operation: "sop_run",
		CharToTokenRatio: 1.4, CompletionPromptRatio: 0.4, SafetyBufferPct: 0.1,
		Version: 2, IsActive: false,
	}).Error)

	ctrl := admin_credit.NewCoefficientController(&stubEstimationBiz{}, ds)
	r := newRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/estimation-coefficients?is_active=all", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Code int                              `json:"code"`
		Data admin_credit.ListCoefficientsResp `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	assert.Equal(t, int64(2), env.Data.Total)
}

// TestListCoefficients_InvalidIsActive rejects other values (spec: "", 0, 1, all).
func TestListCoefficients_InvalidIsActive(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)
	ctrl := admin_credit.NewCoefficientController(&stubEstimationBiz{}, ds)
	r := newRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/estimation-coefficients?is_active=weirdthing", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---------------------------------------------------------------------------
// History test
// ---------------------------------------------------------------------------

// TestListCoefficientHistory_ScopedAndOrdered verifies (p/m/o) filter + DESC order.
func TestListCoefficientHistory_ScopedAndOrdered(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	// Seed 2 versions for target key + 1 version for different key (must not leak).
	for i, ver := range []uint{1, 2} {
		require.NoError(t, db.Create(&model.CreditEstimationCoefficient{
			Provider: "ali", Model: "qwen-turbo", Operation: "sop_run",
			CharToTokenRatio: 1.5, CompletionPromptRatio: 0.5, SafetyBufferPct: 0.2,
			Version: ver, IsActive: i == 1, // only v2 active
		}).Error)
	}
	require.NoError(t, db.Create(&model.CreditEstimationCoefficient{
		Provider: "volc", Model: "deepseek-v3", Operation: "sop_run",
		CharToTokenRatio: 1.5, CompletionPromptRatio: 0.5, SafetyBufferPct: 0.2,
		Version: 1, IsActive: true,
	}).Error)

	ctrl := admin_credit.NewCoefficientController(&stubEstimationBiz{}, ds)
	r := newRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/estimation-coefficients/history?provider=ali&model=qwen-turbo&operation=sop_run", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                       `json:"code"`
		Data admin_credit.HistoryResp `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	require.Len(t, env.Data.List, 2, "both versions of matching key")
	assert.Equal(t, uint(2), env.Data.List[0].Version, "DESC order — newest first")
	assert.Equal(t, uint(1), env.Data.List[1].Version)
}

// TestListCoefficientHistory_MissingParams returns 400 on missing required params.
func TestListCoefficientHistory_MissingParams(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)
	ctrl := admin_credit.NewCoefficientController(&stubEstimationBiz{}, ds)
	r := newRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/estimation-coefficients/history?provider=ali", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---------------------------------------------------------------------------
// Create / Update tests
// ---------------------------------------------------------------------------

// TestCreateCoefficient_Success verifies the biz.UpdateCoefficient round-trip.
func TestCreateCoefficient_Success(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	// Pre-seed the row that biz would insert (handler does refetch by ID).
	fresh := model.CreditEstimationCoefficient{
		Provider: "ali", Model: "qwen-turbo", Operation: "sop_run",
		CharToTokenRatio: 1.5, CompletionPromptRatio: 0.5, SafetyBufferPct: 0.2,
		Version: 1, IsActive: true,
	}
	require.NoError(t, db.Create(&fresh).Error)

	biz := &stubEstimationBiz{updateID: fresh.ID}
	ctrl := admin_credit.NewCoefficientController(biz, ds)
	r := newRouter(t, ctrl)

	body, _ := json.Marshal(map[string]interface{}{
		"provider":                "ali",
		"model":                   "qwen-turbo",
		"operation":               "sop_run",
		"char_to_token_ratio":     1.5,
		"completion_prompt_ratio": 0.5,
		"safety_buffer_pct":       0.2,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/estimation-coefficients", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int `json:"code"`
		Data struct {
			Coefficient *model.CreditEstimationCoefficient `json:"coefficient"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	require.NotNil(t, env.Data.Coefficient)
	assert.Equal(t, "ali", env.Data.Coefficient.Provider)
	assert.True(t, env.Data.Coefficient.IsActive)
}

// TestCreateCoefficient_ConcurrentConflict returns 503 on retry exhaustion.
func TestCreateCoefficient_ConcurrentConflict(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	biz := &stubEstimationBiz{updateErr: errors.New("wrapper: " + creditbiz.ErrCoefficientConcurrent.Error())}
	biz.updateErr = creditbiz.ErrCoefficientConcurrent // direct error for errors.Is match

	ctrl := admin_credit.NewCoefficientController(biz, ds)
	r := newRouter(t, ctrl)

	body, _ := json.Marshal(map[string]interface{}{
		"provider":                "ali",
		"model":                   "qwen-turbo",
		"operation":               "sop_run",
		"char_to_token_ratio":     1.5,
		"completion_prompt_ratio": 0.5,
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/estimation-coefficients", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "ErrCoefficientConcurrent → 503")
	var env struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	assert.Contains(t, env.Message, "繁忙")
}

// TestUpdateCoefficient_InvalidID rejects non-numeric :id.
func TestUpdateCoefficient_InvalidID(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)
	ctrl := admin_credit.NewCoefficientController(&stubEstimationBiz{}, ds)
	r := newRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodPut, "/v1/admin/estimation-coefficients/notanumber", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestUpdateCoefficient_Success verifies PUT path.
func TestUpdateCoefficient_Success(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	fresh := model.CreditEstimationCoefficient{
		Provider: "ali", Model: "qwen-turbo", Operation: "sop_run",
		CharToTokenRatio: 1.4, CompletionPromptRatio: 0.4, SafetyBufferPct: 0.1,
		Version: 2, IsActive: true, ChangeReason: "tune down",
	}
	require.NoError(t, db.Create(&fresh).Error)

	biz := &stubEstimationBiz{updateID: fresh.ID}
	ctrl := admin_credit.NewCoefficientController(biz, ds)
	r := newRouter(t, ctrl)

	body, _ := json.Marshal(map[string]interface{}{
		"provider":                "ali",
		"model":                   "qwen-turbo",
		"operation":               "sop_run",
		"char_to_token_ratio":     1.4,
		"completion_prompt_ratio": 0.4,
		"safety_buffer_pct":       0.1,
		"change_reason":           "tune down",
	})
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/estimation-coefficients/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())
}

// ---------------------------------------------------------------------------
// Delete tests
// ---------------------------------------------------------------------------

// TestDeleteCoefficient_SoftDelete verifies is_active=0 transition.
func TestDeleteCoefficient_SoftDelete(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	row := model.CreditEstimationCoefficient{
		Provider: "ali", Model: "qwen-turbo", Operation: "sop_run",
		CharToTokenRatio: 1.5, CompletionPromptRatio: 0.5, SafetyBufferPct: 0.2,
		Version: 1, IsActive: true,
	}
	require.NoError(t, db.Create(&row).Error)

	ctrl := admin_credit.NewCoefficientController(&stubEstimationBiz{}, ds)
	r := newRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/estimation-coefficients/"+uintToStr(row.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	// Verify persistence
	var after model.CreditEstimationCoefficient
	require.NoError(t, db.First(&after, row.ID).Error)
	assert.False(t, after.IsActive, "soft-delete sets is_active=0")
}

// TestDeleteCoefficient_NotFound returns 400 with helpful message.
func TestDeleteCoefficient_NotFound(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)
	ctrl := admin_credit.NewCoefficientController(&stubEstimationBiz{}, ds)
	r := newRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/estimation-coefficients/9999", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestDeleteCoefficient_AlreadyInactive short-circuits idempotently.
func TestDeleteCoefficient_AlreadyInactive(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	row := model.CreditEstimationCoefficient{
		Provider: "ali", Model: "qwen-turbo", Operation: "sop_run",
		CharToTokenRatio: 1.5, CompletionPromptRatio: 0.5, SafetyBufferPct: 0.2,
		Version: 1, IsActive: false,
	}
	require.NoError(t, db.Create(&row).Error)

	ctrl := admin_credit.NewCoefficientController(&stubEstimationBiz{}, ds)
	r := newRouter(t, ctrl)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/estimation-coefficients/"+uintToStr(row.ID), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Code int `json:"code"`
		Data struct {
			AlreadyInactive bool `json:"already_inactive"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	assert.True(t, env.Data.AlreadyInactive, "already-inactive response carries the flag")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func uintToStr(n uint64) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}
