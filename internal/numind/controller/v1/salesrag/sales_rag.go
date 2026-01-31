package salesrag

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/salesrag"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/middleware"

	"github.com/gin-gonic/gin"
)

type SalesRAGController struct {
	b biz.IBiz
}

func NewSalesRAGController(b biz.IBiz) *SalesRAGController {
	return &SalesRAGController{b: b}
}

// Ingest 处理知识库文档上传
func (ctrl *SalesRAGController) Ingest(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}
	defer file.Close()

	// Parse additional fields
	name := c.DefaultPostForm("name", "")
	description := c.DefaultPostForm("description", "")
	tagsStr := c.DefaultPostForm("tags", "")

	var tags []string
	if tagsStr != "" {
		if strings.HasPrefix(tagsStr, "[") {
			_ = json.Unmarshal([]byte(tagsStr), &tags)
		} else {
			tags = strings.Split(tagsStr, ",")
		}
	}

	opts := salesrag.IngestOptions{
		Description: description,
		Tags:        tags,
	}

	// 获取当前用户
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// 使用用户输入的名称，如果未提供则回退到文件名
	displayName := name
	if displayName == "" {
		displayName = header.Filename
	}

	docID, err := ctrl.b.SalesRAG().Ingest(c, user.ID, displayName, file, opts)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]uint{"document_id": docID})
}

// ChatWithSession 基于会话的销售智能体聊天（SSE 流式输出，保存聊天记录）
func (ctrl *SalesRAGController) ChatWithSession(c *gin.Context) {
	// 获取会话ID
	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	var r struct {
		Query        string `json:"query" binding:"required"`
		DocumentIDs  []uint `json:"document_ids"`
		DeepThinking bool   `json:"deep_thinking"`
		ChatMode     string `json:"chat_mode"` // "sales" (销售话术) 或 "free" (自由讨论)
	}

	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	// 设置默认模式
	if r.ChatMode == "" {
		r.ChatMode = "sales"
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// 注入 userID 到请求 context 中，供业务层使用
	newCtx := context.WithValue(c.Request.Context(), "userID", user.ID)

	// 设置 SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // 禁用 nginx 缓冲

	// 获取 Writer 用于 flush
	w := c.Writer

	// 调用基于会话的流式检索方法（会自动保存消息）
	err = ctrl.b.SalesRAG().ChatWithSession(newCtx, user.ID, uint(sessionID), r.Query, r.DocumentIDs, r.DeepThinking, r.ChatMode,
		func(eventType string, data interface{}) error {
			var eventData []byte
			var marshalErr error

			switch eventType {
			case "verdict":
				eventData, marshalErr = json.Marshal(map[string]interface{}{
					"type": "verdict",
					"data": data,
				})
			case "thinking":
				eventData, marshalErr = json.Marshal(map[string]interface{}{
					"type": "thinking",
					"data": data,
				})
			case "token":
				eventData, marshalErr = json.Marshal(map[string]interface{}{
					"type": "token",
					"data": data,
				})
			case "error":
				eventData, marshalErr = json.Marshal(map[string]interface{}{
					"type": "error",
					"data": data,
				})
			case "done":
				eventData, marshalErr = json.Marshal(map[string]interface{}{
					"type": "done",
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

// ListDocuments 获取文档列表
func (ctrl *SalesRAGController) ListDocuments(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	docs, err := ctrl.b.SalesRAG().ListDocuments(c, user.ID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, docs)
}

// GetDocument 获取单个文档详情
func (ctrl *SalesRAGController) GetDocument(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	doc, err := ctrl.b.SalesRAG().GetDocument(c, user.ID, uint(id))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, doc)
}

// DeleteDocument 删除文档
func (ctrl *SalesRAGController) DeleteDocument(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	if err := ctrl.b.SalesRAG().DeleteDocument(c, user.ID, uint(id)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}

// UpdateDocument 更新文档
func (ctrl *SalesRAGController) UpdateDocument(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	var req salesrag.UpdateDocumentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}
	if err := ctrl.b.SalesRAG().UpdateDocument(c, user.ID, uint(id), req); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}

// ListChunks 获取文档切片列表
func (ctrl *SalesRAGController) ListChunks(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	limitStr := c.DefaultQuery("limit", "1000")
	limit, _ := strconv.Atoi(limitStr)

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	chunks, err := ctrl.b.SalesRAG().ListDocumentChunks(c, user.ID, uint(id), limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, chunks)
}

// ============ 会话管理 API ============

// CreateSession 创建销售会话
func (ctrl *SalesRAGController) CreateSession(c *gin.Context) {
	var req struct {
		Title           string `json:"title"`
		DocumentIDs     []uint `json:"document_ids"`
		DeepThinking    bool   `json:"deep_thinking"`
		CustomerProfile string `json:"customer_profile"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// 如果未提供标题，生成默认标题
	if req.Title == "" {
		req.Title = "新对话"
	}

	createReq := salesrag.CreateSessionRequest{
		Title:           req.Title,
		DocumentIDs:     req.DocumentIDs,
		DeepThinking:    req.DeepThinking,
		CustomerProfile: req.CustomerProfile,
	}

	session, err := ctrl.b.SalesRAG().CreateSession(c, user.ID, createReq)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, session)
}

// ListSessions 获取会话列表
func (ctrl *SalesRAGController) ListSessions(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// 解析分页参数
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "20")
	salesStage := c.DefaultQuery("sales_stage", "")

	offset, _ := strconv.Atoi(offsetStr)
	limit, _ := strconv.Atoi(limitStr)

	sessions, total, err := ctrl.b.SalesRAG().ListSessions(c, user.ID, offset, limit, salesStage)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]interface{}{
		"total":    total,
		"sessions": sessions,
	})
}

// GetSession 获取会话详情
func (ctrl *SalesRAGController) GetSession(c *gin.Context) {
	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	session, err := ctrl.b.SalesRAG().GetSession(c, user.ID, uint(sessionID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, session)
}

// UpdateSession 更新会话信息
func (ctrl *SalesRAGController) UpdateSession(c *gin.Context) {
	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	var req salesrag.UpdateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	if err := ctrl.b.SalesRAG().UpdateSession(c, user.ID, uint(sessionID), req); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{"message": "Session updated successfully"})
}

// DeleteSession 删除会话
func (ctrl *SalesRAGController) DeleteSession(c *gin.Context) {
	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	if err := ctrl.b.SalesRAG().DeleteSession(c, user.ID, uint(sessionID)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{"message": "Session deleted successfully"})
}

// ListMessages 获取会话的消息列表
func (ctrl *SalesRAGController) ListMessages(c *gin.Context) {
	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// 解析分页参数
	offsetStr := c.DefaultQuery("offset", "0")
	limitStr := c.DefaultQuery("limit", "50")
	offset, _ := strconv.Atoi(offsetStr)
	limit, _ := strconv.Atoi(limitStr)

	messages, total, err := ctrl.b.SalesRAG().ListMessages(c, user.ID, uint(sessionID), offset, limit)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]interface{}{
		"total":    total,
		"messages": messages,
	})
}

// UpdateCustomerProfile 更新客户档案
func (ctrl *SalesRAGController) UpdateCustomerProfile(c *gin.Context) {
	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	var req map[string]interface{}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// 序列化为JSON字符串
	profileJSON, err := json.Marshal(req)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	if err := ctrl.b.SalesRAG().UpdateCustomerProfile(c, user.ID, uint(sessionID), string(profileJSON)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{"message": "Customer profile updated successfully"})
}

// GetCustomerProfile 获取客户档案
func (ctrl *SalesRAGController) GetCustomerProfile(c *gin.Context) {
	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	profileJSON, err := ctrl.b.SalesRAG().GetCustomerProfile(c, user.ID, uint(sessionID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	// 解析JSON字符串为对象
	var profile map[string]interface{}
	if profileJSON != "" {
		if err := json.Unmarshal([]byte(profileJSON), &profile); err != nil {
			// 如果解析失败，返回空对象
			profile = make(map[string]interface{})
		}
	} else {
		profile = make(map[string]interface{})
	}

	core.WriteResponse(c, nil, profile)
}

// PinSession 置顶会话
func (ctrl *SalesRAGController) PinSession(c *gin.Context) {
	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	if err := ctrl.b.SalesRAG().PinSession(c, user.ID, uint(sessionID)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{"message": "Session pinned successfully"})
}

// UnpinSession 取消置顶会话
func (ctrl *SalesRAGController) UnpinSession(c *gin.Context) {
	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	if err := ctrl.b.SalesRAG().UnpinSession(c, user.ID, uint(sessionID)); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{"message": "Session unpinned successfully"})
}

// RenameSession 重命名会话
func (ctrl *SalesRAGController) RenameSession(c *gin.Context) {
	sessionIDStr := c.Param("id")
	sessionID, err := strconv.ParseUint(sessionIDStr, 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	var req struct {
		Title string `json:"title" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	if err := ctrl.b.SalesRAG().RenameSession(c, user.ID, uint(sessionID), req.Title); err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{"message": "Session renamed successfully"})
}

// AnalyzeProfile 解析上传的文档生成客户档案
func (ctrl *SalesRAGController) AnalyzeProfile(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}
	defer file.Close()

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	profile, err := ctrl.b.SalesRAG().AnalyzeDocument(c, user.ID, file, header.Filename)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{"profile": profile})
}
