// Package chatbot 智能体配置与对话业务逻辑层
package chatbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
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
	SystemPrompt     string `json:"system_prompt" binding:"required"`
	KnowledgeBaseIDs []uint `json:"knowledge_base_ids"`
	GreetingEnabled  bool   `json:"greeting_enabled"`
	GreetingMessage  string `json:"greeting_message"`
}

// UpdateChatbotReq 更新智能体请求
type UpdateChatbotReq struct {
	Name             *string `json:"name"`
	Description      *string `json:"description"`
	SystemPrompt     *string `json:"system_prompt"`
	KnowledgeBaseIDs *[]uint `json:"knowledge_base_ids"`
	GreetingEnabled  *bool   `json:"greeting_enabled"`
	GreetingMessage  *string `json:"greeting_message"`
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
	ListVisibleChatbotsWithPermission(ctx context.Context, user *model.User) ([]ChatbotVisibleItem, error)
	CheckChatbotPermission(ctx context.Context, userID uint, chatbotID uint) (bool, error)
	CreateSession(ctx context.Context, userID uint, chatbotID uint) (*model.ChatbotSession, error)
	ListSessions(ctx context.Context, userID uint, offset, limit int) ([]model.ChatbotSession, int64, error)
	DeleteSession(ctx context.Context, userID uint, sessionID uint) error
	ListMessages(ctx context.Context, userID uint, sessionID uint, offset, limit int) ([]model.ChatbotMessage, int64, error)
	ChatStream(ctx context.Context, userID uint, sessionID uint, message string, modelKey string, thinking bool, handler StreamHandler) error

	// C端：会话管理（Task 3 — rename-pin feature）
	RenameSession(ctx context.Context, userID, sessionID uint, title string) error
	PinSession(ctx context.Context, userID, sessionID uint, pinned bool) (*time.Time, error)
	ListSessionsByChatbot(ctx context.Context, userID, chatbotID uint, offset, limit int) ([]model.ChatbotSession, int64, error)
}

type chatbotBiz struct {
	ds          store.IStore
	vectorStore port.VectorStore
	embedder    Embedder
}

var _ IChatbotBiz = (*chatbotBiz)(nil)

// NewChatbotBiz 创建智能体业务层实例
// llmRouter 参数已废弃（Task 9 迁移至 AI Gateway），保留签名兼容调用方，传 nil 即可。
func NewChatbotBiz(ds store.IStore, vectorStore port.VectorStore, embedder Embedder) IChatbotBiz {
	return &chatbotBiz{
		ds:          ds,
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
		UserID:          userID,
		Name:            req.Name,
		Description:     req.Description,
		SystemPrompt:    req.SystemPrompt,
		Status:          model.ChatbotStatusDraft,
		GreetingEnabled: req.GreetingEnabled,
		GreetingMessage: req.GreetingMessage,
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
	if req.SystemPrompt != nil {
		config.SystemPrompt = *req.SystemPrompt
	}
	if req.GreetingEnabled != nil {
		config.GreetingEnabled = *req.GreetingEnabled
	}
	if req.GreetingMessage != nil {
		config.GreetingMessage = *req.GreetingMessage
	}

	if err := b.ds.ChatbotConfig().Update(ctx, config); err != nil {
		return fmt.Errorf("UpdateChatbot: %w", err)
	}

	// 更新知识库挂载（先清除再重建）
	if req.KnowledgeBaseIDs != nil {
		if err := b.ds.ChatbotConfig().ReplaceKnowledgeBases(ctx, id, *req.KnowledgeBaseIDs); err != nil {
			return fmt.Errorf("UpdateChatbot: replace KBs: %w", err)
		}
	}

	return nil
}

// DeleteChatbot 删除智能体配置（软删除），同事务清理可见范围授权.
//
// EC-6 (sop-chatbot-visibility-scope spec §9): 实体删除时清理它的所有 visibility grant
// 记录, 避免 grant 表残留指向不存在 chatbot 的孤儿数据. 软删 grant 保留审计.
//
// 与 SOP 对称 (sop.go DeleteTemplate, Task 14): 同事务模式.
func (b *chatbotBiz) DeleteChatbot(ctx context.Context, userID uint, id uint) error {
	_, err := b.getAndCheckOwnership(ctx, userID, id)
	if err != nil {
		return err
	}

	return b.ds.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// EC-6: 软删该 chatbot 的所有 visibility grant
		if err := b.ds.ChatbotVisibilityGrant().CleanupByEntity(ctx, tx, id); err != nil {
			return fmt.Errorf("DeleteChatbot: cleanup visibility grants: %w", err)
		}
		// 既有: 软删 chatbot_config (GORM 默认软删, gorm.DeletedAt)
		if err := tx.Delete(&model.ChatbotConfig{}, id).Error; err != nil {
			return fmt.Errorf("DeleteChatbot: delete chatbot_config: %w", err)
		}
		return nil
	})
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
// 主用户（ParentUserID == nil）: 仅返回自己已发布的智能体
// 子用户（ParentUserID != nil）: 仅返回父用户已发布的智能体（不按白名单隐藏）
// 注：工作区只展示已发布的配置；草稿/下线状态的智能体需从配置中心管理入口访问。
// 与 SOP /v1/sop/templates 对称：列表全量可见，点击时再走 /v1/chatbots/:id/check-permission
// 与 CreateSession/ChatStream 的运行时白名单守卫共同构成纵深防御。
func (b *chatbotBiz) ListVisibleChatbots(ctx context.Context, user *model.User) ([]model.ChatbotConfig, error) {
	ownerID := user.ID
	if user.ParentUserID != nil {
		ownerID = *user.ParentUserID
	}
	configs, err := b.ds.ChatbotConfig().ListPublishedByOwner(ctx, ownerID)
	if err != nil {
		return nil, fmt.Errorf("ListVisibleChatbots: %w", err)
	}
	return configs, nil
}

// ChatbotVisibleItem 包装 ChatbotConfig 并附带当前用户对该 chatbot 的运行权限。
// 用于首页 UI 显示锁标志（has_permission=false 时加锁徽章但卡片仍正常渲染）。
// 安全 gate 仍在 CheckChatbotPermission + CreateSession/ChatStream 层强制执行。
type ChatbotVisibleItem struct {
	model.ChatbotConfig
	HasPermission bool `json:"has_permission"`
}

// ListVisibleChatbotsWithPermission 返回可见智能体列表 + 每项的运行权限标志。
//
// 两层 gate 串行 (sop-chatbot-visibility-scope spec §4.2.2):
//   - Layer 1: visibility 过滤 (本 feature 新增) — 物理移除受限不可见的 chatbot, 子用户看不到入口
//   - Layer 2: run-permission 标志 (既有) — 列出剩余 chatbot 但用 HasPermission 标记是否可运行
//
// 父账号 bypass: 不应用 visibility 过滤, 全部 HasPermission=true.
// 子账号 visibility_restricted=false 短路放行 (不查 grant 表).
// 与对称的 ListVisibleTemplatesWithPermission (Task 11) 语义一致.
func (b *chatbotBiz) ListVisibleChatbotsWithPermission(ctx context.Context, user *model.User) ([]ChatbotVisibleItem, error) {
	configs, err := b.ListVisibleChatbots(ctx, user)
	if err != nil {
		return nil, err
	}

	// 父账号：全部 true，不查白名单, 也不应用 visibility
	if user.ParentUserID == nil {
		items := make([]ChatbotVisibleItem, 0, len(configs))
		for _, c := range configs {
			items = append(items, ChatbotVisibleItem{ChatbotConfig: c, HasPermission: true})
		}
		return items, nil
	}

	// Layer 1: visibility 过滤 (visibility_restricted=true 且不在 vis 白名单则物理移除)
	visibilitySet, err := b.ds.ChatbotVisibilityGrant().ListVisibleChatbotIDsBySubUser(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("ListVisibleChatbotsWithPermission visibility: %w", err)
	}
	filtered := make([]model.ChatbotConfig, 0, len(configs))
	for _, c := range configs {
		if c.VisibilityRestricted {
			if _, ok := visibilitySet[c.ID]; !ok {
				continue // 短路移除: 受限且不在 visibility 白名单
			}
		}
		filtered = append(filtered, c)
	}

	// Layer 2: run-permission 标志 (一次查询白名单, 本地匹配)
	ids, err := b.ds.Customers().ListSubUserChatbotIDs(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("ListVisibleChatbotsWithPermission whitelist: %w", err)
	}
	allowed := make(map[uint]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	items := make([]ChatbotVisibleItem, 0, len(filtered))
	for _, c := range filtered {
		_, ok := allowed[c.ID]
		items = append(items, ChatbotVisibleItem{ChatbotConfig: c, HasPermission: ok})
	}
	return items, nil
}

// CheckChatbotPermission 检查用户是否有权运行指定 chatbot。
// 用于前端在跳转 chatbot 聊天页前做权限预检，mirror SOP CheckTemplatePermission。
// 父账号 bypass（永远返回 true）；子账号走 user_chatbot_permission 白名单。
// chatbot 必须是 published 状态；草稿/下线均返回 false。
func (b *chatbotBiz) CheckChatbotPermission(ctx context.Context, userID uint, chatbotID uint) (bool, error) {
	config, err := b.ds.ChatbotConfig().Get(ctx, chatbotID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("CheckChatbotPermission: get chatbot %d: %w", chatbotID, err)
	}
	if config.Status != model.ChatbotStatusPublished {
		return false, nil
	}
	ok, err := b.ds.Customers().HasChatbotPermission(ctx, userID, chatbotID)
	if err != nil {
		return false, fmt.Errorf("CheckChatbotPermission: %w", err)
	}
	return ok, nil
}

// CreateSession 创建对话会话
// 权限模型：父账号永远放行；子账号必须命中 user_chatbot_permission 白名单（default-deny）。
// 未授权 → ErrChatbotRunDenied（403）。
func (b *chatbotBiz) CreateSession(ctx context.Context, userID uint, chatbotID uint) (*model.ChatbotSession, error) {
	// 运行时权限守卫 —— 必须在任何 DB 写操作前执行（child-run-permission §3.5）。
	ok, err := b.ds.Customers().HasChatbotPermission(ctx, userID, chatbotID)
	if err != nil {
		return nil, fmt.Errorf("CreateSession permission: %w", err)
	}
	if !ok {
		return nil, errno.ErrChatbotRunDenied
	}

	// 验证智能体存在且可访问
	config, err := b.ds.ChatbotConfig().Get(ctx, chatbotID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrChatbotNotFound
		}
		return nil, fmt.Errorf("CreateSession: %w", err)
	}

	// 访问权限：只有已发布状态才允许创建会话；草稿/下线的智能体一律禁止运行
	// （即便是创建者本人也不能运行自己的草稿，需先在配置中心发布）
	if config.Status != model.ChatbotStatusPublished {
		return nil, errno.ErrChatbotNotPublished
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

	// 若开启打招呼，写入一条 assistant 首条消息（seq=1）。
	// 写失败不影响 session 创建，仅告警；后续 ChatStream 从 DB 读历史时会自然带上此消息。
	if config.GreetingEnabled && strings.TrimSpace(config.GreetingMessage) != "" {
		greetingMsg := &model.ChatbotMessage{
			SessionID: session.ID,
			UserID:    userID,
			Role:      "assistant",
			Content:   config.GreetingMessage,
			Seq:       1,
			CreatedAt: time.Now(),
		}
		if err := b.ds.ChatbotSession().CreateMessage(ctx, greetingMsg); err != nil {
			log.C(ctx).Warnw("CreateSession: save greeting message failed",
				"session_id", session.ID, "chatbot_id", chatbotID, "error", err)
		} else {
			_ = b.ds.ChatbotSession().IncrementMessageCount(ctx, session.ID)
		}
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
// C端：会话管理（rename-pin feature）
// ============================================================

// RenameSession 重命名会话标题，验证所有权和标题合法性。
func (b *chatbotBiz) RenameSession(ctx context.Context, userID, sessionID uint, title string) error {
	title = strings.TrimSpace(title)
	if len(title) == 0 {
		return errno.ErrBind.SetMessage("标题不能为空")
	}
	// 用 rune 计数与 DB 字段 VARCHAR(200) 的字符语义一致 — 否则中文输入会在 ~66 字时
	// 提前触发上限（200 字节 / 3 字节每字 = 66 中文字）。Spec §1.1 model size:200
	// 在 utf8mb4 下是 200 字符（≈600 bytes 最坏）。
	if utf8.RuneCountInString(title) > 200 {
		return errno.ErrBind.SetMessage("标题最长 200 字符")
	}

	session, err := b.ds.ChatbotSession().GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrSessionNotFound
		}
		return fmt.Errorf("RenameSession: %w", err)
	}
	if session.UserID != userID {
		return errno.ErrForbidden
	}

	if err := b.ds.ChatbotSession().UpdateTitle(ctx, sessionID, title); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errno.ErrSessionNotFound
		}
		return fmt.Errorf("RenameSession: %w", err)
	}
	return nil
}

// PinSession 置顶或取消置顶会话，验证所有权。
// 返回写入的 pinnedAt 指针（pinned=true 时为当前时间，pinned=false 时为 nil）。
// 重复置顶（EC-14）会刷新 pinned_at 为最新 NOW()。
func (b *chatbotBiz) PinSession(ctx context.Context, userID, sessionID uint, pinned bool) (*time.Time, error) {
	session, err := b.ds.ChatbotSession().GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSessionNotFound
		}
		return nil, fmt.Errorf("PinSession: %w", err)
	}
	if session.UserID != userID {
		return nil, errno.ErrForbidden
	}

	var newPinnedAt *time.Time
	if pinned {
		now := time.Now()
		newPinnedAt = &now
	}

	if err := b.ds.ChatbotSession().SetPinnedAt(ctx, sessionID, newPinnedAt); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errno.ErrSessionNotFound
		}
		return nil, fmt.Errorf("PinSession: %w", err)
	}
	return newPinnedAt, nil
}

// ListSessionsByChatbot 获取用户在指定智能体下的会话列表（分页）。
// 排序：置顶优先，置顶组内按 pinned_at DESC，未置顶组按 updated_at DESC。
func (b *chatbotBiz) ListSessionsByChatbot(ctx context.Context, userID, chatbotID uint, offset, limit int) ([]model.ChatbotSession, int64, error) {
	sessions, total, err := b.ds.ChatbotSession().ListSessionsByChatbot(ctx, userID, chatbotID, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("ListSessionsByChatbot: %w", err)
	}
	return sessions, total, nil
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
// 2 态合法转换：draft↔published 双向
func isValidStatusTransition(from, to string) bool {
	switch from {
	case model.ChatbotStatusDraft:
		return to == model.ChatbotStatusPublished
	case model.ChatbotStatusPublished:
		return to == model.ChatbotStatusDraft
	default:
		return false
	}
}
