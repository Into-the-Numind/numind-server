// Package document 是文档系统 v1 的 HTTP 控制层（document-system feature）。
//
// 职责边界（controller 硬规则）：本层只做参数绑定 + 鉴权上下文提取 + 调用 biz +
// core.WriteResponse。DTO 与 domain error 由 biz 层拥有，controller 直接透传，不含业务逻辑。
package document

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"

	documentbiz "numind-server/internal/numind/biz/document"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// Controller 是文档系统用户端控制器。
type Controller struct {
	biz documentbiz.IDocumentService
}

// NewController 创建文档系统控制器（沿用 NewXxxController(biz) 模式）。
func NewController(biz documentbiz.IDocumentService) *Controller {
	return &Controller{biz: biz}
}

// currentUser 从 gin 上下文提取当前用户（AuthMiddleware 注入 current_user）。
func currentUser(c *gin.Context) (*model.User, bool) {
	v, exists := c.Get("current_user")
	if !exists {
		return nil, false
	}
	u, ok := v.(*model.User)
	return u, ok
}

// parseID 解析路径参数 :id。
func parseID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// Open 打开/懒建档一个 agent 生成产物为可编辑文档。POST /v1/documents/open
func (ctl *Controller) Open(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}
	var req documentbiz.OpenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	dto, err := ctl.biz.OpenFromArtifact(c, uint(u.ID), u.ParentUserID, req)
	core.WriteResponse(c, err, dto)
}

// Get 取文档（重开）。GET /v1/documents/:id
func (ctl *Controller) Get(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}
	id, ok := parseID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid document id"), nil)
		return
	}
	dto, err := ctl.biz.Get(c, uint(u.ID), id)
	core.WriteResponse(c, err, dto)
}

// Save 保存文档正文/标题（自动保存）。PUT /v1/documents/:id
func (ctl *Controller) Save(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}
	id, ok := parseID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid document id"), nil)
		return
	}
	var req documentbiz.SaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("%s", err.Error()), nil)
		return
	}
	dto, err := ctl.biz.Save(c, uint(u.ID), id, req)
	core.WriteResponse(c, err, dto)
}

// Export 导出下载文档。GET /v1/documents/:id/export?format=md|pdf|docx
func (ctl *Controller) Export(c *gin.Context) {
	u, ok := currentUser(c)
	if !ok {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}
	id, ok := parseID(c)
	if !ok {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid document id"), nil)
		return
	}
	format := c.Query("format")
	filename, contentType, data, err := ctl.biz.Export(c, uint(u.ID), id, format)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	// RFC 5987 filename*，支持非 ASCII（中文标题）下载文件名。
	c.Header("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(filename))
	c.Data(http.StatusOK, contentType, data)
}
