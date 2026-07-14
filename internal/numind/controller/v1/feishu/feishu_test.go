package feishu

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	feishubiz "numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/model"
)

type lifecycleServiceFake struct {
	connect *feishubiz.ConnectResult
	status  *feishubiz.StatusResult
	resume  *feishubiz.OperationResult
	refresh *feishubiz.OperationAction
	unbound *feishubiz.UnbindResult
	err     error

	connectCalls int
	resumeID     string
	resumeAction string
	refreshID    string
	unbindCalls  int
}

func (f *lifecycleServiceFake) Connect(_ context.Context, _ uint) (*feishubiz.ConnectResult, error) {
	f.connectCalls++
	return f.connect, f.err
}
func (f *lifecycleServiceFake) Status(context.Context, uint) (*feishubiz.StatusResult, error) {
	return f.status, f.err
}
func (f *lifecycleServiceFake) Resume(_ context.Context, _ uint, operationID, action string) (*feishubiz.OperationResult, error) {
	f.resumeID, f.resumeAction = operationID, action
	return f.resume, f.err
}
func (f *lifecycleServiceFake) RefreshAction(_ context.Context, _ uint, sessionID string) (*feishubiz.OperationAction, error) {
	f.refreshID = sessionID
	return f.refresh, f.err
}

func (f *lifecycleServiceFake) Unbind(context.Context, uint) (*feishubiz.UnbindResult, error) {
	f.unbindCalls++
	return f.unbound, f.err
}

func init() { gin.SetMode(gin.TestMode) }

func withUser(uid uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("current_user", &model.User{Model: gorm.Model{ID: uid}})
		c.Next()
	}
}

func TestStatus_JSONIsReadOnlyShape(t *testing.T) {
	svc := &lifecycleServiceFake{status: &feishubiz.StatusResult{
		State: "connected", Connected: true, AppIDMasked: "cli_****5678",
		Capabilities: map[string]feishubiz.CapabilityStatus{"docs": {State: "available"}},
	}}
	ctrl := NewController(svc)
	r := gin.New()
	r.GET("/v1/feishu/status", withUser(42), ctrl.Status)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/feishu/status", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"state":"connected"`)
	require.NotContains(t, w.Body.String(), `"url"`)
	require.NotContains(t, w.Body.String(), "device_code")
}

func TestConnectAcceptsOnlyManualIntent(t *testing.T) {
	service := &lifecycleServiceFake{connect: &feishubiz.ConnectResult{State: "user_auth", Action: &feishubiz.OperationAction{
		Provider: "lark", SessionID: "session-1", URL: "https://open.feishu.cn/suite/passport/oauth/device", ExpiresAt: time.Now().Add(time.Minute),
	}}}
	ctrl := NewController(service)
	r := gin.New()
	r.POST("/v1/feishu/connect", withUser(42), ctrl.Connect)

	invalid := httptest.NewRecorder()
	r.ServeHTTP(invalid, httptest.NewRequest(http.MethodPost, "/v1/feishu/connect", strings.NewReader(`{"intent":"manual","scopes":["im:message"]}`)))
	require.Equal(t, http.StatusBadRequest, invalid.Code)
	require.Zero(t, service.connectCalls)

	valid := httptest.NewRecorder()
	r.ServeHTTP(valid, httptest.NewRequest(http.MethodPost, "/v1/feishu/connect", strings.NewReader(`{"intent":"manual"}`)))
	require.Equal(t, http.StatusOK, valid.Code)
	require.Equal(t, 1, service.connectCalls)
}

func TestResumeAllowsOnlyFixedActionAndCollapsesCrossUserTo404(t *testing.T) {
	service := &lifecycleServiceFake{resume: &feishubiz.OperationResult{OperationID: "op-1", State: "waiting_user_auth"}}
	ctrl := NewController(service)
	r := gin.New()
	r.POST("/v1/feishu/operations/:id/resume", withUser(8), ctrl.ResumeOperation)

	invalid := httptest.NewRecorder()
	r.ServeHTTP(invalid, httptest.NewRequest(http.MethodPost, "/v1/feishu/operations/op-1/resume", strings.NewReader(`{"action":"user_completed","argv":["docs"]}`)))
	require.Equal(t, http.StatusBadRequest, invalid.Code)
	require.Empty(t, service.resumeID)

	service.err = feishubiz.ErrWorkspaceLifecycleNotFound
	notFound := httptest.NewRecorder()
	r.ServeHTTP(notFound, httptest.NewRequest(http.MethodPost, "/v1/feishu/operations/op-owned-by-7/resume", strings.NewReader(`{"action":"user_completed"}`)))
	require.Equal(t, http.StatusNotFound, notFound.Code)
}

func TestRefreshUsesPathSessionOnlyAndUnbindReturnsRemoteAppDisclosure(t *testing.T) {
	service := &lifecycleServiceFake{
		refresh: &feishubiz.OperationAction{Provider: "lark", SessionID: "fresh", URL: "https://open.feishu.cn/suite/passport/oauth/device"},
		unbound: &feishubiz.UnbindResult{State: "none", Connected: false, Message: "有数侧连接已删除；飞书侧个人自建应用仍保留，可在飞书开放平台自行删除"},
	}
	ctrl := NewController(service)
	r := gin.New()
	r.POST("/v1/feishu/actions/:session_id/refresh", withUser(8), ctrl.RefreshAction)
	r.DELETE("/v1/feishu/connection", withUser(8), ctrl.Unbind)

	refresh := httptest.NewRecorder()
	r.ServeHTTP(refresh, httptest.NewRequest(http.MethodPost, "/v1/feishu/actions/old-session/refresh", strings.NewReader(`{"device_code":"never"}`)))
	require.Equal(t, http.StatusBadRequest, refresh.Code)
	require.Empty(t, service.refreshID)

	refresh = httptest.NewRecorder()
	r.ServeHTTP(refresh, httptest.NewRequest(http.MethodPost, "/v1/feishu/actions/old-session/refresh", nil))
	require.Equal(t, http.StatusOK, refresh.Code)
	require.Equal(t, "old-session", service.refreshID)

	unbound := httptest.NewRecorder()
	r.ServeHTTP(unbound, httptest.NewRequest(http.MethodDelete, "/v1/feishu/connection", nil))
	require.Equal(t, http.StatusOK, unbound.Code)
	require.Contains(t, unbound.Body.String(), "飞书侧个人自建应用仍保留")
	require.Equal(t, 1, service.unbindCalls)
}

func TestRefreshUnavailableResponseDoesNotLeakLiveAction(t *testing.T) {
	service := &lifecycleServiceFake{
		refresh: &feishubiz.OperationAction{
			Provider: "lark", SessionID: "fresh", URL: "https://open.feishu.cn/device?device_code=secret",
		},
		err: feishubiz.ErrWorkspaceLifecycleUnavailable,
	}
	ctrl := NewController(service)
	r := gin.New()
	r.POST("/v1/feishu/actions/:session_id/refresh", withUser(8), ctrl.RefreshAction)

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/feishu/actions/session-1/refresh", nil))
	require.Equal(t, http.StatusInternalServerError, response.Code)
	require.NotContains(t, response.Body.String(), "https://open.feishu.cn")
	require.NotContains(t, response.Body.String(), "device_code")
	require.NotContains(t, response.Body.String(), "secret")
}

func TestUnbindRejectsAnyRequestBodyBeforeInvokingLifecycleService(t *testing.T) {
	service := &lifecycleServiceFake{unbound: &feishubiz.UnbindResult{State: "none"}}
	ctrl := NewController(service)
	r := gin.New()
	r.DELETE("/v1/feishu/connection", withUser(8), ctrl.Unbind)

	for _, body := range []string{`{"argv":["auth","logout"]}`, `{"scopes":["im:message"]}`, `null`, `trailing bytes`} {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/v1/feishu/connection", strings.NewReader(body)))
		require.Equal(t, http.StatusBadRequest, response.Code, body)
	}
	require.Zero(t, service.unbindCalls)
}

func TestUnauthenticatedLifecycleActionFails(t *testing.T) {
	ctrl := NewController(&lifecycleServiceFake{err: errors.New("must not be reached")})
	r := gin.New()
	r.POST("/v1/feishu/connect", ctrl.Connect)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/feishu/connect", strings.NewReader(`{"intent":"manual"}`)))
	require.NotEqual(t, http.StatusOK, w.Code)
}
