package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
)

// signTestToken 用给定 claims 签发一个 HS256 token（复用测试 jwt.secret）。
func signTestToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(viper.GetString("jwt.secret")))
	if err != nil {
		t.Fatalf("签发测试 token 失败: %v", err)
	}
	return s
}

// newScopeTestRouter 构造一个挂了 AuthMiddleware 的 gin engine，
// /v1/xhs/notes 与 /v1/sop/run 都回 200，便于断言中间件是否放行。
func newScopeTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	v1.Use(AuthMiddleware())
	v1.GET("/xhs/notes", func(c *gin.Context) { c.String(http.StatusOK, "xhs-ok") })
	v1.GET("/sop/run", func(c *gin.Context) { c.String(http.StatusOK, "sop-ok") })
	return r
}

func doScopeReq(r *gin.Engine, path, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	return w
}

// TestEnforceTokenScope 覆盖 xhs-collector T7 的最小权限收敛：
// scope=xhs token 打非 /v1/xhs/* 被 403、打 /v1/xhs/* 放行、无 scope 旧 token 不受影响。
func TestEnforceTokenScope(t *testing.T) {
	// store.S == nil 时 ValidateToken 走"简化用户"分支，不查 DB，测试无需数据库。
	viper.Set("jwt.secret", "test-secret-scope")

	exp := time.Now().Add(time.Hour).Unix()
	xhsToken := signTestToken(t, jwt.MapClaims{"user_id": float64(7), "exp": exp, "scope": "xhs"})
	plainToken := signTestToken(t, jwt.MapClaims{"user_id": float64(7), "exp": exp})
	unknownScopeToken := signTestToken(t, jwt.MapClaims{"user_id": float64(7), "exp": exp, "scope": "bogus"})

	r := newScopeTestRouter()

	t.Run("scope=xhs token 打 /v1/sop/* 被 403 拒绝", func(t *testing.T) {
		w := doScopeReq(r, "/v1/sop/run", xhsToken)
		if w.Code != http.StatusForbidden {
			t.Fatalf("期望 403，实际 %d (body=%s)", w.Code, w.Body.String())
		}
	})

	t.Run("scope=xhs token 打 /v1/xhs/* 放行", func(t *testing.T) {
		w := doScopeReq(r, "/v1/xhs/notes", xhsToken)
		if w.Code != http.StatusOK {
			t.Fatalf("期望 200，实际 %d (body=%s)", w.Code, w.Body.String())
		}
		if w.Body.String() != "xhs-ok" {
			t.Fatalf("期望命中 handler 'xhs-ok'，实际 %q", w.Body.String())
		}
	})

	t.Run("无 scope 旧 token 打 /v1/sop/* 正常放行", func(t *testing.T) {
		w := doScopeReq(r, "/v1/sop/run", plainToken)
		if w.Code != http.StatusOK {
			t.Fatalf("期望 200，实际 %d (body=%s)", w.Code, w.Body.String())
		}
		if w.Body.String() != "sop-ok" {
			t.Fatalf("期望命中 handler 'sop-ok'，实际 %q", w.Body.String())
		}
	})

	t.Run("无 scope 旧 token 打 /v1/xhs/* 正常放行", func(t *testing.T) {
		w := doScopeReq(r, "/v1/xhs/notes", plainToken)
		if w.Code != http.StatusOK {
			t.Fatalf("期望 200，实际 %d (body=%s)", w.Code, w.Body.String())
		}
	})

	t.Run("未知 scope token 一律保守拒绝", func(t *testing.T) {
		w := doScopeReq(r, "/v1/xhs/notes", unknownScopeToken)
		if w.Code != http.StatusForbidden {
			t.Fatalf("期望 403，实际 %d (body=%s)", w.Code, w.Body.String())
		}
	})
}
