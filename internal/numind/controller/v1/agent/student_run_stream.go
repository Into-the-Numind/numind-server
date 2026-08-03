package agent

// student_run_stream.go implements observer-only SSE endpoints for agent runs.
// Execution is admitted and supervised by biz/agent before the controller
// switches to SSE; the HTTP connection only observes replayable published
// events. Client disconnects therefore stop the observer without cancelling the
// detached runner/resume.
//
// Wire format: each event is a JSON-encoded stream.Event value sent as:
//
//	data: <json>\n\n
//
// Published events may also carry a replay cursor, sent as:
//
//	id: <cursor>\n
//	data: <json>\n\n
//
// Keepalive frames are sent as SSE comments every 25 s:
//
//	:ping\n\n

import (
	"context"
	"encoding/json"
	"errors"
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
	// ssePingInterval is the SSE keepalive comment interval.
	ssePingInterval = 25 * time.Second
)

// streamingRunSvc is the subset of StudentRunService used by the SSE handlers.
// Tests in this package implement the seam without wiring the full concrete
// service, while production still passes *agentbiz.StudentRunService.
type streamingRunSvc interface {
	PrepareStreamRun(ctx context.Context, userID uint, req agentbiz.CreateRunRequest) (*agentbiz.PreparedStreamRun, error)
	StartPreparedStreamRun(prepared *agentbiz.PreparedStreamRun) bool
	StartPreparedAnswerStream(ctx context.Context, userID uint, runID uint64, req agentbiz.AnswerRequest) (bool, error)
	SubscribeRunEvents(ctx context.Context, userID uint, runID uint64, after string) (<-chan stream.PublishedEvent, error)
}

type observerFallbackStart struct {
	runID     uint64
	sessionID string
}

// CreateStream handles POST /v1/agent-runs/stream.
//
// It prepares the run synchronously so validation/authorization errors remain
// JSON responses, starts detached supervised execution, then observes the run
// event broker over SSE.
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

	prepared, err := h.runSvc.PrepareStreamRun(c.Request.Context(), user.ID, req)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	if prepared == nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("prepare stream run returned nil"), nil)
		return
	}

	_ = h.runSvc.StartPreparedStreamRun(prepared)
	if switchToSSE(c) != nil {
		return
	}
	observeRunEvents(c, h.runSvc, user.ID, prepared.RunID, "", &observerFallbackStart{
		runID:     prepared.RunID,
		sessionID: prepared.SessionID,
	})
}

// AnswerStream handles POST /v1/agent-runs/:id/answer-stream.
//
// StartPreparedAnswerStream performs ownership/state/answer validation and
// persists the answer before the controller switches to SSE. Once it returns
// started=true, the resumed runner is supervised outside the request context.
func (h *StudentRunController) AnswerStream(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	runID, ok := mustParseRunID(c)
	if !ok {
		return
	}

	var req agentbiz.AnswerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	started, err := h.runSvc.StartPreparedAnswerStream(c.Request.Context(), user.ID, runID, req)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	if !started {
		subscribeThenObserveRunEvents(c, h.runSvc, user.ID, runID, "", nil)
		return
	}

	if switchToSSE(c) != nil {
		return
	}
	observeRunEvents(c, h.runSvc, user.ID, runID, "", nil)
}

// SubscribeEvents attaches the authenticated browser to a run's replayable
// event transport after the original HTTP response ended at an external-action
// card. Each client has its own cursor; events are never consumer-grouped.
func (h *StudentRunController) SubscribeEvents(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	runID, ok := mustParseRunID(c)
	if !ok {
		return
	}

	subscribeThenObserveRunEvents(c, h.runSvc, user.ID, runID, c.Query("after"), nil)
}

func observeRunEvents(
	c *gin.Context,
	svc streamingRunSvc,
	userID uint,
	runID uint64,
	after string,
	fallback *observerFallbackStart,
) {
	events, err := svc.SubscribeRunEvents(c.Request.Context(), userID, runID, after)
	if err != nil {
		if errors.Is(err, stream.ErrRunEventBrokerUnavailable) && fallback != nil {
			_ = writeObserverFallbackStart(c, fallback.runID, fallback.sessionID)
			return
		}
		if !errors.Is(err, context.Canceled) {
			log.C(c.Request.Context()).Warnw("observeRunEvents: subscribe after SSE failed",
				"run_id", runID, "error", err)
		}
		return
	}

	writePublishedRunEvents(c, runID, events)
}

func subscribeThenObserveRunEvents(
	c *gin.Context,
	svc streamingRunSvc,
	userID uint,
	runID uint64,
	after string,
	fallback *observerFallbackStart,
) {
	events, err := svc.SubscribeRunEvents(c.Request.Context(), userID, runID, after)
	if err != nil {
		if errors.Is(err, stream.ErrRunEventBrokerUnavailable) && fallback != nil {
			if switchToSSE(c) != nil {
				return
			}
			_ = writeObserverFallbackStart(c, fallback.runID, fallback.sessionID)
			return
		}
		writeRunEventSubscribeError(c, err)
		return
	}
	if switchToSSE(c) != nil {
		return
	}
	writePublishedRunEvents(c, runID, events)
}

func writeRunEventSubscribeError(c *gin.Context, err error) {
	if errors.Is(err, stream.ErrInvalidRunEventCursor) {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid event cursor"), nil)
		return
	}
	if errors.Is(err, stream.ErrRunEventBrokerUnavailable) {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"code":    "FailedOperation.AgentEventStreamUnavailable",
			"message": "Agent event stream is temporarily unavailable.",
		})
		return
	}
	core.WriteResponse(c, err, nil)
}

func switchToSSE(c *gin.Context) error {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	if _, err := fmt.Fprint(c.Writer, ":ok\n\n"); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
}

func writePublishedRunEvents(c *gin.Context, runID uint64, events <-chan stream.PublishedEvent) {
	ctx := c.Request.Context()
	traceID := ""
	if tc := langfuse.FromContext(ctx); tc != nil {
		traceID = tc.TraceID
	}
	_, recordFirstByte, finalizeSpan := stream.StartSSESpanWithFirstByte(ctx, traceID, runID)

	eventCount := 0
	disconnectReason := "run_complete"
	defer func() { finalizeSpan(eventCount, disconnectReason) }()

	pingTicker := time.NewTicker(ssePingInterval)
	defer pingTicker.Stop()

	firstByteRecorded := false
	for {
		select {
		case <-ctx.Done():
			disconnectReason = "observer_disconnect"
			return
		case <-pingTicker.C:
			if _, err := fmt.Fprint(c.Writer, ":ping\n\n"); err != nil {
				disconnectReason = "write_error"
				return
			}
			c.Writer.Flush()
		case published, open := <-events:
			if !open {
				return
			}
			data, marshalErr := json.Marshal(published.Event)
			if marshalErr != nil {
				log.C(ctx).Warnw("writePublishedRunEvents: marshal event failed",
					"run_id", runID, "event_type", published.Event.Type, "error", marshalErr)
				continue
			}

			var writeErr error
			if published.Cursor != "" {
				_, writeErr = fmt.Fprintf(c.Writer, "id: %s\ndata: %s\n\n", published.Cursor, data)
			} else {
				_, writeErr = fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			}
			if writeErr != nil {
				disconnectReason = "write_error"
				return
			}
			c.Writer.Flush()

			if !firstByteRecorded {
				recordFirstByte()
				firstByteRecorded = true
			}
			eventCount++

			if isFinalPublishedEvent(published.Event) {
				return
			}
		}
	}
}

func writeObserverFallbackStart(c *gin.Context, runID uint64, sessionID string) error {
	frame := struct {
		Type  stream.EventType `json:"type"`
		RunID uint64           `json:"run_id"`
		Data  struct {
			SessionID        string `json:"session_id"`
			RunID            uint64 `json:"run_id"`
			ObserverFallback bool   `json:"observer_fallback"`
		} `json:"data"`
	}{
		Type:  stream.EventStreamStart,
		RunID: runID,
	}
	frame.Data.SessionID = sessionID
	frame.Data.RunID = runID
	frame.Data.ObserverFallback = true

	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", data); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
}

func isFinalPublishedEvent(event stream.Event) bool {
	return event.Type == stream.EventTerminal || event.Type == stream.EventError
}
