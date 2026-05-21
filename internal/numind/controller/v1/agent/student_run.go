// Package agent implements HTTP handlers for the agent student-run endpoints.
// All handlers are thin: param binding + auth extraction + biz call + core.WriteResponse.
// Business logic lives entirely in biz/agent and biz/attachment.
package agent

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/attachment"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// StudentRunController handles the 6 learner-facing run lifecycle endpoints.
type StudentRunController struct {
	runSvc    *agent.StudentRunService
	attachSvc *attachment.UploadService
}

// NewStudentRunController constructs a StudentRunController from IBiz.
func NewStudentRunController(b biz.IBiz) *StudentRunController {
	return &StudentRunController{
		runSvc:    b.StudentRun(),
		attachSvc: b.Attachment(),
	}
}

// RegisterStudentRunRoutes registers all 6 endpoints on authGroup.
// Must be called AFTER all other agentcontroller route registrations to avoid
// path conflicts with the /agent-runs prefix.
func RegisterStudentRunRoutes(authGroup *gin.RouterGroup, b biz.IBiz) {
	c := NewStudentRunController(b)
	authGroup.POST("/agent-runs/estimate", c.Estimate)
	authGroup.POST("/agent-runs", c.Create)
	authGroup.GET("/agent-runs/:id/narration", c.PollNarration)
	authGroup.POST("/agent-runs/:id/cancel", c.Cancel)
	authGroup.POST("/agent-runs/:id/extend-budget", c.ExtendBudget)
	authGroup.POST("/agent-attachments", c.UploadAttachment)
}

// ---------------------------------------------------------------------------
// Estimate — POST /v1/agent-runs/estimate
// ---------------------------------------------------------------------------

// Estimate handles POST /v1/agent-runs/estimate.
func (h *StudentRunController) Estimate(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req agent.EstimateRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	resp, err := h.runSvc.Estimate(c.Request.Context(), user.ID, req)
	core.WriteResponse(c, err, resp)
}

// ---------------------------------------------------------------------------
// Create — POST /v1/agent-runs
// ---------------------------------------------------------------------------

// Create handles POST /v1/agent-runs.
func (h *StudentRunController) Create(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req agent.CreateRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	resp, err := h.runSvc.Create(c.Request.Context(), user.ID, req)
	core.WriteResponse(c, err, resp)
}

// ---------------------------------------------------------------------------
// PollNarration — GET /v1/agent-runs/:id/narration?since=<RFC3339Nano>
// ---------------------------------------------------------------------------

// PollNarration handles GET /v1/agent-runs/:id/narration.
func (h *StudentRunController) PollNarration(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	runID, ok := mustParseRunID(c)
	if !ok {
		return
	}

	var since time.Time
	if sinceStr := c.Query("since"); sinceStr != "" {
		parsed, parseErr := time.Parse(time.RFC3339Nano, sinceStr)
		if parseErr != nil {
			core.WriteResponse(c, errno.ErrBind.SetMessage("invalid since parameter: %s", parseErr.Error()), nil)
			return
		}
		since = parsed
	}

	events, err := h.runSvc.PollNarration(c.Request.Context(), user.ID, runID, since)
	// Frontend expects raw array NarrationEvent[] (web-v3 src/api/agent.ts:81).
	if events == nil {
		events = []*narration.Event{}
	}
	core.WriteResponse(c, err, events)
}

// ---------------------------------------------------------------------------
// Cancel — POST /v1/agent-runs/:id/cancel
// ---------------------------------------------------------------------------

// Cancel handles POST /v1/agent-runs/:id/cancel.
func (h *StudentRunController) Cancel(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	runID, ok := mustParseRunID(c)
	if !ok {
		return
	}

	err := h.runSvc.Cancel(c.Request.Context(), user.ID, runID)
	core.WriteResponse(c, err, nil)
}

// ---------------------------------------------------------------------------
// ExtendBudget — POST /v1/agent-runs/:id/extend-budget
// ---------------------------------------------------------------------------

// ExtendBudget handles POST /v1/agent-runs/:id/extend-budget.
func (h *StudentRunController) ExtendBudget(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	runID, ok := mustParseRunID(c)
	if !ok {
		return
	}

	var req agent.ExtendBudgetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	updated, err := h.runSvc.ExtendBudget(c.Request.Context(), user.ID, runID, req)
	core.WriteResponse(c, err, updated)
}

// ---------------------------------------------------------------------------
// UploadAttachment — POST /v1/agent-attachments
// ---------------------------------------------------------------------------

// UploadAttachment handles POST /v1/agent-attachments (multipart/form-data).
func (h *StudentRunController) UploadAttachment(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	file, hdr, err := c.Request.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("file field missing or invalid: %s", err.Error()), nil)
		return
	}
	defer file.Close()

	result, err := h.attachSvc.Upload(c.Request.Context(), user.ID, file, hdr)
	core.WriteResponse(c, err, result)
}

// ---------------------------------------------------------------------------
// mustParseRunID — shared with other agent controller files in this package
// ---------------------------------------------------------------------------

// mustParseRunID parses the :id path parameter as uint64.
// Writes 400 and returns false on failure.
func mustParseRunID(c *gin.Context) (uint64, bool) {
	raw := c.Param("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid run id: %s", raw), nil)
		return 0, false
	}
	return id, true
}
