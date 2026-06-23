// Package xhs 是小红书选题采集（xhs-collector）的 HTTP 控制层。
//
// 职责边界（controller 硬规则，见 .claude/rules/api-design.md §6）：本层只做参数绑定、
// 鉴权上下文提取、调用 biz、core.WriteResponse 格式化。业务逻辑全在 biz/xhs 层。
// user_id 一律从鉴权上下文取（而非 payload），保证多租户私有选题库归属不可伪造。
package xhs

import (
	"github.com/gin-gonic/gin"

	xhsbiz "numind-server/internal/numind/biz/xhs"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"
)

// Controller 是小红书选题采集用户端控制器。
type Controller struct {
	biz *xhsbiz.XhsBiz
}

// NewController 创建小红书选题采集控制器（沿用 NewXxxController(biz) 模式）。
func NewController(biz *xhsbiz.XhsBiz) *Controller {
	return &Controller{biz: biz}
}

// IngestRequest 是 POST /v1/xhs/notes 的请求体：浏览器插件批量上送的笔记 payload。
type IngestRequest struct {
	Notes []xhsbiz.NotePayload `json:"notes" binding:"required"`
}

// IngestResponse 返回成功摄入的条数与对应笔记主键（按入参顺序）。
type IngestResponse struct {
	Ingested int      `json:"ingested"`
	IDs      []uint64 `json:"ids"`
}

// Ingest 批量摄入插件上送的小红书笔记。POST /v1/xhs/notes
//
// user_id 从鉴权上下文取（本仓库 AuthMiddleware 只 Set "current_user"，不 Set "userID"
// gin key，故用 middleware.GetCurrentUser(c).ID，与既有 controller 约定一致）。
func (ctl *Controller) Ingest(c *gin.Context) {
	u := middleware.GetCurrentUser(c)
	if u == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req IngestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	ingested, ids, err := ctl.biz.Ingest(c.Request.Context(), u.ID, req.Notes)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, IngestResponse{Ingested: ingested, IDs: ids})
}
