// Package credit_test contains HTTP-handler level tests for the user-facing
// credits endpoint (Estimate). Business rules live in biz/credit and are
// covered by dedicated biz tests; these tests focus on the HTTP contract:
// response envelope shape, error → status mapping, and the per-operation
// branching in Estimate.
//
// T9: ListPackages tests removed — credit_package dead route deleted.
package credit_test

import (
	"bytes"
	"context"
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

// CheckAndEstimateBudget: budget-aware variant. Default mock returns a
// PreCheckResult with sufficient credits + zero estimate so tests that
// don't exercise context-budget paths don't crash if this method is called.
func (s *stubCreditSvc) CheckAndEstimateBudget(_ context.Context, _ *model.User, _ creditbiz.BudgetPrecheckInput) (*creditbiz.PreCheckResult, error) {
	return &creditbiz.PreCheckResult{
		SkipDeduction:    false,
		Sufficient:       true,
		EstimatedCredits: 0,
	}, nil
}

// ReserveBudget: returns (nil, nil) — nil reservation signals the legacy/skip
// path; safe no-op for any test that does not exercise context-budget reservation.
func (s *stubCreditSvc) ReserveBudget(_ context.Context, _ *model.User, _ creditbiz.BudgetReservationInput) (*creditbiz.Reservation, error) {
	return nil, nil
}

// ReserveAgentTest: Agent Builder 试聊 path — stub returns (nil, nil) for tests
// that don't exercise admin_test pool reservation (#12 agent-mode-billing-integration).
func (s *stubCreditSvc) ReserveAgentTest(_ context.Context, _ *model.User, _ int64, _ *string) (*creditbiz.Reservation, error) {
	return nil, nil
}

// ReconcileAgentTest: stub no-op for tests that don't exercise admin_test reconciliation (#12).
func (s *stubCreditSvc) ReconcileAgentTest(_ context.Context, _ uint64, _ int64) error {
	return nil
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

	// T11: CreditPackage removed — table dropped, archived to legacy_credit_package_archive_20260515.
	require.NoError(t, db.AutoMigrate(
		&model.SopTemplate{},
		&model.SopNode{},
	))
	return db
}

// mustUser returns a User with current_user installed; handler reads
// c.Get("current_user").
// Post-T4: billingMode parameter retained for caller symmetry but ignored
// (User.BillingMode field removed; everyone is credits-only).
func mustUser(id uint, _ string) *model.User {
	u := &model.User{}
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
	// T9: GET /v1/credits/packages route deleted
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
				BillingMode: "credits",
				SubRemain:   1000, SubTotal: 1000,
			},
		},
	}
	pe := &stubPromptEstimator{chars: 300, model: "qwen-turbo", provider: "ali"}

	ctrl := creditctl.New(nil, svc, pe, ds)
	r := newRouter(t, ctrl, mustUser(7, "credits"))

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
		Code int                    `json:"code"`
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
			BillingMode: "credits",
			SubRemain:   100,
		},
	}
	svc := &stubCreditSvc{
		checkResult: pre,
		checkErr:    creditbiz.ErrInsufficientCredits,
	}
	pe := &stubPromptEstimator{chars: 100, model: "qwen-turbo", provider: "ali"}

	ctrl := creditctl.New(nil, svc, pe, ds)
	r := newRouter(t, ctrl, mustUser(7, "credits"))

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
		Code int                    `json:"code"`
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
	r := newRouter(t, ctrl, mustUser(1, "credits"))

	req := httptest.NewRequest(http.MethodPost, "/v1/credits/estimate", bytes.NewReader([]byte("{not-json}")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// ---------------------------------------------------------------------------
// T9: ListPackages tests removed — credit_package dead route deleted.
// ---------------------------------------------------------------------------

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
