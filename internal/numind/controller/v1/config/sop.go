package config

import (
	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

// SopConfigController B端SOP模板配置控制器
type SopConfigController struct {
	sopBiz sop.ISopBiz
}

// NewSopConfigController 创建SOP配置控制器
func NewSopConfigController(sopBiz sop.ISopBiz) *SopConfigController {
	return &SopConfigController{sopBiz: sopBiz}
}

// Create 创建SOP模板
func (ctrl *SopConfigController) Create(c *gin.Context) {
	userID := c.GetUint("userID")

	var req sop.CreateTemplateByUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	template, err := ctrl.sopBiz.CreateTemplateByUser(c, userID, &req)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, template)
}

// List 获取当前用户创建的SOP模板列表
func (ctrl *SopConfigController) List(c *gin.Context) {
	userID := c.GetUint("userID")
	offset, limit := parsePagination(c)

	list, total, err := ctrl.sopBiz.ListTemplatesByCreator(c, userID, offset, limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// Get 获取SOP模板详情（含节点）
func (ctrl *SopConfigController) Get(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	template, err := ctrl.sopBiz.GetTemplate(c, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	nodes, err := ctrl.sopBiz.ListNodesByTemplate(c, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"template": template, "nodes": nodes})
}

// Update 更新SOP模板
func (ctrl *SopConfigController) Update(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	if err := ctrl.sopBiz.UpdateTemplate(c, id, updates); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// Delete 删除SOP模板
func (ctrl *SopConfigController) Delete(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	if err := ctrl.sopBiz.DeleteTemplate(c, id); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// sopStatusReq SOP模板状态更新请求
type sopStatusReq struct {
	Status string `json:"status" binding:"required"`
}

// UpdateStatus 更新SOP模板状态（发布/下线）
func (ctrl *SopConfigController) UpdateStatus(c *gin.Context) {
	userID := c.GetUint("userID")
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req sopStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	var err error
	switch req.Status {
	case "published":
		err = ctrl.sopBiz.PublishTemplate(c, userID, id)
	case "offline":
		err = ctrl.sopBiz.UnpublishTemplate(c, userID, id)
	default:
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效状态: %s，仅支持 published/offline", req.Status), nil)
		return
	}

	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// CreateNode 创建SOP节点
func (ctrl *SopConfigController) CreateNode(c *gin.Context) {
	templateID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var node model.SopNode
	if err := c.ShouldBindJSON(&node); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}
	node.TemplateID = templateID

	created, err := ctrl.sopBiz.CreateNode(c, &node)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, created)
}

// UpdateNode 更新SOP节点
func (ctrl *SopConfigController) UpdateNode(c *gin.Context) {
	_, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	nodeID, ok := parseUintParam(c, "nodeId")
	if !ok {
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	if err := ctrl.sopBiz.UpdateNode(c, nodeID, updates); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// DeleteNode 删除SOP节点
func (ctrl *SopConfigController) DeleteNode(c *gin.Context) {
	_, ok := parseUintParam(c, "id")
	if !ok {
		return
	}
	nodeID, ok := parseUintParam(c, "nodeId")
	if !ok {
		return
	}

	if err := ctrl.sopBiz.DeleteNode(c, nodeID); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// nodeSortItem 节点排序项
type nodeSortItem struct {
	ID   uint `json:"id" binding:"required"`
	Sort int  `json:"sort"`
}

// BatchSortNodes 批量更新节点排序
func (ctrl *SopConfigController) BatchSortNodes(c *gin.Context) {
	_, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var items []nodeSortItem
	if err := c.ShouldBindJSON(&items); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	for _, item := range items {
		updates := map[string]interface{}{"sort": item.Sort}
		if err := ctrl.sopBiz.UpdateNode(c, item.ID, updates); err != nil {
			core.WriteResponse(c, err, nil)
			return
		}
	}

	core.WriteResponse(c, nil, nil)
}
