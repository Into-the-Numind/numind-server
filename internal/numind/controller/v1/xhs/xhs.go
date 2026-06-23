// Package xhs 是小红书选题采集（xhs-collector）的 HTTP 控制层。
//
// 职责边界（controller 硬规则，见 .claude/rules/api-design.md §6）：本层只做参数绑定、
// 鉴权上下文提取、调用 biz、core.WriteResponse 格式化。业务逻辑全在 biz/xhs 层。
// user_id 一律从鉴权上下文取（而非 payload），保证多租户私有选题库归属不可伪造。
package xhs

import (
	"strconv"

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

// ListQuery 是 GET /v1/xhs/notes 的查询参数。page 1-based、page_size 上限 100（biz 层归一化）；
// note_type / keyword / enrich_status / sort 均为可选过滤/排序。不含 user_id（从鉴权上下文取）。
type ListQuery struct {
	Page         int    `form:"page"`
	PageSize     int    `form:"page_size"`
	NoteType     string `form:"note_type"`
	Keyword      string `form:"keyword"`
	EnrichStatus string `form:"enrich_status"`
	Sort         string `form:"sort"`
}

// List 分页查询当前用户的选题库。GET /v1/xhs/notes
//
// user_id 从鉴权上下文取（AuthMiddleware 只 Set "current_user"），保证多租户隔离。
// 返回 {list, total, page, page_size} 供前端分页（api-design.md §4）。
func (ctl *Controller) List(c *gin.Context) {
	u := middleware.GetCurrentUser(c)
	if u == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var q ListQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}

	list, total, err := ctl.biz.ListNotes(c.Request.Context(), u.ID, xhsbiz.ListFilter{
		NoteType:     q.NoteType,
		Keyword:      q.Keyword,
		EnrichStatus: q.EnrichStatus,
		Sort:         q.Sort,
	}, q.Page, q.PageSize)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"list":      list,
		"total":     total,
		"page":      q.Page,
		"page_size": q.PageSize,
	})
}

// Get 获取当前用户的单条选题笔记详情。GET /v1/xhs/notes/:id
//
// 跨用户取不到（biz/store 带 user 隔离）→ errno.ErrXhsNoteNotFound（HTTP 404），防越权读取。
func (ctl *Controller) Get(c *gin.Context) {
	u := middleware.GetCurrentUser(c)
	if u == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	id, err := parseNoteID(c)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	item, gErr := ctl.biz.GetNote(c.Request.Context(), u.ID, id)
	if gErr != nil {
		core.WriteResponse(c, gErr, nil)
		return
	}
	core.WriteResponse(c, nil, item)
}

// Delete 删除当前用户的单条选题笔记。DELETE /v1/xhs/notes/:id
//
// 跨用户删不到（biz/store 带 user 隔离）→ 静默成功（幂等），不泄露他人笔记是否存在。
func (ctl *Controller) Delete(c *gin.Context) {
	u := middleware.GetCurrentUser(c)
	if u == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	id, err := parseNoteID(c)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	if dErr := ctl.biz.DeleteNote(c.Request.Context(), u.ID, id); dErr != nil {
		core.WriteResponse(c, dErr, nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}

// parseNoteID 解析并校验路径参数 :id（正整数）。
func parseNoteID(c *gin.Context) (uint64, error) {
	raw := c.Param("id")
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, errno.ErrBind.SetMessage("invalid id: %s", raw)
	}
	return id, nil
}
