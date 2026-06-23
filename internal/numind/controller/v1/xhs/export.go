package xhs

import (
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// ExportRequest 是 POST /v1/xhs/notes/export 的请求体：要导出的笔记主键列表。
// 不含 user_id（从鉴权上下文取，越权由 biz/store 的 user 隔离裁决）。
type ExportRequest struct {
	IDs []uint64 `json:"ids" binding:"required"`
}

// ExportResponse 返回 CSV 的签名下载链接（1 小时有效）。
type ExportResponse struct {
	DownloadURL string `json:"download_url"`
}

// Export 导出当前用户选中的若干条笔记为 CSV，返回签名下载链接。POST /v1/xhs/notes/export
//
// user_id 从鉴权上下文取（AuthMiddleware 只 Set "current_user"，不 Set "userID" gin key），
// 请求体禁含 user_id；ids 上限 200（biz 层裁决，超过返回 ErrBind）。
func (ctl *Controller) Export(c *gin.Context) {
	u := middleware.GetCurrentUser(c)
	if u == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req ExportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	url, err := ctl.biz.Export(c.Request.Context(), u.ID, req.IDs)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, ExportResponse{DownloadURL: url})
}
