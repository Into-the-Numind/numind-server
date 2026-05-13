package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testHandler is a simple handler that returns 200 OK if the middleware passes.
func testHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"code": 0})
}

// newTestRouter creates a minimal test router with the idempotency middleware.
func newTestRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/test-write", RequireIdempotencyKey(), testHandler)
	r.PUT("/test-write", RequireIdempotencyKey(), testHandler)
	r.PATCH("/test-write", RequireIdempotencyKey(), testHandler)
	r.GET("/test-read", RequireIdempotencyKey(), testHandler)
	return r
}

// TestIdempotencyKey_ValidKeyPost validates that POST with a valid Idempotency-Key passes.
func TestIdempotencyKey_ValidKeyPost(t *testing.T) {
	r := newTestRouter()
	req := httptest.NewRequest(http.MethodPost, "/test-write", nil)
	req.Header.Set(headerIdempotencyKey, "valid-key-123")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "POST with valid key should pass")
}

// TestIdempotencyKey_ValidKeyPut validates that PUT with a valid Idempotency-Key passes.
func TestIdempotencyKey_ValidKeyPut(t *testing.T) {
	r := newTestRouter()
	req := httptest.NewRequest(http.MethodPut, "/test-write", nil)
	req.Header.Set(headerIdempotencyKey, "another-valid-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "PUT with valid key should pass")
}

// TestIdempotencyKey_MissingKeyPost validates that POST without Idempotency-Key returns 400.
func TestIdempotencyKey_MissingKeyPost(t *testing.T) {
	r := newTestRouter()
	req := httptest.NewRequest(http.MethodPost, "/test-write", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "POST without key should return 400")
	assert.Contains(t, w.Body.String(), "必填", "error message should indicate field is required")
}

// TestIdempotencyKey_MissingKeyPatch validates that PATCH without Idempotency-Key returns 400.
func TestIdempotencyKey_MissingKeyPatch(t *testing.T) {
	r := newTestRouter()
	req := httptest.NewRequest(http.MethodPatch, "/test-write", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "PATCH without key should return 400")
	assert.Contains(t, w.Body.String(), "必填", "error message should indicate field is required")
}

// TestIdempotencyKey_MissingKeyGet validates that GET without Idempotency-Key passes (read method exemption).
func TestIdempotencyKey_MissingKeyGet(t *testing.T) {
	r := newTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/test-read", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "GET without key should pass (read method exemption)")
}

// TestIdempotencyKey_TooLong validates that a key exceeding 64 chars returns 400.
func TestIdempotencyKey_TooLong(t *testing.T) {
	r := newTestRouter()
	req := httptest.NewRequest(http.MethodPost, "/test-write", nil)
	req.Header.Set(headerIdempotencyKey, "this-key-is-way-too-long-and-exceeds-the-maximum-length-of-64-characters-allowed-by-the-spec")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code, "key exceeding 64 chars should return 400")
	assert.Contains(t, w.Body.String(), "超限", "error message should indicate length exceeded")
}

// TestIdempotencyKey_Exactly64Chars validates that a key with exactly 64 chars passes.
func TestIdempotencyKey_Exactly64Chars(t *testing.T) {
	r := newTestRouter()
	key64 := "this-key-is-exactly-64-characters-long-and-should-pass-the-testa"
	require.Equal(t, 64, len(key64), "test key must be exactly 64 chars")
	req := httptest.NewRequest(http.MethodPost, "/test-write", nil)
	req.Header.Set(headerIdempotencyKey, key64)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "key with exactly 64 chars should pass")
}
