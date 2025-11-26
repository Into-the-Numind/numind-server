package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/chat"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/spf13/viper"
)

// ChatController 是 chat 模块在 Controller 层的实现
type ChatController struct {
	b       biz.IBiz
	chatBiz chat.ChatBiz
}

// New 创建一个 chat controller
func New(chatBiz chat.ChatBiz) *ChatController {
	return &ChatController{chatBiz: chatBiz}
}

// WebSocket升级器
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许所有来源，生产环境中应该更严格
	},
}

// parseTokenFromString 从token字符串中解析用户ID
func parseTokenFromString(tokenString string) (uint, error) {
	log.Infow("Parsing token", "token_length", len(tokenString))

	// 使用viper获取JWT密钥
	jwtSecret := viper.GetString("jwt.secret")
	if jwtSecret == "" {
		log.Errorw("JWT secret not configured")
		return 0, fmt.Errorf("jwt secret not configured")
	}

	log.Infow("JWT secret configured", "secret_length", len(jwtSecret))

	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})

	if err != nil {
		log.Errorw("Failed to parse JWT token", "error", err)
		return 0, err
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		log.Infow("Token claims", "claims", claims)
		if userID, exists := claims["user_id"]; exists {
			switch v := userID.(type) {
			case float64:
				log.Infow("User ID from token", "user_id", uint(v))
				return uint(v), nil
			case int:
				log.Infow("User ID from token", "user_id", uint(v))
				return uint(v), nil
			case uint:
				log.Infow("User ID from token", "user_id", v)
				return v, nil
			default:
				log.Errorw("Invalid user_id type in token", "type", fmt.Sprintf("%T", userID))
				return 0, fmt.Errorf("invalid user_id type in token")
			}
		}
		log.Errorw("user_id not found in token claims")
		return 0, fmt.Errorf("user_id not found in token")
	}

	log.Errorw("Invalid token or claims")
	return 0, fmt.Errorf("invalid token")
}

// WebSocket 处理WebSocket连接
func (ctrl *ChatController) WebSocket(c *gin.Context) {
	// 检查是否是WebSocket升级请求
	if !websocket.IsWebSocketUpgrade(c.Request) {
		// 如果是普通HTTP请求，返回错误
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("此端点仅支持WebSocket连接"), nil)
		return
	}

	var userID uint
	var currentUser *model.User

	// 首先尝试从认证中间件中获取用户信息（HTTP header方式）
	currentUser = middleware.GetCurrentUser(c)
	if currentUser != nil {
		userID = currentUser.ID
		log.Infow("WebSocket authentication via HTTP header", "user_id", userID)
	} else {
		// 如果HTTP header认证失败，尝试从URL参数中获取token
		tokenParam := c.Query("token")
		if tokenParam == "" {
			// 尝试从Authorization header中获取token
			authHeader := c.GetHeader("Authorization")
			if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
				tokenParam = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if tokenParam != "" {
			// 验证token并获取用户信息
			var err error
			userID, err = parseTokenFromString(tokenParam)
			if err != nil {
				log.Errorw("Failed to parse token from URL parameter", "error", err)
				core.WriteResponse(c, errno.ErrUnauthorized, nil)
				return
			}

			log.Infow("WebSocket authentication via URL parameter", "user_id", userID)
		} else {
			log.Errorw("No valid authentication found for WebSocket connection")
			core.WriteResponse(c, errno.ErrUnauthorized, nil)
			return
		}
	}

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

		// 统一请求结构：支持 question 和 book_ids（与HTTP保持一致）
		// 如果提供了 Content 和 BookID（旧格式），转换为新格式
		if wsMsg.Question == "" && wsMsg.Content != "" {
			wsMsg.Question = wsMsg.Content
		}
		if len(wsMsg.BookIDs) == 0 && wsMsg.BookID != nil {
			wsMsg.BookIDs = []uint{*wsMsg.BookID}
		}

		// 自动判断消息类型：如果有 question 或 content，且没有指定其他 type，则默认为聊天消息
		// type 字段可以完全省略，系统会自动判断
		if wsMsg.Type == "" {
			if wsMsg.Question != "" || wsMsg.Content != "" {
				wsMsg.Type = "chat" // 有问题的消息，默认为聊天
			}
		}

		// 处理消息（对于 chat 类型或未指定 type 但有 question 的使用流式处理）
		if wsMsg.Type == "chat" || wsMsg.Type == "message" || (wsMsg.Type == "" && wsMsg.Question != "") {
			// 使用流式处理
			log.Infow("开始处理WebSocket流式消息", "user_id", userID, "question", wsMsg.Question, "book_ids", wsMsg.BookIDs, "session_id", wsMsg.SessionID)
			_, err := ctrl.chatBiz.ProcessWebSocketMessageStream(c.Request.Context(), userID, &wsMsg, conn)
			if err != nil {
				// 记录详细错误日志（包含原始错误信息，用于调试）
				log.Errorw("Failed to process WebSocket message stream", "error", err, "user_id", userID)
				// 发送用户友好的错误消息（不暴露内部细节）
				errorMsg := &model.WebSocketMessage{
					Type:      "error",
					Error:     err.Error(), // err.Error() 已经是用户友好的消息（经过 wrapChatError 处理）
					Timestamp: time.Now(),
				}
				errorBytes, _ := json.Marshal(errorMsg)
				conn.WriteMessage(websocket.TextMessage, errorBytes)
			} else {
				log.Infow("WebSocket流式消息处理完成", "user_id", userID)
			}
		} else {
			// 其他类型的消息使用原有逻辑（session, search_books, ping等）
			response, err := ctrl.chatBiz.ProcessWebSocketMessage(c.Request.Context(), userID, &wsMsg)
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

	session, err := ctrl.chatBiz.CreateSession(c.Request.Context(), currentUser.ID, req.Title, nil)
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

	session, err := ctrl.chatBiz.GetSession(c.Request.Context(), uint(sessionID), currentUser.ID)
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

	sessions, total, err := ctrl.chatBiz.ListSessions(c.Request.Context(), currentUser.ID, offset, limit)
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

	err = ctrl.chatBiz.UpdateSession(c.Request.Context(), uint(sessionID), currentUser.ID, req.Title)
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

	err = ctrl.chatBiz.DeleteSession(c.Request.Context(), uint(sessionID), currentUser.ID)
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

	messages, total, err := ctrl.chatBiz.ListMessages(c.Request.Context(), uint(sessionID), currentUser.ID, offset, limit)
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

	session, err := ctrl.chatBiz.GetSessionWithMessages(c.Request.Context(), uint(sessionID), currentUser.ID)
	if err != nil {
		core.WriteResponse(c, errno.ErrInternalServer, nil)
		return
	}

	core.WriteResponse(c, nil, session)
}

// GetSessionHistory 获取会话的聊天记录
func (ctrl *ChatController) GetSessionHistory(c *gin.Context) {
	// 从认证中间件中获取用户信息
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 获取会话ID
	sessionIDStr := c.Param("session_id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的会话ID"), nil)
		return
	}

	// 获取limit参数（可选，默认50）
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	// 获取offset参数（可选，默认0）
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 {
		offset = 0
	}

	// 获取聊天记录
	session, messages, total, err := ctrl.chatBiz.GetSessionHistory(c.Request.Context(), currentUser.ID, uint(sessionID), offset, limit)
	if err != nil {
		log.C(c).Errorw("获取会话聊天记录失败", "error", err, "session_id", sessionID)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("获取会话聊天记录失败: %s", err.Error()), nil)
		return
	}

	// 构建响应
	response := gin.H{
		"session":  session,
		"messages": messages,
		"total":    total,
		"offset":   offset,
		"limit":    limit,
	}

	core.WriteResponse(c, nil, response)
}

// ListBookSessions 列出笔记的所有会话
func (ctrl *ChatController) ListBookSessions(c *gin.Context) {
	// 从认证中间件中获取用户信息
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	// 获取笔记ID
	bookIDStr := c.Param("book_id")
	bookID, err := strconv.ParseUint(bookIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "10"))

	sessions, total, err := ctrl.chatBiz.ListSessionsByBook(c.Request.Context(), currentUser.ID, uint(bookID), offset, limit)
	if err != nil {
		log.C(c).Errorw("列出笔记会话失败", "error", err, "book_id", bookID)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("列出笔记会话失败: %s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total":    total,
		"sessions": sessions,
	})
}
