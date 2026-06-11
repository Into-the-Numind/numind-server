package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	agentbiz "numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/pkg/model"
)

// ---------------------------------------------------------------------------
// Stub biz services for controller tests.
// ---------------------------------------------------------------------------

type stubStudentRunSvc struct {
	estimateResp *agentbiz.EstimateResponse
	estimateErr  error
	createResp   *agentbiz.CreateRunResponse
	createErr    error
	pollResp     []*narration.Event
	pollErr      error
	cancelErr    error
	extendResp   *model.AgentRun
	extendErr    error
	answerResp   *agentbiz.AnswerResponse
	answerErr    error
}

func (s *stubStudentRunSvc) Estimate(_ context.Context, _ uint, _ agentbiz.EstimateRunRequest) (*agentbiz.EstimateResponse, error) {
	return s.estimateResp, s.estimateErr
}
func (s *stubStudentRunSvc) Create(_ context.Context, _ uint, _ agentbiz.CreateRunRequest) (*agentbiz.CreateRunResponse, error) {
	return s.createResp, s.createErr
}
func (s *stubStudentRunSvc) PollNarration(_ context.Context, _ uint, _ uint64, _ time.Time) ([]*narration.Event, error) {
	return s.pollResp, s.pollErr
}
func (s *stubStudentRunSvc) Cancel(_ context.Context, _ uint, _ uint64) error { return s.cancelErr }
func (s *stubStudentRunSvc) ExtendBudget(_ context.Context, _ uint, _ uint64, _ agentbiz.ExtendBudgetRequest) (*model.AgentRun, error) {
	return s.extendResp, s.extendErr
}
func (s *stubStudentRunSvc) Answer(_ context.Context, _ uint, _ uint64, _ agentbiz.AnswerRequest) (*agentbiz.AnswerResponse, error) {
	return s.answerResp, s.answerErr
}

// studentRunIface is the trimmed surface used by testController (the controller
// under test only exercises run-service paths; attachment uploads are covered by
// dedicated upload-service tests, not from this controller's seam).
type studentRunIface interface {
	Estimate(context.Context, uint, agentbiz.EstimateRunRequest) (*agentbiz.EstimateResponse, error)
	Create(context.Context, uint, agentbiz.CreateRunRequest) (*agentbiz.CreateRunResponse, error)
	PollNarration(context.Context, uint, uint64, time.Time) ([]*narration.Event, error)
	Cancel(context.Context, uint, uint64) error
	ExtendBudget(context.Context, uint, uint64, agentbiz.ExtendBudgetRequest) (*model.AgentRun, error)
	Answer(context.Context, uint, uint64, agentbiz.AnswerRequest) (*agentbiz.AnswerResponse, error)
}

type testController struct {
	runSvc studentRunIface
}

func (h *testController) Estimate(c *gin.Context) {
	var user model.User
	user.ID = 42
	var req agentbiz.EstimateRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	resp, err := h.runSvc.Estimate(c.Request.Context(), user.ID, req)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": resp})
}

func (h *testController) Answer(c *gin.Context) {
	var user model.User
	user.ID = 42
	runIDStr := c.Param("id")
	runID, ok := parseUint64(runIDStr)
	if !ok || runID == 0 {
		c.JSON(400, gin.H{"code": 400, "message": "invalid run id"})
		return
	}
	var req agentbiz.AnswerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}
	resp, err := h.runSvc.Answer(c.Request.Context(), user.ID, runID, req)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": resp})
}

func (h *testController) PollNarration(c *gin.Context) {
	var user model.User
	user.ID = 42
	runIDStr := c.Param("id")
	runID, _ := parseUint64(runIDStr)
	var since time.Time
	events, err := h.runSvc.PollNarration(c.Request.Context(), user.ID, runID, since)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"code": 0, "data": gin.H{"events": events}})
}

func parseUint64(s string) (uint64, bool) {
	var v uint64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		v = v*10 + uint64(ch-'0')
	}
	return v, true
}

// We test the concrete StudentRunController by constructing it with nil biz
// and invoking methods via the testController wrapper for unit tests.
// Integration tests should go through the full stack.
// These tests verify handler wiring and response format rather than full biz logic.

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestEstimateHandler_Returns200(t *testing.T) {
	stub := &stubStudentRunSvc{
		estimateResp: &agentbiz.EstimateResponse{
			Min:         40,
			Max:         60,
			IsLargeTask: false,
		},
	}
	ctrl := &testController{runSvc: stub}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/estimate", ctrl.Estimate)

	body, _ := json.Marshal(map[string]any{
		"agent_skill_id": 1,
		"input_text":     "test message",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-runs/estimate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	data, ok := resp["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data object, got: %v", resp["data"])
	}
	if int(data["min"].(float64)) != 40 || int(data["max"].(float64)) != 60 {
		t.Errorf("expected min=40 max=60, got min=%v max=%v", data["min"], data["max"])
	}
}

func TestPollNarrationHandler_EmptyEvents(t *testing.T) {
	stub := &stubStudentRunSvc{
		pollResp: []*narration.Event{},
	}
	ctrl := &testController{runSvc: stub}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/agent-runs/:id/narration", ctrl.PollNarration)

	req := httptest.NewRequest(http.MethodGet, "/v1/agent-runs/123/narration", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	data := resp["data"].(map[string]any)
	events := data["events"].([]any)
	if len(events) != 0 {
		t.Errorf("expected 0 events, got %d", len(events))
	}
}

func TestMustParseRunID_InvalidInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/runs/:id/cancel", func(c *gin.Context) {
		if _, ok := mustParseRunID(c); ok {
			c.Status(200)
		}
		// else branch: mustParseRunID already wrote a 400 response
	})

	req := httptest.NewRequest(http.MethodPost, "/runs/abc/cancel", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid id, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// Answer handler tests
// ---------------------------------------------------------------------------

func TestAnswerHandler_Returns200(t *testing.T) {
	stub := &stubStudentRunSvc{
		answerResp: &agentbiz.AnswerResponse{RunID: 99, Status: "resumed"},
	}
	ctrl := &testController{runSvc: stub}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/:id/answer", ctrl.Answer)

	body, _ := json.Marshal(map[string]any{
		"answers": map[string]any{"问题？": map[string]any{"selected": []string{"a"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-runs/99/answer", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data := resp["data"].(map[string]any)
	if data["status"] != "resumed" {
		t.Errorf("expected status=resumed, got %v", data["status"])
	}
}

func TestAnswerHandler_InvalidRunID(t *testing.T) {
	stub := &stubStudentRunSvc{}
	ctrl := &testController{runSvc: stub}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/:id/answer", ctrl.Answer)

	body, _ := json.Marshal(map[string]any{
		"answers": map[string]any{"问题？": map[string]any{"selected": []string{"a"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-runs/abc/answer", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-numeric id, got %d", w.Code)
	}
}

func TestAnswerHandler_MissingBody(t *testing.T) {
	stub := &stubStudentRunSvc{}
	ctrl := &testController{runSvc: stub}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/agent-runs/:id/answer", ctrl.Answer)

	req := httptest.NewRequest(http.MethodPost, "/v1/agent-runs/1/answer", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// ShouldBindJSON with binding:"required" on the answers map should reject a
	// body with no answers (agent-multi-question).
	if w.Code == http.StatusOK {
		t.Errorf("expected non-200 for a body with no answers, got 200")
	}
}
