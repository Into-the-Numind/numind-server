package salesrag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/salesrag"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/util"

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
	// 设置文件大小限制：100MB
	const maxFileSize = 100 * 1024 * 1024 // 100MB

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件上传失败: %s", err.Error()), nil)
		return
	}
	defer file.Close()

	// 检查文件大小
	if header.Size > maxFileSize {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件大小 %.2fMB 超过100MB限制", float64(header.Size)/1024/1024), nil)
		return
	}

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

	docID, err := ctrl.b.SalesRAG().Ingest(c, user.ID, header.Filename, displayName, file, opts)
	if err != nil {
		log.Errorw("Ingest failed", "error", err, "user_id", user.ID, "filename", displayName)
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
		Query        string   `json:"query" binding:"required"`
		Images       []string `json:"images"` // 图片链接列表
		DocumentIDs  []uint   `json:"document_ids"`
		DeepThinking bool     `json:"deep_thinking"`
		ChatMode     string   `json:"chat_mode"` // "sales" (销售话术) 或 "free" (自由讨论)
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
	err = ctrl.b.SalesRAG().ChatWithSession(newCtx, user.ID, uint(sessionID), r.Query, r.Images, r.DocumentIDs, r.DeepThinking, r.ChatMode,
		func(eventType string, data interface{}) error {
			var eventData []byte
			var marshalErr error

			switch eventType {
			case "status":
				eventData, marshalErr = json.Marshal(map[string]interface{}{
					"type": "status",
					"data": data,
				})
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

	limitStr := c.DefaultQuery("limit", "10000")
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

	var req struct {
		Profile string `json:"profile"`
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

	if err := ctrl.b.SalesRAG().UpdateCustomerProfile(c, user.ID, uint(sessionID), req.Profile); err != nil {
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

	profile, err := ctrl.b.SalesRAG().GetCustomerProfile(c, user.ID, uint(sessionID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{"profile": profile})
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

// AnalyzeProfile 解析上传的文档生成客户档案 (支持 SSE 流式)
func (ctrl *SalesRAGController) AnalyzeProfile(c *gin.Context) {
	log.Infow("[AnalyzeProfile] Received request")
	
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		log.Errorw("[AnalyzeProfile] FormFile error", "error", err)
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}
	defer file.Close()

	user := middleware.GetCurrentUser(c)
	if user == nil {
		log.Errorw("[AnalyzeProfile] No user found")
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	log.Infow("[AnalyzeProfile] User uploading file", "user_id", user.ID, "filename", header.Filename, "size", header.Size)

	// 设置 SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	w := c.Writer

	// 发送初始状态
	statusData, _ := json.Marshal(map[string]interface{}{
		"type": "status",
		"data": "正在分析图片内容...",
	})
	fmt.Fprintf(w, "data: %s\n\n", statusData)
	w.Flush()
	log.Infow("[AnalyzeProfile] Sent initial status")

	log.Infow("[AnalyzeProfile] Calling AnalyzeDocumentStream...")
	profile, err := ctrl.b.SalesRAG().AnalyzeDocumentStream(c, user.ID, file, header.Filename, func(token string) error {
		log.Infow("[AnalyzeProfile] Received token", "token_preview", token[:min(len(token), 30)])
		eventData, _ := json.Marshal(map[string]interface{}{
			"type": "token",
			"data": token,
		})
		_, err := fmt.Fprintf(w, "data: %s\n\n", eventData)
		if err == nil {
			w.Flush()
		}
		return err
	})

	if err != nil {
		log.Errorw("[AnalyzeProfile] AnalyzeDocumentStream error", "error", err)
		errData, _ := json.Marshal(map[string]interface{}{
			"type": "error",
			"data": err.Error(),
		})
		fmt.Fprintf(w, "data: %s\n\n", errData)
		w.Flush()
		return
	}

	log.Infow("[AnalyzeProfile] AnalyzeDocumentStream completed", "profile_length", len(profile))

	// 发送完成并附带完整结果
	doneData, _ := json.Marshal(map[string]interface{}{
		"type":    "done",
		"profile": profile,
	})
	fmt.Fprintf(w, "data: %s\n\n", doneData)
	w.Flush()
	log.Infow("[AnalyzeProfile] Sent done event")
}

// AnalyzeChatStyle 分析聊天风格（语言指纹分析）
// 支持上传文件或直接传入文本
// AnalyzeChatStyle 分析聊天风格（语言指纹分析）
// 支持上传文件或直接传入文本
func (ctrl *SalesRAGController) AnalyzeChatStyle(c *gin.Context) {
	var reader io.Reader
	var filename string

	// 尝试获取上传的文件
	file, header, err := c.Request.FormFile("file")
	if err == nil && file != nil {
		defer file.Close()
		reader = file
		filename = header.Filename
	} else {
		// 没有文件，尝试获取 text 字段
		text := c.PostForm("text")
		if text == "" {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("请提供聊天文本或上传文件"), nil)
			return
		}
		reader = strings.NewReader(text)
		filename = "input_text.txt"
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// 调用业务层分析 (业务层会处理解析和截断)
	result, err := ctrl.b.SalesRAG().AnalyzeChatStyle(c, user.ID, reader, filename)
	if err != nil {
		log.Errorw("[AnalyzeChatStyle] Error", "userID", user.ID, "error", err.Error())
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{
		"analysis": result,
	})
}

// GetLanguageStyle 获取用户的语言风格
func (ctrl *SalesRAGController) GetLanguageStyle(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	style, err := ctrl.b.SalesRAG().GetLanguageStyle(c, user.ID)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{
		"style": style,
	})
}

// OCR 识别图片中的文本 (调用阿里云百炼视觉大模型)
func (ctrl *SalesRAGController) OCR(c *gin.Context) {
	// 1. 获取上传的文件
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("未找到上传的图片文件"), nil)
		return
	}
	defer file.Close()

	// 获取当前用户
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// 2. 读取并上传到 COS
	data, err := io.ReadAll(file)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("读取文件数据失败"), nil)
		return
	}

	// 生成 object key: sales_chat/{userID}/{sessionID}/{timestamp}_{filename}
	sessionID := c.DefaultPostForm("session_id", "no_session")
	objectKey := fmt.Sprintf("sales_chat/%d/%s/%d_%s", user.ID, sessionID, time.Now().Unix(), header.Filename)

	// 上传到 COS
	cosURL, err := util.UploadBytesToCOS(c.Request.Context(), objectKey, header.Header.Get("Content-Type"), data)
	if err != nil {
		log.Errorw("Upload image to COS failed", "error", err, "user_id", user.ID, "key", objectKey)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("图片存储失败"), nil)
		return
	}

	// 3. 调用阿里云百炼视觉模型进行 OCR 识别
	// 使用 qwen3-vl-flash-2026-01-22 模型
	prompt := `你是一个专业的微信聊天记录识别专家。请识别这张微信聊天截图中的对话内容。

  ## 识别要求

  ### 1. 气泡布局识别
  - **左边气泡（白色/灰色）= 客户消息**
  - **右边气泡（绿色）= 销售消息**
  - 从上到下完整扫描所有对话气泡

  ### 2. 内容提取规则
  - **保留表情符号**：如 [微笑]、[呲牙]、🌹 等
  - **只提取文字气泡**：忽略图片、语音、视频等多媒体消息
  - **保持原文**：不要修改、总结或解释对话内容

  ### 3. 输出格式（严格遵守）

  **如果截图包含多轮对话**（2条及以上消息），按以下格式输出：

  【对话历史】
  客户：[第1条客户消息]
  销售：[第1条销售消息]
  客户：[第2条客户消息]
  ...（所有历史对话）

  【客户最新消息】
  客户：[最后一条客户消息]

  **如果截图只有单条消息**，直接输出：

  客户：[消息内容]

  ### 4. 特殊处理规则

  - **最新消息必须是客户发的**：如果截图最后一条是销售发的，往前找到最近的客户消息作为"最新消息"
  - **空消息处理**：如果气泡只有表情没有文字，保留表情符号
  - **时间戳忽略**：不要提取对话中的时间信息

  ## 输出示例

  ### 示例1：多轮对话
  【对话历史】
  客户：你们这个产品怎么样？
  销售：我们的产品在行业内评价很高，已经服务了1000+客户
  客户：价格呢？
  销售：我们有三种套餐，基础版998元...

  【客户最新消息】
  客户：太贵了，能便宜点吗？

  ### 示例2：单条消息
  客户：在吗？

  ### 示例3：包含表情
  【对话历史】
  客户：你好[微笑]
  销售：您好，很高兴为您服务

  【客户最新消息】
  客户：我想了解一下产品

  ## 关键约束

  1. **严格使用【对话历史】和【客户最新消息】标记**，不要使用其他标记
  2. **每条消息前必须有"客户："或"销售："前缀**
  3. **不要添加任何分析、解释或总结**，只输出识别的对话内容
  4. **保持对话的原始顺序和完整性**

  现在请识别这张截图。`
	model := "qwen3-vl-flash-2026-01-22"

	// 生成 10 分钟有效的签名 URL 供阿里云 API 访问
	signedURL, err := util.GenerateSignedURL(c.Request.Context(), objectKey, 600)
	if err != nil {
		log.Warnw("Generate signed URL failed, use raw cosURL", "error", err, "key", objectKey)
		signedURL = cosURL
	}

	ocrText, err := ctrl.b.Ali().QianwenVision(c.Request.Context(), signedURL, prompt, model)
	if err != nil {
		log.Errorw("Alibaba Cloud Vision OCR failed", "error", err, "user_id", user.ID, "url", signedURL)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("图片识别失败，请检查模型配置"), nil)
		return
	}

	// 4. 返回结果
	core.WriteResponse(c, nil, map[string]string{
		"text": ocrText,
		"url":  cosURL,
	})
}
