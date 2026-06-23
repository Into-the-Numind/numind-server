package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	feishubiz "numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// fakeSvc is a scripted IFeishuService for controller tests.
type fakeSvc struct {
	connect  *feishubiz.ConnectResult
	status   *feishubiz.StatusResult
	callback *feishubiz.CallbackResult
	connErr  error
	statErr  error
	unbErr   error
	cbErr    error

	gotCode  string
	gotState string
	unbinds  int
}

func (f *fakeSvc) Connect(_ context.Context, _ uint, _ uint64, _ string) (*feishubiz.ConnectResult, error) {
	return f.connect, f.connErr
}
func (f *fakeSvc) Status(_ context.Context, _ uint) (*feishubiz.StatusResult, error) {
	return f.status, f.statErr
}
func (f *fakeSvc) Unbind(_ context.Context, _ uint) error {
	f.unbinds++
	return f.unbErr
}
func (f *fakeSvc) HandleCallback(_ context.Context, code, state string) (*feishubiz.CallbackResult, error) {
	f.gotCode = code
	f.gotState = state
	return f.callback, f.cbErr
}

func init() { gin.SetMode(gin.TestMode) }

// withUser injects a current-user into the gin context (mirrors AuthMiddleware,
// which c.Set("current_user", *model.User); middleware.GetCurrentUser reads it).
func withUser(uid uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("current_user", &model.User{Model: gorm.Model{ID: uid}})
		c.Next()
	}
}

func TestCallback_StateInvalid_RedirectsToError(t *testing.T) {
	svc := &fakeSvc{
		callback: &feishubiz.CallbackResult{
			RedirectURL: "https://youshu.asia/settings/connections?feishu=error&reason=invalid_state",
			Success:     false,
		},
		cbErr: errno.ErrLarkStateInvalid,
	}
	c := NewController(svc)
	r := gin.New()
	r.GET("/v1/feishu/oauth/callback", c.Callback)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/feishu/oauth/callback?code=x&state=bad", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != svc.callback.RedirectURL {
		t.Fatalf("Location = %q, want %q", loc, svc.callback.RedirectURL)
	}
	if svc.gotCode != "x" || svc.gotState != "bad" {
		t.Fatalf("svc got code=%q state=%q", svc.gotCode, svc.gotState)
	}
}

func TestCallback_Success_RedirectsToConnected(t *testing.T) {
	svc := &fakeSvc{
		callback: &feishubiz.CallbackResult{
			RedirectURL: "https://youshu.asia/settings/connections?feishu=connected",
			Success:     true,
		},
	}
	c := NewController(svc)
	r := gin.New()
	r.GET("/v1/feishu/oauth/callback", c.Callback)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/feishu/oauth/callback?code=abc&state=ok", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != svc.callback.RedirectURL {
		t.Fatalf("Location = %q", got)
	}
}

func TestStatus_JSON(t *testing.T) {
	svc := &fakeSvc{status: &feishubiz.StatusResult{Connected: true, Status: "active", AppID: "cli_app", Scopes: []string{"docx:document"}}}
	c := NewController(svc)
	r := gin.New()
	r.GET("/v1/feishu/status", withUser(42), c.Status)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/feishu/status", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"code":0`) || !contains(body, `"app_id":"cli_app"`) || !contains(body, `"status":"active"`) {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestConnect_JSON(t *testing.T) {
	svc := &fakeSvc{connect: &feishubiz.ConnectResult{NextStep: feishubiz.NextStepAuthorize, URL: "https://open.feishu.cn/auth", State: "sig"}}
	c := NewController(svc)
	r := gin.New()
	r.POST("/v1/feishu/connect", withUser(42), c.Connect)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/feishu/connect", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"next_step":"authorize"`) || !contains(body, `"state":"sig"`) {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestUnbind_JSON(t *testing.T) {
	svc := &fakeSvc{}
	c := NewController(svc)
	r := gin.New()
	r.DELETE("/v1/feishu/connection", withUser(42), c.Unbind)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/feishu/connection", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if svc.unbinds != 1 {
		t.Fatalf("unbinds = %d, want 1", svc.unbinds)
	}
}

func TestUnauthenticated_Connect(t *testing.T) {
	svc := &fakeSvc{}
	c := NewController(svc)
	r := gin.New()
	r.POST("/v1/feishu/connect", c.Connect) // no user injected

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/feishu/connect", nil)
	r.ServeHTTP(w, req)

	body := w.Body.String()
	if contains(body, `"code":0`) {
		t.Fatalf("expected an auth error, got success: %s", body)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
