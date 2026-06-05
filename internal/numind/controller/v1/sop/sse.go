package sop

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/log"
)

// sseHeartbeatInterval is how often an SSE comment heartbeat is written to keep
// a streaming connection alive during long generations.
const sseHeartbeatInterval = 15 * time.Second

// startSSEHeartbeat launches a goroutine that writes an SSE comment line
// (":\n\n") every sseHeartbeatInterval so proxies / browsers keep the streaming
// connection open while a long generation produces no output (problem 3).
//
// It returns the write mutex — callers MUST hold it for every c.Writer write so
// heartbeats never interleave with content frames — and a stop func to defer.
// The heartbeat also stops on request-context cancellation (client disconnect)
// or on the first failed write. The previous per-handler copies wrapped the
// ticker tick in a redundant second c.Request.Context().Done() check; this
// helper drops it (the goroutine's ctx is already derived from the request ctx).
func startSSEHeartbeat(c *gin.Context, flusher http.Flusher) (*sync.Mutex, func()) {
	mu := &sync.Mutex{}
	ctx, cancel := context.WithCancel(c.Request.Context())
	ticker := time.NewTicker(sseHeartbeatInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				_, err := c.Writer.WriteString(":\n\n")
				if err == nil {
					flusher.Flush()
				}
				mu.Unlock()
				if err != nil {
					log.C(c).Warnw("Failed to send heartbeat", "error", err)
					return
				}
			}
		}
	}()
	return mu, cancel
}
