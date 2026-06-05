package sop

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestStartSSEHeartbeat_LifecycleAndMutex verifies the helper returns a usable
// write mutex and a stop func that tears the goroutine down cleanly (no panic,
// no deadlock).
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

// TestStartSSEHeartbeat_WritesHeartbeat shortens the interval and asserts the
// goroutine actually writes an SSE comment frame (behavior preserved per spec
// §3). The recorder body is read under the returned mutex — the same lock the
// heartbeat holds while writing — so the read is race-free.
func TestStartSSEHeartbeat_WritesHeartbeat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	old := sseHeartbeatInterval
	sseHeartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { sseHeartbeatInterval = old })

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/stream", nil)

	mu, stop := startSSEHeartbeat(c, w)
	defer stop()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(w.Body.String(), ":\n\n")
	}, time.Second, 5*time.Millisecond, "heartbeat must write an SSE comment within the interval")
}
