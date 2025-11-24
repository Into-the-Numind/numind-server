package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/rag"
	"numind-server/internal/numind/biz/user"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"

	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

// ChatBiz 定义了对话相关的业务逻辑接口
type ChatBiz interface {
	CreateSession(ctx context.Context, userID uint, title string, bookID *uint) (*model.ChatSession, error)
	GetSession(ctx context.Context, sessionID uint, userID uint) (*model.ChatSession, error)
	ListSessions(ctx context.Context, userID uint, offset, limit int) ([]*model.ChatSession, int64, error)
	UpdateSession(ctx context.Context, sessionID uint, userID uint, title string) error
	DeleteSession(ctx context.Context, sessionID uint, userID uint) error

	CreateMessage(ctx context.Context, sessionID uint, userID uint, content string, role string) (*model.ChatMessage, error)
	GetMessage(ctx context.Context, messageID uint, userID uint) (*model.ChatMessage, error)
	ListMessages(ctx context.Context, sessionID uint, userID uint, offset, limit int) ([]*model.ChatMessage, int64, error)
	UpdateMessage(ctx context.Context, messageID uint, userID uint, content string) error
	DeleteMessage(ctx context.Context, messageID uint, userID uint) error

	GetSessionWithMessages(ctx context.Context, sessionID uint, userID uint) (*model.ChatSession, error)

	// 新增：根据笔记ID获取或创建会话
	GetOrCreateSessionByBook(ctx context.Context, userID uint, bookID uint, title string) (*model.ChatSession, error)

	// 新增：获取笔记的聊天记录
	GetBookChatHistory(ctx context.Context, userID uint, bookID uint, limit int) (*model.ChatSession, []*model.ChatMessage, error)

	// 新增：列出笔记的所有会话
	ListSessionsByBook(ctx context.Context, userID uint, bookID uint, offset, limit int) ([]*model.ChatSession, int64, error)

	// WebSocket相关方法
	ProcessWebSocketMessage(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error)
	ProcessWebSocketMessageStream(ctx context.Context, userID uint, msg *model.WebSocketMessage, conn *websocket.Conn) (*model.WebSocketMessage, error)
	GenerateAssistantResponse(ctx context.Context, userMessage string) (string, error)
}

// chatBiz 是 ChatBiz 的具体实现
type chatBiz struct {
	ds         store.IStore
	userBiz    user.UserBiz
	ragService *rag.RagService
	aliBiz     ali.AliBiz
	volcBiz    volc.VolcBiz
}

// New 创建一个新的 ChatBiz 实例
func New(ds store.IStore, userBiz user.UserBiz, aliBiz ali.AliBiz, volcBiz volc.VolcBiz, ragService *rag.RagService) ChatBiz {
	return &chatBiz{
		ds:         ds,
		userBiz:    userBiz,
		ragService: ragService,
		aliBiz:     aliBiz,
		volcBiz:    volcBiz,
	}
}

// wrapChatError 将内部错误包装为用户友好的错误消息
// 避免暴露数据库内部细节（如表名、外键约束等）
func wrapChatError(err error, operation string) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()

	// 检查是否是记录不存在错误（这是业务逻辑错误，可以返回具体消息）
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if strings.Contains(operation, "session") {
			return fmt.Errorf("对话会话不存在")
		}
		if strings.Contains(operation, "book") || strings.Contains(operation, "笔记") {
			return fmt.Errorf("笔记不存在")
		}
		return fmt.Errorf("资源不存在")
	}

	// 检查是否是权限错误（这是业务逻辑错误，可以返回具体消息）
	if strings.Contains(errStr, "unauthorized") || strings.Contains(errStr, "forbidden") {
		return fmt.Errorf("无权访问该资源")
	}

	// 对于已知的业务错误，直接返回（这些错误消息已经是用户友好的）
	if strings.Contains(errStr, "笔记不存在") ||
		strings.Contains(errStr, "无权访问") ||
		strings.Contains(errStr, "对话会话不存在") ||
		strings.Contains(errStr, "资源不存在") {
		return err
	}

	// 所有数据库相关错误（外键约束、连接错误、SQL错误等）统一返回内部错误
	if strings.Contains(errStr, "foreign key") ||
		strings.Contains(errStr, "1452") ||
		strings.Contains(errStr, "23000") ||
		strings.Contains(errStr, "Cannot add or update") ||
		strings.Contains(errStr, "database") ||
		strings.Contains(errStr, "sql") ||
		strings.Contains(errStr, "connection") ||
		strings.Contains(errStr, "timeout") {
		return fmt.Errorf("内部错误，请稍后重试")
	}

	// 未知错误，返回通用错误消息
	return fmt.Errorf("内部错误，请稍后重试")
}

// CreateSession 创建新的对话会话
func (b *chatBiz) CreateSession(ctx context.Context, userID uint, title string, bookID *uint) (*model.ChatSession, error) {
	session := &model.ChatSession{
		UserID: userID,
		BookID: bookID,
		Title:  title,
		Status: "active",
	}

	if err := b.ds.Chats().CreateSession(ctx, session); err != nil {
		return nil, wrapChatError(err, "create session")
	}

	return session, nil
}

// GetSession 获取对话会话
func (b *chatBiz) GetSession(ctx context.Context, sessionID uint, userID uint) (*model.ChatSession, error) {
	session, err := b.ds.Chats().GetSession(ctx, sessionID)
	if err != nil {
		return nil, wrapChatError(err, "get session")
	}

	// 验证用户权限
	if session.UserID != userID {
		return nil, wrapChatError(fmt.Errorf("unauthorized access to session"), "get session")
	}

	return session, nil
}

// ListSessions 获取用户的对话会话列表
func (b *chatBiz) ListSessions(ctx context.Context, userID uint, offset, limit int) ([]*model.ChatSession, int64, error) {
	return b.ds.Chats().ListSessions(ctx, userID, offset, limit)
}

// UpdateSession 更新对话会话
func (b *chatBiz) UpdateSession(ctx context.Context, sessionID uint, userID uint, title string) error {
	session, err := b.GetSession(ctx, sessionID, userID)
	if err != nil {
		return err
	}

	session.Title = title
	return b.ds.Chats().UpdateSession(ctx, session)
}

// DeleteSession 删除对话会话
func (b *chatBiz) DeleteSession(ctx context.Context, sessionID uint, userID uint) error {
	// 验证用户权限
	_, err := b.GetSession(ctx, sessionID, userID)
	if err != nil {
		return err
	}

	return b.ds.Chats().DeleteSession(ctx, sessionID)
}

// CreateMessage 创建新的对话消息
func (b *chatBiz) CreateMessage(ctx context.Context, sessionID uint, userID uint, content string, role string) (*model.ChatMessage, error) {
	// 验证会话存在且用户有权限
	_, err := b.GetSession(ctx, sessionID, userID)
	if err != nil {
		return nil, err
	}

	message := &model.ChatMessage{
		SessionID: sessionID,
		UserID:    userID,
		Role:      role,
		Content:   content,
		Status:    "sent",
	}

	if err := b.ds.Chats().CreateMessage(ctx, message); err != nil {
		return nil, wrapChatError(err, "create message")
	}

	// 更新会话的消息数量
	if err := b.ds.Chats().UpdateSessionMessageCount(ctx, sessionID); err != nil {
		log.Errorw("Failed to update session message count", "error", err)
	}

	return message, nil
}

// GetMessage 获取对话消息
func (b *chatBiz) GetMessage(ctx context.Context, messageID uint, userID uint) (*model.ChatMessage, error) {
	message, err := b.ds.Chats().GetMessage(ctx, messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}

	// 验证用户权限
	if message.UserID != userID {
		return nil, fmt.Errorf("unauthorized access to message")
	}

	return message, nil
}

// ListMessages 获取会话的消息列表
func (b *chatBiz) ListMessages(ctx context.Context, sessionID uint, userID uint, offset, limit int) ([]*model.ChatMessage, int64, error) {
	// 验证会话权限
	_, err := b.GetSession(ctx, sessionID, userID)
	if err != nil {
		return nil, 0, err
	}

	return b.ds.Chats().ListMessages(ctx, sessionID, offset, limit)
}

// UpdateMessage 更新对话消息
func (b *chatBiz) UpdateMessage(ctx context.Context, messageID uint, userID uint, content string) error {
	message, err := b.GetMessage(ctx, messageID, userID)
	if err != nil {
		return err
	}

	message.Content = content
	return b.ds.Chats().UpdateMessage(ctx, message)
}

// DeleteMessage 删除对话消息
func (b *chatBiz) DeleteMessage(ctx context.Context, messageID uint, userID uint) error {
	// 验证用户权限
	_, err := b.GetMessage(ctx, messageID, userID)
	if err != nil {
		return err
	}

	return b.ds.Chats().DeleteMessage(ctx, messageID)
}

// GetSessionWithMessages 获取会话及其消息
func (b *chatBiz) GetSessionWithMessages(ctx context.Context, sessionID uint, userID uint) (*model.ChatSession, error) {
	// 验证会话权限
	_, err := b.GetSession(ctx, sessionID, userID)
	if err != nil {
		return nil, err
	}

	return b.ds.Chats().GetSessionWithMessages(ctx, sessionID)
}

// GetOrCreateSessionByBook 根据笔记ID获取或创建会话
func (b *chatBiz) GetOrCreateSessionByBook(ctx context.Context, userID uint, bookID uint, title string) (*model.ChatSession, error) {
	// 先尝试获取现有会话
	session, err := b.ds.Chats().GetSessionByBookID(ctx, userID, bookID)
	if err == nil {
		return session, nil
	}

	// 如果不存在，创建新会话
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 如果没有提供标题，使用笔记标题
		if title == "" {
			book, err := b.ds.Books().GetByID(ctx, bookID)
			if err == nil {
				title = book.Title + " - AI对话"
			} else {
				// 如果获取笔记失败，可能是笔记不存在
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, wrapChatError(err, "get book for session")
				}
				title = "AI对话"
			}
		}

		bookIDPtr := &bookID
		session, err := b.CreateSession(ctx, userID, title, bookIDPtr)
		if err != nil {
			return nil, wrapChatError(err, "create session by book")
		}

		return session, nil
	}

	return nil, wrapChatError(err, "get session by book")
}

// GetBookChatHistory 获取笔记的聊天记录
func (b *chatBiz) GetBookChatHistory(ctx context.Context, userID uint, bookID uint, limit int) (*model.ChatSession, []*model.ChatMessage, error) {
	// 验证笔记属于用户
	book, err := b.ds.Books().GetByID(ctx, bookID)
	if err != nil {
		return nil, nil, wrapChatError(err, "get book for chat history")
	}

	if book.UserID != userID {
		return nil, nil, wrapChatError(fmt.Errorf("unauthorized access to book"), "get book chat history")
	}

	// 获取聊天记录
	session, messages, err := b.ds.Chats().GetBookChatHistory(ctx, userID, bookID, limit)
	if err != nil {
		return nil, nil, wrapChatError(err, "get book chat history")
	}

	return session, messages, nil
}

// ListSessionsByBook 列出笔记的所有会话
func (b *chatBiz) ListSessionsByBook(ctx context.Context, userID uint, bookID uint, offset, limit int) ([]*model.ChatSession, int64, error) {
	// 验证笔记属于用户
	book, err := b.ds.Books().GetByID(ctx, bookID)
	if err != nil {
		return nil, 0, wrapChatError(err, "get book for sessions")
	}

	if book.UserID != userID {
		return nil, 0, wrapChatError(fmt.Errorf("unauthorized access to book"), "list sessions by book")
	}

	return b.ds.Chats().ListSessionsByBookID(ctx, userID, bookID, offset, limit)
}

// ProcessWebSocketMessage 处理WebSocket消息（非流式，用于非聊天消息）
func (b *chatBiz) ProcessWebSocketMessage(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error) {
	// 如果 type 为空或 "chat"，且提供了 question，则视为聊天消息
	if (msg.Type == "" || msg.Type == "chat" || msg.Type == "message") && msg.Question != "" {
		return b.handleChatMessage(ctx, userID, msg)
	}

	switch msg.Type {
	case "session":
		return b.handleSessionMessage(ctx, userID, msg)
	case "search_books":
		return b.handleBookSearch(ctx, userID, msg)
	case "ping":
		return &model.WebSocketMessage{
			Type:      "pong",
			Timestamp: time.Now(),
		}, nil
	default:
		return &model.WebSocketMessage{
			Type:      "error",
			Error:     "unknown message type",
			Timestamp: time.Now(),
		}, nil
	}
}

// handleChatMessage 处理聊天消息（非流式，保持向后兼容）
func (b *chatBiz) handleChatMessage(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error) {
	// 统一请求结构：使用 question 和 book_ids（与HTTP保持一致）
	question := msg.Question
	if question == "" {
		question = msg.Content // 向后兼容：如果没有 question，使用 content
	}

	if question == "" {
		return nil, fmt.Errorf("question 不能为空")
	}

	bookIDs := msg.BookIDs
	if len(bookIDs) == 0 && msg.BookID != nil {
		// 向后兼容：如果只有 bookID，转换为 book_ids 数组
		bookIDs = []uint{*msg.BookID}
	}

	if len(bookIDs) == 0 {
		return nil, fmt.Errorf("book_ids 不能为空")
	}

	// 创建或获取会话（使用第一个笔记ID作为会话关联）
	// 如果是第一次对话（未提供 session_id），系统会自动创建新会话
	var sessionID uint
	var session *model.ChatSession
	var err error

	if msg.SessionID == 0 {
		// 第一次对话，自动创建新会话
		bookID := bookIDs[0] // 使用第一个笔记ID
		var sessionTitle string
		if len(bookIDs) == 1 {
			sessionTitle = "" // 使用默认标题
		} else {
			sessionTitle = fmt.Sprintf("多笔记对话（%d篇笔记）", len(bookIDs))
		}
		// 使用GetOrCreateSessionByBook确保同一笔记使用同一个会话
		session, err = b.GetOrCreateSessionByBook(ctx, userID, bookID, sessionTitle)
		if err != nil {
			return nil, err
		}
		sessionID = session.ID
		log.C(ctx).Infow("自动创建新会话", "session_id", sessionID, "user_id", userID, "book_id", bookID)
	} else {
		// 验证现有会话
		session, err = b.GetSession(ctx, msg.SessionID, userID)
		if err != nil {
			return nil, err
		}
		sessionID = session.ID
	}

	// 保存用户消息
	_, err = b.CreateMessage(ctx, sessionID, userID, question, "user")
	if err != nil {
		return nil, err
	}

	// 使用 RagService 生成助手回复（支持多笔记）
	var assistantContent string
	if b.ragService != nil {
		assistantContent, err = b.ragService.ChatWithRAG(ctx, userID, question, bookIDs)
		if err != nil {
			log.C(ctx).Errorw("RAG生成回答失败", "error", err)
			assistantContent = "抱歉，生成回答时遇到了一些问题，请稍后重试。"
		}
	} else {
		// 如果 RagService 未初始化，返回提示
		assistantContent = "RAG服务未初始化，请稍后重试。"
	}

	// 保存助手消息
	assistantMessage, err := b.CreateMessage(ctx, sessionID, userID, assistantContent, "assistant")
	if err != nil {
		return nil, err
	}

	// AI对话成功后，增加用户的聊天数量
	if err := b.userBiz.IncrementUserChatNum(ctx, userID); err != nil {
		// 记录错误但不影响对话流程
		log.C(ctx).Errorw("Failed to increment user chat num", "userID", userID, "error", err)
	}

	return &model.WebSocketMessage{
		Type:      "message_done",
		SessionID: sessionID,
		MessageID: assistantMessage.ID,
		Content:   assistantContent,
		Role:      "assistant",
		Timestamp: time.Now(),
	}, nil
}

// handleChatMessageStream 处理聊天消息（流式）
func (b *chatBiz) handleChatMessageStream(ctx context.Context, userID uint, msg *model.WebSocketMessage, conn *websocket.Conn) (*model.WebSocketMessage, error) {
	// 统一请求结构：使用 question 和 book_ids（与HTTP保持一致）
	question := msg.Question
	if question == "" {
		question = msg.Content // 向后兼容：如果没有 question，使用 content
	}

	if question == "" {
		return nil, fmt.Errorf("question 不能为空")
	}

	bookIDs := msg.BookIDs
	if len(bookIDs) == 0 && msg.BookID != nil {
		// 向后兼容：如果只有 bookID，转换为 book_ids 数组
		bookIDs = []uint{*msg.BookID}
	}

	if len(bookIDs) == 0 {
		return nil, fmt.Errorf("book_ids 不能为空")
	}

	// 创建或获取会话（使用第一个笔记ID作为会话关联）
	// 如果是第一次对话（未提供 session_id），系统会自动创建新会话
	var sessionID uint
	var session *model.ChatSession
	var err error

	if msg.SessionID == 0 {
		// 第一次对话，自动创建新会话
		bookID := bookIDs[0] // 使用第一个笔记ID
		var sessionTitle string
		if len(bookIDs) == 1 {
			sessionTitle = "" // 使用默认标题
		} else {
			sessionTitle = fmt.Sprintf("多笔记对话（%d篇笔记）", len(bookIDs))
		}
		// 使用GetOrCreateSessionByBook确保同一笔记使用同一个会话
		session, err = b.GetOrCreateSessionByBook(ctx, userID, bookID, sessionTitle)
		if err != nil {
			return nil, err
		}
		sessionID = session.ID
		log.C(ctx).Infow("自动创建新会话", "session_id", sessionID, "user_id", userID, "book_id", bookID)

		// 如果是新创建的会话，立即通知客户端（在开始发送消息块之前）
		sessionCreatedMsg := &model.WebSocketMessage{
			Type:      "session_created",
			SessionID: sessionID,
			Data: map[string]interface{}{
				"session_id": sessionID,
				"title":      session.Title,
				"book_id":    session.BookID,
				"book_ids":   bookIDs,
			},
			Timestamp: time.Now(),
		}
		sessionCreatedBytes, _ := json.Marshal(sessionCreatedMsg)
		if err := conn.WriteMessage(websocket.TextMessage, sessionCreatedBytes); err != nil {
			log.C(ctx).Errorw("发送会话创建通知失败", "error", err)
			// 不返回错误，继续处理消息
		} else {
			log.C(ctx).Infow("✅ 已发送session_created消息", "session_id", sessionID)
		}
	} else {
		// 验证现有会话
		session, err = b.GetSession(ctx, msg.SessionID, userID)
		if err != nil {
			return nil, err
		}
		sessionID = session.ID
	}

	// 保存用户消息（如果保存失败，记录错误但继续处理，因为助手消息更重要）
	_, err = b.CreateMessage(ctx, sessionID, userID, question, "user")
	if err != nil {
		log.C(ctx).Errorw("保存用户消息失败", "error", err, "session_id", sessionID)
		// 不返回错误，继续处理，因为助手消息更重要
	}

	// 使用 RagService 生成流式回答（支持多笔记）
	var fullResponse strings.Builder

	// 检查 RagService 是否可用
	if b.ragService == nil {
		log.C(ctx).Errorw("RagService 未初始化，无法生成回答")
		errorMsg := &model.WebSocketMessage{
			Type:      "error",
			SessionID: sessionID, // 确保错误消息也包含session_id
			Error:     "RAG服务未初始化，请稍后重试",
			Timestamp: time.Now(),
		}
		errorBytes, _ := json.Marshal(errorMsg)
		conn.WriteMessage(websocket.TextMessage, errorBytes)
		return nil, fmt.Errorf("RAG服务未初始化")
	}

	// 使用 RagService 的流式方法，支持多笔记
	log.C(ctx).Infow("开始调用RAG流式服务", "session_id", sessionID, "question", question, "book_ids", bookIDs)
	err = b.ragService.ChatWithRAGStream(
		ctx,
		userID,
		question,
		bookIDs, // 直接传递所有笔记ID
		func(chunk string) error {
			// 累积完整回答
			fullResponse.WriteString(chunk)

			// 实时发送chunk给客户端
			chunkMsg := &model.WebSocketMessage{
				Type:      "message_chunk",
				SessionID: sessionID,
				Content:   chunk,
				Role:      "assistant",
				Timestamp: time.Now(),
			}

			chunkBytes, err := json.Marshal(chunkMsg)
			if err != nil {
				log.C(ctx).Errorw("序列化chunk消息失败", "error", err)
				return err
			}

			if err := conn.WriteMessage(websocket.TextMessage, chunkBytes); err != nil {
				log.C(ctx).Errorw("发送chunk消息失败", "error", err)
				return err
			}

			return nil
		},
	)

	if err == nil {
		log.C(ctx).Infow("RAG流式服务调用完成", "session_id", sessionID, "response_length", fullResponse.Len())
	}

	if err != nil {
		log.C(ctx).Errorw("RAG生成回答失败", "error", err)
		errorMsg := &model.WebSocketMessage{
			Type:      "error",
			SessionID: sessionID, // 确保错误消息也包含session_id
			Error:     "生成回答失败，请稍后重试",
			Timestamp: time.Now(),
		}
		errorBytes, _ := json.Marshal(errorMsg)
		conn.WriteMessage(websocket.TextMessage, errorBytes)
		return nil, err
	}

	// 保存完整的助手消息
	assistantContent := fullResponse.String()
	assistantMessage, err := b.CreateMessage(ctx, sessionID, userID, assistantContent, "assistant")
	if err != nil {
		log.C(ctx).Errorw("保存助手消息失败", "error", err)
	}

	// 发送完成消息
	doneMsg := &model.WebSocketMessage{
		Type:      "message_done",
		SessionID: sessionID,
		MessageID: assistantMessage.ID,
		Content:   assistantContent,
		Role:      "assistant",
		Timestamp: time.Now(),
	}

	// 通过WebSocket发送完成消息
	doneBytes, err := json.Marshal(doneMsg)
	if err != nil {
		log.C(ctx).Errorw("序列化完成消息失败", "error", err)
	} else {
		if err := conn.WriteMessage(websocket.TextMessage, doneBytes); err != nil {
			log.C(ctx).Errorw("发送完成消息失败", "error", err)
		}
	}

	// AI对话成功后，增加用户的聊天数量
	if err := b.userBiz.IncrementUserChatNum(ctx, userID); err != nil {
		log.C(ctx).Errorw("Failed to increment user chat num", "userID", userID, "error", err)
	}

	return doneMsg, nil
}

// handleSessionMessage 处理会话相关消息
func (b *chatBiz) handleSessionMessage(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error) {
	// 这里可以处理会话相关的操作，比如获取会话列表等
	return &model.WebSocketMessage{
		Type:      "session",
		Data:      "session operation completed",
		Timestamp: time.Now(),
	}, nil
}

// handleBookSearch 处理书籍搜索消息（简化版本，不再使用关键词匹配）
func (b *chatBiz) handleBookSearch(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error) {
	log.C(ctx).Infow("Handling book search request", "user_id", userID, "content", msg.Content)

	// 获取用户的书籍列表（简化实现，不再使用关键词搜索）
	_, books, err := b.ds.Books().ListAll(ctx, 0, 100) // 获取前100本书
	if err != nil {
		log.C(ctx).Errorw("Failed to find books", "error", err)
		return &model.WebSocketMessage{
			Type:      "error",
			Error:     "Failed to search books",
			Timestamp: time.Now(),
		}, nil
	}

	// 简单的标题匹配（不再使用关键词匹配）
	var searchData []map[string]interface{}
	for _, book := range books {
		if len(searchData) >= 5 {
			break
		}
		// 简单的标题包含匹配
		if strings.Contains(strings.ToLower(book.Title), strings.ToLower(msg.Content)) {
			searchData = append(searchData, map[string]interface{}{
				"id":          book.ID,
				"title":       book.Title,
				"tags":        book.Tags,
				"category_id": book.CategoryID,
				"image_url":   book.ImageUrl,
				"card_count":  book.CardCount,
			})
		}
	}

	return &model.WebSocketMessage{
		Type:      "search_books_result",
		Content:   fmt.Sprintf("找到 %d 本相关书籍", len(searchData)),
		Data:      searchData,
		Timestamp: time.Now(),
	}, nil
}

// GenerateAssistantResponse 生成助手回复（已废弃，保留接口兼容性）
// 现在应该使用 RagService.ChatWithRAG 代替
func (b *chatBiz) GenerateAssistantResponse(ctx context.Context, userMessage string) (string, error) {
	// 返回默认回复（不再使用关键词搜索）
	return "抱歉，此方法已废弃。请使用基于笔记的 RAG 对话功能。", nil
}

// ProcessWebSocketMessageStream 流式处理WebSocket消息
func (b *chatBiz) ProcessWebSocketMessageStream(ctx context.Context, userID uint, msg *model.WebSocketMessage, conn *websocket.Conn) (*model.WebSocketMessage, error) {
	// 处理聊天消息（type为"chat"、"message"或为空）
	if msg.Type == "chat" || msg.Type == "message" || (msg.Type == "" && msg.Question != "") {
		return b.handleChatMessageStream(ctx, userID, msg, conn)
	}
	// 其他类型使用原有逻辑
	return b.ProcessWebSocketMessage(ctx, userID, msg)
}
