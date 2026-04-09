package config

import (
	"numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"

	"github.com/gin-gonic/gin"
)

// ChatbotConfigController B端智能体配置控制器
type ChatbotConfigController struct {
	chatbotBiz chatbot.IChatbotBiz
}

// NewChatbotConfigController 创建智能体配置控制器
func NewChatbotConfigController(chatbotBiz chatbot.IChatbotBiz) *ChatbotConfigController {
	return &ChatbotConfigController{chatbotBiz: chatbotBiz}
}

// Create 创建智能体
func (ctrl *ChatbotConfigController) Create(c *gin.Context) {
	userID := c.GetUint("userID")

	var req chatbot.CreateChatbotReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	bot, err := ctrl.chatbotBiz.CreateChatbot(c, userID, &req)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, bot)
}

// List 获取智能体列表
func (ctrl *ChatbotConfigController) List(c *gin.Context) {
	userID := c.GetUint("userID")
	offset, limit := parsePagination(c)

	list, total, err := ctrl.chatbotBiz.ListChatbots(c, userID, offset, limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// Get 获取智能体详情
func (ctrl *ChatbotConfigController) Get(c *gin.Context) {
	userID := c.GetUint("userID")
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	detail, err := ctrl.chatbotBiz.GetChatbot(c, userID, id)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, detail)
}

// Update 更新智能体
func (ctrl *ChatbotConfigController) Update(c *gin.Context) {
	userID := c.GetUint("userID")
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req chatbot.UpdateChatbotReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	if err := ctrl.chatbotBiz.UpdateChatbot(c, userID, id, &req); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// Delete 删除智能体
func (ctrl *ChatbotConfigController) Delete(c *gin.Context) {
	userID := c.GetUint("userID")
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	if err := ctrl.chatbotBiz.DeleteChatbot(c, userID, id); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// updateStatusReq 更新状态请求
type updateStatusReq struct {
	Status string `json:"status" binding:"required"`
}

// UpdateStatus 更新智能体状态（发布/下线）
func (ctrl *ChatbotConfigController) UpdateStatus(c *gin.Context) {
	userID := c.GetUint("userID")
	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req updateStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	if err := ctrl.chatbotBiz.UpdateStatus(c, userID, id, req.Status); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}
