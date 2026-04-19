package config

import (
	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/model/dto"

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
	userID := currentUserID(c)

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
	userID := currentUserID(c)
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

	// P0 安全：禁止直接序列化 model.SopNode（含 api_key/base_url/model_name/
	// timeout_seconds 4 个基础设施字段）。B 端编辑器需要 prompt 字段，所以
	// 使用 SopNodeEditDTO（保留 prompt，隐藏 4 个基础设施字段）。
	core.WriteResponse(c, nil, gin.H{
		"template": dto.ToSopTemplatePublicDTO(template),
		"nodes":    dto.ToSopNodeEditDTOList(nodes),
	})
}

// updateTemplateReq SOP模板更新请求
type updateTemplateReq struct {
	Name                *string `json:"name"`
	Description         *string `json:"description"`
	TrailingChatEnabled *bool   `json:"trailing_chat_enabled"`
}

// Update 更新SOP模板
func (ctrl *SopConfigController) Update(c *gin.Context) {
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req updateTemplateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.TrailingChatEnabled != nil {
		updates["trailing_chat_enabled"] = *req.TrailingChatEnabled
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
	userID := currentUserID(c)
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
	case "draft":
		err = ctrl.sopBiz.UnpublishTemplate(c, userID, id)
	default:
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效状态: %s，仅支持 published/draft", req.Status), nil)
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

	// 白名单 binding：拒绝 B 端写入 base_url/model_name/api_key/timeout_seconds
	// 这些字段是平台后端的 LLM 服务配置，B 端不应也不能修改。
	// 即使前端发送了这些字段，也会被 anonymous struct 丢弃。
	// 详见 spec §2.2 + 用户决策"B 端可配置字段范围 = prompt/name/description/顺序"
	var req struct {
		Name        string `json:"name" binding:"required"`
		Description string `json:"description"`
		Prompt      string `json:"prompt" binding:"required"`
		Sort        int    `json:"sort"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	node := &model.SopNode{
		TemplateID:  templateID,
		Name:        req.Name,
		Description: req.Description,
		Prompt:      req.Prompt,
		Sort:        req.Sort,
		Status:      "active",
	}

	created, err := ctrl.sopBiz.CreateNode(c, node)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// P0 安全：返回 EditDTO 而非 *model.SopNode
	core.WriteResponse(c, nil, dto.ToSopNodeEditDTO(created))
}

// updateNodeReq SOP节点更新请求
type updateNodeReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Prompt      *string `json:"prompt"`
	Sort        *int    `json:"sort"`
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

	var req updateNodeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	updates := make(map[string]interface{})
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Prompt != nil {
		updates["prompt"] = *req.Prompt
	}
	if req.Sort != nil {
		updates["sort"] = *req.Sort
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

// batchSortReq wraps the items array to match the frontend payload { items: [...] }
type batchSortReq struct {
	Items []nodeSortItem `json:"items" binding:"required"`
}

// BatchSortNodes 批量更新节点排序
func (ctrl *SopConfigController) BatchSortNodes(c *gin.Context) {
	_, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req batchSortReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}
	items := req.Items

	for _, item := range items {
		updates := map[string]interface{}{"sort": item.Sort}
		if err := ctrl.sopBiz.UpdateNode(c, item.ID, updates); err != nil {
			core.WriteResponse(c, err, nil)
			return
		}
	}

	core.WriteResponse(c, nil, nil)
}
