package salesrag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
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
	"github.com/spf13/viper"
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
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(fmt.Sprintf("文件上传失败: %s", err.Error())), nil)
		return
	}
	defer file.Close()

	// 检查文件大小
	if header.Size > maxFileSize {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(fmt.Sprintf("文件大小 %.2fMB 超过100MB限制", float64(header.Size)/1024/1024)), nil)
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

// OCR 识别图片中的文本 (转发给 Python OCR 微服务)
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

	// 2. 读取并在上传到 COS 的同时准备转发
	data, err := io.ReadAll(file)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("读取文件数据失败"), nil)
		return
	}

	// 生成 object key: {env}/sales_chat/{userID}/{sessionID}/{timestamp}_{filename}
	env := viper.GetString("runmode")
	if env == "" {
		env = "unknown"
	}
	// 可选的 session_id，如果前端没传则使用 no_session 目录
	sessionID := c.DefaultPostForm("session_id", "no_session")
	objectKey := fmt.Sprintf("%s/sales_chat/%d/%s/%d_%s", env, user.ID, sessionID, time.Now().Unix(), header.Filename)

	// 上传到 COS
	cosURL, err := util.UploadBytesToCOS(c.Request.Context(), objectKey, header.Header.Get("Content-Type"), data)
	if err != nil {
		log.Errorw("Upload image to COS failed", "error", err, "user_id", user.ID, "key", objectKey)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("图片存储失败"), nil)
		return
	}

	// 3. 构建转发请求到 Python OCR 服务 (9093 端口)
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", header.Filename)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("构建请求失败"), nil)
		return
	}
	_, err = io.Copy(part, bytes.NewReader(data))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("复制文件数据失败"), nil)
		return
	}
	writer.Close()

	ocrURL := "http://localhost:9093/ocr"
	req, err := http.NewRequest("POST", ocrURL, body)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("创建转发请求失败"), nil)
		return
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("调用 OCR 服务超时或失败"), nil)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("OCR 服务返回错误"), nil)
		return
	}

	// 4. 解析结果
	var result struct {
		Success bool   `json:"success"`
		Text    string `json:"text"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("解析 OCR 结果失败"), nil)
		return
	}

	if !result.Success {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("OCR 识别失败: "+result.Error), nil)
		return
	}

	core.WriteResponse(c, nil, map[string]string{
		"text": result.Text,
		"url":  cosURL,
	})
}
