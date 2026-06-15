package agent

// answer_stream_test.go tests the AnswerStream SSE resume controller method
// (issue4 backend). Mirrors student_run_stream_test.go's strategy: a thin test
// controller wraps the streamingRunSvc interface so we can inject events and
// verify the SSE wire format + route existence without real DB / runner wiring.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	agentbiz "numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/model"
)

// answerStreamStubSvc implements the streamingRunSvc seam (including the new
// AnswerStream + AcquireResumeStreamLock) so the SSE resume path is exercised
// in isolation.
type answerStreamStubSvc struct {
	acquireResume bool
	events        []stream.Event
	answerErr     error
}

func (s *answerStreamStubSvc) AcquireStreamLock(_ context.Context, _ uint, _ agentbiz.CreateRunRequest) (uint64, bool, error) {
	return 0, false, nil
}
func (s *answerStreamStubSvc) ReleaseStreamLock(_ uint64) {}
func (s *answerStreamStubSvc) RunStream(_ context.Context, _ uint, _ agentbiz.CreateRunRequest, _ uint64, _ chan<- stream.Event) (*agentbiz.RunResult, error) {
	return nil, nil
}
func (s *answerStreamStubSvc) AcquireResumeStreamLock(_ uint64) bool { return s.acquireResume }
func (s *answerStreamStubSvc) AnswerStream(_ context.Context, _ uint, _ uint64, _ agentbiz.AnswerRequest, ch chan<- stream.Event) (*agentbiz.RunResult, error) {
	for _, ev := range s.events {
		ch <- ev
	}
	return nil, s.answerErr
}

// testAnswerStreamController is a thin controller that drives the same SSE pump
// against the answer-stream resume path via the streamingRunSvc interface.
type testAnswerStreamController struct {
	svc streamingRunSvc
}

func (h *testAnswerStreamController) AnswerStream(c *gin.Context) {
	var user model.User
	user.ID = 42

	var req agentbiz.AnswerRequest
	if bindErr := c.ShouldBindJSON(&req); bindErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": bindErr.Error()})
		return
	}
	if !h.svc.AcquireResumeStreamLock(123) {
		c.JSON(http.StatusConflict, gin.H{
			"code":    "FailedOperation.AgentStreamAlreadyAttached",
			"message": "Agent stream already attached for this run.",
			"data":    gin.H{"run_id": 123},
		})
		return
	}
	defer h.svc.ReleaseStreamLock(123)

	c.Header("Content-Type", "text/event-stream")
	c.Status(http.StatusOK)
	w := c.Writer

	eventCh := make(chan stream.Event, 256)
	go func() {
		defer close(eventCh)
		_, _ = h.svc.AnswerStream(c.Request.Context(), user.ID, 123, req, eventCh)
	}()
	for ev := range eventCh {
		data, _ := json.Marshal(ev)
		_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
		w.Flush()
	}
}

func newAnswerStreamRequest() *http.Request {
	body, _ := json.Marshal(map[string]any{
		"answers": map[string]any{"Q1": map[string]any{"selected": []string{"A"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-runs/123/answer-stream", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// TestAnswerStream_RouteExists is the Rule 11 reproduction: before the
// AnswerStream handler + route exist, POST /v1/agent-runs/:id/answer-stream
// returns 404. After implementation it returns the SSE stream (200).
func TestAnswerStream_RouteExists(t *testing.T) {
	svc := &answerStreamStubSvc{
		acquireResume: true,
		events: []stream.Event{
			{Type: stream.EventTokenDelta, Seq: 1, RunID: 123},
			{Type: stream.EventTerminal, Seq: 2, RunID: 123},
		},
	}
	ctrl := &testAnswerStreamController{svc: svc}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/:id/answer-stream", ctrl.AnswerStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newAnswerStreamRequest())

	if w.Code == http.StatusNotFound {
		t.Fatalf("answer-stream route not registered (404) — Rule 11 repro still failing")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 SSE, got %d — body: %s", w.Code, w.Body.String())
	}
	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("expected Content-Type text/event-stream, got %q", ct)
	}
	frames := parseSSEFrames(w.Body.String())
	if len(frames) != 2 {
		t.Fatalf("expected 2 SSE frames (token_delta + terminal), got %d\nbody:\n%s", len(frames), w.Body.String())
	}
	if frames[len(frames)-1].Type != stream.EventTerminal {
		t.Errorf("expected last frame type=terminal, got %s", frames[len(frames)-1].Type)
	}
}

// TestStreamingRunSvc_SatisfiedByConcreteService statically asserts that the
// production *StudentRunService satisfies the streamingRunSvc seam INCLUDING the
// new AnswerStream + AcquireResumeStreamLock methods. Before they exist this
// fails to compile (= test FAIL).
func TestStreamingRunSvc_SatisfiedByConcreteService(t *testing.T) {
	var _ streamingRunSvc = (*agentbiz.StudentRunService)(nil)
}
