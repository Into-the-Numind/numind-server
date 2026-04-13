package salesrag

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"numind-server/internal/numind/biz"
	"numind-server/internal/numind/biz/credit"
	"numind-server/internal/numind/biz/salesrag"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"

	"github.com/gin-gonic/gin"
)

type SalesRAGController struct {
	b         biz.IBiz
	creditBiz credit.ICreditBiz
}

// NewSalesRAGController 创建 SalesRAG 控制器
func NewSalesRAGController(b biz.IBiz, creditBiz credit.ICreditBiz) *SalesRAGController {
	return &SalesRAGController{b: b, creditBiz: creditBiz}
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

	// 积分预检：检查用户是否有足够积分执行文件解析
	if canPerform, reason := ctrl.creditBiz.CanPerformAIOperation(c, user, "file_parse"); !canPerform {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("%s", reason), nil)
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

	// 积分扣减（旧会员跳过）
	deductCredits(c, ctrl.creditBiz, user, "file_parse", "salesrag_ingest", fmt.Sprintf("%d", docID))

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
		Query         string   `json:"query"`     // 用户文字（可为空，仅图片时）
		OcrTexts      []string `json:"ocr_texts"` // OCR识别文字，仅用于知识库检索
		Images        []string `json:"images"`    // 图片链接列表
		DocumentIDs   []uint   `json:"document_ids"`
		ProductDocIDs []uint   `json:"product_doc_ids"` // 产品文档
		CaseDocIDs    []uint   `json:"case_doc_ids"`    // 成功案例
		FAQDocIDs     []uint   `json:"faq_doc_ids"`     // 百问百答
		DeepThinking  bool     `json:"deep_thinking"`
		ChatMode      string   `json:"chat_mode"` // "sales" (销售话术) 或 "free" (自由讨论)
	}

	if err := c.ShouldBindJSON(&r); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	// 至少需要文字或图片之一
	if strings.TrimSpace(r.Query) == "" && len(r.Images) == 0 {
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

	// 积分预检：检查用户是否有足够积分执行聊天
	if canPerform, reason := ctrl.creditBiz.CanPerformAIOperation(c, user, "salesrag_chat"); !canPerform {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("%s", reason), nil)
		return
	}

	// 注入 userID 到请求 context 中，供业务层使用
	newCtx := middleware.NewContextWithUserID(c.Request.Context(), user.ID)

	// 设置 SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // 禁用 nginx 缓冲

	// 获取 Writer 用于 flush
	w := c.Writer

	// 调用基于会话的流式检索方法（会自动保存消息）
	err = ctrl.b.SalesRAG().ChatWithSession(newCtx, user.ID, uint(sessionID), r.Query, r.OcrTexts, r.Images, r.DocumentIDs, r.DeepThinking, r.ChatMode,
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
	} else {
		// 积分扣减（旧会员跳过）— 仅在聊天成功完成后扣减
		deductCredits(c, ctrl.creditBiz, user, "salesrag_chat", "salesrag_chat", fmt.Sprintf("%d", sessionID))
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
		ProductDocIDs   []uint `json:"product_doc_ids"`   // 产品文档
		CaseDocIDs      []uint `json:"case_doc_ids"`      // 成功案例
		FAQDocIDs       []uint `json:"faq_doc_ids"`       // 百问百答
		OpinionDocIDs   []uint `json:"opinion_doc_ids"`   // 观点库（用户上传）
		OpinionTrackIDs []uint `json:"opinion_track_ids"` // 观点库（系统赛道）
		DeepThinking    bool   `json:"deep_thinking"`
		CustomerProfile string `json:"customer_profile"`
		SalesStage      string `json:"sales_stage"` // 销售阶段
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
		ProductDocIDs:   req.ProductDocIDs,
		CaseDocIDs:      req.CaseDocIDs,
		FAQDocIDs:       req.FAQDocIDs,
		OpinionDocIDs:   req.OpinionDocIDs,
		OpinionTrackIDs: req.OpinionTrackIDs,
		DeepThinking:    req.DeepThinking,
		CustomerProfile: req.CustomerProfile,
		SalesStage:      req.SalesStage,
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

// ListOpinionTracks 获取系统内置观点赛道列表
func (ctrl *SalesRAGController) ListOpinionTracks(c *gin.Context) {
	tracks, err := ctrl.b.SalesRAG().ListOpinionTracks(c)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, tracks)
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
		log.Errorw("[UpdateCustomerProfile] Failed to bind JSON", "error", err)
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	log.Infow("[UpdateCustomerProfile] Updating profile",
		"user_id", user.ID,
		"session_id", sessionID,
		"profile_length", len(req.Profile),
		"profile_preview", req.Profile[:min(len(req.Profile), 100)])

	if err := ctrl.b.SalesRAG().UpdateCustomerProfile(c, user.ID, uint(sessionID), req.Profile); err != nil {
		log.Errorw("[UpdateCustomerProfile] Failed to update", "error", err)
		core.WriteResponse(c, err, nil)
		return
	}

	log.Infow("[UpdateCustomerProfile] Profile updated successfully", "user_id", user.ID, "session_id", sessionID)
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

// AnalyzeProfile 解析上传的文档生成客户档案 (支持 SSE 流式，支持多文件)
func (ctrl *SalesRAGController) AnalyzeProfile(c *gin.Context) {
	log.Infow("[AnalyzeProfile] Received request")

	// 解析 multipart form
	if err := c.Request.ParseMultipartForm(100 << 20); err != nil { // 100 MB max
		log.Errorw("[AnalyzeProfile] ParseMultipartForm error", "error", err)
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	form := c.Request.MultipartForm
	files := form.File["file"] // 前端 input name="file" multiple

	if len(files) == 0 {
		// 尝试获取 "files" 字段
		files = form.File["files"]
	}

	if len(files) == 0 {
		log.Errorw("[AnalyzeProfile] No files found")
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("请上传至少一个文件"), nil)
		return
	}

	if len(files) > 5 {
		log.Errorw("[AnalyzeProfile] Too many files", "count", len(files))
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("最多只能上传 5 个文件"), nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		log.Errorw("[AnalyzeProfile] No user found")
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// 积分预检：检查用户是否有足够积分执行客户档案分析
	if canPerform, reason := ctrl.creditBiz.CanPerformAIOperation(c, user, "profile_analysis"); !canPerform {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("%s", reason), nil)
		return
	}

	log.Infow("[AnalyzeProfile] User uploading files", "user_id", user.ID, "count", len(files))

	// 设置 SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	w := c.Writer

	// 发送初始状态
	statusData, _ := json.Marshal(map[string]interface{}{
		"type": "status",
		"data": fmt.Sprintf("正在综合分析 %d 份资料...", len(files)),
	})
	fmt.Fprintf(w, "data: %s\n\n", statusData)
	w.Flush()
	log.Infow("[AnalyzeProfile] Sent initial status")

	log.Infow("[AnalyzeProfile] Calling AnalyzeProfileMultiFiles...")
	profile, err := ctrl.b.SalesRAG().AnalyzeProfileMultiFiles(c, user.ID, files, func(token string) error {
		// log.Infow("[AnalyzeProfile] Received token", "token_preview", token[:min(len(token), 30)])
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
		log.Errorw("[AnalyzeProfile] AnalyzeProfileMultiFiles error", "error", err)
		errData, _ := json.Marshal(map[string]interface{}{
			"type": "error",
			"data": err.Error(),
		})
		fmt.Fprintf(w, "data: %s\n\n", errData)
		w.Flush()
		return
	}

	log.Infow("[AnalyzeProfile] AnalyzeProfileMultiFiles completed", "profile_length", len(profile))

	// 积分扣减（旧会员跳过）
	deductCredits(c, ctrl.creditBiz, user, "profile_analysis", "salesrag_profile", fmt.Sprintf("user_%d", user.ID))

	// 发送完成并附带完整结果
	doneData, _ := json.Marshal(map[string]interface{}{
		"type":    "done",
		"profile": profile,
	})
	fmt.Fprintf(w, "data: %s\n\n", doneData)
	w.Flush()
	log.Infow("[AnalyzeProfile] Sent done event")
}

// AnalyzeProfileText 纯文本分析生成客户档案（SSE 流式）
func (ctrl *SalesRAGController) AnalyzeProfileText(c *gin.Context) {
	log.Infow("[AnalyzeProfileText] Received request")

	var req struct {
		Text string `json:"text"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Text) == "" {
		log.Errorw("[AnalyzeProfileText] Invalid request", "error", err)
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("请输入客户资料文本"), nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		log.Errorw("[AnalyzeProfileText] No user found")
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// 积分预检：检查用户是否有足够积分执行客户档案分析
	if canPerform, reason := ctrl.creditBiz.CanPerformAIOperation(c, user, "profile_analysis"); !canPerform {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("%s", reason), nil)
		return
	}

	log.Infow("[AnalyzeProfileText] User submitting text", "user_id", user.ID, "text_length", len(req.Text))

	// 设置 SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	w := c.Writer

	// 发送初始状态
	statusData, _ := json.Marshal(map[string]interface{}{
		"type": "status",
		"data": "正在分析文本资料...",
	})
	fmt.Fprintf(w, "data: %s\n\n", statusData)
	w.Flush()

	profile, err := ctrl.b.SalesRAG().AnalyzeProfileText(c, user.ID, req.Text, func(token string) error {
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
		log.Errorw("[AnalyzeProfileText] Error", "error", err)
		errData, _ := json.Marshal(map[string]interface{}{
			"type": "error",
			"data": err.Error(),
		})
		fmt.Fprintf(w, "data: %s\n\n", errData)
		w.Flush()
		return
	}

	log.Infow("[AnalyzeProfileText] Completed", "profile_length", len(profile))

	// 积分扣减（旧会员跳过）
	deductCredits(c, ctrl.creditBiz, user, "profile_analysis", "salesrag_profile_text", fmt.Sprintf("user_%d", user.ID))

	doneData, _ := json.Marshal(map[string]interface{}{
		"type":    "done",
		"profile": profile,
	})
	fmt.Fprintf(w, "data: %s\n\n", doneData)
	w.Flush()
}

// AnalyzeChatStyle 分析聊天风格（语言指纹分析，支持 SSE 流式）
// 支持上传文件或直接传入文本
func (ctrl *SalesRAGController) AnalyzeChatStyle(c *gin.Context) {
	log.Infow("[AnalyzeChatStyle] Received request")

	var reader io.Reader
	var filename string

	// 尝试获取上传的文件
	file, header, err := c.Request.FormFile("file")
	if err == nil && file != nil {
		defer file.Close()
		reader = file
		filename = header.Filename
		log.Infow("[AnalyzeChatStyle] File upload", "filename", header.Filename, "size", header.Size)
	} else {
		// 没有文件，尝试获取 text 字段
		text := c.PostForm("text")
		if text == "" {
			log.Errorw("[AnalyzeChatStyle] No file or text provided")
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("请提供聊天文本或上传文件"), nil)
			return
		}
		reader = strings.NewReader(text)
		filename = "input_text.txt"
		log.Infow("[AnalyzeChatStyle] Text input", "length", len(text))
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		log.Errorw("[AnalyzeChatStyle] No user found")
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	// 积分预检：检查用户是否有足够积分执行风格分析
	if canPerform, reason := ctrl.creditBiz.CanPerformAIOperation(c, user, "style_analysis"); !canPerform {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("%s", reason), nil)
		return
	}

	log.Infow("[AnalyzeChatStyle] User analyzing", "user_id", user.ID, "filename", filename)

	// 设置 SSE 响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	w := c.Writer

	// 发送初始状态
	statusData, _ := json.Marshal(map[string]interface{}{
		"type": "status",
		"data": "正在分析语言风格...",
	})
	fmt.Fprintf(w, "data: %s\n\n", statusData)
	w.Flush()
	log.Infow("[AnalyzeChatStyle] Sent initial status")

	// 调用流式业务层分析
	log.Infow("[AnalyzeChatStyle] Calling AnalyzeChatStyleStream...")
	result, err := ctrl.b.SalesRAG().AnalyzeChatStyleStream(c, user.ID, reader, filename, func(token string) error {
		log.Infow("[AnalyzeChatStyle] Received token", "token_preview", token[:min(len(token), 30)])
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
		log.Errorw("[AnalyzeChatStyle] AnalyzeChatStyleStream error", "error", err)
		errData, _ := json.Marshal(map[string]interface{}{
			"type": "error",
			"data": err.Error(),
		})
		fmt.Fprintf(w, "data: %s\n\n", errData)
		w.Flush()
		return
	}

	log.Infow("[AnalyzeChatStyle] AnalyzeChatStyleStream completed", "result_length", len(result))

	// 积分扣减（旧会员跳过）
	deductCredits(c, ctrl.creditBiz, user, "style_analysis", "salesrag_style", fmt.Sprintf("user_%d", user.ID))

	// 发送完成并附带完整结果
	doneData, _ := json.Marshal(map[string]interface{}{
		"type":     "done",
		"analysis": result,
		"style":    result, // 兼容前端的两种字段名
	})
	fmt.Fprintf(w, "data: %s\n\n", doneData)
	w.Flush()
	log.Infow("[AnalyzeChatStyle] Sent done event")
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

// SaveLanguageStyle 保存用户的语言风格
func (ctrl *SalesRAGController) SaveLanguageStyle(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req struct {
		Style string `json:"style" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Errorw("[SaveLanguageStyle] Invalid request", "error", err)
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("请提供语言风格内容"), nil)
		return
	}

	log.Infow("[SaveLanguageStyle] Saving for user", "user_id", user.ID, "style_length", len(req.Style))

	if err := ctrl.b.SalesRAG().SaveLanguageStyle(c, user.ID, req.Style); err != nil {
		log.Errorw("[SaveLanguageStyle] Error", "userID", user.ID, "error", err.Error())
		core.WriteResponse(c, err, nil)
		return
	}

	log.Infow("[SaveLanguageStyle] Successfully saved", "user_id", user.ID)
	core.WriteResponse(c, nil, map[string]string{
		"message": "语言风格保存成功",
	})
}

// CheckSalesPermission 检查当前用户是否有销售智能体使用权限
func (ctrl *SalesRAGController) CheckSalesPermission(c *gin.Context) {
	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	hasPermission, err := ctrl.b.Customers().CheckFeaturePermission(c, user.ID, model.FeatureKeySalesAgent)
	if err != nil {
		log.Errorw("Failed to check sales permission", "user_id", user.ID, "err", err)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("权限检查失败"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"has_permission": hasPermission,
	})
}

// OCR 识别图片中的文本 (调用视觉大模型)
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

	// 积分预检：检查用户是否有足够积分执行 OCR
	if canPerform, reason := ctrl.creditBiz.CanPerformAIOperation(c, user, "ocr"); !canPerform {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("%s", reason), nil)
		return
	}

	// 2. 读取图片数据
	imageData, err := io.ReadAll(file)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("读取文件数据失败"), nil)
		return
	}

	// 3. 调用 biz 层完成上传、识别
	contentType := header.Header.Get("Content-Type")
	sessionID := c.DefaultPostForm("session_id", "no_session")
	engine := c.DefaultPostForm("engine", "baidu") // baidu（默认）或 vision

	ocrText, cosURL, err := ctrl.b.SalesRAG().OCRAnalyze(c.Request.Context(), user.ID, imageData, contentType, sessionID, header.Filename, engine)
	if err != nil {
		log.Errorw("OCRAnalyze failed", "error", err, "user_id", user.ID)
		core.WriteResponse(c, errno.InternalServerError.SetMessage("%s", err.Error()), nil)
		return
	}

	// 积分扣减（旧会员跳过）
	deductCredits(c, ctrl.creditBiz, user, "ocr", "salesrag_ocr", sessionID)

	// 4. 返回结果
	core.WriteResponse(c, nil, map[string]string{
		"text": ocrText,
		"url":  cosURL,
	})
}

// SubmitFeedback 提交消息反馈（点赞/点踩）
func (ctrl *SalesRAGController) SubmitFeedback(c *gin.Context) {
	sessionID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}
	messageID, err := strconv.ParseUint(c.Param("message_id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	var req struct {
		Rating  int    `json:"rating" binding:"required,oneof=-1 1"`
		Comment string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("rating 必须为 1 或 -1"), nil)
		return
	}

	err = ctrl.b.SalesRAG().SubmitFeedback(c.Request.Context(), user.ID, uint(sessionID), uint(messageID), req.Rating, req.Comment)
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}
	core.WriteResponse(c, nil, nil)
}

// GetFeedback 获取消息反馈
func (ctrl *SalesRAGController) GetFeedback(c *gin.Context) {
	sessionID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}
	messageID, err := strconv.ParseUint(c.Param("message_id"), 10, 64)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter, nil)
		return
	}

	user := middleware.GetCurrentUser(c)
	if user == nil {
		core.WriteResponse(c, errno.ErrTokenInvalid, nil)
		return
	}

	feedback, err := ctrl.b.SalesRAG().GetFeedback(c.Request.Context(), user.ID, uint(sessionID), uint(messageID))
	if err != nil {
		core.WriteResponse(c, err, nil)
		return
	}

	if feedback == nil {
		core.WriteResponse(c, nil, map[string]interface{}{})
		return
	}

	core.WriteResponse(c, nil, map[string]interface{}{
		"rating":  feedback.Rating,
		"comment": feedback.Comment,
	})
}

// deductCredits 积分扣减辅助函数（旧会员跳过，失败不阻塞主流程）
func deductCredits(c *gin.Context, creditBiz credit.ICreditBiz, user *model.User, operation, bizRefType, bizRefID string) {
	if user.HasActiveMembership() {
		return
	}
	estimatedCost := credit.GetEstimatedCredits(operation)
	if err := creditBiz.DeductCredits(c.Request.Context(), user.ID, estimatedCost, operation, bizRefType, bizRefID, nil); err != nil {
		log.Warnw("Failed to deduct credits", "error", err, "user_id", user.ID, "operation", operation)
	}
}
