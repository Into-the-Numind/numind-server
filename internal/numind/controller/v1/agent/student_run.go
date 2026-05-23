// Package agent implements HTTP handlers for the agent student-run endpoints.
// All handlers are thin: param binding + auth extraction + biz call + core.WriteResponse.
// Business logic lives entirely in biz/agent and biz/attachment.
package agent

import (
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/attachment"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// StudentRunController handles the learner-facing run lifecycle and attachment endpoints.
type StudentRunController struct {
	runSvc    *agent.StudentRunService
	attachSvc *attachment.UploadService
	attStore  store.IAgentAttachmentStore // for GetAttachmentStatus (V1.5 task 1.2)
}

// NewStudentRunController constructs a StudentRunController from IBiz.
func NewStudentRunController(b biz.IBiz) *StudentRunController {
	return &StudentRunController{
		runSvc:    b.StudentRun(),
		attachSvc: b.Attachment(),
	}
}

// NewStudentRunControllerWithStore constructs a StudentRunController with access
// to the agent_attachment store for the GetAttachmentStatus handler (V1.5 task 1.2).
func NewStudentRunControllerWithStore(b biz.IBiz, attStore store.IAgentAttachmentStore) *StudentRunController {
	return &StudentRunController{
		runSvc:    b.StudentRun(),
		attachSvc: b.Attachment(),
		attStore:  attStore,
	}
}

// RegisterStudentRunRoutes registers all learner-facing endpoints on authGroup.
// Must be called AFTER all other agentcontroller route registrations to avoid
// path conflicts with the /agent-runs prefix.
func RegisterStudentRunRoutes(authGroup *gin.RouterGroup, b biz.IBiz, attStore store.IAgentAttachmentStore) {
	c := NewStudentRunControllerWithStore(b, attStore)
	authGroup.POST("/agent-runs/estimate", c.Estimate)
	authGroup.POST("/agent-runs", c.Create)
	authGroup.GET("/agent-runs/:id/narration", c.PollNarration)
	authGroup.POST("/agent-runs/:id/cancel", c.Cancel)
	authGroup.POST("/agent-runs/:id/extend-budget", c.ExtendBudget)
	// T4 ask_user_question answer endpoint.
	authGroup.POST("/agent-runs/:id/answer", c.Answer)
	authGroup.POST("/agent-attachments", c.UploadAttachment)
	// V1.5 task 1.2: fallback status polling.
	authGroup.GET("/agent-attachments/:id/status", c.GetAttachmentStatus)
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
//
// Response (V1.5 task 1.2):
//
//	{"id": N, "url": "...", "modality": "image|pdf|audio|unknown", "fallback_ready": false}
//
// fallback_ready is always false at upload time. Clients may poll
// GET /v1/agent-attachments/:id/status to track readiness.
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
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{
		"id":             result.ID,
		"url":            result.URL,
		"filename":       result.Filename,
		"mime_type":      result.MimeType,
		"size":           result.Size,
		"modality":       result.Modality,
		"fallback_ready": result.FallbackReady,
	})
}

// ---------------------------------------------------------------------------
// GetAttachmentStatus — GET /v1/agent-attachments/:id/status
// ---------------------------------------------------------------------------

// GetAttachmentStatus handles GET /v1/agent-attachments/:id/status.
// Returns the current fallback generation status for the given attachment.
// Only the owning user can query status (ownership enforced at store layer).
func (h *StudentRunController) GetAttachmentStatus(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	raw := c.Param("id")
	attID, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || attID == 0 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid attachment id: %s", raw), nil)
		return
	}

	if h.attStore == nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("attachment store not configured"), nil)
		return
	}

	att, err := h.attStore.GetByIDAndUser(c.Request.Context(), attID, user.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			core.WriteResponse(c, errno.ErrPageNotFound.SetMessage("attachment not found"), nil)
			return
		}
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("get attachment: %s", err.Error()), nil)
		return
	}

	resp := gin.H{
		"id":             att.ID,
		"fallback_ready": att.FallbackReady,
		"modality":       att.Modality,
	}
	if att.FallbackError != nil {
		resp["fallback_error"] = *att.FallbackError
	}
	core.WriteResponse(c, nil, resp)
}

// ---------------------------------------------------------------------------
// Answer — POST /v1/agent-runs/:id/answer
// ---------------------------------------------------------------------------

// Answer handles POST /v1/agent-runs/:id/answer.
// The request body must contain {"selected": ["key1", ...], "free_text": "..."}.
// Returns 200 {"code":0,"data":{"run_id":N,"status":"resumed"}} on success.
func (h *StudentRunController) Answer(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	runID, ok := mustParseRunID(c)
	if !ok {
		return
	}

	var req agent.AnswerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	resp, err := h.runSvc.Answer(c.Request.Context(), user.ID, runID, req)
	core.WriteResponse(c, err, resp)
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
