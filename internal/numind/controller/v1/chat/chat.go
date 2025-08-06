package chat

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// ChatController 是 chat 模块在 Controller 层的实现
type ChatController struct {
	b biz.IBiz
}

// New 创建一个 chat controller
func New(ds store.IStore) *ChatController {
	return &ChatController{b: biz.NewBiz(ds)}
}

// WebSocket升级器
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许所有来源，生产环境中应该更严格
	},
}

// WebSocket 处理WebSocket连接
func (ctrl *ChatController) WebSocket(c *gin.Context) {
	// 从认证中间件中获取用户信息
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	userID := currentUser.ID

	// 升级HTTP连接为WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Errorw("Failed to upgrade connection to WebSocket", "error", err)
		return
	}
	defer conn.Close()

	log.Infow("WebSocket connection established", "user_id", userID)

	// 处理WebSocket消息
	for {
		// 读取消息
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Errorw("Failed to read WebSocket message", "error", err)
			break
		}

		// 解析消息
		var wsMsg model.WebSocketMessage
		if err := json.Unmarshal(message, &wsMsg); err != nil {
			log.Errorw("Failed to unmarshal WebSocket message", "error", err)
			continue
		}

		// 设置时间戳
		wsMsg.Timestamp = time.Now()

		// 处理消息
		response, err := ctrl.b.Chats().ProcessWebSocketMessage(c.Request.Context(), userID, &wsMsg)
		if err != nil {
			log.Errorw("Failed to process WebSocket message", "error", err)
			response = &model.WebSocketMessage{
				Type:      "error",
				Error:     err.Error(),
				Timestamp: time.Now(),
			}
		}

		// 发送响应
		responseBytes, err := json.Marshal(response)
		if err != nil {
			log.Errorw("Failed to marshal WebSocket response", "error", err)
			continue
		}

		if err := conn.WriteMessage(websocket.TextMessage, responseBytes); err != nil {
			log.Errorw("Failed to write WebSocket message", "error", err)
			break
		}
	}

	log.Infow("WebSocket connection closed", "user_id", userID)
}

// CreateSession 创建新的对话会话
func (ctrl *ChatController) CreateSession(c *gin.Context) {
	// 从认证中间件中获取用户信息
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	var req struct {
		Title string `json:"title" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	session, err := ctrl.b.Chats().CreateSession(c.Request.Context(), currentUser.ID, req.Title)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(c, nil, session)
}

// GetSession 获取对话会话
func (ctrl *ChatController) GetSession(c *gin.Context) {
	// 从认证中间件中获取用户信息
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	session, err := ctrl.b.Chats().GetSession(c.Request.Context(), uint(sessionID), currentUser.ID)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(c, nil, session)
}

// ListSessions 获取用户的对话会话列表
func (ctrl *ChatController) ListSessions(c *gin.Context) {
	// 从认证中间件中获取用户信息
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))

	sessions, total, err := ctrl.b.Chats().ListSessions(c.Request.Context(), currentUser.ID, offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total":    total,
		"sessions": sessions,
	})
}

// UpdateSession 更新对话会话
func (ctrl *ChatController) UpdateSession(c *gin.Context) {
	// 从认证中间件中获取用户信息
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	var req struct {
		Title string `json:"title" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	err = ctrl.b.Chats().UpdateSession(c.Request.Context(), uint(sessionID), currentUser.ID, req.Title)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "Session updated successfully"})
}

// DeleteSession 删除对话会话
func (ctrl *ChatController) DeleteSession(c *gin.Context) {
	// 从认证中间件中获取用户信息
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	err = ctrl.b.Chats().DeleteSession(c.Request.Context(), uint(sessionID), currentUser.ID)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{"message": "Session deleted successfully"})
}

// ListMessages 获取会话的消息列表
func (ctrl *ChatController) ListMessages(c *gin.Context) {
	// 从认证中间件中获取用户信息
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	sessionIDStr := c.Param("session_id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))

	messages, total, err := ctrl.b.Chats().ListMessages(c.Request.Context(), uint(sessionID), currentUser.ID, offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total":    total,
		"messages": messages,
	})
}

// GetSessionWithMessages 获取会话及其消息
func (ctrl *ChatController) GetSessionWithMessages(c *gin.Context) {
	// 从认证中间件中获取用户信息
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	session, err := ctrl.b.Chats().GetSessionWithMessages(c.Request.Context(), uint(sessionID), currentUser.ID)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(c, nil, session)
}
