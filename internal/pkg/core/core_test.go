package core

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"numind-server/internal/pkg/model"
)

// buildCtx 构造一个最简 gin.Context，附带指定 path/method。
func buildCtx(t *testing.T, path, method string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	return c
}

// TestBuildErrorLogFields_SkipsNoiseAndSuccess 断言噪声过滤：
// 401（token 过期）、404（favicon/missing route）不打印；
// < 400 的响应（含 200/302）也不打印。
func TestBuildErrorLogFields_SkipsNoiseAndSuccess(t *testing.T) {
	cases := []struct {
		name     string
		httpCode int
	}{
		{"200_ok", 200},
		{"302_redirect", 302},
		{"401_unauthorized", 401},
		{"404_not_found", 404},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := buildCtx(t, "/v1/test", "GET")
			fields := buildErrorLogFields(c, tc.httpCode, "X.Code", "msg")
			if fields != nil {
				t.Errorf("httpCode=%d expected nil fields, got %v", tc.httpCode, fields)
			}
		})
	}
}

// TestBuildErrorLogFields_LogsErrors 断言 400/403/500/其他 4xx/5xx 都会记录。
func TestBuildErrorLogFields_LogsErrors(t *testing.T) {
	cases := []struct {
		name     string
		httpCode int
	}{
		{"400_bind_error", 400},
		{"403_forbidden", 403},
		{"405_method_not_allowed", 405},
		{"422_unprocessable", 422},
		{"500_internal", 500},
		{"502_bad_gateway", 502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := buildCtx(t, "/v1/test", "POST")
			fields := buildErrorLogFields(c, tc.httpCode, "Some.Code", "some message")
			if fields == nil {
				t.Fatalf("httpCode=%d expected non-nil fields, got nil", tc.httpCode)
			}
			// 基础字段齐全：http_code / errno / message / path / method / client_ip（6 对 = 12 项）
			if len(fields) < 12 {
				t.Errorf("expected at least 12 field items (6 kv pairs), got %d: %v", len(fields), fields)
			}
		})
	}
}

// TestBuildErrorLogFields_AttachesUserID 断言 current_user 被 auth 中间件塞入 context 时，
// user_id 被附加到日志字段。
func TestBuildErrorLogFields_AttachesUserID(t *testing.T) {
	c := buildCtx(t, "/v1/sop/chat/stream", "POST")
	c.Set("current_user", &model.User{Model: gorm.Model{ID: 349}})

	fields := buildErrorLogFields(c, 403, "AuthFailure.Forbidden", "积分不足请充值")
	if fields == nil {
		t.Fatal("expected non-nil fields for 403")
	}

	// 在 kv 对里查 user_id
	found := false
	for i := 0; i < len(fields)-1; i += 2 {
		if fields[i] == "user_id" {
			if uid, ok := fields[i+1].(uint); ok && uid == 349 {
				found = true
				break
			}
			t.Errorf("user_id 类型或值异常: %T=%v", fields[i+1], fields[i+1])
		}
	}
	if !found {
		t.Error("未在日志字段中找到 user_id=349")
	}
}

// TestBuildErrorLogFields_AnonymousNoUserID 断言没有 current_user 时，
// 不会出现 user_id 字段（避免塞 0 或 nil 干扰日志）。
func TestBuildErrorLogFields_AnonymousNoUserID(t *testing.T) {
	c := buildCtx(t, "/v1/web/login", "POST")
	// 不设置 current_user

	fields := buildErrorLogFields(c, 400, "InvalidParameter.BindError", "body 无效")
	if fields == nil {
		t.Fatal("expected non-nil fields for 400")
	}
	for i := 0; i < len(fields)-1; i += 2 {
		if fields[i] == "user_id" {
			t.Errorf("匿名请求不应出现 user_id，实际: %v", fields[i+1])
		}
	}
}

// TestBuildErrorLogFields_WrongUserType 断言 current_user 类型不是 *model.User 时，
// 不会 panic 且不塞 user_id（防御性）。
func TestBuildErrorLogFields_WrongUserType(t *testing.T) {
	c := buildCtx(t, "/v1/test", "GET")
	c.Set("current_user", "not-a-user-struct")

	fields := buildErrorLogFields(c, 500, "InternalError", "boom")
	if fields == nil {
		t.Fatal("expected non-nil fields for 500")
	}
	for i := 0; i < len(fields)-1; i += 2 {
		if fields[i] == "user_id" {
			t.Errorf("类型不匹配时不应塞 user_id，实际: %v", fields[i+1])
		}
	}
}
