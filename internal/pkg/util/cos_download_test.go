package util

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClassifyCOSGetErr 验证 COS 下载错误归类（document-system T2）：
// 仅 404 → ErrCOSObjectNotFound（biz 映射 410 源已过期）；其它（含 403）原样返回，
// 不可误判为"不存在"。
func TestClassifyCOSGetErr(t *testing.T) {
	orig := errors.New("boom")

	tests := []struct {
		name       string
		statusCode int
		in         error
		wantNF     bool // 期望归类为 ErrCOSObjectNotFound
	}{
		{"404 → NotFound", http.StatusNotFound, orig, true},
		{"403 不可误判为 NotFound", http.StatusForbidden, orig, false},
		{"500 原样返回", http.StatusInternalServerError, orig, false},
		{"无状态码(0) 原样返回", 0, orig, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyCOSGetErr(tt.statusCode, tt.in)
			if tt.wantNF {
				assert.True(t, errors.Is(got, ErrCOSObjectNotFound), "404 应归类为 ErrCOSObjectNotFound")
			} else {
				assert.False(t, errors.Is(got, ErrCOSObjectNotFound), "非 404 不应被误判为 NotFound")
				assert.Equal(t, tt.in, got, "非 404 应原样返回原始错误")
			}
		})
	}
}

// TestCosRespStatus_NilSafe 验证 nil 响应安全取码。
func TestCosRespStatus_NilSafe(t *testing.T) {
	assert.Equal(t, 0, cosRespStatus(nil), "nil 响应应返回 0，不 panic")
}
