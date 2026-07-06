package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestMaintenanceMode_Disabled_PassesAll tests that all requests are passed when MAINTENANCE_MODE is not set
func TestMaintenanceMode_Disabled_PassesAll(t *testing.T) {
	// Ensure MAINTENANCE_MODE is not set
	t.Cleanup(func() {
		os.Unsetenv("MAINTENANCE_MODE")
	})
	os.Unsetenv("MAINTENANCE_MODE")

	// Test GET request
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/sop/list", nil)

		handler := MaintenanceMode()
		handler(c)

		assert.Equal(t, http.StatusOK, c.Writer.Status())
	}

	// Test POST request
	{
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/sop/execute", nil)

		handler := MaintenanceMode()
		handler(c)

		assert.Equal(t, http.StatusOK, c.Writer.Status())
	}
}

// TestMaintenanceMode_Enabled_GetPasses tests that GET requests pass when maintenance mode is enabled
func TestMaintenanceMode_Enabled_GetPasses(t *testing.T) {
	t.Cleanup(func() {
		os.Unsetenv("MAINTENANCE_MODE")
	})
	os.Setenv("MAINTENANCE_MODE", "true")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/sop/list", nil)

	handler := MaintenanceMode()
	handler(c)

	assert.Equal(t, http.StatusOK, c.Writer.Status())
}

// TestMaintenanceMode_Enabled_PostBlocks tests that POST requests return 503 when maintenance mode is enabled
func TestMaintenanceMode_Enabled_PostBlocks(t *testing.T) {
	t.Cleanup(func() {
		os.Unsetenv("MAINTENANCE_MODE")
	})
	os.Setenv("MAINTENANCE_MODE", "true")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/sop/execute", nil)

	handler := MaintenanceMode()
	handler(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "600", w.Header().Get("Retry-After"))
	assert.True(t, c.IsAborted())
}

// TestMaintenanceMode_Enabled_PaymentNotifyExempt tests that payment notify callbacks are exempt
func TestMaintenanceMode_Enabled_PaymentNotifyExempt(t *testing.T) {
	t.Cleanup(func() {
		os.Unsetenv("MAINTENANCE_MODE")
	})
	os.Setenv("MAINTENANCE_MODE", "true")

	paths := []string{
		"/v1/payment/wechat/notify",
		"/api/v1/payment/wechat/notify",
		"/v1/payment/alipay/notify",
		"/api/v1/payment/alipay/notify",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, path, nil)

			handler := MaintenanceMode()
			handler(c)

			assert.Equal(t, http.StatusOK, c.Writer.Status())
			assert.False(t, c.IsAborted())
		})
	}
}

// TestMaintenanceMode_Enabled_HeadPasses tests that HEAD requests pass when maintenance mode is enabled
func TestMaintenanceMode_Enabled_HeadPasses(t *testing.T) {
	t.Cleanup(func() {
		os.Unsetenv("MAINTENANCE_MODE")
	})
	os.Setenv("MAINTENANCE_MODE", "true")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodHead, "/v1/sop/list", nil)

	handler := MaintenanceMode()
	handler(c)

	assert.Equal(t, http.StatusOK, c.Writer.Status())
}
