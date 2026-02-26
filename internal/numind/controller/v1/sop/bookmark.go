package sop

import (
	"context"
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
	v1 "numind-server/pkg/api/numind/v1"
)

// SaveBookmarkRequest 保存书签请求
type SaveBookmarkRequest struct {
	RunID        uint   `json:"run_id" binding:"required"`  // SOP Run ID
	NodeID       uint   `json:"node_id" binding:"required"` // Node ID
	BookmarkName string `json:"bookmark_name"`
	Description  string `json:"description"`
}

// SaveBookmarkResponse 保存书签响应
type SaveBookmarkResponse struct {
	ID               uint   `json:"id"`
	UserID           uint   `json:"user_id"`
	TemplateID       uint   `json:"template_id"`
	NodeID           uint   `json:"node_id"`
	NodeSort         int    `json:"node_sort"`
	NodeName         string `json:"node_name,omitempty"`
	Input            string `json:"input"`
	Output           string `json:"output"`
	Thinking         string `json:"thinking"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	BookmarkName     string `json:"bookmark_name"`
	Description      string `json:"description"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// BookmarkListItem 书签列表项
type BookmarkListItem struct {
	ID            uint   `json:"id"`
	NodeID        uint   `json:"node_id"`
	NodeSort      int    `json:"node_sort"`
	NodeName      string `json:"node_name"`
	BookmarkName  string `json:"bookmark_name"`
	OutputPreview string `json:"output_preview"` // 前200字
	HasThinking   bool   `json:"has_thinking"`
	TotalTokens   int    `json:"total_tokens"`
	CreatedAt     string `json:"created_at"`
}

// ApplyBookmarkRequest 应用书签请求
type ApplyBookmarkRequest struct {
	BookmarkID *uint `json:"bookmark_id"` // 可选，如果不提供则自动查找该节点的书签
}

// ApplyBookmarkResponse 应用书签响应
type ApplyBookmarkResponse struct {
	NodeRunID    uint   `json:"node_run_id"`
	FromBookmark bool   `json:"from_bookmark"`
	BookmarkID   uint   `json:"bookmark_id"`
	Output       string `json:"output"`
	Thinking     string `json:"thinking"`
}

// SaveBookmark 保存节点为书签
// POST /v1/sop/bookmarks
func (ctrl *SopController) SaveBookmark(c *gin.Context) {
	log.C(c).Infow("Save bookmark called")

	// 获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 解析请求
	var req SaveBookmarkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误"), nil)
		return
	}

	// 调用业务逻辑（传入 runID 和 nodeID，由业务层查询 nodeRunID）
	bookmark, err := ctrl.sopBiz.SaveNodeBookmarkByRunAndNode(c.Request.Context(), user.ID, req.RunID, req.NodeID, req.BookmarkName, req.Description)
	if err != nil {
		log.C(c).Errorw("Failed to save bookmark", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("%s", err.Error()), nil)
		return
	}

	// 构建响应
	resp := SaveBookmarkResponse{
		ID:               bookmark.ID,
		UserID:           bookmark.UserID,
		TemplateID:       bookmark.TemplateID,
		NodeID:           bookmark.NodeID,
		NodeSort:         bookmark.NodeSort,
		Input:            bookmark.Input,
		Output:           bookmark.Output,
		Thinking:         bookmark.Thinking,
		PromptTokens:     bookmark.PromptTokens,
		CompletionTokens: bookmark.CompletionTokens,
		TotalTokens:      bookmark.TotalTokens,
		BookmarkName:     bookmark.BookmarkName,
		Description:      bookmark.Description,
		CreatedAt:        bookmark.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:        bookmark.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}

	// 如果有Node信息，添加节点名称
	if bookmark.Node != nil {
		resp.NodeName = bookmark.Node.Name
	}

	core.WriteResponse(c, nil, resp)
}

// GetBookmark 获取书签详情
// GET /v1/sop/bookmarks/:id
func (ctrl *SopController) GetBookmark(c *gin.Context) {
	log.C(c).Infow("Get bookmark called")

	// 获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 解析书签ID
	bookmarkID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的书签ID"), nil)
		return
	}

	// 调用业务逻辑
	bookmark, err := ctrl.sopBiz.GetBookmark(c.Request.Context(), uint(bookmarkID), user.ID)
	if err != nil {
		log.C(c).Errorw("Failed to get bookmark", "error", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	// 构建响应
	resp := SaveBookmarkResponse{
		ID:               bookmark.ID,
		UserID:           bookmark.UserID,
		TemplateID:       bookmark.TemplateID,
		NodeID:           bookmark.NodeID,
		NodeSort:         bookmark.NodeSort,
		Input:            bookmark.Input,
		Output:           bookmark.Output,
		Thinking:         bookmark.Thinking,
		PromptTokens:     bookmark.PromptTokens,
		CompletionTokens: bookmark.CompletionTokens,
		TotalTokens:      bookmark.TotalTokens,
		BookmarkName:     bookmark.BookmarkName,
		Description:      bookmark.Description,
		CreatedAt:        bookmark.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:        bookmark.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}

	// 如果有Node信息，添加节点名称
	if bookmark.Node != nil {
		resp.NodeName = bookmark.Node.Name
	}

	core.WriteResponse(c, nil, resp)
}

// ListBookmarksByTemplate 获取模板的所有书签
// GET /v1/sop/templates/:id/bookmarks
func (ctrl *SopController) ListBookmarksByTemplate(c *gin.Context) {
	log.C(c).Infow("List bookmarks called")

	// 获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 解析模板ID
	templateID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的模板ID"), nil)
		return
	}

	// 调用业务逻辑
	bookmarks, err := ctrl.sopBiz.ListBookmarksByTemplate(c.Request.Context(), user.ID, uint(templateID))
	if err != nil {
		log.C(c).Errorw("Failed to list bookmarks", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("%s", err.Error()), nil)
		return
	}

	// 构建响应
	items := make([]BookmarkListItem, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		// 截取输出前200字作为预览
		outputPreview := bookmark.Output
		if len(outputPreview) > 200 {
			// 处理多字节字符，避免截断乱码
			runes := []rune(outputPreview)
			if len(runes) > 200 {
				outputPreview = string(runes[:200]) + "..."
			}
		}

		item := BookmarkListItem{
			ID:            bookmark.ID,
			NodeID:        bookmark.NodeID,
			NodeSort:      bookmark.NodeSort,
			BookmarkName:  bookmark.BookmarkName,
			OutputPreview: outputPreview,
			HasThinking:   bookmark.Thinking != "",
			TotalTokens:   bookmark.TotalTokens,
			CreatedAt:     bookmark.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		}

		// 如果有Node信息，添加节点名称
		if bookmark.Node != nil {
			item.NodeName = bookmark.Node.Name
		}

		items = append(items, item)
	}

	core.WriteResponse(c, nil, gin.H{
		"bookmarks": items,
	})
}

// DeleteBookmark 删除书签
// DELETE /v1/sop/bookmarks/:id
func (ctrl *SopController) DeleteBookmark(c *gin.Context) {
	log.C(c).Infow("Delete bookmark called")

	// 获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 解析书签ID
	bookmarkID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的书签ID"), nil)
		return
	}

	// 调用业务逻辑
	if err := ctrl.sopBiz.DeleteBookmark(c.Request.Context(), uint(bookmarkID), user.ID); err != nil {
		log.C(c).Errorw("Failed to delete bookmark", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"message": "删除成功",
	})
}

// CreateRunWithBookmarks 创建Run并支持自动应用书签
// 这个方法会替代原有的CreateRun方法调用
func (ctrl *SopController) CreateRunWithBookmarks(c *gin.Context, req v1.CreateSopRunRequest, user *model.User) (*model.SopRun, []uint, error) {
	// 调用业务逻辑创建Run并自动应用书签
	run, appliedBookmarkIDs, err := ctrl.sopBiz.CreateRunWithBookmarks(c.Request.Context(), req.TemplateID, user.ID, req.Text, req.AutoApplyBookmarks)
	if err != nil {
		return nil, nil, err
	}

	return run, appliedBookmarkIDs, nil
}

// ApplyBookmark 应用书签到当前Run
// POST /v1/sop/runs/:run_id/nodes/:node_id/apply-bookmark
func (ctrl *SopController) ApplyBookmark(c *gin.Context) {
	log.C(c).Infow("Apply bookmark called")

	// 获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 解析RunID和NodeID
	runID, err := strconv.ParseUint(c.Param("run_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的运行ID"), nil)
		return
	}

	nodeID, err := strconv.ParseUint(c.Param("node_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的节点ID"), nil)
		return
	}

	// 解析请求（可选的书签ID）
	var req ApplyBookmarkRequest
	_ = c.ShouldBindJSON(&req) // 忽略错误，因为整个body是可选的

	// 调用业务逻辑
	nodeRun, err := ctrl.sopBiz.ApplyBookmarkToNode(c.Request.Context(), user.ID, uint(runID), uint(nodeID), req.BookmarkID)
	if err != nil {
		log.C(c).Errorw("Failed to apply bookmark", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("%s", err.Error()), nil)
		return
	}

	// 构建响应
	resp := ApplyBookmarkResponse{
		NodeRunID:    nodeRun.ID,
		FromBookmark: nodeRun.FromBookmark,
		Output:       nodeRun.Output,
		Thinking:     nodeRun.Thinking,
	}

	if nodeRun.BookmarkID != nil {
		resp.BookmarkID = *nodeRun.BookmarkID
	}

	core.WriteResponse(c, nil, resp)
}

// DeleteDraftRun 删除草稿状态的 run（用户离开页面时调用）
// DELETE /v1/sop/runs/:id/draft
// POST /v1/sop/runs/:id/draft (Beacon API使用POST，query参数传token)
func (ctrl *SopController) DeleteDraftRun(c *gin.Context) {
	log.C(c).Infow("Delete draft run called")

	var user *model.User

	// 1. 尝试从 context 获取用户（标准方式：使用Authorization header）
	currentUser, exists := c.Get("current_user")
	if exists {
		user = currentUser.(*model.User)
	} else {
		// 2. 尝试从 query 参数获取 token（Beacon方式：token在query中）
		token := c.Query("token")
		if token == "" {
			core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到认证信息"), nil)
			return
		}

		// 验证 token 并获取用户
		var err error
		user, err = validateTokenFromQuery(c.Request.Context(), token)
		if err != nil {
			log.C(c).Errorw("Token validation failed", "error", err)
			core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("Token验证失败: %s", err.Error()), nil)
			return
		}

		log.C(c).Infow("Beacon API token validated successfully", "user_id", user.ID)
	}

	// 解析 run ID
	runID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的运行ID"), nil)
		return
	}

	// 调用业务逻辑
	if err := ctrl.sopBiz.DeleteDraftRun(c.Request.Context(), uint(runID), user.ID); err != nil {
		log.C(c).Errorw("Failed to delete draft run", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("%s", err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"message": "草稿已删除",
	})
}

// validateTokenFromQuery 从 query 参数验证 JWT token 并返回用户信息
// 用于 Beacon API 调用（无法设置 Authorization header）
func validateTokenFromQuery(ctx context.Context, tokenString string) (*model.User, error) {
	// 检查token是否在黑名单中
	blacklist := middleware.GetTokenBlacklist()
	if blacklist.IsTokenBlacklisted(tokenString) {
		return nil, fmt.Errorf("token已失效")
	}

	// 解析 JWT token
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(viper.GetString("jwt.secret")), nil
	})

	if err != nil {
		return nil, fmt.Errorf("token解析失败: %w", err)
	}

	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		// 再次检查黑名单（防止在解析过程中token被加入黑名单）
		if blacklist.IsTokenBlacklisted(tokenString) {
			return nil, fmt.Errorf("token已失效")
		}

		// 安全地获取 user_id
		userIDValue, exists := claims["user_id"]
		if !exists {
			return nil, fmt.Errorf("user_id not found in token")
		}

		var userID uint
		switch v := userIDValue.(type) {
		case float64:
			userID = uint(v)
		case int:
			userID = uint(v)
		case uint:
			userID = v
		case int64:
			userID = uint(v)
		default:
			return nil, fmt.Errorf("invalid user_id type in token: %T", v)
		}

		// 安全地获取 openid
		openIDValue, exists := claims["openid"]
		if !exists {
			return nil, fmt.Errorf("openid not found in token")
		}

		openID, ok := openIDValue.(string)
		if !ok {
			return nil, fmt.Errorf("invalid openid type in token: %T", openIDValue)
		}

		// 从数据库验证用户是否存在，并验证openid是否匹配
		if store.S == nil {
			log.C(ctx).Warnw("store.S未初始化，跳过数据库验证")
			// 如果store未初始化，返回简化的用户对象（向后兼容）
			user := &model.User{}
			user.ID = userID
			user.OpenID = openID
			return user, nil
		}

		// 从数据库查询用户
		user, err := store.S.Users().GetUserByID(ctx, userID)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, fmt.Errorf("用户不存在")
			}
			log.C(ctx).Errorw("查询用户失败", "user_id", userID, "error", err)
			return nil, fmt.Errorf("查询用户失败: %w", err)
		}

		// 验证openid是否匹配（安全措施）
		if user.OpenID != openID {
			log.C(ctx).Warnw("token中的openid与数据库不匹配", "user_id", userID, "token_openid", openID, "db_openid", user.OpenID)
			return nil, fmt.Errorf("token验证失败")
		}

		return user, nil
	}

	return nil, fmt.Errorf("invalid token")
}
