package chat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/store"
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
