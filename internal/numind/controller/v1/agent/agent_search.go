// Package agent — agent_search.go implements the FULLTEXT message search
// endpoint that the web-v3 history page calls.
//
// Auth: user_token middleware. The handler pulls UserID from middleware
// context and ALWAYS passes it as SearchOpts.UserID — store-level WHERE
// user_id = ? gives hard cross-user isolation (B2B2C父子隔离).
//
// agent-mode-v15-memory-layer-a Task 3.5.
package agent

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/agent/search"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// AgentSearchController wraps search.Service for the HTTP layer.
type AgentSearchController struct {
	svc search.Service
}

// NewAgentSearchController constructs the controller.
func NewAgentSearchController(svc search.Service) *AgentSearchController {
	return &AgentSearchController{svc: svc}
}

// RegisterAgentSearchRoutes registers GET /v1/agent-runs/search under authGroup.
// Called from router.go after the user auth middleware is wired.
func RegisterAgentSearchRoutes(authGroup *gin.RouterGroup, svc search.Service) {
	ctrl := NewAgentSearchController(svc)
	authGroup.GET("/agent-runs/search", ctrl.Search)
}

// Search handles GET /v1/agent-runs/search.
//
// Query params:
//   - q (optional): search query
//   - session_id (optional): scope to one session
//   - from (optional): RFC3339 / "2026-01-01" date lower bound (inclusive)
//   - to (optional): RFC3339 / "2026-05-23" date upper bound (inclusive)
//   - limit (optional, default 20, max 100)
//   - offset (optional, default 0)
//
// Response: {"results": [SearchResult], "total": N}
func (h *AgentSearchController) Search(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	if h.svc == nil {
		// Should not happen in production wiring; guards against test/HTTP-only
		// callers that skip biz init.
		core.WriteResponse(c, errno.InternalServerError.SetMessage("search service unavailable"), nil)
		return
	}

	opts := search.SearchOpts{
		UserID:    user.ID,
		Query:     c.Query("q"),
		SessionID: c.Query("session_id"),
	}

	if from := c.Query("from"); from != "" {
		if t, err := parseSearchDate(from); err == nil {
			opts.DateFrom = &t
		}
		// Silently ignore parse errors — empty filter is safer than 400.
	}
	if to := c.Query("to"); to != "" {
		if t, err := parseSearchDate(to); err == nil {
			// Use end-of-day for inclusive upper bound when the input is a
			// plain date (no time component).
			opts.DateTo = &t
		}
	}
	if limitStr := c.DefaultQuery("limit", "20"); limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	if offsetStr := c.DefaultQuery("offset", "0"); offsetStr != "" {
		if n, err := strconv.Atoi(offsetStr); err == nil && n >= 0 {
			opts.Offset = n
		}
	}

	results, total, err := h.svc.Search(c.Request.Context(), opts)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	if results == nil {
		results = []search.SearchResult{}
	}
	core.WriteResponse(c, nil, gin.H{
		"results": results,
		"total":   total,
	})
}

// parseSearchDate accepts RFC3339, "2006-01-02 15:04:05" and "2006-01-02"
// (the latter at start-of-day UTC). Returns the first format that parses.
func parseSearchDate(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	var lastErr error
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}
