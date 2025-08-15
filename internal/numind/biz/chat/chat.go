package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/numind/biz/book"
	"numind-server/internal/numind/biz/user"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// ChatBiz 定义了对话相关的业务逻辑接口
type ChatBiz interface {
	CreateSession(ctx context.Context, userID uint, title string) (*model.ChatSession, error)
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

	// WebSocket相关方法
	ProcessWebSocketMessage(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error)
	GenerateAssistantResponse(ctx context.Context, userMessage string) (string, error)
}

// chatBiz 是 ChatBiz 的具体实现
type chatBiz struct {
	ds            store.IStore
	userBiz       user.UserBiz
	searchService *book.SearchService
}

// New 创建一个新的 ChatBiz 实例
func New(ds store.IStore, userBiz user.UserBiz) ChatBiz {
	return &chatBiz{
		ds:            ds,
		userBiz:       userBiz,
		searchService: book.NewSearchService(),
	}
}

// CreateSession 创建新的对话会话
func (b *chatBiz) CreateSession(ctx context.Context, userID uint, title string) (*model.ChatSession, error) {
	session := &model.ChatSession{
		UserID: userID,
		Title:  title,
		Status: "active",
	}

	if err := b.ds.Chats().CreateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	return session, nil
}

// GetSession 获取对话会话
func (b *chatBiz) GetSession(ctx context.Context, sessionID uint, userID uint) (*model.ChatSession, error) {
	session, err := b.ds.Chats().GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	// 验证用户权限
	if session.UserID != userID {
		return nil, fmt.Errorf("unauthorized access to session")
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
		return nil, fmt.Errorf("failed to create message: %w", err)
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

// ProcessWebSocketMessage 处理WebSocket消息
func (b *chatBiz) ProcessWebSocketMessage(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error) {
	switch msg.Type {
	case "message":
		return b.handleChatMessage(ctx, userID, msg)
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

// handleChatMessage 处理聊天消息
func (b *chatBiz) handleChatMessage(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error) {
	// 创建或获取会话
	var sessionID uint
	if msg.SessionID == 0 {
		// 创建新会话
		session, err := b.CreateSession(ctx, userID, "新对话")
		if err != nil {
			return nil, err
		}
		sessionID = session.ID
	} else {
		// 验证现有会话
		session, err := b.GetSession(ctx, msg.SessionID, userID)
		if err != nil {
			return nil, err
		}
		sessionID = session.ID
	}

	// 保存用户消息
	_, err := b.CreateMessage(ctx, sessionID, userID, msg.Content, "user")
	if err != nil {
		return nil, err
	}

	// 生成助手回复
	assistantContent, err := b.GenerateAssistantResponse(ctx, msg.Content)
	if err != nil {
		return nil, err
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
		Type:      "message",
		SessionID: sessionID,
		MessageID: assistantMessage.ID,
		Content:   assistantContent,
		Role:      "assistant",
		Timestamp: time.Now(),
	}, nil
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

// handleBookSearch 处理书籍搜索消息
func (b *chatBiz) handleBookSearch(ctx context.Context, userID uint, msg *model.WebSocketMessage) (*model.WebSocketMessage, error) {
	log.C(ctx).Infow("Handling book search request", "user_id", userID, "content", msg.Content)

	// 假设我们有一个函数来获取所有书籍
	// 这里需要根据实际的数据库查询来实现
	books, err := b.findAllBooks(ctx)
	if err != nil {
		log.C(ctx).Errorw("Failed to find books", "error", err)
		return &model.WebSocketMessage{
			Type:      "error",
			Error:     "Failed to search books",
			Timestamp: time.Now(),
		}, nil
	}

	// 使用搜索服务进行关键词匹配
	searchResults := b.searchService.SearchBooks(ctx, msg.Content, books, 5)

	// 构建搜索结果响应
	var searchData []map[string]interface{}
	for _, book := range searchResults {
		searchData = append(searchData, map[string]interface{}{
			"id":          book.ID,
			"title":       book.Title,
			"tags":        book.Tags,
			"keywords":    book.Keywords,  // 添加关键词信息
			"category_id": book.CategoryID,
			"image_url":   book.ImageUrl,
			"card_count":  book.CardCount,
		})
	}

	return &model.WebSocketMessage{
		Type:      "search_books_result",
		Content:   fmt.Sprintf("找到 %d 本相关书籍", len(searchResults)),
		Data:      searchData,
		Timestamp: time.Now(),
	}, nil
}

// findAllBooks 获取所有书籍（这里需要根据实际的数据库查询来实现）
func (b *chatBiz) findAllBooks(ctx context.Context) ([]*model.BookM, error) {
	// 使用store层的新方法获取所有书籍
	// 在实际生产环境中，可能需要实现分页查询或者专门的搜索接口

	// 获取所有状态不为failed的书籍作为搜索范围
	// 这里可以根据实际需求调整，比如搜索所有公开的书籍
	_, books, err := b.ds.Books().ListAll(ctx, 0, 1000) // 获取前1000本书
	if err != nil {
		log.C(ctx).Errorw("Failed to get books from database", "error", err)
		return nil, err
	}

	// 自动为所有书籍生成关键词（如果还没有的话）
	b.searchService.BatchUpdateKeywords(books)

	log.C(ctx).Infow("Retrieved books for search", "count", len(books))
	return books, nil
}

// GenerateAssistantResponse 生成助手回复
func (b *chatBiz) GenerateAssistantResponse(ctx context.Context, userMessage string) (string, error) {
	// 智能分析用户消息，判断是否需要搜索卡册
	if b.shouldSearchBooks(userMessage) {
		// 进行卡册搜索
		searchResults, err := b.performBookSearch(ctx, userMessage)
		if err != nil {
			log.C(ctx).Errorw("Failed to perform book search", "error", err, "query", userMessage)
			// 搜索失败时返回友好提示
			return "抱歉，我在搜索卡册时遇到了一些问题。请稍后再试，或者直接告诉我您想要什么类型的卡册。", nil
		}

		// 根据搜索结果生成回复
		return b.generateSearchResponse(userMessage, searchResults), nil
	}

	// 如果不是搜索相关的消息，返回默认回复
	return b.generateDefaultResponse(userMessage), nil
}

// shouldSearchBooks 判断用户消息是否需要搜索卡册
func (b *chatBiz) shouldSearchBooks(userMessage string) bool {
	// 定义搜索相关的关键词
	searchKeywords := []string{
		"搜索", "查找", "找", "推荐", "建议", "有什么", "哪些", "卡册", "相册", "照片", "图片",
		"旅行", "美食", "摄影", "艺术", "设计", "技术", "学习", "工作", "生活", "回忆",
		"关于", "相关", "类似", "这种", "那种", "想要", "需要", "喜欢", "感兴趣",
	}

	// 检查用户消息是否包含搜索关键词
	for _, keyword := range searchKeywords {
		if strings.Contains(userMessage, keyword) {
			return true
		}
	}

	// 检查消息长度，较长的消息更可能是搜索请求
	if len(userMessage) > 10 {
		return true
	}

	return false
}

// performBookSearch 执行卡册搜索
func (b *chatBiz) performBookSearch(ctx context.Context, userMessage string) ([]*model.BookM, error) {
	// 获取所有书籍
	books, err := b.findAllBooks(ctx)
	if err != nil {
		return nil, err
	}

	// 使用搜索服务进行关键词匹配
	searchResults := b.searchService.SearchBooks(ctx, userMessage, books, 5)

	return searchResults, nil
}

// generateSearchResponse 根据搜索结果生成回复
func (b *chatBiz) generateSearchResponse(userMessage string, searchResults []*model.BookM) string {
	if len(searchResults) == 0 {
		return fmt.Sprintf("抱歉，我没有找到与\"%s\"相关的卡册。您可以尝试使用其他关键词，或者告诉我您具体想要什么类型的卡册。", userMessage)
	}

	// 生成个性化回复
	var response strings.Builder
	response.WriteString(fmt.Sprintf("根据您的查询\"%s\"，我为您找到了 %d 本相关卡册：\n\n", userMessage, len(searchResults)))

	for i, book := range searchResults {
		response.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, book.Title))

		// 添加标签信息
		if book.Tags != "" {
			response.WriteString(fmt.Sprintf("   标签: %s\n", book.Tags))
		}

		// 添加关键词信息
		if len(book.Keywords) > 0 {
			response.WriteString(fmt.Sprintf("   关键词: %s\n", strings.Join(book.Keywords, ", ")))
		}

		// 添加卡片数量信息
		if book.CardCount > 0 {
			response.WriteString(fmt.Sprintf("   包含 %d 张卡片\n", book.CardCount))
		}

		// 添加分类信息
		if book.CategoryName != "" {
			response.WriteString(fmt.Sprintf("   分类: %s\n", book.CategoryName))
		}

		response.WriteString("\n")
	}

	// 添加建议
	response.WriteString("💡 **小贴士**: 您可以点击任意卡册查看详情，或者告诉我您想要什么特定类型的卡册，我可以为您提供更精准的推荐。")

	return response.String()
}

// generateDefaultResponse 生成默认回复
func (b *chatBiz) generateDefaultResponse(userMessage string) string {
	// 根据消息内容生成智能回复
	if strings.Contains(userMessage, "你好") || strings.Contains(userMessage, "hello") || strings.Contains(userMessage, "hi") {
		return "你好！我是您的智能卡册助手。我可以帮您搜索和推荐各种类型的卡册，包括旅行照片、美食记录、艺术创作等。请告诉我您想要什么类型的卡册，或者有什么其他问题需要帮助。"
	}

	if strings.Contains(userMessage, "谢谢") || strings.Contains(userMessage, "感谢") {
		return "不客气！很高兴能帮到您。如果您还需要其他帮助，比如搜索特定类型的卡册、了解卡册功能等，随时告诉我。"
	}

	if strings.Contains(userMessage, "帮助") || strings.Contains(userMessage, "怎么用") || strings.Contains(userMessage, "功能") {
		return "我可以帮您：\n\n" +
			"🔍 **搜索卡册**: 告诉我您想要什么类型的卡册，比如\"旅行照片\"、\"美食记录\"等\n" +
			"📚 **推荐卡册**: 根据您的兴趣推荐相关卡册\n" +
			"💡 **使用建议**: 提供卡册使用和创作的建议\n\n" +
			"试试告诉我您想要什么类型的卡册吧！"
	}

	// 通用回复
	return fmt.Sprintf("我收到了您的消息：%s\n\n我可以帮您搜索和推荐各种类型的卡册。请告诉我您想要什么类型的卡册，比如旅行照片、美食记录、艺术创作等，我会为您找到最相关的内容。", userMessage)
}
