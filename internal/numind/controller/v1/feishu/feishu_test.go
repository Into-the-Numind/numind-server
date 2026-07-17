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
	refresh *feishubiz.RefreshActionResult
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
func (f *lifecycleServiceFake) RefreshAction(_ context.Context, _ uint, sessionID string) (*feishubiz.RefreshActionResult, error) {
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
	expiresAt := time.Date(2026, 7, 14, 12, 2, 0, 0, time.UTC)
	svc := &lifecycleServiceFake{status: &feishubiz.StatusResult{
		State: "connected", Connected: true, AppIDMasked: "cli_****5678",
		Capabilities: map[string]feishubiz.CapabilityStatus{"docs": {State: "available"}},
		ActiveAction: &feishubiz.StatusAction{
			OperationID: "operation-0", SessionID: "session-0", Phase: "user_auth", ExpiresAt: expiresAt, LinkAvailable: true,
		},
	}}
	ctrl := NewController(svc)
	r := gin.New()
	r.GET("/v1/feishu/status", withUser(42), ctrl.Status)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/feishu/status", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"state":"connected"`)
	require.Contains(t, w.Body.String(), `"phase":"user_auth"`)
	require.NotContains(t, w.Body.String(), `"url"`)
	require.NotContains(t, w.Body.String(), "device_code")
}

func TestConnectAcceptsOnlyManualIntent(t *testing.T) {
	expiresAt := time.Date(2026, 7, 14, 12, 3, 0, 0, time.UTC)
	service := &lifecycleServiceFake{connect: &feishubiz.ConnectResult{State: "user_auth", Action: &feishubiz.OperationAction{
		Provider: "lark", OperationID: "operation-1", SessionID: "session-1", Phase: "user_auth",
		URL: "https://open.feishu.cn/suite/passport/oauth/device", Scopes: []string{"docx:document:create"}, ExpiresAt: expiresAt,
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
	require.JSONEq(t, `{
		"code": 0,
		"message": "",
		"data": {
			"state": "user_auth",
			"action": {
				"operation_id": "operation-1",
				"session_id": "session-1",
				"phase": "user_auth",
				"url": "https://open.feishu.cn/suite/passport/oauth/device",
				"expires_at": "2026-07-14T12:03:00Z"
			}
		}
	}`, valid.Body.String())
	require.NotContains(t, valid.Body.String(), `"scopes"`)
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

func TestResumePublicActionOmitsInternalScopes(t *testing.T) {
	expiresAt := time.Date(2026, 7, 14, 12, 5, 0, 0, time.UTC)
	service := &lifecycleServiceFake{resume: &feishubiz.OperationResult{
		OperationID: "operation-3", State: "waiting_user_auth",
		Action: &feishubiz.OperationAction{
			Provider: "lark", OperationID: "operation-3", SessionID: "session-3", Phase: "user_auth",
			URL: "https://open.feishu.cn/suite/passport/oauth/device", Scopes: []string{"wiki:node:create"}, ExpiresAt: expiresAt,
		},
	}}
	ctrl := NewController(service)
	r := gin.New()
	r.POST("/v1/feishu/operations/:id/resume", withUser(8), ctrl.ResumeOperation)

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/feishu/operations/operation-3/resume", strings.NewReader(`{"action":"user_completed"}`)))
	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{
		"code": 0,
		"message": "",
		"data": {
			"operation_id": "operation-3",
			"state": "waiting_user_auth",
			"action": {
				"operation_id": "operation-3",
				"session_id": "session-3",
				"phase": "user_auth",
				"url": "https://open.feishu.cn/suite/passport/oauth/device",
				"expires_at": "2026-07-14T12:05:00Z"
			}
		}
	}`, response.Body.String())
	require.NotContains(t, response.Body.String(), `"scopes"`)
	require.Contains(t, response.Body.String(), `"url"`)
}

func TestFeishuResumeAuthorizationHTTPMatrix(t *testing.T) {
	expiresAt := time.Date(2026, 7, 17, 8, 45, 0, 0, time.UTC)
	liveAction := &feishubiz.OperationAction{
		Provider: "lark-internal", OperationID: "operation-auth", SessionID: "session-live", Phase: "user_auth",
		URL: "https://open.feishu.cn/authorize/live-link", Scopes: []string{"wiki:node:create"}, ExpiresAt: expiresAt,
	}
	tests := []struct {
		name       string
		body       string
		result     *feishubiz.OperationResult
		err        error
		wantStatus int
		wantJSON   string
	}{
		{
			name: "pending", body: `{"action":"user_completed"}`, wantStatus: http.StatusOK,
			result:   &feishubiz.OperationResult{OperationID: "operation-auth", State: model.FeishuOperationWaitingUserAuth, NoticeCode: feishubiz.AuthorizationPending},
			wantJSON: `{"code":0,"message":"","data":{"operation_id":"operation-auth","state":"waiting_user_auth","notice_code":"authorization_pending"}}`,
		},
		{
			name: "processing", body: `{"action":"user_completed"}`, wantStatus: http.StatusOK,
			result:   &feishubiz.OperationResult{OperationID: "operation-auth", State: model.FeishuOperationWaitingUserAuth, NoticeCode: feishubiz.AuthorizationProcessing},
			wantJSON: `{"code":0,"message":"","data":{"operation_id":"operation-auth","state":"waiting_user_auth","notice_code":"authorization_processing"}}`,
		},
		{
			name: "rejected", body: `{"action":"user_completed"}`, wantStatus: http.StatusOK,
			result:   &feishubiz.OperationResult{OperationID: "operation-auth", State: model.FeishuOperationWaitingUserAuth, NoticeCode: feishubiz.AuthorizationRejected, Action: liveAction},
			wantJSON: `{"code":0,"message":"","data":{"operation_id":"operation-auth","state":"waiting_user_auth","notice_code":"authorization_rejected","action":{"operation_id":"operation-auth","session_id":"session-live","phase":"user_auth","url":"https://open.feishu.cn/authorize/live-link","expires_at":"2026-07-17T08:45:00Z"}}}`,
		},
		{
			name: "expired", body: `{"action":"user_completed"}`, wantStatus: http.StatusOK,
			result:   &feishubiz.OperationResult{OperationID: "operation-auth", State: model.FeishuOperationWaitingUserAuth, NoticeCode: feishubiz.AuthorizationExpired, Action: liveAction},
			wantJSON: `{"code":0,"message":"","data":{"operation_id":"operation-auth","state":"waiting_user_auth","notice_code":"authorization_expired","action":{"operation_id":"operation-auth","session_id":"session-live","phase":"user_auth","url":"https://open.feishu.cn/authorize/live-link","expires_at":"2026-07-17T08:45:00Z"}}}`,
		},
		{
			name: "updated", body: `{"action":"user_completed"}`, wantStatus: http.StatusOK,
			result:   &feishubiz.OperationResult{OperationID: "operation-auth", State: model.FeishuOperationWaitingUserAuth, NoticeCode: feishubiz.AuthorizationUpdated, Action: liveAction},
			wantJSON: `{"code":0,"message":"","data":{"operation_id":"operation-auth","state":"waiting_user_auth","notice_code":"authorization_updated","action":{"operation_id":"operation-auth","session_id":"session-live","phase":"user_auth","url":"https://open.feishu.cn/authorize/live-link","expires_at":"2026-07-17T08:45:00Z"}}}`,
		},
		{
			name: "success", body: `{"action":"user_completed"}`, wantStatus: http.StatusOK,
			result:   &feishubiz.OperationResult{OperationID: "operation-auth", State: model.FeishuOperationSucceeded},
			wantJSON: `{"code":0,"message":"","data":{"operation_id":"operation-auth","state":"succeeded"}}`,
		},
		{name: "invalid", body: `{"action":"unexpected"}`, err: feishubiz.ErrWorkspaceLifecycleInvalid, wantStatus: http.StatusBadRequest},
		{name: "not found", body: `{"action":"user_completed"}`, err: feishubiz.ErrWorkspaceLifecycleNotFound, wantStatus: http.StatusNotFound},
		{name: "conflict", body: `{"action":"user_completed"}`, err: feishubiz.ErrWorkspaceLifecycleConflict, wantStatus: http.StatusConflict},
		{name: "dependency", body: `{"action":"user_completed"}`, err: feishubiz.ErrWorkspaceLifecycleDependency, wantStatus: http.StatusServiceUnavailable},
		{
			name: "invariant", body: `{"action":"user_completed"}`,
			err:        errors.New("raw invariant device_code=PRIVATE_DEVICE_CODE url=https://open.feishu.cn/device?user_code=PRIVATE_QUERY home=/tmp/private"),
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := &lifecycleServiceFake{resume: tc.result, err: tc.err}
			ctrl := NewController(service)
			router := gin.New()
			router.POST("/v1/feishu/operations/:id/resume", withUser(8), ctrl.ResumeOperation)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/feishu/operations/operation-auth/resume", strings.NewReader(tc.body)))

			require.Equal(t, tc.wantStatus, response.Code)
			if tc.wantJSON != "" {
				require.JSONEq(t, tc.wantJSON, response.Body.String())
			}
			assertLifecycleResponseContainsNoInternalMaterial(t, response.Body.String())
			require.NotContains(t, response.Body.String(), "PRIVATE_DEVICE_CODE")
			require.NotContains(t, response.Body.String(), "PRIVATE_QUERY")
			if tc.wantStatus == http.StatusConflict {
				require.Contains(t, response.Body.String(), "飞书授权状态已更新，请使用最新步骤")
			}
			if tc.wantStatus == http.StatusServiceUnavailable {
				require.Contains(t, response.Body.String(), "飞书授权服务暂时不可用，请稍后重试")
			}
		})
	}
}

func assertLifecycleResponseContainsNoInternalMaterial(t *testing.T, body string) {
	t.Helper()
	lowerBody := strings.ToLower(body)
	for _, forbidden := range []string{"device_code", "scope", "app_id", "credential", "token", "home", "argv", "lark-internal"} {
		require.NotContains(t, lowerBody, forbidden)
	}
}

func TestRefreshUsesPathSessionOnlyAndUnbindReturnsRemoteAppDisclosure(t *testing.T) {
	expiresAt := time.Date(2026, 7, 14, 12, 4, 0, 0, time.UTC)
	service := &lifecycleServiceFake{
		refresh: &feishubiz.RefreshActionResult{Action: &feishubiz.OperationAction{
			Provider: "lark", OperationID: "operation-2", SessionID: "fresh", Phase: "app_scope",
			URL: "https://open.feishu.cn/suite/passport/oauth/device", Scopes: []string{"base:record:update"}, ExpiresAt: expiresAt,
		}},
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
	require.JSONEq(t, `{
		"code": 0,
		"message": "",
		"data": {
			"action": {
				"operation_id": "operation-2",
				"session_id": "fresh",
				"phase": "app_scope",
				"url": "https://open.feishu.cn/suite/passport/oauth/device",
				"expires_at": "2026-07-14T12:04:00Z"
			}
		}
	}`, refresh.Body.String())
	require.NotContains(t, refresh.Body.String(), `"scopes"`)

	unbound := httptest.NewRecorder()
	r.ServeHTTP(unbound, httptest.NewRequest(http.MethodDelete, "/v1/feishu/connection", nil))
	require.Equal(t, http.StatusOK, unbound.Code)
	require.Contains(t, unbound.Body.String(), "飞书侧个人自建应用仍保留")
	require.Equal(t, 1, service.unbindCalls)
}

func TestRefreshTerminalResultIsAllowlistedAndContainsNoAuthorizationMaterial(t *testing.T) {
	service := &lifecycleServiceFake{refresh: &feishubiz.RefreshActionResult{
		Terminal: &feishubiz.RefreshTerminalResult{OperationID: "operation-terminal", State: model.FeishuOperationFailed},
	}}
	ctrl := NewController(service)
	r := gin.New()
	r.POST("/v1/feishu/actions/:session_id/refresh", withUser(8), ctrl.RefreshAction)

	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/feishu/actions/stale-session/refresh", nil))

	require.Equal(t, http.StatusOK, response.Code)
	require.JSONEq(t, `{
		"code": 0,
		"message": "",
		"data": {
			"terminal": {
				"operation_id": "operation-terminal",
				"state": "failed"
			}
		}
	}`, response.Body.String())
	require.NotContains(t, response.Body.String(), `"action"`)
	require.NotContains(t, response.Body.String(), `"url"`)
	require.NotContains(t, response.Body.String(), `"scopes"`)
	require.NotContains(t, response.Body.String(), "device_code")
}

func TestRefreshUnavailableResponseDoesNotLeakLiveAction(t *testing.T) {
	service := &lifecycleServiceFake{
		refresh: &feishubiz.RefreshActionResult{Action: &feishubiz.OperationAction{
			Provider: "lark", SessionID: "fresh", URL: "https://open.feishu.cn/device?device_code=secret",
		}},
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
