// Package feishu implements the HTTP handlers for the 飞书 (Lark) integration
// connection endpoints. Handlers are THIN: auth/param extraction → biz call →
// core.WriteResponse. All business logic lives in biz/feishu.
//
// G2-authorize device-code redesign (2026-06-24): connection goes entirely through
// lark-cli (config init + device-code), so there is NO redirect-OAuth and NO no-JWT
// callback endpoint anymore. The primary connect path is the agent feishu_connect
// tool; the settings page uses Connect/Status/Unbind below.
//
// Endpoints:
//
//	POST   /v1/feishu/connect      user_token  发起/推进连接（device-code）
//	GET    /v1/feishu/status       user_token  连接状态
//	DELETE /v1/feishu/connection   user_token  解绑
package feishu

import (
	"github.com/gin-gonic/gin"

	feishubiz "numind-server/internal/numind/biz/feishu"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
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
// token). It advances the device-code connect flow one step and returns the next
// action (create_app page URL / authorize verification URL / done). The user opens
// the URL; a later Connect/Status call advances or completes the connection.
func (h *Controller) Connect(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	res, err := h.svc.Connect(c.Request.Context(), user.ID)
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
