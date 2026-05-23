// Package marketplace_test — HTTP-level smoke tests for marketplace.Controller.
//
// Validates: handler ↔ biz wiring, JSON binding, path param parsing, sentinel
// error → errno mapping (mapBizError). Business rules already covered by
// biz/marketplace/*_test.go. Auth middleware not exercised here — handlers
// rely on middleware.GetCurrentUser; tests inject *model.User into ctx via
// a tiny middleware shim.
package marketplace_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	bizmarketplace "numind-server/internal/numind/biz/marketplace"
	mpctrl "numind-server/internal/numind/controller/v1/marketplace"
	"numind-server/internal/pkg/model"
)

// ---- fake Service ----

type fakeService struct {
	sanitizeOut string
	sanitizeErr error
	publishErr  error
	publishMP   *model.SkillMarketplace

	subscribeID    uint
	subscribeSubID uint
	subscribeErr   error

	getMP  *model.SkillMarketplace
	getErr error

	lastSetRecommendedID uint
	lastRecommendedFlag  bool
	setRecommendedErr    error
}

func (f *fakeService) SanitizePreview(ctx context.Context, _ uint, _ uint) (*bizmarketplace.SanitizeResult, error) {
	if f.sanitizeErr != nil {
		return nil, f.sanitizeErr
	}
	return &bizmarketplace.SanitizeResult{
		SanitizedBodyMD:  f.sanitizeOut,
		Stages:           []string{"regex", "llm"},
		PromptTokens:     123,
		CompletionTokens: 45,
	}, nil
}
func (f *fakeService) Publish(ctx context.Context, _ uint, _ bizmarketplace.PublishRequest) (*model.SkillMarketplace, error) {
	return f.publishMP, f.publishErr
}
func (f *fakeService) Unpublish(ctx context.Context, _ uint, _ uint) error { return nil }
func (f *fakeService) List(ctx context.Context, _ bizmarketplace.BrowseQuery) ([]*model.SkillMarketplace, int64, error) {
	return []*model.SkillMarketplace{}, 0, nil
}
func (f *fakeService) Get(ctx context.Context, _ uint, _ uint) (*model.SkillMarketplace, error) {
	return f.getMP, f.getErr
}
func (f *fakeService) Subscribe(ctx context.Context, _ uint, _ uint) (uint, uint, error) {
	return f.subscribeID, f.subscribeSubID, f.subscribeErr
}
func (f *fakeService) Unsubscribe(ctx context.Context, _ uint, _ uint) error { return nil }
func (f *fakeService) ListMySubscriptions(ctx context.Context, _ uint, _, _ int) ([]bizmarketplace.SubscriptionItem, int64, error) {
	return []bizmarketplace.SubscriptionItem{}, 0, nil
}
func (f *fakeService) SetRecommended(ctx context.Context, id uint, rec bool) error {
	f.lastSetRecommendedID = id
	f.lastRecommendedFlag = rec
	return f.setRecommendedErr
}

// ---- helpers ----

// withUser injects a fake user into Gin context (bypassing auth middleware for tests).
// Pattern mirrors middleware.GetCurrentUser semantics: stored under the same key as auth
// middleware uses. Use parent=true for father account, false for child.
func withUser(userID uint, parent bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		u := &model.User{}
		u.ID = userID
		if !parent {
			pid := uint(1)
			u.ParentUserID = &pid
		}
		// middleware.GetCurrentUser reads from gin.Context key "current_user".
		c.Set("current_user", u)
		c.Next()
	}
}

func newRouter(t *testing.T, svc bizmarketplace.Service, userID uint, parent bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(withUser(userID, parent))

	ctrl := mpctrl.NewController(svc)
	mp := r.Group("/v1/marketplace")
	{
		mp.POST("/sanitize-preview", ctrl.SanitizePreview)
		mp.POST("/publish", ctrl.Publish)
		mp.GET("/list", ctrl.List)
		mp.GET("/my-subscriptions", ctrl.ListMySubscriptions) // MUST precede /:id
		mp.GET("/:id", ctrl.Get)
		mp.POST("/:id/unpublish", ctrl.Unpublish)
		mp.POST("/:id/subscribe", ctrl.Subscribe)
		mp.DELETE("/:id/unsubscribe", ctrl.Unsubscribe)
	}

	admin := r.Group("/v1/admin/marketplace")
	{
		admin.POST("/:id/recommend", ctrl.SetRecommended)
	}
	return r
}

func doJSON(t *testing.T, r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// ---- tests: routing + binding smoke ----

func TestSanitizePreview_HappyPath(t *testing.T) {
	svc := &fakeService{sanitizeOut: "脱敏后正文"}
	r := newRouter(t, svc, 1, true)

	w := doJSON(t, r, http.MethodPost, "/v1/marketplace/sanitize-preview", map[string]uint{"skill_id": 10})
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 0, resp.Code)
	assert.Equal(t, "脱敏后正文", resp.Data["sanitized_body_md"])
}

func TestSanitizePreview_BindError(t *testing.T) {
	r := newRouter(t, &fakeService{}, 1, true)

	w := doJSON(t, r, http.MethodPost, "/v1/marketplace/sanitize-preview", map[string]string{"skill_id": "not-a-number"})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSanitizePreview_ChildBlockedViaBizError(t *testing.T) {
	svc := &fakeService{sanitizeErr: bizmarketplace.ErrChildAccountCannotAccessMarketplace}
	r := newRouter(t, svc, 3, false)

	w := doJSON(t, r, http.MethodPost, "/v1/marketplace/sanitize-preview", map[string]uint{"skill_id": 10})
	assert.Equal(t, http.StatusForbidden, w.Code, "ErrChildAccountCannotAccessMarketplace → 403")
}

func TestPublish_BindError(t *testing.T) {
	r := newRouter(t, &fakeService{}, 1, true)

	// Missing required fields (skill_id, category_tags, confirmed_sanitized_body)
	w := doJSON(t, r, http.MethodPost, "/v1/marketplace/publish", map[string]any{})
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestPublish_HappyPath(t *testing.T) {
	mp := &model.SkillMarketplace{Name: "X", IsPublic: true}
	mp.ID = 99
	svc := &fakeService{publishMP: mp}
	r := newRouter(t, svc, 1, true)

	body := map[string]any{
		"skill_id":                 10,
		"category_tags":            []string{"销售"},
		"confirmed_sanitized_body": "脱敏后正文",
	}
	w := doJSON(t, r, http.MethodPost, "/v1/marketplace/publish", body)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestPublish_SanitizeUnavailable_503(t *testing.T) {
	svc := &fakeService{publishErr: bizmarketplace.ErrSanitizeUnavailable}
	r := newRouter(t, svc, 1, true)

	body := map[string]any{
		"skill_id":                 10,
		"category_tags":            []string{"销售"},
		"confirmed_sanitized_body": "x",
	}
	w := doJSON(t, r, http.MethodPost, "/v1/marketplace/publish", body)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, "T7: 503 reflects LLM provider down, not internal server bug")
}

func TestPublish_ConfirmationMismatch_422(t *testing.T) {
	svc := &fakeService{publishErr: bizmarketplace.ErrSanitizeConfirmationMismatch}
	r := newRouter(t, svc, 1, true)

	body := map[string]any{
		"skill_id":                 10,
		"category_tags":            []string{"x"},
		"confirmed_sanitized_body": "tampered",
	}
	w := doJSON(t, r, http.MethodPost, "/v1/marketplace/publish", body)
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "T7: 422 reflects well-formed but semantically invalid request")
}

func TestGet_NotFound_404(t *testing.T) {
	svc := &fakeService{getErr: bizmarketplace.ErrMarketplaceNotFound}
	r := newRouter(t, svc, 1, true)

	w := doJSON(t, r, http.MethodGet, "/v1/marketplace/123", nil)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestGet_InvalidID_400(t *testing.T) {
	r := newRouter(t, &fakeService{}, 1, true)

	w := doJSON(t, r, http.MethodGet, "/v1/marketplace/abc", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code, "non-numeric :id → 400, not 500")
}

func TestGet_ZeroID_400(t *testing.T) {
	r := newRouter(t, &fakeService{}, 1, true)

	w := doJSON(t, r, http.MethodGet, "/v1/marketplace/0", nil)
	assert.Equal(t, http.StatusBadRequest, w.Code, ":id=0 → 400")
}

func TestSubscribe_SelfSubscribe_409(t *testing.T) {
	svc := &fakeService{subscribeErr: bizmarketplace.ErrSelfSubscribeForbidden}
	r := newRouter(t, svc, 1, true)

	w := doJSON(t, r, http.MethodPost, "/v1/marketplace/123/subscribe", nil)
	assert.Equal(t, http.StatusConflict, w.Code, "T7: 409 reflects publisher == subscriber state conflict")
}

func TestSubscribe_AlreadySubscribed_409(t *testing.T) {
	svc := &fakeService{subscribeErr: bizmarketplace.ErrAlreadySubscribed}
	r := newRouter(t, svc, 1, true)

	w := doJSON(t, r, http.MethodPost, "/v1/marketplace/123/subscribe", nil)
	assert.Equal(t, http.StatusConflict, w.Code, "T7: 409 reflects duplicate subscription resource state")
}

func TestSubscribe_HappyPath(t *testing.T) {
	svc := &fakeService{subscribeID: 777, subscribeSubID: 321}
	r := newRouter(t, svc, 1, true)

	w := doJSON(t, r, http.MethodPost, "/v1/marketplace/123/subscribe", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(777), resp.Data["cloned_skill_id"])
	assert.Equal(t, float64(321), resp.Data["subscription_id"], "spec §4.1 requires subscription_id, not marketplace_id")
}

func TestUnsubscribe_HappyPath(t *testing.T) {
	r := newRouter(t, &fakeService{}, 1, true)

	w := doJSON(t, r, http.MethodDelete, "/v1/marketplace/123/unsubscribe", nil)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestList_DefaultsApplied(t *testing.T) {
	r := newRouter(t, &fakeService{}, 1, true)

	w := doJSON(t, r, http.MethodGet, "/v1/marketplace/list", nil)
	assert.Equal(t, http.StatusOK, w.Code)
}

// Gin path order regression: /my-subscriptions must NOT be captured by /:id.
func TestMySubscriptions_NotCapturedAs_Id(t *testing.T) {
	r := newRouter(t, &fakeService{}, 1, true)

	w := doJSON(t, r, http.MethodGet, "/v1/marketplace/my-subscriptions", nil)
	require.Equal(t, http.StatusOK, w.Code, "must hit ListMySubscriptions handler (200), NOT Get with :id='my-subscriptions' (400)")
}

func TestMySubscriptions_PaginationDefaults(t *testing.T) {
	r := newRouter(t, &fakeService{}, 1, true)

	w := doJSON(t, r, http.MethodGet, "/v1/marketplace/my-subscriptions?page=0&page_size=999", nil)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, float64(1), resp.Data["page"], "page < 1 → default 1")
	assert.Equal(t, float64(100), resp.Data["page_size"], "page_size > 100 → cap 100")
}

func TestSetRecommended_AdminHappyPath(t *testing.T) {
	svc := &fakeService{}
	r := newRouter(t, svc, 1, true) // admin endpoint doesn't depend on user; we just need a router.

	body := map[string]bool{"recommended": true}
	idStr := strconv.Itoa(456)
	w := doJSON(t, r, http.MethodPost, "/v1/admin/marketplace/"+idStr+"/recommend", body)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, uint(456), svc.lastSetRecommendedID)
	assert.True(t, svc.lastRecommendedFlag)
}

func TestSetRecommended_NotFound_404(t *testing.T) {
	svc := &fakeService{setRecommendedErr: bizmarketplace.ErrMarketplaceNotFound}
	r := newRouter(t, svc, 1, true)

	w := doJSON(t, r, http.MethodPost, "/v1/admin/marketplace/999/recommend", map[string]bool{"recommended": true})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// Sanity: the sentinel-to-errno mapping is exhaustive — any unmapped
// marketplace sentinel returns 500 (caller should add to mapBizError). We
// verify the known set is covered by hitting each through a route.
func TestSentinelMapping_KnownErrorsAllMapped(t *testing.T) {
	// Build a mapping table; iterate and assert each maps to expected HTTP.
	cases := []struct {
		name     string
		sentinel error
		expected int
	}{
		// T7: HTTP codes from errno package match RFC 7231 semantics.
		{"ChildAccount", bizmarketplace.ErrChildAccountCannotAccessMarketplace, http.StatusForbidden},
		{"SkillNotOwned", bizmarketplace.ErrSkillNotOwned, http.StatusForbidden},
		{"AlreadyPublished", bizmarketplace.ErrSkillAlreadyPublished, http.StatusConflict}, // 409 — resource state conflict
		{"SelfSubscribe", bizmarketplace.ErrSelfSubscribeForbidden, http.StatusConflict},   // 409 — state conflict
		{"AlreadySubscribed", bizmarketplace.ErrAlreadySubscribed, http.StatusConflict},    // 409 — duplicate subscription
		{"MarketplaceNotFound", bizmarketplace.ErrMarketplaceNotFound, http.StatusNotFound},
		{"SubscriptionNotFound", bizmarketplace.ErrSubscriptionNotFound, http.StatusNotFound},
		{"SanitizeUnavailable", bizmarketplace.ErrSanitizeUnavailable, http.StatusServiceUnavailable},       // 503 — LLM down
		{"ConfirmMismatch", bizmarketplace.ErrSanitizeConfirmationMismatch, http.StatusUnprocessableEntity}, // 422 — well-formed but unprocessable
		{"SkillBodyEmpty", bizmarketplace.ErrSkillBodyEmpty, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Use Subscribe endpoint as a single funnel — biz Subscribe returns tc.sentinel.
			svc := &fakeService{subscribeErr: tc.sentinel}
			r := newRouter(t, svc, 1, true)
			w := doJSON(t, r, http.MethodPost, "/v1/marketplace/123/subscribe", nil)
			assert.Equal(t, tc.expected, w.Code, "sentinel %s must map to HTTP %d", tc.name, tc.expected)
		})
	}
}

// Compile-time guard: fakeService implements the Service interface.
var _ bizmarketplace.Service = (*fakeService)(nil)
