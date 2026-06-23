// Package feishu implements the HTTP handlers for the 飞书 (Lark) integration
// connection endpoints (feishu-integration T7). Handlers are THIN: auth/param
// extraction → biz call → core.WriteResponse (JSON endpoints) or c.Redirect
// (the no-JWT OAuth callback). All business logic lives in biz/feishu.
//
// Endpoints (design.md §5):
//
//	POST   /v1/feishu/connect          user_token  发起连接
//	GET    /v1/feishu/oauth/callback   NO JWT      飞书授权重定向回调 → 302
//	GET    /v1/feishu/status           user_token  连接状态
//	DELETE /v1/feishu/connection       user_token  解绑
//
// The callback is the only no-JWT endpoint: 飞书 redirects the user's browser
// here and a JWT cannot be attached. Its trust comes from the signed,
// one-time-use OAuth state (verified in biz/feishu), NOT from a session token.
package feishu

import (
	"net/http"

	"github.com/gin-gonic/gin"

	feishubiz "numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
)

// Controller wires the 飞书 connection HTTP handlers to the biz service.
type Controller struct {
	svc feishubiz.IFeishuService
}

// NewController constructs a Controller from the biz service.
func NewController(svc feishubiz.IFeishuService) *Controller {
	return &Controller{svc: svc}
}

// Connect handles POST /v1/feishu/connect. The body is empty (userID from the
// token); the settings-initiated connect has no paused run, so runID=0 /
// questionText="" — the callback will then connect the account but not resume
// any run (run 0 is never waiting). Agent-card-initiated authorize URLs are
// minted by the agent tool yield (T8/T10) with a real run context.
func (h *Controller) Connect(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	res, err := h.svc.Connect(c.Request.Context(), user.ID, 0, "")
	core.WriteResponse(c, err, res)
}

// Status handles GET /v1/feishu/status.
func (h *Controller) Status(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	res, err := h.svc.Status(c.Request.Context(), user.ID)
	core.WriteResponse(c, err, res)
}

// Unbind handles DELETE /v1/feishu/connection.
func (h *Controller) Unbind(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	err := h.svc.Unbind(c.Request.Context(), user.ID)
	core.WriteResponse(c, err, nil)
}

// Callback handles GET /v1/feishu/oauth/callback?code=&state= (NO JWT). It NEVER
// returns JSON: it always 302-redirects to the frontend (connected on success,
// error+reason otherwise). The biz layer returns a redirect target even on
// error so the user always lands on a friendly page; the error (if any) is
// logged here for observability and swallowed from the HTTP surface.
func (h *Controller) Callback(c *gin.Context) {
	code := c.Query("code")
	state := c.Query("state")

	res, err := h.svc.HandleCallback(c.Request.Context(), code, state)
	if err != nil {
		// Log for diagnosis; do NOT echo the error to the browser (it may carry
		// internal detail). The redirect target is the user-facing signal.
		log.Warnw("feishu oauth callback handled with error", "error", err)
	}
	if res == nil || res.RedirectURL == "" {
		// Defensive: biz always returns a redirect, but never leave the browser
		// hanging. This path is unreachable in normal operation.
		c.String(http.StatusBadRequest, "飞书授权回调处理失败")
		return
	}
	c.Redirect(http.StatusFound, res.RedirectURL)
}
