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
	// sseEventChanCap is the recommended channel buffer for SSE event pumps.
	// 256 events gives plenty of headroom for fast-producing tool-call chains
	// without blocking the runner goroutine.
	sseEventChanCap = 256

	// ssePingInterval is the SSE keepalive comment interval.
	ssePingInterval = 25 * time.Second
)

// streamingRunSvc is the subset of StudentRunService used by the SSE handlers
// (CreateStream + AnswerStream). Defined here so test files in the same package
// can implement a small stub rather than wiring the full concrete service.
type streamingRunSvc interface {
	AcquireStreamLock(ctx context.Context, userID uint, req agentbiz.CreateRunRequest) (runID uint64, acquired bool, err error)
	ReleaseStreamLock(runID uint64)
	RunStream(ctx context.Context, userID uint, req agentbiz.CreateRunRequest, runID uint64, ch chan<- stream.Event) (*agentbiz.RunResult, error)
	// AcquireResumeStreamLock acquires the SSE lock for an existing paused run
	// (issue4 streaming answer resume); does NOT pre-create a row.
	AcquireResumeStreamLock(runID uint64) bool
	// AnswerStream validates + persists the answer, then streams the resumed leg
	// onto ch (issue4). ch is owned by the caller (controller closes it).
	AnswerStream(ctx context.Context, userID uint, runID uint64, req agentbiz.AnswerRequest, ch chan<- stream.Event) (*agentbiz.RunResult, error)
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

	// Pump the run's events to the client over SSE. The produce closure drives
	// RunStream on the (cancellable) runCtx; the shared streamEvents method owns
	// the headers + first-byte flush + drain-mode loop (identical to AnswerStream).
	h.streamEvents(c, runID, func(runCtx context.Context, ch chan<- stream.Event) {
		_, _ = h.runSvc.RunStream(runCtx, user.ID, req, runID, ch)
	})
}

// AnswerStream handles POST /v1/agent-runs/:id/answer-stream (issue4): the
// streaming counterpart of Answer. It validates + persists the user's answer
// inside the biz layer (shared with the poll path), then streams the resumed
// agent leg over SSE so the user sees assistant prose live instead of poll-only
// tool narration. The legacy Answer + polling path is preserved as a fallback;
// on a 409 (already-attached) or network failure the frontend falls back to it.
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

	// Acquire the SSE lock on the EXISTING paused run (does not pre-create a row).
	// A second subscriber gets 409 + run_id so the frontend resumes polling.
	if !h.runSvc.AcquireResumeStreamLock(runID) {
		c.JSON(http.StatusConflict, gin.H{
			"code":    errno.ErrAgentStreamAlreadyAttached.Code,
			"message": errno.ErrAgentStreamAlreadyAttached.Message,
			"data":    gin.H{"run_id": runID},
		})
		return
	}
	defer h.runSvc.ReleaseStreamLock(runID)

	// Ownership / state / answer validation happens inside AnswerStream (the
	// shared validateAndPersistAnswer helper, step 1). A cross-user caller would
	// transiently hold the lock until validation fails, which is harmless (the
	// deferred Release frees it immediately).
	h.streamEvents(c, runID, func(runCtx context.Context, ch chan<- stream.Event) {
		_, _ = h.runSvc.AnswerStream(runCtx, user.ID, runID, req, ch)
	})
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
	after := c.Query("after")
	events, err := h.runSvc.SubscribeRunEvents(c.Request.Context(), user.ID, runID, after)
	if err != nil {
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
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	w := c.Writer
	_, _ = fmt.Fprint(w, ":ok\n\n")
	w.Flush()

	pingTicker := time.NewTicker(ssePingInterval)
	defer pingTicker.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-pingTicker.C:
			if _, writeErr := fmt.Fprint(w, ":ping\n\n"); writeErr != nil {
				return
			}
			w.Flush()
		case published, open := <-events:
			if !open {
				return
			}
			data, marshalErr := json.Marshal(published.Event)
			if marshalErr != nil {
				continue
			}
			if _, writeErr := fmt.Fprintf(w, "id: %s\ndata: %s\n\n", published.Cursor, data); writeErr != nil {
				return
			}
			w.Flush()
			if isFinalPublishedTerminal(published.Event) {
				return
			}
		}
	}
}

func isFinalPublishedTerminal(event stream.Event) bool {
	if event.Type != stream.EventTerminal {
		return false
	}
	var payload stream.TerminalPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return true
	}
	return payload.Reason != string(agentbiz.TerminalWaitingForUserChoice)
}

// streamEvents runs the shared SSE pump: it switches the response to the SSE
// protocol, flushes a first byte, spawns produce in a goroutine to feed the
// event channel, and drains the channel to the client until terminal + channel
// close. produce MUST eventually return (closing the channel) and must respect
// the runCtx it is handed (cancelled on client disconnect). The drain-mode logic
// (keep selecting after a terminal/error frame so the producer's finalize DB
// writes land on a live context) is identical for CreateStream and AnswerStream;
// centralising it here avoids re-deriving the subtle drain handling per handler
// (dev agent_run 45 empty-session bug). runID is used only for the Langfuse span.
func (h *StudentRunController) streamEvents(c *gin.Context, runID uint64, produce func(runCtx context.Context, ch chan<- stream.Event)) {
	// --- Switch to SSE protocol ---
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	w := c.Writer

	// Push HTTP response status + headers to the client immediately via an SSE
	// comment line (the ":<text>\n\n" frame is silently ignored by EventSource
	// and parseAgentSseChunk). Without this, RunStream's ~450 lines of prep
	// (skill load / prompt build / memory load / model resolve) run before the
	// channel has anything to emit, the response stays buffered, and fetch()
	// readers time out after ~10 s with an empty UI. See test
	// TestCreateStream_FirstByteFlushedBeforeRunStream.
	_, _ = fmt.Fprint(w, ":ok\n\n")
	w.Flush()

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

	// Goroutine: owns eventCh; closes it when produce returns.
	go func() {
		defer close(eventCh)
		produce(runCtx, eventCh)
	}()

	// SSE keepalive ticker.
	pingTicker := time.NewTicker(ssePingInterval)
	defer pingTicker.Stop()

	firstByteRecorded := false
	// terminalSeen flips true once we've emitted a terminal/error frame. After
	// that we MUST keep selecting on eventCh until it's closed by the RunStream
	// goroutine's deferred close — because finalizeRun (which persists
	// agent_run.messages + status to the DB) still runs after the terminal
	// event is pushed. Returning here would fire defer runCancel() and
	// cancel the runCtx mid-WriteTurn, leaving messages empty in the DB and
	// the user seeing an empty session on reload (dev agent_run 45, 2026-05-28).
	terminalSeen := false
	// doneCh is a swappable handle on c.Request.Context().Done(). We nil it
	// out after a client disconnect arrives during drain mode so the select
	// stops firing on it (a closed channel is always-ready); otherwise we'd
	// spin and re-cancel runCtx on every iteration.
	doneCh := c.Request.Context().Done()

	for {
		select {
		case <-doneCh:
			if terminalSeen {
				// Client disconnected after the terminal frame was emitted but
				// before finalizeRun completed. Returning here would cancel the
				// runCtx that finalizeRun's WriteTurn / UpdateState use to
				// persist agent_run.messages — exactly the empty-session bug
				// we're fixing, just triggered by client drop instead of an
				// early controller return. Disable this case (nil channel) and
				// keep draining until eventCh closes.
				disconnectReason = "client_disconnect_during_drain"
				doneCh = nil
				continue
			}
			disconnectReason = "client_disconnect"
			return

		case <-pingTicker.C:
			if terminalSeen {
				// Stream is past terminal — client has already (likely) closed.
				// Don't write keepalives that could fail; just keep draining.
				continue
			}
			if _, writeErr := fmt.Fprint(w, ":ping\n\n"); writeErr != nil {
				disconnectReason = "write_error"
				return
			}
			w.Flush()

		case ev, ok := <-eventCh:
			if !ok {
				// Channel closed — RunStream goroutine returned (incl. finalize).
				return
			}
			if terminalSeen {
				// Drain mode: don't write post-terminal events to the client
				// (it's already finishing or finished reading). Just wait for
				// channel close so finalizeRun's DB writes complete.
				continue
			}

			cursor, publishErr := h.runSvc.PublishRunEvent(ctx, runID, ev)
			if publishErr != nil {
				log.C(ctx).Warnw("streamEvents: publish run event failed",
					"run_id", runID, "event_type", ev.Type, "error", publishErr)
			}

			data, marshalErr := json.Marshal(ev)
			if marshalErr != nil {
				// P2 fix: log the marshal failure before skipping.
				log.C(ctx).Warnw("streamEvents: marshal event failed",
					"run_id", runID, "event_type", ev.Type, "error", marshalErr)
				continue
			}

			var writeErr error
			if cursor != "" {
				_, writeErr = fmt.Fprintf(w, "id: %s\ndata: %s\n\n", cursor, data)
			} else {
				_, writeErr = fmt.Fprintf(w, "data: %s\n\n", data)
			}
			if writeErr != nil {
				disconnectReason = "write_error"
				return
			}
			w.Flush()

			if !firstByteRecorded {
				recordFirstByte()
				firstByteRecorded = true
			}
			eventCount++

			// Terminal/error event: switch to drain mode. The runner goroutine
			// is still doing finalizeRun work; we wait for channel close.
			if ev.Type == stream.EventTerminal || ev.Type == stream.EventError {
				terminalSeen = true
			}
		}
	}
}
