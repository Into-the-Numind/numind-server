package xhs

import (
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// extTokenScope 是浏览器插件 ext-token 的 scope claim 值（与中间件白名单一致）。
const extTokenScope = "xhs"

// ExtTokenResponse 返回浏览器插件用的受限 token 及其到期时刻（ISO8601）。
type ExtTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// ExtToken 为当前登录用户签发带 scope="xhs" claim 的受限 web token。GET /v1/xhs/ext-token
//
// 一键授权链路（design §5）：插件 popup 打开有数 web /connect-extension（已登录态）→
// 页面带现有 web JWT 调本端点 → 拿到 scope=xhs token 交给插件 → 插件后续用此 token 上送笔记。
// 受限 token 由 user_token 中间件收敛：仅放行 /v1/xhs/*，打其它 /v1/* 路由被 403。
//
// user_id 从鉴权上下文取（本仓库 AuthMiddleware 只 Set "current_user"），请求不含任何 user 参数。
// 本端点不扣分。
func (ctl *Controller) ExtToken(c *gin.Context) {
	u := middleware.GetCurrentUser(c)
	if u == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	token, expiresAt, err := ctl.userBiz.IssueScopedToken(c.Request.Context(), u.ID, extTokenScope)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, ExtTokenResponse{
		Token:     token,
		ExpiresAt: expiresAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}
