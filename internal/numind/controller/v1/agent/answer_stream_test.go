package agent

// answer_stream_test.go tests the AnswerStream observer-only SSE resume
// controller path. It reuses testStreamController from
// student_run_stream_test.go so the same shared observer helpers are exercised
// by create and answer streams.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	agentbiz "numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/errno"
)

func newAnswerStreamRequest() *http.Request {
	body, _ := json.Marshal(map[string]any{
		"answers": map[string]any{"Q1": map[string]any{"selected": []string{"A"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-runs/123/answer-stream", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func newAnswerStreamServer(svc *observerStreamSvc) *httptest.Server {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ctrl := &testStreamController{svc: svc}
	r.POST("/v1/agent-runs/:id/answer-stream", ctrl.AnswerStream)
	return httptest.NewServer(r)
}

func TestAnswerStream_RouteExists(t *testing.T) {
	events := make(chan stream.PublishedEvent, 2)
	events <- publishedEvent(t, "2000-1", stream.EventTokenDelta, 123, 1)
	events <- publishedEvent(t, "2000-2", stream.EventTerminal, 123, 2)
	close(events)
	svc := newObserverStreamSvc()
	svc.events = events
	ctrl := &testStreamController{svc: svc}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/:id/answer-stream", ctrl.AnswerStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newAnswerStreamRequest())

	if w.Code == http.StatusNotFound {
		t.Fatalf("answer-stream route not registered (404)")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 SSE, got %d; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}
	frames := parseSSEFrames(w.Body.String())
	if len(frames) != 2 {
		t.Fatalf("expected 2 SSE frames, got %d\nbody:\n%s", len(frames), w.Body.String())
	}
	if frames[len(frames)-1].Type != stream.EventTerminal {
		t.Fatalf("expected last frame type=terminal, got %s", frames[len(frames)-1].Type)
	}
}

func TestAnswerStream_ValidationErrorReturnsJSONBeforeSSE(t *testing.T) {
	svc := newObserverStreamSvc()
	svc.answerErr = errno.ErrBind.SetMessage("invalid answer payload")
	ctrl := &testStreamController{svc: svc}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/:id/answer-stream", ctrl.AnswerStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newAnswerStreamRequest())

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 JSON, got %d; body: %s", w.Code, w.Body.String())
	}
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("validation error must be returned before switching to SSE; body: %s", w.Body.String())
	}
	_, _, _, answerStartCalls, subscribeCalls := svc.snapshot()
	if answerStartCalls != 1 {
		t.Fatalf("expected StartPreparedAnswerStream validation call, got %d", answerStartCalls)
	}
	if subscribeCalls != 0 {
		t.Fatalf("SubscribeRunEvents must not be called after validation error, got %d", subscribeCalls)
	}
}

func TestAnswerStream_DisconnectDoesNotCancelSupervisedResume(t *testing.T) {
	events := make(chan stream.PublishedEvent)
	svc := newObserverStreamSvc()
	svc.events = events
	svc.answerStarted = make(chan struct{}, 1)
	svc.subscribeDone = make(chan struct{})
	server := newAnswerStreamServer(svc)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent-runs/123/answer-stream", newAnswerStreamRequest().Body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}

	select {
	case <-svc.answerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("StartPreparedAnswerStream was not called")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	select {
	case <-svc.subscribeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("observer context was not cancelled after client disconnect")
	}
	_, _, _, answerStartCalls, _ := svc.snapshot()
	if answerStartCalls != 1 {
		t.Fatalf("expected one supervised answer start, got %d", answerStartCalls)
	}
}

func TestStreamingRunSvc_SatisfiedByConcreteService(t *testing.T) {
	var _ streamingRunSvc = (*agentbiz.StudentRunService)(nil)
}
