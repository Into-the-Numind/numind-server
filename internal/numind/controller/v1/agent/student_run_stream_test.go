package agent

// student_run_stream_test.go tests the CreateStream observer-only SSE
// controller path. Test controllers below keep only the production
// auth/bind/prepare/start shell; all SSE observation uses the shared helpers
// from student_run_stream.go so tests do not drift into a second stream loop.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	agentbiz "numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

type observerStreamSvc struct {
	mu sync.Mutex

	prepared   *agentbiz.PreparedStreamRun
	prepareErr error

	startOK       bool
	answerStartOK bool
	answerErr     error

	subscribeErr error
	events       <-chan stream.PublishedEvent

	prepareCalls     int
	startCalls       int
	answerStartCalls int
	subscribeCalls   int

	order []string

	started          chan struct{}
	answerStarted    chan struct{}
	subscribeBlock   <-chan struct{}
	subscribeEntered chan struct{}
	subscribeDone    chan struct{}
}

func newObserverStreamSvc() *observerStreamSvc {
	return &observerStreamSvc{
		prepared: &agentbiz.PreparedStreamRun{
			RunID:     10,
			SessionID: "sess-10",
			UserID:    42,
			Request: agentbiz.CreateRunRequest{
				AgentDefinitionID: 1,
				Message:           "hello streaming",
			},
		},
		startOK:       true,
		answerStartOK: true,
	}
}

func (s *observerStreamSvc) record(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.order = append(s.order, step)
}

func (s *observerStreamSvc) PrepareStreamRun(_ context.Context, userID uint, req agentbiz.CreateRunRequest) (*agentbiz.PreparedStreamRun, error) {
	s.record("prepare")
	s.mu.Lock()
	s.prepareCalls++
	s.mu.Unlock()
	if s.prepareErr != nil {
		return nil, s.prepareErr
	}
	prepared := *s.prepared
	prepared.UserID = userID
	prepared.Request = req
	return &prepared, nil
}

func (s *observerStreamSvc) StartPreparedStreamRun(prepared *agentbiz.PreparedStreamRun) bool {
	s.record("start")
	s.mu.Lock()
	s.startCalls++
	s.mu.Unlock()
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	return s.startOK && prepared != nil && prepared.RunID != 0
}

func (s *observerStreamSvc) StartPreparedAnswerStream(_ context.Context, _ uint, _ uint64, _ agentbiz.AnswerRequest) (bool, error) {
	s.record("answer_start")
	s.mu.Lock()
	s.answerStartCalls++
	s.mu.Unlock()
	if s.answerStarted != nil {
		select {
		case s.answerStarted <- struct{}{}:
		default:
		}
	}
	return s.answerStartOK, s.answerErr
}

func (s *observerStreamSvc) SubscribeRunEvents(ctx context.Context, _ uint, _ uint64, _ string) (<-chan stream.PublishedEvent, error) {
	s.record("subscribe")
	s.mu.Lock()
	s.subscribeCalls++
	s.mu.Unlock()
	if s.subscribeEntered != nil {
		select {
		case s.subscribeEntered <- struct{}{}:
		default:
		}
	}
	if s.subscribeBlock != nil {
		select {
		case <-s.subscribeBlock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.subscribeErr != nil {
		return nil, s.subscribeErr
	}
	if s.subscribeDone != nil {
		go func() {
			<-ctx.Done()
			close(s.subscribeDone)
		}()
	}
	if s.events != nil {
		return s.events, nil
	}
	ch := make(chan stream.PublishedEvent)
	close(ch)
	return ch, nil
}

func (s *observerStreamSvc) snapshot() (order []string, prepareCalls, startCalls, answerStartCalls, subscribeCalls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order = append([]string(nil), s.order...)
	return order, s.prepareCalls, s.startCalls, s.answerStartCalls, s.subscribeCalls
}

type testStreamController struct {
	svc    streamingRunSvc
	userID uint
}

func (h *testStreamController) currentUser() *model.User {
	userID := h.userID
	if userID == 0 {
		userID = 42
	}
	user := &model.User{}
	user.ID = userID
	return user
}

func (h *testStreamController) CreateStream(c *gin.Context) {
	user := h.currentUser()
	var req agentbiz.CreateRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	prepared, err := h.svc.PrepareStreamRun(c.Request.Context(), user.ID, req)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	_ = h.svc.StartPreparedStreamRun(prepared)
	if switchToSSE(c) != nil {
		return
	}
	observeRunEvents(c, h.svc, user.ID, prepared.RunID, "", &observerFallbackStart{
		runID:     prepared.RunID,
		sessionID: prepared.SessionID,
	})
}

func (h *testStreamController) AnswerStream(c *gin.Context) {
	user := h.currentUser()
	runID, ok := mustParseRunID(c)
	if !ok {
		return
	}
	var req agentbiz.AnswerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	started, err := h.svc.StartPreparedAnswerStream(c.Request.Context(), user.ID, runID, req)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	if !started {
		subscribeThenObserveRunEvents(c, h.svc, user.ID, runID, "", nil)
		return
	}
	if switchToSSE(c) != nil {
		return
	}
	observeRunEvents(c, h.svc, user.ID, runID, "", nil)
}

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

func newStreamServer(svc *observerStreamSvc) *httptest.Server {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ctrl := &testStreamController{svc: svc}
	r.POST("/v1/agent-runs/stream", ctrl.CreateStream)
	return httptest.NewServer(r)
}

func streamTestHTTPClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Second}
}

func publishedEvent(t *testing.T, cursor string, eventType stream.EventType, runID, seq uint64) stream.PublishedEvent {
	t.Helper()
	ev, err := stream.Encode(eventType, nil, seq, runID, 0)
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	return stream.PublishedEvent{Cursor: cursor, Event: ev}
}

func publishedTerminalEvent(t *testing.T, cursor string, runID, seq uint64, reason agentbiz.TerminalReason) stream.PublishedEvent {
	t.Helper()
	ev, err := stream.Encode(stream.EventTerminal, stream.TerminalPayload{
		Reason: string(reason),
	}, seq, runID, 0)
	if err != nil {
		t.Fatalf("encode terminal event: %v", err)
	}
	return stream.PublishedEvent{Cursor: cursor, Event: ev}
}

func parseSSEFrames(body string) []stream.Event {
	var events []stream.Event
	for _, payload := range parseSSEDataLines(body) {
		var ev stream.Event
		if err := json.Unmarshal([]byte(payload), &ev); err == nil {
			events = append(events, ev)
		}
	}
	return events
}

func parseSSEDataLines(body string) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			lines = append(lines, strings.TrimPrefix(line, "data: "))
		}
	}
	return lines
}

func TestCreateStream_HappyPath(t *testing.T) {
	events := make(chan stream.PublishedEvent, 5)
	events <- publishedEvent(t, "1000-1", stream.EventTokenDelta, 10, 1)
	events <- publishedEvent(t, "1000-2", stream.EventTokenDelta, 10, 2)
	events <- publishedEvent(t, "1000-3", stream.EventTokenDelta, 10, 3)
	events <- publishedEvent(t, "1000-4", stream.EventStepDone, 10, 4)
	events <- publishedEvent(t, "1000-5", stream.EventTerminal, 10, 5)
	close(events)
	svc := newObserverStreamSvc()
	svc.events = events

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/stream", (&testStreamController{svc: svc}).CreateStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newStreamRequest(t))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "id: 1000-5\n") {
		t.Fatalf("expected SSE cursor ids in body:\n%s", body)
	}
	frames := parseSSEFrames(body)
	if len(frames) != 5 {
		t.Fatalf("expected 5 SSE data frames, got %d\nbody:\n%s", len(frames), body)
	}
	if frames[0].Type != stream.EventTokenDelta || frames[len(frames)-1].Type != stream.EventTerminal {
		t.Fatalf("unexpected frame sequence: first=%s last=%s", frames[0].Type, frames[len(frames)-1].Type)
	}
}

func TestCreateStream_StartFalseStillObservesAndDoesNot409(t *testing.T) {
	events := make(chan stream.PublishedEvent, 1)
	events <- publishedEvent(t, "1000-1", stream.EventTerminal, 77, 1)
	close(events)
	svc := newObserverStreamSvc()
	svc.prepared.RunID = 77
	svc.startOK = false
	svc.events = events
	ctrl := &testStreamController{svc: svc}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/stream", ctrl.CreateStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newStreamRequest(t))

	if w.Code != http.StatusOK {
		t.Fatalf("expected observer SSE 200, got %d; body: %s", w.Code, w.Body.String())
	}
	if _, _, _, _, subscribeCalls := svc.snapshot(); subscribeCalls != 1 {
		t.Fatalf("SubscribeRunEvents should be called when execution is already active, got %d", subscribeCalls)
	}
	frames := parseSSEFrames(w.Body.String())
	if len(frames) != 1 || frames[0].RunID != 77 {
		t.Fatalf("expected one observed terminal for run 77, got %+v\nbody:\n%s", frames, w.Body.String())
	}
}

func TestCreateStream_ClientDisconnectDoesNotCancelSupervisedRun(t *testing.T) {
	events := make(chan stream.PublishedEvent)
	svc := newObserverStreamSvc()
	svc.events = events
	svc.started = make(chan struct{}, 1)
	svc.subscribeDone = make(chan struct{})
	server := newStreamServer(svc)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent-runs/stream", newStreamRequest(t).Body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := streamTestHTTPClient().Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}

	select {
	case <-svc.started:
	case <-time.After(2 * time.Second):
		t.Fatal("StartPreparedStreamRun was not called")
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
	_, _, startCalls, _, _ := svc.snapshot()
	if startCalls != 1 {
		t.Fatalf("expected one supervised start, got %d", startCalls)
	}
}

func TestCreateStream_ClientDisconnectBeforeFirstWriteStillStartsPreparedRun(t *testing.T) {
	svc := newObserverStreamSvc()
	ctx, _ := gin.CreateTestContext(&failingResponseWriter{header: make(http.Header)})
	ctx.Request = newStreamRequest(t)

	(&testStreamController{svc: svc}).CreateStream(ctx)

	_, _, startCalls, _, subscribeCalls := svc.snapshot()
	if startCalls != 1 {
		t.Fatalf("StartPreparedStreamRun must run before the first SSE write, got %d calls", startCalls)
	}
	if subscribeCalls != 0 {
		t.Fatalf("SubscribeRunEvents should not run when first SSE write fails, got %d calls", subscribeCalls)
	}
}

func TestCreateStream_StartsPreparedRunBeforeObserving(t *testing.T) {
	svc := newObserverStreamSvc()
	ctrl := &testStreamController{svc: svc}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/stream", ctrl.CreateStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newStreamRequest(t))

	order, _, _, _, _ := svc.snapshot()
	want := []string{"prepare", "start", "subscribe"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("expected call order %v, got %v", want, order)
	}
}

func TestCreateStream_SuccessFlushesBeforeSubscribe(t *testing.T) {
	svc := newObserverStreamSvc()
	svc.subscribeBlock = make(chan struct{})
	svc.subscribeEntered = make(chan struct{}, 1)
	server := newStreamServer(svc)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent-runs/stream", newStreamRequest(t).Body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := streamTestHTTPClient().Do(req)
	if err != nil {
		t.Fatalf("client.Do should receive SSE headers before SubscribeRunEvents returns: %v", err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 16)
	n, err := resp.Body.Read(buf)
	if err != nil {
		t.Fatalf("read first SSE bytes: %v", err)
	}
	if n == 0 || buf[0] != ':' {
		t.Fatalf("expected initial SSE comment before subscribe completes, n=%d chunk=%q", n, buf[:n])
	}
	select {
	case <-svc.subscribeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("SubscribeRunEvents was not attempted after first-byte flush")
	}
}

func TestCreateStream_BrokerUnavailableEmitsObserverFallbackStart(t *testing.T) {
	svc := newObserverStreamSvc()
	svc.prepared.RunID = 123
	svc.prepared.SessionID = "sess-1"
	svc.subscribeErr = stream.ErrRunEventBrokerUnavailable
	ctrl := &testStreamController{svc: svc}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/stream", ctrl.CreateStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newStreamRequest(t))

	if w.Code != http.StatusOK {
		t.Fatalf("expected SSE fallback 200, got %d; body: %s", w.Code, w.Body.String())
	}
	lines := parseSSEDataLines(w.Body.String())
	if len(lines) != 1 {
		t.Fatalf("expected one data-only fallback frame, got %d\nbody:\n%s", len(lines), w.Body.String())
	}
	expected := `{"type":"stream_start","run_id":123,"data":{"session_id":"sess-1","run_id":123,"observer_fallback":true}}`
	if !jsonEqual(lines[0], expected) {
		t.Fatalf("fallback frame mismatch\nwant: %s\n got: %s", expected, lines[0])
	}
}

func TestCreateStream_FirstByteFlushedBeforePublishedEvent(t *testing.T) {
	events := make(chan stream.PublishedEvent)
	svc := newObserverStreamSvc()
	svc.events = events
	server := newStreamServer(svc)
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent-runs/stream", newStreamRequest(t).Body)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := streamTestHTTPClient().Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()

	buf := make([]byte, 16)
	readCh := make(chan int, 1)
	go func() {
		n, _ := resp.Body.Read(buf)
		readCh <- n
	}()

	select {
	case n := <-readCh:
		if n == 0 || buf[0] != ':' {
			t.Fatalf("expected first SSE byte ':' before any published event, n=%d chunk=%q", n, buf[:n])
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no SSE first byte before first published event")
	}
}

func TestCreateStream_ErrorEventTerminatesLoop(t *testing.T) {
	events := make(chan stream.PublishedEvent, 3)
	events <- publishedEvent(t, "1-1", stream.EventTokenDelta, 5, 1)
	events <- publishedEvent(t, "1-2", stream.EventError, 5, 2)
	events <- publishedEvent(t, "1-3", stream.EventTokenDelta, 5, 3)
	close(events)
	svc := newObserverStreamSvc()
	svc.prepared.RunID = 5
	svc.events = events
	ctrl := &testStreamController{svc: svc}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/stream", ctrl.CreateStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newStreamRequest(t))

	frames := parseSSEFrames(w.Body.String())
	if len(frames) != 2 {
		t.Fatalf("expected token_delta + error only, got %d\nbody:\n%s", len(frames), w.Body.String())
	}
	if frames[1].Type != stream.EventError {
		t.Fatalf("expected second frame type=error, got %s", frames[1].Type)
	}
}

func TestCreateStream_WaitingTerminalClosesObserver(t *testing.T) {
	events := make(chan stream.PublishedEvent, 2)
	events <- publishedTerminalEvent(t, "1-1", 6, 1, agentbiz.TerminalWaitingForUserChoice)
	events <- publishedEvent(t, "1-2", stream.EventTokenDelta, 6, 2)
	close(events)
	svc := newObserverStreamSvc()
	svc.prepared.RunID = 6
	svc.events = events
	ctrl := &testStreamController{svc: svc}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/stream", ctrl.CreateStream)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, newStreamRequest(t))

	frames := parseSSEFrames(w.Body.String())
	if len(frames) != 1 {
		t.Fatalf("expected waiting terminal to close observer before later events, got %d\nbody:\n%s", len(frames), w.Body.String())
	}
	if frames[0].Type != stream.EventTerminal {
		t.Fatalf("expected terminal frame, got %s", frames[0].Type)
	}
}

func jsonEqual(got, want string) bool {
	var gotAny any
	var wantAny any
	if err := json.Unmarshal([]byte(got), &gotAny); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &wantAny); err != nil {
		return false
	}
	return reflect.DeepEqual(gotAny, wantAny)
}

type failingResponseWriter struct {
	header http.Header
	code   int
}

func (w *failingResponseWriter) Header() http.Header {
	return w.header
}

func (w *failingResponseWriter) WriteHeader(code int) {
	w.code = code
}

func (w *failingResponseWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("client disconnected before first write")
}

func (w *failingResponseWriter) Flush() {}

var _ http.Flusher = (*failingResponseWriter)(nil)
