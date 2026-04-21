// Package chatbot C端智能体对话控制器
package chatbot

import (
	"encoding/json"
	"fmt"
	"strconv"

	"numind-server/internal/numind/biz/chatbot"
	"numind-server/internal/numind/biz/llmrouter"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
)

// ChatbotController C端智能体对话控制器
type ChatbotController struct {
	chatbotBiz chatbot.IChatbotBiz
	llmRouter  *llmrouter.Router
}

// NewChatbotController 创建C端智能体对话控制器
func NewChatbotController(chatbotBiz chatbot.IChatbotBiz, llmRouter *llmrouter.Router) *ChatbotController {
	return &ChatbotController{chatbotBiz: chatbotBiz, llmRouter: llmRouter}
}

// List 获取当前用户可见的智能体列表（每项含 has_permission 标志供 UI 显示锁）。
// 父账号所有项为 true；子账号按 user_chatbot_permission 白名单批量判定。
// 安全 gate 仍由 check-permission / CreateSession / ChatStream 强制，本端点仅影响 UI。
func (ctrl *ChatbotController) List(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	list, err := ctrl.chatbotBiz.ListVisibleChatbotsWithPermission(c, user)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, list)
}

// CheckPermission 检查当前用户是否有权运行指定 chatbot
// 用于前端在跳转 chatbot 聊天页前做权限预检，mirror SOP CheckTemplatePermission。
func (ctrl *ChatbotController) CheckPermission(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	chatbotID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	hasPermission, err := ctrl.chatbotBiz.CheckChatbotPermission(c, user.ID, chatbotID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"has_permission": hasPermission,
	})
}

// createSessionReq 创建会话请求
type createSessionReq struct {
	ChatbotID uint `json:"chatbot_id" binding:"required"`
}

// CreateSession 创建对话会话
func (ctrl *ChatbotController) CreateSession(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req createSessionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	session, err := ctrl.chatbotBiz.CreateSession(c, user.ID, req.ChatbotID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, session)
}

// ListSessions 获取用户的对话会话列表
func (ctrl *ChatbotController) ListSessions(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	offset, limit := parsePagination(c)

	list, total, err := ctrl.chatbotBiz.ListSessions(c, user.ID, offset, limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// DeleteSession 删除对话会话
func (ctrl *ChatbotController) DeleteSession(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	if err := ctrl.chatbotBiz.DeleteSession(c, user.ID, id); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, nil)
}

// ListMessages 获取会话消息列表
func (ctrl *ChatbotController) ListMessages(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	id, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	offset, limit := parsePagination(c)

	list, total, err := ctrl.chatbotBiz.ListMessages(c, user.ID, id, offset, limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"list": list, "total": total})
}

// chatReq 聊天请求
type chatReq struct {
	Message string `json:"message" binding:"required"`
}

// Chat SSE流式聊天接口
func (ctrl *ChatbotController) Chat(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	sessionID, ok := parseUintParam(c, "id")
	if !ok {
		return
	}

	var req chatReq
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("参数绑定失败: %s", err.Error()), nil)
		return
	}

	// 读取模型参数（可选），走三级 fallback 解析
	queryModelKey := c.Query("model_key")
	thinkingStr := c.Query("thinking")
	queryThinking := thinkingStr == "1" || thinkingStr == "true"
	var queryThinkingPtr *bool
	if thinkingStr != "" {
		queryThinkingPtr = &queryThinking
	}

	resolvedModelKey, resolvedThinking, resolveErr := ctrl.llmRouter.ResolveUserModel(
		c.Request.Context(), user.ID, "chatbot", queryModelKey, queryThinkingPtr)
	if resolveErr != nil {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("模型解析失败: %s", resolveErr.Error()), nil)
		return
	}

	// 设置 SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	w := c.Writer

	err := ctrl.chatbotBiz.ChatStream(c, user.ID, sessionID, req.Message, resolvedModelKey, resolvedThinking,
		func(eventType string, data interface{}) error {
			var eventData []byte
			var marshalErr error

			switch eventType {
			case "token", "thinking":
				// biz 层传入 map[string]string{"content": "..."}, 提取实际文本
				text := ""
				if m, ok := data.(map[string]string); ok {
					text = m["content"]
				} else if s, ok := data.(string); ok {
					text = s
				}
				eventData, marshalErr = json.Marshal(map[string]interface{}{
					"type": eventType,
					"data": text,
				})
			case "done":
				// biz 层传入的 doneData 是 map[string]interface{}，补充 type 字段
				if m, ok := data.(map[string]interface{}); ok {
					m["type"] = "done"
					eventData, marshalErr = json.Marshal(m)
				} else {
					eventData, marshalErr = json.Marshal(map[string]interface{}{"type": "done"})
				}
			case "error":
				eventData, marshalErr = json.Marshal(map[string]interface{}{
					"type": "error",
					"data": data,
				})
			default:
				return nil
			}

			if marshalErr != nil {
				return marshalErr
			}

			_, writeErr := fmt.Fprintf(w, "data: %s\n\n", eventData)
			if writeErr != nil {
				return writeErr
			}

			w.Flush()
			return nil
		})

	if err != nil {
		errData, _ := json.Marshal(map[string]interface{}{
			"type": "error",
			"data": err.Error(),
		})
		fmt.Fprintf(w, "data: %s\n\n", errData)
		w.Flush()
	}
}

// parseUintParam 解析路径中的 uint 参数
func parseUintParam(c *gin.Context, name string) (uint, bool) {
	v, err := strconv.ParseUint(c.Param(name), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("invalid %s", name), nil)
		return 0, false
	}
	return uint(v), true
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
