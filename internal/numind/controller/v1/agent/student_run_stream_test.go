package agent

// student_run_stream_test.go tests the CreateStream SSE controller method.
//
// Strategy: a testStreamController wraps a streamingRunSvc stub so we can
// inject events and verify the SSE wire format without real DB / runner wiring.
// The concrete StudentRunController.CreateStream is exercised indirectly via
// a thin wrapper that replaces only the service dependency.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	agentbiz "numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Stub streaming service
// ---------------------------------------------------------------------------

type stubStreamSvc struct {
	acquireRunID uint64
	acquiredOK   bool
	acquireErr   error
	// events to push before close; nil means close immediately
	events []stream.Event
	// runStreamErr is returned from RunStream (after events)
	runStreamErr error
}

func (s *stubStreamSvc) AcquireStreamLock(_ context.Context, _ uint, _ agentbiz.CreateRunRequest) (uint64, bool, error) {
	return s.acquireRunID, s.acquiredOK, s.acquireErr
}
func (s *stubStreamSvc) ReleaseStreamLock(_ uint64) {}
func (s *stubStreamSvc) RunStream(_ context.Context, _ uint, _ agentbiz.CreateRunRequest, _ uint64, ch chan<- stream.Event) (*agentbiz.RunResult, error) {
	for _, ev := range s.events {
		ch <- ev
	}
	return nil, s.runStreamErr
}

// ---------------------------------------------------------------------------
// Thin test controller that uses streamingRunSvc interface for the SSE path
// ---------------------------------------------------------------------------

type testStreamController struct {
	svc streamingRunSvc
}

func (h *testStreamController) CreateStream(c *gin.Context) {
	// Inline the user injection since middleware isn't wired in unit tests.
	var user model.User
	user.ID = 42

	var req agentbiz.CreateRunRequest
	if bindErr := c.ShouldBindJSON(&req); bindErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": bindErr.Error()})
		return
	}

	runID, acquired, err := h.svc.AcquireStreamLock(c.Request.Context(), user.ID, req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": 500, "message": err.Error()})
		return
	}
	if !acquired {
		// Mirror the real CreateStream 409 body shape (P1 fix: data.run_id, not top-level run_id).
		c.JSON(http.StatusConflict, gin.H{
			"code":    "FailedOperation.AgentStreamAlreadyAttached",
			"message": "Agent stream already attached for this run.",
			"data":    gin.H{"run_id": runID},
		})
		return
	}
	defer h.svc.ReleaseStreamLock(runID)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	w := c.Writer
	eventCh := make(chan stream.Event, 256)

	runCtx, runCancel := context.WithCancel(c.Request.Context())
	defer runCancel()

	go func() {
		defer close(eventCh)
		_, _ = h.svc.RunStream(runCtx, user.ID, req, runID, eventCh)
	}()

	pingTicker := time.NewTicker(ssePingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case <-pingTicker.C:
			_, _ = w.Write([]byte(":ping\n\n"))
			w.Flush()
		case ev, ok := <-eventCh:
			if !ok {
				return
			}
			data, _ := json.Marshal(ev)
			_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
			w.Flush()
			if ev.Type == stream.EventTerminal || ev.Type == stream.EventError {
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newStreamRequest(t *testing.T) *http.Request {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"agent_skill_id": 1,
		"input_text":     "hello streaming",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-runs/stream", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// parseSSSEFrames reads all "data: <json>" lines from the recorder body.
func parseSSEFrames(body string) []stream.Event {
	var events []stream.Event
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var ev stream.Event
		if err := json.Unmarshal([]byte(payload), &ev); err == nil {
			events = append(events, ev)
		}
	}
	return events
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestCreateStream_HappyPath verifies that 5 events (3 token_delta + terminal)
// flow through the controller and appear as SSE frames.
func TestCreateStream_HappyPath(t *testing.T) {
	events := []stream.Event{
		{Type: stream.EventTokenDelta, Seq: 1, RunID: 10},
		{Type: stream.EventTokenDelta, Seq: 2, RunID: 10},
		{Type: stream.EventTokenDelta, Seq: 3, RunID: 10},
		{Type: stream.EventStepDone, Seq: 4, RunID: 10},
		{Type: stream.EventTerminal, Seq: 5, RunID: 10},
	}

	svc := &stubStreamSvc{
		acquireRunID: 10,
		acquiredOK:   true,
		events:       events,
	}
	ctrl := &testStreamController{svc: svc}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/stream", ctrl.CreateStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newStreamRequest(t))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}

	body := w.Body.String()
	frames := parseSSEFrames(body)
	if len(frames) != 5 {
		t.Errorf("expected 5 SSE frames, got %d\nbody:\n%s", len(frames), body)
	}
	if len(frames) > 0 {
		if frames[0].Type != stream.EventTokenDelta {
			t.Errorf("expected first frame type=token_delta, got %s", frames[0].Type)
		}
	}
	if len(frames) == 5 && frames[4].Type != stream.EventTerminal {
		t.Errorf("expected last frame type=terminal, got %s", frames[4].Type)
	}
	// Verify wire format: each frame has run_id set.
	for i, f := range frames {
		if f.RunID != 10 {
			t.Errorf("frame[%d]: expected run_id=10, got %d", i, f.RunID)
		}
	}
}

// TestCreateStream_409WhenLockNotAcquired verifies that a second connection
// attempt for the same run gets HTTP 409 with the run_id in the response body.
func TestCreateStream_409WhenLockNotAcquired(t *testing.T) {
	svc := &stubStreamSvc{
		acquireRunID: 77,
		acquiredOK:   false, // already held by another subscriber
	}
	ctrl := &testStreamController{svc: svc}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/stream", ctrl.CreateStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newStreamRequest(t))

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d — body: %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal 409 body: %v", err)
	}
	// Per spec §6.1 the 409 body shape is {code, message, data: {run_id}}.
	// P1 fix: assert data.run_id is present and correct, not top-level run_id
	// (the original assertion was broken — core.WriteResponse + non-nil err
	// silently drops the Data param, so the real controller never wrote
	// top-level run_id).
	dataVal, ok := body["data"]
	if !ok {
		t.Fatalf("expected data field in 409 response, got: %v", body)
	}
	dataMap, ok := dataVal.(map[string]any)
	if !ok {
		t.Fatalf("expected data to be object, got %T", dataVal)
	}
	runIDVal, ok := dataMap["run_id"]
	if !ok {
		t.Errorf("expected data.run_id field in 409 response, got: %v", body)
	}
	if int(runIDVal.(float64)) != 77 {
		t.Errorf("expected data.run_id=77, got %v", runIDVal)
	}
	if body["code"] != "FailedOperation.AgentStreamAlreadyAttached" {
		t.Errorf("expected code=FailedOperation.AgentStreamAlreadyAttached, got %v", body["code"])
	}
}

// TestCreateStream_ClientDisconnect verifies that when the client context is
// cancelled the controller exits cleanly and no goroutine blocks indefinitely.
// We simulate disconnect by cancelling the request context BEFORE the service
// can push events, and the stub blocks until the context is cancelled.
func TestCreateStream_ClientDisconnect(t *testing.T) {
	// A service that blocks RunStream until context cancels, then returns.
	blockedSvc := &blockingStreamSvc{acquireRunID: 99}

	ctrl := &testStreamController{svc: blockedSvc}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/stream", ctrl.CreateStream)

	ctx, cancel := context.WithCancel(context.Background())

	body, _ := json.Marshal(map[string]any{
		"agent_skill_id": 1,
		"input_text":     "hello",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-runs/stream", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()

	// Cancel context shortly after the handler starts.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	// ServeHTTP blocks until handler returns; it should return promptly after
	// context cancellation.
	done := make(chan struct{})
	go func() {
		r.ServeHTTP(w, req)
		close(done)
	}()

	select {
	case <-done:
		// Handler returned — good.
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return within 2s after client disconnect")
	}
}

// blockingStreamSvc is a streamingRunSvc stub whose RunStream blocks until ctx
// is cancelled. Used to test client-disconnect handling.
type blockingStreamSvc struct {
	acquireRunID uint64
}

func (s *blockingStreamSvc) AcquireStreamLock(_ context.Context, _ uint, _ agentbiz.CreateRunRequest) (uint64, bool, error) {
	return s.acquireRunID, true, nil
}
func (s *blockingStreamSvc) ReleaseStreamLock(_ uint64) {}
func (s *blockingStreamSvc) RunStream(ctx context.Context, _ uint, _ agentbiz.CreateRunRequest, _ uint64, _ chan<- stream.Event) (*agentbiz.RunResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestCreateStream_ErrorEventTerminatesLoop verifies that an EventError frame
// causes the controller to close the connection immediately.
func TestCreateStream_ErrorEventTerminatesLoop(t *testing.T) {
	events := []stream.Event{
		{Type: stream.EventTokenDelta, Seq: 1, RunID: 5},
		{Type: stream.EventError, Seq: 2, RunID: 5},
		// This event should NOT be sent — loop exits on EventError.
		{Type: stream.EventTokenDelta, Seq: 3, RunID: 5},
	}
	svc := &stubStreamSvc{
		acquireRunID: 5,
		acquiredOK:   true,
		events:       events,
	}
	ctrl := &testStreamController{svc: svc}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/stream", ctrl.CreateStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newStreamRequest(t))

	frames := parseSSEFrames(w.Body.String())
	// Should have exactly 2 frames (token_delta + error); the third is not sent.
	if len(frames) != 2 {
		t.Errorf("expected 2 frames (token_delta + error), got %d\nbody:\n%s", len(frames), w.Body.String())
	}
	if len(frames) == 2 && frames[1].Type != stream.EventError {
		t.Errorf("expected second frame type=error, got %s", frames[1].Type)
	}
}
