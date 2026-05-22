// Package agent implements HTTP handlers for the agent system.
// student_query.go contains the 7 GET + 1 POST student-facing endpoints that
// web-v3 calls. Controller is a thin layer: param binding + auth ctx +
// biz call + core.WriteResponse. No business logic here.
package agent

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/agent"
	bizagent "numind-server/internal/numind/biz/agent"
	"numind-server/internal/numind/biz/skill"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// StudentQueryController handles the 8 student-facing endpoints.
type StudentQueryController struct {
	skillSvc skill.Service
	querySvc *bizagent.StudentQueryService
}

// NewStudentQueryController constructs a StudentQueryController.
func NewStudentQueryController(skillSvc skill.Service, querySvc *bizagent.StudentQueryService) *StudentQueryController {
	return &StudentQueryController{skillSvc: skillSvc, querySvc: querySvc}
}

// RegisterStudentQueryRoutes registers all 8 student-facing endpoints under authGroup.
// Called by router.go after the auth group is set up.
func RegisterStudentQueryRoutes(authGroup *gin.RouterGroup, skillSvc skill.Service, querySvc *bizagent.StudentQueryService) {
	ctrl := NewStudentQueryController(skillSvc, querySvc)

	// 1. GET /v1/agent-skills/available
	authGroup.GET("/agent-skills/available", ctrl.ListAvailableSkills)
	// 2. GET /v1/agent-sessions/recent?limit=N
	authGroup.GET("/agent-sessions/recent", ctrl.ListRecentSessions)
	// 3. GET /v1/agent-sessions/history
	authGroup.GET("/agent-sessions/history", ctrl.ListAllHistorySessions)
	// 4. GET /v1/sessions/:id/snapshot
	authGroup.GET("/sessions/:id/snapshot", ctrl.GetSessionSnapshot)
	// 5. GET /v1/agent-runs/:id  (student detail — distinct from admin endpoint)
	authGroup.GET("/agent-runs/:id", ctrl.GetRun)
	// 6. POST /v1/agent-runs/:id/feedback
	authGroup.POST("/agent-runs/:id/feedback", ctrl.WriteFeedback)
	// 7. GET /v1/tenant-settings/support-contact
	authGroup.GET("/tenant-settings/support-contact", ctrl.GetSupportContact)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// ListAvailableSkills handles GET /v1/agent-skills/available.
// Returns active agent_definitions visible to the learner (filtered by parent).
func (h *StudentQueryController) ListAvailableSkills(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	skills, err := h.skillSvc.AvailableForStudent(c.Request.Context(), user.ID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, gin.H{"list": skills})
}

// ListRecentSessions handles GET /v1/agent-sessions/recent?limit=N.
// Returns the last N sessions for this user (default 5, max 100).
func (h *StudentQueryController) ListRecentSessions(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	limitStr := c.DefaultQuery("limit", "5")
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 {
		limit = 5
	}
	if limit > 100 {
		limit = 100
	}
	sessions, err := h.querySvc.ListRecentSessions(c.Request.Context(), user.ID, limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	// Frontend expects raw array (web-v3 src/api/agent.ts:32 → RecentSession[]).
	if sessions == nil {
		sessions = []*agent.RunSummary{}
	}
	core.WriteResponse(c, nil, sessions)
}

// ListAllHistorySessions handles GET /v1/agent-sessions/history.
// Returns all sessions for this user in the last 30 days.
func (h *StudentQueryController) ListAllHistorySessions(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	sessions, err := h.querySvc.ListAllHistorySessions(c.Request.Context(), user.ID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	// Frontend expects raw array (web-v3 src/api/agent.ts:41 → RecentSession[]).
	if sessions == nil {
		sessions = []*agent.RunSummary{}
	}
	core.WriteResponse(c, nil, sessions)
}

// GetSessionSnapshot handles GET /v1/sessions/:id/snapshot.
// Returns messages + compact_summary for the learner's resume flow.
//
// :id here is agent_run.session_id (UUID string varchar(64)), NOT the
// numeric agent_run.id PK. Reusing mustParseID historically returned
// 400 "invalid id: <uuid>" for every learner click on a session-history
// row because strconv.ParseUint rejected the UUID. The URL contract was
// always a UUID; the controller just spoke a different language. Lightly
// validate non-empty + length-bounded so pathological inputs get a clean
// 400 instead of hitting the store with a runaway parameter.
func (h *StudentQueryController) GetSessionSnapshot(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	sessionID := c.Param("id")
	if sessionID == "" || len(sessionID) > 64 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid session id: %q", sessionID), nil)
		return
	}
	snap, err := h.querySvc.GetSessionSnapshot(c.Request.Context(), user.ID, sessionID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, snap)
}

// GetRun handles GET /v1/agent-runs/:id.
// Returns the single run detail, ownership-checked.
func (h *StudentQueryController) GetRun(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	id, ok := mustParseID(c)
	if !ok {
		return
	}
	run, err := h.querySvc.GetRun(c.Request.Context(), user.ID, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, run)
}

// feedbackRequest is the JSON body for POST /v1/agent-runs/:id/feedback.
type feedbackRequest struct {
	Verdict string `json:"verdict" binding:"required"`
	Text    string `json:"text"`
}

// WriteFeedback handles POST /v1/agent-runs/:id/feedback.
// Persists 👍/👎 + optional text to agent_run.terminal_metadata["feedback"].
func (h *StudentQueryController) WriteFeedback(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	id, ok := mustParseID(c)
	if !ok {
		return
	}
	var req feedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	err := h.querySvc.WriteFeedback(c.Request.Context(), user.ID, id, bizagent.FeedbackRequest{
		Verdict: req.Verdict,
		Text:    req.Text,
	})
	core.WriteResponse(c, err, nil)
}

// GetSupportContact handles GET /v1/tenant-settings/support-contact.
// v1 placeholder: returns static contact info.
// TODO(v2): read from tenant_settings table keyed by parent_user_id once that table exists.
func (h *StudentQueryController) GetSupportContact(c *gin.Context) {
	core.WriteResponse(c, nil, gin.H{
		"name":   "客服",
		"wechat": "",
		"phone":  "",
		"note":   "联系您的购买方获取支持",
	})
}
