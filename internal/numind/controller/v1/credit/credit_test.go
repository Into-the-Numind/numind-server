// Package credit_test contains HTTP-handler level tests for the user-facing
// credits endpoints (Estimate + ListPackages). Business rules live in biz/credit
// and are covered by dedicated biz tests; these tests focus on the HTTP contract:
// response envelope shape, error → status mapping, sort/filter whitelisting,
// and the per-operation branching in Estimate.
package credit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	creditbiz "numind-server/internal/numind/biz/credit"
	creditctl "numind-server/internal/numind/controller/v1/credit"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// stubs
// ---------------------------------------------------------------------------

// stubCreditSvc is an in-memory ICreditService for HTTP handler tests.
// Only the methods the HTTP handler calls are exercised; the others panic.
type stubCreditSvc struct {
	checkResult *creditbiz.PreCheckResult
	checkErr    error
	balance     *creditbiz.BalanceBreakdown
	balanceErr  error
}

func (s *stubCreditSvc) CheckAndEstimate(_ context.Context, _ *model.User, _ creditbiz.Operation, _ creditbiz.EstimationInput) (*creditbiz.PreCheckResult, error) {
	return s.checkResult, s.checkErr
}
func (s *stubCreditSvc) Reserve(_ context.Context, _ *model.User, _ creditbiz.Operation, _ int64, _ uint64, _ *string) (*creditbiz.Reservation, error) {
	panic("not used in tests")
}
func (s *stubCreditSvc) Reconcile(_ context.Context, _ uint64, _ int64) error {
	panic("not used in tests")
}
func (s *stubCreditSvc) Refund(_ context.Context, _ uint64, _ string) error {
	panic("not used in tests")
}
func (s *stubCreditSvc) FinalizeReservation(_ context.Context, _ *creditbiz.Reservation, _ *int64, _ *error) error {
	panic("not used in tests")
}
func (s *stubCreditSvc) GetBalance(_ context.Context, _ *model.User) (*creditbiz.BalanceBreakdown, error) {
	return s.balance, s.balanceErr
}

// stubPromptEstimator always returns a fixed (chars, model, provider).
type stubPromptEstimator struct {
	chars    int
	model    string
	provider string
	err      error
}

func (s *stubPromptEstimator) Estimate(_ context.Context, _, _ string) (int, string, string, error) {
	return s.chars, s.model, s.provider, s.err
}

// stubCreditBiz implements ICreditBiz with panics for unused methods; only
// GetBalance is called by GetBalance handler (not under test here).
type stubCreditBiz struct{}

// These helper constructors live outside the stub so tests can opt into them.

// newTestDB creates an in-memory SQLite DB with only the tables the handler
// touches (credit_package + sop_template + sop_node). We hand-roll the tables
// to avoid AutoMigrating models that depend on MySQL ENUM types (e.g. User).
// Uses a per-test in-memory DB (no cache=shared) so parallel test runs under
// -race don't cross-pollinate one another's writes.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&model.CreditPackage{},
		&model.SopTemplate{},
		&model.SopNode{},
	))
	return db
}

// mustUser returns a User with current_user installed; handler reads
// c.Get("current_user").
func mustUser(id uint, billingMode string) *model.User {
	u := &model.User{BillingMode: billingMode}
	u.ID = id
	return u
}

// setCurrentUserMiddleware mounts a user on the gin context for testing.
func setCurrentUserMiddleware(user *model.User) gin.HandlerFunc {
	return func(c *gin.Context) {
		if user != nil {
			c.Set("current_user", user)
		}
		c.Next()
	}
}

// newRouter installs a controller on a test router with the optional user
// middleware; pass nil user to simulate an unauthenticated call.
func newRouter(t *testing.T, ctrl *creditctl.CreditController, user *model.User) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(setCurrentUserMiddleware(user))
	r.POST("/v1/credits/estimate", ctrl.Estimate)
	r.GET("/v1/credits/packages", ctrl.ListPackages)
	return r
}

// ---------------------------------------------------------------------------
// Estimate tests
// ---------------------------------------------------------------------------

// TestEstimate_CreditsMode_SOP_ReturnsAggregate verifies SOP case returns
// total_estimated_credits / first_node_estimate / node_count when the user is
// on credits billing mode.
func TestEstimate_CreditsMode_SOP_ReturnsAggregate(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	// Seed a template + 3 nodes; prompt_chars are small so test stays predictable.
	tpl := model.SopTemplate{Name: "tmpl", Description: "desc", Prompt: "pre"}
	require.NoError(t, db.Create(&tpl).Error)
	for i, p := range []string{"n1-prompt", "n2-prompt", "n3-prompt"} {
		n := model.SopNode{TemplateID: tpl.ID, Name: "n", Description: "d", Prompt: p, ModelName: "qwen-turbo", Sort: i + 1}
		require.NoError(t, db.Create(&n).Error)
	}

	svc := &stubCreditSvc{
		checkResult: &creditbiz.PreCheckResult{
			SkipDeduction:    false,
			Sufficient:       true,
			EstimatedCredits: 600,
			CoefficientID:    42,
			Balance: creditbiz.BalanceBreakdown{
				BillingMode: model.BillingModeCredits,
				SubRemain:   1000, SubTotal: 1000,
			},
		},
	}
	pe := &stubPromptEstimator{chars: 300, model: "qwen-turbo", provider: "ali"}

	ctrl := creditctl.New(nil, svc, pe, ds)
	r := newRouter(t, ctrl, mustUser(7, model.BillingModeCredits))

	body, _ := json.Marshal(map[string]string{
		"operation":    "sop_run",
		"reference_id": intToStr(uint64(tpl.ID)),
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/credits/estimate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                  `json:"code"`
		Data creditctl.EstimateResp `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, 0, env.Code)
	assert.Equal(t, int64(600), env.Data.TotalEstimatedCredits)
	assert.True(t, env.Data.Sufficient)
	assert.False(t, env.Data.SkipDeduction)
	assert.Equal(t, uint64(42), env.Data.CoefficientID)
	require.NotNil(t, env.Data.NodeCount)
	assert.Equal(t, 3, *env.Data.NodeCount, "node_count must equal seeded rows")
	require.NotNil(t, env.Data.FirstNodeEstimate)
	assert.Equal(t, int64(600), *env.Data.FirstNodeEstimate, "stub always returns 600; first-node call also hits stub")
}

// TestEstimate_Legacy_SkipDeduction verifies legacy_tier returns skip_deduction=true
// and does NOT invoke the sop node query (node_count defaults to 1).
func TestEstimate_Legacy_SkipDeduction(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	svc := &stubCreditSvc{
		checkResult: &creditbiz.PreCheckResult{
			SkipDeduction: true,
			Sufficient:    true,
			Balance: creditbiz.BalanceBreakdown{
				BillingMode: model.BillingModeLegacyTier,
			},
		},
	}
	pe := &stubPromptEstimator{chars: 0}

	ctrl := creditctl.New(nil, svc, pe, ds)
	r := newRouter(t, ctrl, mustUser(7, model.BillingModeLegacyTier))

	body, _ := json.Marshal(map[string]string{
		"operation":    "sop_run",
		"reference_id": "1",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/credits/estimate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                  `json:"code"`
		Data creditctl.EstimateResp `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.True(t, env.Data.SkipDeduction)
	require.NotNil(t, env.Data.NodeCount)
	assert.Equal(t, 1, *env.Data.NodeCount, "legacy should default to 1 (no node lookup)")
}

// TestEstimate_InsufficientCredits_Returns200 verifies ErrInsufficientCredits
// is surfaced as HTTP 200 with sufficient=false so the frontend interceptor
// can distinguish "estimate shortfall" from "other errors".
func TestEstimate_InsufficientCredits_Returns200(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	pre := &creditbiz.PreCheckResult{
		SkipDeduction:    false,
		Sufficient:       false,
		EstimatedCredits: 500,
		Balance: creditbiz.BalanceBreakdown{
			BillingMode: model.BillingModeCredits,
			SubRemain:   100,
		},
	}
	svc := &stubCreditSvc{
		checkResult: pre,
		checkErr:    creditbiz.ErrInsufficientCredits,
	}
	pe := &stubPromptEstimator{chars: 100, model: "qwen-turbo", provider: "ali"}

	ctrl := creditctl.New(nil, svc, pe, ds)
	r := newRouter(t, ctrl, mustUser(7, model.BillingModeCredits))

	body, _ := json.Marshal(map[string]string{
		"operation":    "salesrag_chat",
		"reference_id": "123",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/credits/estimate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                  `json:"code"`
		Data creditctl.EstimateResp `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	assert.Equal(t, 0, env.Code)
	assert.False(t, env.Data.Sufficient, "sufficient must be false on shortfall")
	assert.Equal(t, int64(500), env.Data.TotalEstimatedCredits)
}

// TestEstimate_Unauthenticated returns 401.
func TestEstimate_Unauthenticated(t *testing.T) {
	ctrl := creditctl.New(nil, &stubCreditSvc{}, &stubPromptEstimator{}, nil)
	r := newRouter(t, ctrl, nil)

	body, _ := json.Marshal(map[string]string{"operation": "sop_run", "reference_id": "1"})
	req := httptest.NewRequest(http.MethodPost, "/v1/credits/estimate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code, "body: %s", w.Body.String())
}

// TestEstimate_BindError returns 400 InvalidParameter.BindError.
func TestEstimate_BindError(t *testing.T) {
	ctrl := creditctl.New(nil, &stubCreditSvc{}, &stubPromptEstimator{}, nil)
	r := newRouter(t, ctrl, mustUser(1, model.BillingModeCredits))

	req := httptest.NewRequest(http.MethodPost, "/v1/credits/estimate", bytes.NewReader([]byte("{not-json}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---------------------------------------------------------------------------
// ListPackages tests
// ---------------------------------------------------------------------------

// TestListPackages_Basic verifies the basic shape + user_id scoping.
func TestListPackages_Basic(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)

	// seed 2 packages for user 7, 1 package for user 99 (must NOT appear).
	now := time.Now()
	require.NoError(t, db.Create(&model.CreditPackage{UserID: 7, Type: "subscription", TotalCredits: 100, RemainCredits: 80,
		ActivatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour), Status: "active"}).Error)
	require.NoError(t, db.Create(&model.CreditPackage{UserID: 7, Type: "booster", TotalCredits: 500, RemainCredits: 500,
		ActivatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour), Status: "active"}).Error)
	require.NoError(t, db.Create(&model.CreditPackage{UserID: 99, Type: "subscription", TotalCredits: 100, RemainCredits: 100,
		ActivatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour), Status: "active"}).Error)

	ctrl := creditctl.New(nil, &stubCreditSvc{}, &stubPromptEstimator{}, ds)
	r := newRouter(t, ctrl, mustUser(7, model.BillingModeCredits))

	req := httptest.NewRequest(http.MethodGet, "/v1/credits/packages", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                       `json:"code"`
		Data creditctl.ListPackagesResp `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))

	assert.Equal(t, int64(2), env.Data.Total, "only user 7's packages; user 99 must NOT leak")
	assert.Len(t, env.Data.List, 2)
}

// TestListPackages_TypeFilter verifies the type filter.
func TestListPackages_TypeFilter(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)
	now := time.Now()
	require.NoError(t, db.Create(&model.CreditPackage{UserID: 7, Type: "subscription", TotalCredits: 100, RemainCredits: 80,
		ActivatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour), Status: "active"}).Error)
	require.NoError(t, db.Create(&model.CreditPackage{UserID: 7, Type: "booster", TotalCredits: 500, RemainCredits: 500,
		ActivatedAt: now, ExpiresAt: now.Add(90 * 24 * time.Hour), Status: "active"}).Error)

	ctrl := creditctl.New(nil, &stubCreditSvc{}, &stubPromptEstimator{}, ds)
	r := newRouter(t, ctrl, mustUser(7, model.BillingModeCredits))

	req := httptest.NewRequest(http.MethodGet, "/v1/credits/packages?type=booster", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var env struct {
		Code int                       `json:"code"`
		Data creditctl.ListPackagesResp `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&env))
	assert.Equal(t, int64(1), env.Data.Total)
	require.Len(t, env.Data.List, 1)
	assert.Equal(t, "booster", env.Data.List[0].Type)
}

// TestListPackages_RejectsInvalidSort rejects non-whitelisted sort fields.
func TestListPackages_RejectsInvalidSort(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)
	ctrl := creditctl.New(nil, &stubCreditSvc{}, &stubPromptEstimator{}, ds)
	r := newRouter(t, ctrl, mustUser(7, model.BillingModeCredits))

	// Sort by a non-whitelisted column (SQL-injection guard).
	req := httptest.NewRequest(http.MethodGet, "/v1/credits/packages?sort=remain_credits:asc", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestListPackages_RejectsInvalidType rejects non-whitelisted type filters.
func TestListPackages_RejectsInvalidType(t *testing.T) {
	db := newTestDB(t)
	ds := store.NewTestStore(db)
	ctrl := creditctl.New(nil, &stubCreditSvc{}, &stubPromptEstimator{}, ds)
	r := newRouter(t, ctrl, mustUser(7, model.BillingModeCredits))

	req := httptest.NewRequest(http.MethodGet, "/v1/credits/packages?type=weirdthing", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestListPackages_Unauthenticated returns 401.
func TestListPackages_Unauthenticated(t *testing.T) {
	ctrl := creditctl.New(nil, &stubCreditSvc{}, &stubPromptEstimator{}, nil)
	r := newRouter(t, ctrl, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/credits/packages", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

func intToStr(n uint64) string {
	b := make([]byte, 0, 20)
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
