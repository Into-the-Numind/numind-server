package agent

// student_run_stream.go implements the SSE streaming endpoint for agent runs.
// POST /v1/agent-runs/stream creates an agent_run row, acquires a single-
// subscriber SSE lock, and pumps stream.Event values to the client as
// Server-Sent Events.
//
// Wire format: each event is a JSON-encoded stream.Event value sent as:
//
//	data: <json>\n\n
//
// Keepalive frames are sent as SSE comments every 25 s:
//
//	:ping\n\n
//
// The connection terminates on:
//   - EventTerminal or EventError (normal/error end of run)
//   - Client disconnect (c.Request.Context() cancellation)
//   - Internal write failure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	agentbiz "numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

const (
	// sseEventChanCap is the recommended channel buffer for SSE event pumps.
	// 256 events gives plenty of headroom for fast-producing tool-call chains
	// without blocking the runner goroutine.
	sseEventChanCap = 256

	// ssePingInterval is the SSE keepalive comment interval.
	ssePingInterval = 25 * time.Second
)

// streamingRunSvc is the subset of StudentRunService used by CreateStream.
// Defined here so test files in the same package can implement a small stub
// rather than wiring the full concrete service.
type streamingRunSvc interface {
	AcquireStreamLock(ctx context.Context, userID uint, req agentbiz.CreateRunRequest) (runID uint64, acquired bool, err error)
	ReleaseStreamLock(runID uint64)
	RunStream(ctx context.Context, userID uint, req agentbiz.CreateRunRequest, runID uint64, ch chan<- stream.Event) (*agentbiz.RunResult, error)
}

// CreateStream handles POST /v1/agent-runs/stream.
//
// It creates the agent_run row, acquires the SSE single-subscriber lock, and
// then pumps stream.Event values to the client until the run terminates or the
// client disconnects.
func (h *StudentRunController) CreateStream(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req agentbiz.CreateRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	// Acquire lock (also pre-creates the agent_run row).
	runID, acquired, err := h.runSvc.AcquireStreamLock(c.Request.Context(), user.ID, req)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	if !acquired {
		// P1 fix (T07): core.WriteResponse always sets Data:nil when err != nil, so the
		// gin.H{"run_id": runID} was silently discarded. Bypass WriteResponse and write
		// the full structured response directly so the frontend can resume polling
		// the existing run ID.
		c.JSON(http.StatusConflict, gin.H{
			"code":    errno.ErrAgentStreamAlreadyAttached.Code,
			"message": errno.ErrAgentStreamAlreadyAttached.Message,
			"data":    gin.H{"run_id": runID},
		})
		return
	}
	defer h.runSvc.ReleaseStreamLock(runID)

	// --- Switch to SSE protocol ---
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	w := c.Writer

	// Langfuse SSE span: track first_byte_ms + total event count.
	ctx := c.Request.Context()
	traceID := ""
	if tc := langfuse.FromContext(ctx); tc != nil {
		traceID = tc.TraceID
	}
	_, recordFirstByte, finalizeSpan := stream.StartSSESpanWithFirstByte(ctx, traceID, runID)

	eventCount := 0
	disconnectReason := "run_complete"
	defer func() { finalizeSpan(eventCount, disconnectReason) }()

	// Derive a cancellable context so we can propagate client disconnect into
	// the runner goroutine.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	// Buffered channel owned by the pump goroutine below.
	eventCh := make(chan stream.Event, sseEventChanCap)

	// Goroutine: owns eventCh; closes it when RunStream returns.
	go func() {
		defer close(eventCh)
		_, _ = h.runSvc.RunStream(runCtx, user.ID, req, runID, eventCh)
	}()

	// SSE keepalive ticker.
	pingTicker := time.NewTicker(ssePingInterval)
	defer pingTicker.Stop()

	firstByteRecorded := false

	for {
		select {
		case <-c.Request.Context().Done():
			disconnectReason = "client_disconnect"
			return

		case <-pingTicker.C:
			if _, writeErr := fmt.Fprint(w, ":ping\n\n"); writeErr != nil {
				disconnectReason = "write_error"
				return
			}
			w.Flush()

		case ev, ok := <-eventCh:
			if !ok {
				// Channel closed — RunStream returned.
				return
			}

			data, marshalErr := json.Marshal(ev)
			if marshalErr != nil {
				// P2 fix: log the marshal failure before skipping.
				log.C(ctx).Warnw("CreateStream: marshal event failed",
					"event_type", ev.Type, "error", marshalErr)
				continue
			}

			if _, writeErr := fmt.Fprintf(w, "data: %s\n\n", data); writeErr != nil {
				disconnectReason = "write_error"
				return
			}
			w.Flush()

			if !firstByteRecorded {
				recordFirstByte()
				firstByteRecorded = true
			}
			eventCount++

			// Terminal events signal end of stream.
			if ev.Type == stream.EventTerminal || ev.Type == stream.EventError {
				return
			}
		}
	}
}
