// Package chatbot 智能体配置与对话业务逻辑层
package chatbot

import (
	"context"
	"errors"
	"fmt"

	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

// StreamHandler 流式输出回调函数
type StreamHandler func(event string, data interface{}) error

// Embedder 向量化函数签名
type Embedder func(ctx context.Context, text string) ([]float32, error)

// CreateChatbotReq 创建智能体请求
type CreateChatbotReq struct {
	Name             string `json:"name" binding:"required"`
	Description      string `json:"description"`
	Avatar           string `json:"avatar"`
	SystemPrompt     string `json:"system_prompt" binding:"required"`
	KnowledgeBaseIDs []uint `json:"knowledge_base_ids"`
}

// UpdateChatbotReq 更新智能体请求
type UpdateChatbotReq struct {
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	Avatar       *string `json:"avatar"`
	SystemPrompt *string `json:"system_prompt"`
}

// ChatbotDetail 智能体详情（含知识库列表）
type ChatbotDetail struct {
	model.ChatbotConfig
	KnowledgeBases []model.KnowledgeBase `json:"knowledge_bases"`
}

// IChatbotBiz 智能体业务层接口（B端配置 + C端对话）
type IChatbotBiz interface {
	// B端：配置 CRUD
	CreateChatbot(ctx context.Context, userID uint, req *CreateChatbotReq) (*model.ChatbotConfig, error)
	GetChatbot(ctx context.Context, userID uint, id uint) (*ChatbotDetail, error)
	ListChatbots(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotConfig, int64, error)
	UpdateChatbot(ctx context.Context, userID uint, id uint, req *UpdateChatbotReq) error
	DeleteChatbot(ctx context.Context, userID uint, id uint) error
	UpdateStatus(ctx context.Context, userID uint, id uint, status string) error

	// C端：对话会话（Task 7 实现）
	ListVisibleChatbots(ctx context.Context, user *model.User) ([]model.ChatbotConfig, error)
	CreateSession(ctx context.Context, userID uint, chatbotID uint) (*model.ChatbotSession, error)
	ListSessions(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotSession, int64, error)
	DeleteSession(ctx context.Context, userID uint, sessionID uint) error
	ListMessages(ctx context.Context, userID uint, sessionID uint, offset, limit int) ([]model.ChatbotMessage, int64, error)
	ChatStream(ctx context.Context, userID uint, sessionID uint, message string, handler StreamHandler) error
}

type chatbotBiz struct {
	ds          store.IStore
	volcBiz     volc.VolcBiz
	vectorStore port.VectorStore
	embedder    Embedder
}

var _ IChatbotBiz = (*chatbotBiz)(nil)

// NewChatbotBiz 创建智能体业务层实例
func NewChatbotBiz(ds store.IStore, volcBiz volc.VolcBiz, vectorStore port.VectorStore, embedder Embedder) IChatbotBiz {
	return &chatbotBiz{
		ds:          ds,
		volcBiz:     volcBiz,
		vectorStore: vectorStore,
		embedder:    embedder,
	}
}

// ============================================================
// B端：配置 CRUD
// ============================================================

// CreateChatbot 创建智能体配置，可选挂载知识库
func (b *chatbotBiz) CreateChatbot(ctx context.Context, userID uint, req *CreateChatbotReq) (*model.ChatbotConfig, error) {
	config := &model.ChatbotConfig{
		UserID:       userID,
		Name:         req.Name,
		Description:  req.Description,
		Avatar:       req.Avatar,
		SystemPrompt: req.SystemPrompt,
		Status:       model.ChatbotStatusDraft,
	}

	if err := b.ds.ChatbotConfig().Create(ctx, config); err != nil {
		return nil, fmt.Errorf("CreateChatbot: %w", err)
	}

	// 挂载知识库（如有）
	if len(req.KnowledgeBaseIDs) > 0 {
		if err := b.ds.ChatbotConfig().MountKnowledgeBases(ctx, config.ID, req.KnowledgeBaseIDs); err != nil {
			return nil, fmt.Errorf("CreateChatbot: mount KBs: %w", err)
		}
	}

	return config, nil
}

// GetChatbot 获取智能体详情（含挂载的知识库列表），验证所有权
func (b *chatbotBiz) GetChatbot(ctx context.Context, userID uint, id uint) (*ChatbotDetail, error) {
	config, err := b.getAndCheckOwnership(ctx, userID, id)
	if err != nil {
		return nil, err
	}

	kbs, err := b.ds.ChatbotConfig().ListMountedKBs(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("GetChatbot: list mounted KBs: %w", err)
	}

	return &ChatbotDetail{
		ChatbotConfig:  *config,
		KnowledgeBases: kbs,
	}, nil
}

// ListChatbots 获取用户的智能体配置列表（分页）
func (b *chatbotBiz) ListChatbots(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotConfig, int64, error) {
	configs, total, err := b.ds.ChatbotConfig().List(ctx, userID, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("ListChatbots: %w", err)
	}
	return configs, total, nil
}

// UpdateChatbot 更新智能体配置，验证所有权
func (b *chatbotBiz) UpdateChatbot(ctx context.Context, userID uint, id uint, req *UpdateChatbotReq) error {
	config, err := b.getAndCheckOwnership(ctx, userID, id)
	if err != nil {
		return err
	}

	if req.Name != nil {
		config.Name = *req.Name
	}
	if req.Description != nil {
		config.Description = *req.Description
	}
	if req.Avatar != nil {
		config.Avatar = *req.Avatar
	}
	if req.SystemPrompt != nil {
		config.SystemPrompt = *req.SystemPrompt
	}

	if err := b.ds.ChatbotConfig().Update(ctx, config); err != nil {
		return fmt.Errorf("UpdateChatbot: %w", err)
	}
	return nil
}

// DeleteChatbot 删除智能体配置（软删除），验证所有权
func (b *chatbotBiz) DeleteChatbot(ctx context.Context, userID uint, id uint) error {
	_, err := b.getAndCheckOwnership(ctx, userID, id)
	if err != nil {
		return err
	}

	if err := b.ds.ChatbotConfig().Delete(ctx, id); err != nil {
		return fmt.Errorf("DeleteChatbot: %w", err)
	}
	return nil
}

// UpdateStatus 更新智能体状态，验证所有权和合法状态转换
func (b *chatbotBiz) UpdateStatus(ctx context.Context, userID uint, id uint, status string) error {
	config, err := b.getAndCheckOwnership(ctx, userID, id)
	if err != nil {
		return err
	}

	if !isValidStatusTransition(config.Status, status) {
		return errno.ErrInvalidParameter.SetMessage("不允许从 %s 转换到 %s", config.Status, status)
	}

	if err := b.ds.ChatbotConfig().UpdateStatus(ctx, id, status); err != nil {
		return fmt.Errorf("UpdateStatus: %w", err)
	}
	return nil
}

// ============================================================
// C端：对话会话
// ============================================================

// ListVisibleChatbots 获取用户可见的智能体列表（C端）
// 主用户（ParentUserID == nil）: 返回自己所有智能体（全状态）
// 子用户（ParentUserID != nil）: 仅返回父用户已发布的智能体
func (b *chatbotBiz) ListVisibleChatbots(ctx context.Context, user *model.User) ([]model.ChatbotConfig, error) {
	if user.ParentUserID != nil {
		// 子用户：返回父用户的已发布智能体
		configs, err := b.ds.ChatbotConfig().ListPublishedByOwner(ctx, *user.ParentUserID)
		if err != nil {
			return nil, fmt.Errorf("ListVisibleChatbots: %w", err)
		}
		return configs, nil
	}
	// 主用户：返回自己的全部智能体（不分页，C 端展示用）
	configs, _, err := b.ds.ChatbotConfig().List(ctx, user.ID, 0, 100)
	if err != nil {
		return nil, fmt.Errorf("ListVisibleChatbots: %w", err)
	}
	return configs, nil
}

// CreateSession 创建对话会话
func (b *chatbotBiz) CreateSession(ctx context.Context, userID uint, chatbotID uint) (*model.ChatbotSession, error) {
	// 验证智能体存在且可访问
	config, err := b.ds.ChatbotConfig().Get(ctx, chatbotID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrChatbotNotFound
		}
		return nil, fmt.Errorf("CreateSession: %w", err)
	}

	// 检查访问权限：已发布 或 草稿+本人
	if config.Status != model.ChatbotStatusPublished {
		if config.UserID != userID {
			return nil, errno.ErrChatbotNotPublished
		}
	}

	session := &model.ChatbotSession{
		UserID:    userID,
		ChatbotID: chatbotID,
		Title:     config.Name,
		Status:    "active",
	}
	if err := b.ds.ChatbotSession().CreateSession(ctx, session); err != nil {
		return nil, fmt.Errorf("CreateSession: %w", err)
	}
	return session, nil
}

// ListSessions 获取用户的对话会话列表
func (b *chatbotBiz) ListSessions(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotSession, int64, error) {
	sessions, total, err := b.ds.ChatbotSession().ListSessions(ctx, userID, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("ListSessions: %w", err)
	}
	return sessions, total, nil
}

// DeleteSession 删除对话会话
func (b *chatbotBiz) DeleteSession(ctx context.Context, userID uint, sessionID uint) error {
	session, err := b.ds.ChatbotSession().GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrSessionNotFound
		}
		return fmt.Errorf("DeleteSession: %w", err)
	}
	if session.UserID != userID {
		return errno.ErrForbidden
	}

	// 先硬删除消息，再软删除会话
	if err := b.ds.ChatbotSession().DeleteMessagesBySession(ctx, sessionID); err != nil {
		return fmt.Errorf("DeleteSession: delete messages: %w", err)
	}
	if err := b.ds.ChatbotSession().DeleteSession(ctx, sessionID); err != nil {
		return fmt.Errorf("DeleteSession: %w", err)
	}
	return nil
}

// ListMessages 获取会话消息列表
func (b *chatbotBiz) ListMessages(ctx context.Context, userID uint, sessionID uint, offset, limit int) ([]model.ChatbotMessage, int64, error) {
	// 验证会话所有权
	session, err := b.ds.ChatbotSession().GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, 0, errno.ErrSessionNotFound
		}
		return nil, 0, fmt.Errorf("ListMessages: %w", err)
	}
	if session.UserID != userID {
		return nil, 0, errno.ErrForbidden
	}

	messages, total, err := b.ds.ChatbotSession().ListMessages(ctx, sessionID, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("ListMessages: %w", err)
	}
	return messages, total, nil
}

// ============================================================
// 内部辅助方法
// ============================================================

// getAndCheckOwnership 获取智能体配置并验证所有权
func (b *chatbotBiz) getAndCheckOwnership(ctx context.Context, userID uint, id uint) (*model.ChatbotConfig, error) {
	config, err := b.ds.ChatbotConfig().Get(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrChatbotNotFound
		}
		return nil, fmt.Errorf("getAndCheckOwnership: %w", err)
	}
	if config.UserID != userID {
		return nil, errno.ErrForbidden
	}
	return config, nil
}

// isValidStatusTransition 检查状态转换是否合法
// 合法转换：draft→published, published→offline, offline→published
func isValidStatusTransition(from, to string) bool {
	switch from {
	case model.ChatbotStatusDraft:
		return to == model.ChatbotStatusPublished
	case model.ChatbotStatusPublished:
		return to == model.ChatbotStatusOffline
	case model.ChatbotStatusOffline:
		return to == model.ChatbotStatusPublished
	default:
		return false
	}
}
