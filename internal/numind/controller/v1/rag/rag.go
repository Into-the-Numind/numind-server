package rag

import (
	"numind-server/internal/numind/biz/chat"
	"numind-server/internal/numind/biz/rag"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
)

// RagController RAG控制器
type RagController struct {
	ragService *rag.RagService
	chatBiz    chat.ChatBiz
}

// NewRagController 创建新的RAG控制器
func NewRagController(ragService *rag.RagService, chatBiz chat.ChatBiz) *RagController {
	return &RagController{
		ragService: ragService,
		chatBiz:    chatBiz,
	}
}

// ChatWithRAGRequest 基于笔记对话请求
type ChatWithRAGRequest struct {
	Question string `json:"question" binding:"required"`
	BookIDs  []uint `json:"book_ids" binding:"required,min=1"` // 必填的笔记ID数组，用于基于多个笔记进行聊天
}

// ChatWithRAGResponse 基于笔记对话响应
type ChatWithRAGResponse struct {
	Answer string `json:"answer"`
}

// ChatWithRAG 基于笔记进行RAG对话
func (ctrl *RagController) ChatWithRAG(c *gin.Context) {
	// 从认证中间件中获取用户信息
	currentUser := middleware.GetCurrentUser(c)
	if currentUser == nil {
		core.WriteResponse(c, errno.ErrUnauthorized, nil)
		return
	}

	var req ChatWithRAGRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.C(c).Errorw("绑定请求参数失败", "error", err)
		core.WriteResponse(c, errno.ErrBind, nil)
		return
	}

	// 验证 book_ids 数组不为空
	if len(req.BookIDs) == 0 {
		core.WriteResponse(c, errno.ErrBind.SetMessage("book_ids 不能为空"), nil)
		return
	}

	// 调用 RagService 进行RAG对话
	// 注意：此接口用于自测，不保存聊天记录，不创建session
	answer, err := ctrl.ragService.ChatWithRAG(c.Request.Context(), currentUser.ID, req.Question, req.BookIDs)
	if err != nil {
		log.C(c).Errorw("RAG对话失败", "error", err, "user_id", currentUser.ID, "book_ids", req.BookIDs)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("RAG对话失败: %s", err.Error()), nil)
		return
	}

	// HTTP接口用于自测，不保存聊天记录，不创建session
	core.WriteResponse(c, nil, ChatWithRAGResponse{
		Answer: answer,
	})
}
