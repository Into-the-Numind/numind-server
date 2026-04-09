package config

import (
	"strconv"

	"numind-server/internal/numind/biz/knowledgebase"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

// KnowledgeBaseController B端知识库配置控制器
type KnowledgeBaseController struct {
	kbBiz knowledgebase.IKnowledgeBaseBiz
}

// NewKnowledgeBaseController 创建知识库配置控制器
func NewKnowledgeBaseController(kbBiz knowledgebase.IKnowledgeBaseBiz) *KnowledgeBaseController {
	return &KnowledgeBaseController{kbBiz: kbBiz}
}

// Create 创建知识库
func (ctrl *KnowledgeBaseController) Create(c *gin.Context) {
	userID := currentUserID(c)

	var req knowledgebase.CreateKBReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	kb, err := ctrl.kbBiz.Create(c, userID, &req)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, kb)
}

// List 获取知识库列表
func (ctrl *KnowledgeBaseController) List(c *gin.Context) {
	userID := currentUserID(c)
	offset, limit := parsePagination(c)

	list, total, err := ctrl.kbBiz.List(c, userID, offset, limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// Get 获取知识库详情
func (ctrl *KnowledgeBaseController) Get(c *gin.Context) {
	userID := currentUserID(c)
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	detail, err := ctrl.kbBiz.Get(c, userID, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, detail)
}

// Update 更新知识库
func (ctrl *KnowledgeBaseController) Update(c *gin.Context) {
	userID := currentUserID(c)
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req knowledgebase.UpdateKBReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	if err := ctrl.kbBiz.Update(c, userID, id, &req); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// Delete 删除知识库
func (ctrl *KnowledgeBaseController) Delete(c *gin.Context) {
	userID := currentUserID(c)
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	if err := ctrl.kbBiz.Delete(c, userID, id); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// UploadDocument 上传知识库文档
func (ctrl *KnowledgeBaseController) UploadDocument(c *gin.Context) {
	userID := currentUserID(c)
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("文件上传失败: %s", err.Error()), nil)
		return
	}
	defer file.Close()

	doc, err := ctrl.kbBiz.AddDocument(c, userID, id, file, header)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, doc)
}

// RemoveDocument 删除知识库文档
func (ctrl *KnowledgeBaseController) RemoveDocument(c *gin.Context) {
	userID := currentUserID(c)
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	docID, ok := parseUintParam(c, "docId")
	if !ok {
		return
	}

	if err := ctrl.kbBiz.RemoveDocument(c, userID, id, docID); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// parseUintParam 解析路径参数为 uint
func parseUintParam(c *gin.Context, name string) (uint, bool) {
	v, err := strconv.ParseUint(c.Param(name), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid %s", name), nil)
		return 0, false
	}
	return uint(v), true
}

// currentUserID 从 middleware 设置的 current_user 中提取用户 ID
func currentUserID(c *gin.Context) uint {
	u, exists := c.Get("current_user")
	if !exists {
		return 0
	}
	if user, ok := u.(*model.User); ok {
		return user.ID
	}
	return 0
}

// parsePagination 解析分页参数
func parsePagination(c *gin.Context) (int, int) {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit > 100 {
		limit = 100
	}
	return offset, limit
}
