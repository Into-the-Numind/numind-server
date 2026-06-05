package sop

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestStartSSEHeartbeat_LifecycleAndMutex verifies the helper returns a usable
// write mutex and a stop func that tears the goroutine down cleanly (no panic,
// no deadlock). The 15s tick interval is a const so the tick itself is not
// fast-unit-testable; the lifecycle + returned mutex contract is what callers
// depend on.
func TestStartSSEHeartbeat_LifecycleAndMutex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder() // *httptest.ResponseRecorder implements http.Flusher
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/stream", nil)

	mu, stop := startSSEHeartbeat(c, w)
	require.NotNil(t, mu, "mutex must be returned for callers to guard writes")
	require.NotNil(t, stop, "stop func must be returned")

	// The mutex must be usable by the caller for content writes.
	mu.Lock()
	mu.Unlock()

	// stop() must tear down the heartbeat goroutine without panic; calling the
	// returned cancel twice (e.g. via defer + explicit) must be safe.
	require.NotPanics(t, func() {
		stop()
		stop()
	})
}
