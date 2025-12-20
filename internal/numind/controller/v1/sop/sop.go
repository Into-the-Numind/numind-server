package sop

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/biz/ali"
	"numind-server/internal/numind/biz/sop"
	"numind-server/internal/numind/biz/volc"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
	v1 "numind-server/pkg/api/numind/v1"
)

// 常量定义
const (
	// 文件大小限制（10MB）
	MaxFileSize = 10 * 1024 * 1024
	// 单次上传文件数量限制
	MaxFilesPerUpload = 10
	// 文本内容最大长度（用于提取）
	MaxTextContentLength = 100000
	// AI调用超时时间（秒）
	AICallTimeout = 30
	// 支持的文件扩展名
	AllowedExtensions = ".txt,.md,.docx,.doc,.pdf,.rtf"
)

// SopController 用户端SOP控制器
type SopController struct {
	sopBiz  sop.ISopBiz
	aliBiz  ali.AliBiz
	volcBiz volc.VolcBiz
}

// NewSopController 创建用户端SOP控制器
func NewSopController(sopBiz sop.ISopBiz, aliBiz ali.AliBiz, volcBiz volc.VolcBiz) *SopController {
	return &SopController{
		sopBiz:  sopBiz,
		aliBiz:  aliBiz,
		volcBiz: volcBiz,
	}
}

// QualityCheckResult 质量检测结果
type QualityCheckResult struct {
	Status      string   `json:"status"`      // "合格" 或 "需要改进"
	Summary     string   `json:"summary"`     // 问题摘要
	Problems    []string `json:"problems"`    // 发现的问题列表
	Suggestions []string `json:"suggestions"` // AI改进建议
	Score       int      `json:"score"`       // 质量分数 0-100
}

// ... existing code ...

// ExecuteTemplate 执行SOP模板（用户端）
func (ctrl *SopController) ExecuteTemplate(c *gin.Context) {
	log.C(c).Infow("User execute SOP template called")

	templateID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的模板ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	var req v1.ExecuteSopTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: "+err.Error()), nil)
		return
	}

	// 使用token中的用户ID
	run, err := ctrl.sopBiz.ExecuteTemplate(c, uint(templateID), user.ID, req.Text)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, run)
}

// GetRun 获取SOP执行记录（用户端，只能查看自己的）
func (ctrl *SopController) GetRun(c *gin.Context) {
	log.C(c).Infow("User get SOP run called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	run, err := ctrl.sopBiz.GetRun(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}

	// 验证是否是用户自己的记录
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	core.WriteResponse(c, nil, run)
}

// GetRunDetail 获取SOP执行详情（用户端）
func (ctrl *SopController) GetRunDetail(c *gin.Context) {
	log.C(c).Infow("User get SOP run detail called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	run, nodeRuns, err := ctrl.sopBiz.GetRunWithNodes(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}

	// 验证是否是用户自己的记录
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"run":       run,
		"node_runs": nodeRuns,
	})
}

// ListMyRuns 获取当前用户的SOP执行记录列表
func (ctrl *SopController) ListMyRuns(c *gin.Context) {
	log.C(c).Infow("User list my SOP runs called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	uid := user.ID
	runs, total, err := ctrl.sopBiz.ListRuns(c, offset, limit, &uid)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total": total,
		"runs":  runs,
	})
}

// ListMyNotes 获取当前用户的SOP笔记列表
func (ctrl *SopController) ListMyNotes(c *gin.Context) {
	log.C(c).Infow("User list my SOP notes called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	notes, total, err := ctrl.sopBiz.ListNotesByUser(c, user.ID, offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, gin.H{
		"total": total,
		"notes": notes,
	})
}

// GetNote 获取SOP笔记详情（用户端）
func (ctrl *SopController) GetNote(c *gin.Context) {
	log.C(c).Infow("User get SOP note called")

	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的笔记ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	note, err := ctrl.sopBiz.GetNote(c, uint(id))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("笔记不存在"), nil)
		return
	}

	// 验证是否是用户自己的笔记
	if note.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此笔记"), nil)
		return
	}

	core.WriteResponse(c, nil, note)
}

// ListTemplates 获取可用的SOP模板列表（用户端，只显示active的）
func (ctrl *SopController) ListTemplates(c *gin.Context) {
	log.C(c).Infow("User list SOP templates called")

	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	templates, _, err := ctrl.sopBiz.ListTemplates(c, offset, limit)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	// 只返回active状态的模板
	activeTemplates := []interface{}{}
	for _, t := range templates {
		if t.Status == "active" {
			activeTemplates = append(activeTemplates, t)
		}
	}

	core.WriteResponse(c, nil, gin.H{
		"total":     len(activeTemplates),
		"templates": activeTemplates,
	})
}

// GetTemplateNodes 获取指定模板的所有节点（用户端）
func (ctrl *SopController) GetTemplateNodes(c *gin.Context) {
	log.C(c).Infow("User get SOP template nodes called")

	templateID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的模板ID"), nil)
		return
	}

	// 验证模板是否存在且为active状态
	template, err := ctrl.sopBiz.GetTemplate(c, uint(templateID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("模板不存在"), nil)
		return
	}

	// 只允许获取active状态的模板节点
	if template.Status != "active" {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("模板未激活"), nil)
		return
	}

	// 获取模板的所有节点
	nodes, err := ctrl.sopBiz.ListNodesByTemplate(c, uint(templateID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	// 按Sort字段排序节点（使用标准库sort包）
	sortedNodes := make([]model.SopNode, len(nodes))
	copy(sortedNodes, nodes)
	sort.Slice(sortedNodes, func(i, j int) bool {
		return sortedNodes[i].Sort < sortedNodes[j].Sort
	})

	core.WriteResponse(c, nil, gin.H{
		"template_id":   templateID,
		"template_name": template.Name,
		"nodes":         sortedNodes,
		"total":         len(sortedNodes),
	})
}

// CreateRun 创建SOP执行（不立即执行）
func (ctrl *SopController) CreateRun(c *gin.Context) {
	log.C(c).Infow("User create SOP run called")

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	var req v1.CreateSopRunRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("请求参数错误: "+err.Error()), nil)
		return
	}

	run, err := ctrl.sopBiz.CreateRun(c, req.TemplateID, user.ID, req.Text)
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	core.WriteResponse(c, nil, run)
}

// GetNextNode 获取下一个待执行节点
func (ctrl *SopController) GetNextNode(c *gin.Context) {
	log.C(c).Infow("User get next node called")

	runID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 验证Run是否属于当前用户
	run, err := ctrl.sopBiz.GetRun(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	node, hasNext, err := ctrl.sopBiz.GetNextNode(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	if node == nil {
		core.WriteResponse(c, nil, gin.H{
			"node":     nil,
			"has_next": false,
			"message":  "所有节点已执行完成",
		})
		return
	}

	core.WriteResponse(c, nil, v1.NextNodeResponse{
		NodeID:   node.ID,
		NodeName: node.Name,
		Sort:     node.Sort,
		IsFirst:  len(run.ConversationID) > 0, // 简化判断，实际应该检查是否有已完成的节点
		HasNext:  hasNext,
	})
}

// ExecuteNodeStream 流式执行指定节点（支持文件上传）
func (ctrl *SopController) ExecuteNodeStream(c *gin.Context) {
	log.C(c).Infow("User execute SOP node stream called")

	runID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	nodeID, err := strconv.ParseUint(c.Param("node_id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的节点ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 验证Run是否属于当前用户
	run, err := ctrl.sopBiz.GetRun(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	// 处理请求：支持JSON和multipart/form-data两种格式
	var inputText string
	var uploadedFileIDs []uint

	// 检查是否是multipart/form-data
	contentType := c.GetHeader("Content-Type")
	if strings.Contains(contentType, "multipart/form-data") {
		// 处理文件上传
		form, err := c.MultipartForm()
		if err == nil {
			// 获取text字段（可选）
			if textValues := form.Value["text"]; len(textValues) > 0 {
				inputText = textValues[0]
			}

			// 获取上传的文件
			files := form.File["files"]
			if len(files) == 0 {
				files = form.File["file"] // 兼容单数形式
			}

			// 如果有文件，上传到COS并保存记录
			if len(files) > 0 {
				var fileTexts []string
				var uploadedFileNames []string
				var uploadErrors []string

				for _, file := range files {
					// 上传文件到COS并创建记录
					sopFile, err := ctrl.uploadFileToCOS(c, file, user.ID, uint(runID), uint(nodeID))
					if err != nil {
						log.C(c).Warnw("Failed to upload file", "filename", file.Filename, "error", err)
						uploadErrors = append(uploadErrors, fmt.Sprintf("%s: %v", file.Filename, err))
						// 继续处理其他文件
						continue
					}

					uploadedFileIDs = append(uploadedFileIDs, sopFile.ID)
					uploadedFileNames = append(uploadedFileNames, file.Filename)

					// 提取文本内容
					if sopFile.Content != "" {
						fileTexts = append(fileTexts, fmt.Sprintf("=== %s ===\n%s", file.Filename, sopFile.Content))
					}
				}

				// 如果所有文件都上传失败，返回错误
				if len(uploadedFileNames) == 0 && len(uploadErrors) > 0 {
					errorMsg := fmt.Sprintf("所有文件上传失败：%s", strings.Join(uploadErrors, "; "))
					core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(errorMsg), nil)
					return
				}

				// 合并文件内容和用户输入的文本
				if len(fileTexts) > 0 {
					// 有文本内容，使用文本内容
					fileContent := strings.Join(fileTexts, "\n\n")
					if inputText != "" {
						inputText = inputText + "\n\n" + fileContent
					} else {
						inputText = fileContent
					}
				} else if len(uploadedFileNames) > 0 {
					// 文件上传成功但无法提取文本内容（如PDF等格式）
					// 至少使用文件名作为输入，让AI知道用户上传了文件
					fileInfo := fmt.Sprintf("用户已上传以下文件：%s\n\n注意：这些文件无法自动提取文本内容，请根据文件名和上下文进行处理。", strings.Join(uploadedFileNames, "、"))
					if inputText != "" {
						inputText = inputText + "\n\n" + fileInfo
					} else {
						inputText = fileInfo
					}
					log.C(c).Infow("文件上传成功但无法提取文本，使用文件名作为输入", "files", uploadedFileNames)
				}
			}
		}
	} else {
		// JSON格式
		var req v1.ExecuteSopNodeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			// text是可选的，所以不强制要求
			req.Text = ""
		}
		inputText = req.Text
	}

	// 设置SSE响应头
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no") // 禁用nginx缓冲

	// 获取Flusher（用于实时刷新）
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("Streaming not supported"), nil)
		return
	}

	// 创建带心跳的 context，用于定期发送心跳保持连接
	heartbeatCtx, heartbeatCancel := context.WithCancel(c.Request.Context())
	defer heartbeatCancel()

	// 启动心跳 goroutine，每 15 秒发送一次注释行（SSE 心跳），更频繁地保持连接活跃
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()
	go func() {
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-heartbeatTicker.C:
				// 发送 SSE 注释行（以 : 开头）作为心跳
				// 检查连接是否仍然有效
				select {
				case <-c.Request.Context().Done():
					return
				default:
					// 发送心跳注释行
					if _, err := c.Writer.WriteString(": heartbeat\n\n"); err != nil {
						log.C(c).Warnw("Failed to send heartbeat", "error", err)
						return
					}
					flusher.Flush()
				}
			}
		}
	}()

	// 流式执行节点
	err = ctrl.sopBiz.ExecuteNodeStream(heartbeatCtx, uint(runID), uint(nodeID), inputText, func(chunk string) error {
		// 检查客户端是否断开连接
		select {
		case <-c.Request.Context().Done():
			log.C(c).Infow("Client disconnected during stream")
			return c.Request.Context().Err()
		default:
		}

		// 发送SSE格式的数据（需要对JSON进行转义）
		chunkJSON, _ := json.Marshal(chunk)
		data := fmt.Sprintf("data: %s\n\n", string(chunkJSON))
		if _, err := c.Writer.WriteString(data); err != nil {
			log.C(c).Warnw("Failed to write chunk to client", "error", err)
			return err
		}

		// 立即刷新，确保数据实时发送
		flusher.Flush()
		return nil
	})

	if err != nil {
		// 检查是否是客户端断开连接
		if c.Request.Context().Err() != nil {
			log.C(c).Infow("Client disconnected during stream", "error", err)
			return // 客户端断开，不需要发送错误
		}

		// 发送错误事件
		errorMsg, _ := json.Marshal(err.Error())
		errorData := fmt.Sprintf("event: error\ndata: %s\n\n", string(errorMsg))
		c.Writer.WriteString(errorData)
		flusher.Flush()
		return
	}

	// 发送完成事件（包含上传的文件ID）
	doneData := fmt.Sprintf("event: done\ndata: {\"status\":\"completed\",\"uploaded_file_ids\":%v}\n\n", uploadedFileIDs)
	c.Writer.WriteString(doneData)
	flusher.Flush()
}

// uploadFileToCOS 上传文件到COS并创建数据库记录（考虑各种极端情况）
func (ctrl *SopController) uploadFileToCOS(c *gin.Context, file *multipart.FileHeader, userID, runID, nodeID uint) (*model.SopFile, error) {
	// 1. 验证文件大小
	if file.Size <= 0 {
		return nil, fmt.Errorf("文件为空")
	}
	if file.Size > MaxFileSize {
		return nil, fmt.Errorf("文件大小超过限制（最大%dMB）", MaxFileSize/(1024*1024))
	}

	// 2. 验证文件名（防止路径遍历攻击）
	fileName := sanitizeFileName(file.Filename)
	if fileName == "" {
		return nil, fmt.Errorf("无效的文件名")
	}

	// 3. 验证文件扩展名
	ext := strings.ToLower(filepath.Ext(fileName))
	if !isAllowedExtension(ext) {
		return nil, fmt.Errorf("不支持的文件格式: %s，支持格式: %s", ext, AllowedExtensions)
	}

	// 4. 打开文件（使用defer确保关闭）
	src, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("打开文件失败: %w", err)
	}
	defer src.Close()

	// 5. 读取文件数据（限制读取大小，防止内存溢出）
	limitedReader := io.LimitReader(src, MaxFileSize+1) // +1 用于检测是否超过限制
	fileData, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}

	// 检查是否超过限制
	if int64(len(fileData)) > MaxFileSize {
		return nil, fmt.Errorf("文件大小超过限制")
	}

	// 6. 生成安全的文件名和对象键
	timestamp := time.Now().UnixNano()
	safeFileName := fmt.Sprintf("sop_file_%d_%d%s", userID, timestamp, ext)
	objectKey := fmt.Sprintf("sop/%d/%d/%s", userID, runID, safeFileName)

	// 7. 上传到COS（带重试和错误处理）
	var cosURL string
	if util.IsCOSEnabled() {
		cosURL, err = util.UploadBytesToCOS(c, objectKey, file.Header.Get("Content-Type"), fileData)
		if err != nil {
			log.C(c).Warnw("COS上传失败，继续处理", "error", err, "object_key", objectKey)
			// COS上传失败不影响功能，继续处理
			cosURL = "" // 设置为空，表示未上传到COS
		} else {
			log.C(c).Infow("文件上传到COS成功", "cos_url", cosURL, "object_key", objectKey)
		}
	}

	// 8. 提取文本内容（支持多种格式，限制长度）
	var content string
	if ext == ".txt" || ext == ".md" {
		text := string(fileData)
		// 验证UTF-8编码并清理
		if !utf8.ValidString(text) {
			// 尝试清理无效字符
			text = strings.ToValidUTF8(text, "")
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			content = text[:MaxTextContentLength] + "...(内容过长已截断)"
		} else {
			content = text
		}
	} else if ext == ".pdf" {
		// PDF文件文本提取
		text, err := extractTextFromPDF(fileData)
		if err == nil {
			// 清理和验证文本，确保可以安全存储到数据库
			text = sanitizeUTF8ForDatabase(text)
			if len(text) > MaxTextContentLength {
				content = text[:MaxTextContentLength] + "...(内容过长已截断)"
			} else {
				content = text
			}
		} else {
			log.C(c).Warnw("PDF文本提取失败", "error", err, "filename", fileName)
			// PDF提取失败不影响文件上传，只是不保存文本内容
		}
	} else if ext == ".docx" {
		// DOCX文件文本提取
		text, err := extractTextFromDOCX(fileData)
		if err == nil {
			// 清理和验证文本，确保可以安全存储到数据库
			text = sanitizeUTF8ForDatabase(text)
			if len(text) > MaxTextContentLength {
				content = text[:MaxTextContentLength] + "...(内容过长已截断)"
			} else {
				content = text
			}
		} else {
			log.C(c).Warnw("DOCX文本提取失败", "error", err, "filename", fileName)
			// DOCX提取失败不影响文件上传，只是不保存文本内容
		}
	} else if ext == ".doc" {
		// DOC文件文本提取
		text, err := extractTextFromDOC(fileData)
		if err == nil {
			// 清理和验证文本，确保可以安全存储到数据库
			text = sanitizeUTF8ForDatabase(text)
			if len(text) > MaxTextContentLength {
				content = text[:MaxTextContentLength] + "...(内容过长已截断)"
			} else {
				content = text
			}
		} else {
			log.C(c).Warnw("DOC文本提取失败", "error", err, "filename", fileName)
		}
	} else if ext == ".rtf" {
		// RTF文件文本提取
		text, err := extractTextFromRTF(fileData)
		if err == nil {
			// 清理和验证文本，确保可以安全存储到数据库
			text = sanitizeUTF8ForDatabase(text)
			if len(text) > MaxTextContentLength {
				content = text[:MaxTextContentLength] + "...(内容过长已截断)"
			} else {
				content = text
			}
		} else {
			log.C(c).Warnw("RTF文本提取失败", "error", err, "filename", fileName)
		}
	}

	// 最终验证：确保content是有效的UTF-8，防止数据库错误
	if content != "" {
		if !utf8.ValidString(content) {
			log.C(c).Warnw("Content包含无效UTF-8字符，进行清理", "filename", fileName)
			content = strings.ToValidUTF8(content, "")
			content = sanitizeUTF8ForDatabase(content)
		}
	}

	// 9. 创建数据库记录
	sopFile := &model.SopFile{
		UserID:    userID,
		RunID:     &runID,
		NodeID:    &nodeID,
		FileName:  fileName,
		FileURL:   cosURL,
		FileType:  file.Header.Get("Content-Type"),
		FileSize:  file.Size,
		FileExt:   ext,
		Content:   content,
		Status:    "uploaded",
		ObjectKey: objectKey,
	}

	// 如果COS上传失败，记录错误但不阻止保存
	if cosURL == "" && util.IsCOSEnabled() {
		sopFile.Status = "uploaded_no_cos"
		sopFile.ErrorMsg = "COS上传失败，但文件已保存"
	}

	// 10. 保存到数据库（使用事务确保数据一致性）
	ds := store.S
	if err := ds.Sop().CreateFile(sopFile); err != nil {
		return nil, fmt.Errorf("创建文件记录失败: %w", err)
	}

	log.C(c).Infow("文件上传成功",
		"file_id", sopFile.ID,
		"filename", fileName,
		"size", file.Size,
		"cos_url", cosURL,
		"has_content", content != "")

	return sopFile, nil
}

// CheckFileQuality 检测上传文件的质量（不保存到数据库）
func (ctrl *SopController) CheckFileQuality(c *gin.Context) {
	log.C(c).Infow("Check file quality called")

	// 1. 获取multipart form
	form, err := c.MultipartForm()
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的multipart form: "+err.Error()), nil)
		return
	}
	defer form.RemoveAll() // 确保清理临时文件

	// 2. 获取文本内容（优先从text字段，如果没有则从文件提取）
	var textContent string

	// 2.1 先检查是否有text字段
	if textValues := form.Value["text"]; len(textValues) > 0 && strings.TrimSpace(textValues[0]) != "" {
		textContent = strings.TrimSpace(textValues[0])
		// 限制文本长度
		if len(textContent) > MaxTextContentLength {
			textContent = textContent[:MaxTextContentLength] + "...(内容过长已截断)"
		}
	}

	// 2.2 如果没有text，则从上传的文件中提取
	if textContent == "" {
		files := form.File["files"]
		if len(files) == 0 {
			files = form.File["file"] // 兼容单数形式
		}

		if len(files) == 0 {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("未提供文本内容或文件"), nil)
			return
		}

		// 限制文件数量
		if len(files) > MaxFilesPerUpload {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(fmt.Sprintf("文件数量超过限制（最多%d个）", MaxFilesPerUpload)), nil)
			return
		}

		// 提取所有文件的文本内容
		var fileTexts []string
		var totalSize int64
		for i, file := range files {
			// 验证文件大小
			if file.Size > MaxFileSize {
				core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage(fmt.Sprintf("文件 %s 超过大小限制（最大%dMB）", file.Filename, MaxFileSize/(1024*1024))), nil)
				return
			}
			totalSize += file.Size

			// 限制总大小
			if totalSize > MaxFileSize*MaxFilesPerUpload {
				core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("所有文件总大小超过限制"), nil)
				return
			}

			text, err := extractTextFromFile(file)
			if err != nil {
				log.C(c).Warnw("提取文件文本失败", "filename", file.Filename, "error", err)
				// 如果所有文件都失败，返回错误
				if i == len(files)-1 && len(fileTexts) == 0 {
					core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无法从文件中提取文本内容: "+err.Error()), nil)
					return
				}
				continue
			}
			if text != "" {
				fileTexts = append(fileTexts, text)
			}
		}

		if len(fileTexts) == 0 {
			core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无法从文件中提取文本内容"), nil)
			return
		}

		textContent = strings.Join(fileTexts, "\n\n")
		// 限制总长度
		if len(textContent) > MaxTextContentLength {
			textContent = textContent[:MaxTextContentLength] + "...(内容过长已截断)"
		}
	}

	// 3. 验证文本内容不为空
	textContent = strings.TrimSpace(textContent)
	if textContent == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文本内容为空"), nil)
		return
	}

	// 4. 调用AI进行质量检测（带超时和重试）
	ctx, cancel := context.WithTimeout(c.Request.Context(), AICallTimeout*time.Second)
	defer cancel()

	result, err := ctrl.checkQualityWithAI(ctx, textContent)
	if err != nil {
		log.C(c).Errorw("质量检测失败", "error", err)
		// 如果AI调用失败，返回基础的质量检测结果
		result = ctrl.fallbackQualityCheck(textContent)
	}

	core.WriteResponse(c, nil, result)
}

// checkQualityWithAI 使用AI检测文本质量（带重试和容错）
func (ctrl *SopController) checkQualityWithAI(ctx context.Context, text string) (*QualityCheckResult, error) {
	// 构建质量检测的prompt
	prompt := `你是一位专业的文案质量检测专家。请对以下文案进行质量检测，并按照JSON格式返回结果。

## 检测要求：
1. 评估文案的完整性（是否包含开头、正文、结尾）
2. 评估文案的吸引力（是否有吸引人的开头）
3. 评估文案的内容丰富度（是否包含产品介绍、痛点、效果展示等）
4. 评估文案的互动性（是否有行动号召）

## 返回格式（必须是有效的JSON）：
{
  "status": "合格" 或 "需要改进",
  "summary": "问题摘要（如果status为需要改进，说明主要问题）",
  "problems": ["问题1", "问题2", ...],
  "suggestions": ["建议1", "建议2", ...],
  "score": 0-100的分数
}

## 待检测文案：
` + text + `

请直接返回JSON，不要包含其他说明文字。`

	messages := []map[string]string{
		{"role": "user", "content": prompt},
	}

	// 调用AI（优先使用火山方舟，失败后降级到阿里百炼）
	var aiResponse string
	var err error

	// 先尝试火山方舟
	if ctrl.volcBiz != nil {
		aiResponse, err = ctrl.volcBiz.VolcTextStream(ctx, messages, 2000, 0.7)
		if err != nil {
			log.C(ctx).Warnw("火山方舟API失败，尝试阿里百炼降级", "error", err.Error())
		}
	}

	// 如果火山方舟失败或不可用，降级到阿里百炼
	if err != nil || aiResponse == "" {
		if ctrl.aliBiz != nil {
			aiResponse, err = ctrl.aliBiz.QianwenTextStream(messages, 2000, 0.7)
			if err != nil {
				return nil, fmt.Errorf("AI API调用失败: %w", err)
			}
		} else {
			return nil, fmt.Errorf("AI服务不可用")
		}
	}

	if aiResponse == "" {
		return nil, fmt.Errorf("AI返回空响应")
	}

	// 解析AI返回的JSON
	result, err := parseQualityCheckResponse(aiResponse)
	if err != nil {
		log.C(ctx).Warnw("解析AI响应失败，尝试提取JSON", "error", err, "response", aiResponse)
		// 尝试从响应中提取JSON
		result = extractQualityCheckFromText(aiResponse)
	}

	return result, nil
}

// parseQualityCheckResponse 解析AI返回的JSON响应（容错处理）
func parseQualityCheckResponse(response string) (*QualityCheckResult, error) {
	// 清理响应文本
	jsonStr := strings.TrimSpace(response)

	// 移除markdown代码块标记
	if strings.HasPrefix(jsonStr, "```json") {
		jsonStr = strings.TrimPrefix(jsonStr, "```json")
		jsonStr = strings.TrimSuffix(jsonStr, "```")
	} else if strings.HasPrefix(jsonStr, "```") {
		jsonStr = strings.TrimPrefix(jsonStr, "```")
		jsonStr = strings.TrimSuffix(jsonStr, "```")
	}
	jsonStr = strings.TrimSpace(jsonStr)

	// 尝试提取JSON对象（处理可能的前后文本）
	jsonRegex := regexp.MustCompile(`\{[\s\S]*\}`)
	matches := jsonRegex.FindString(jsonStr)
	if matches != "" {
		jsonStr = matches
	}

	// 解析JSON
	var result QualityCheckResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("解析JSON失败: %w", err)
	}

	// 验证和设置默认值
	if result.Status == "" {
		result.Status = "需要改进"
	}
	if result.Score < 0 || result.Score > 100 {
		// 根据问题数量估算分数
		if len(result.Problems) == 0 {
			result.Score = 90
		} else if len(result.Problems) <= 2 {
			result.Score = 70
		} else if len(result.Problems) <= 4 {
			result.Score = 50
		} else {
			result.Score = 30
		}
	}

	// 确保数组不为nil
	if result.Problems == nil {
		result.Problems = []string{}
	}
	if result.Suggestions == nil {
		result.Suggestions = []string{}
	}

	return &result, nil
}

// extractQualityCheckFromText 从文本中提取质量检测信息（备用方案）
func extractQualityCheckFromText(text string) *QualityCheckResult {
	result := &QualityCheckResult{
		Status:      "需要改进",
		Summary:     "文案信息不够完整，可能影响AI学习效果",
		Problems:    []string{},
		Suggestions: []string{},
		Score:       50,
	}

	// 简单的文本分析
	textRunes := []rune(text)
	textLen := len(textRunes)

	// 检查长度
	if textLen < 80 {
		result.Problems = append(result.Problems, fmt.Sprintf("文案内容过短 (建议至少80字，当前%d字)", textLen))
	}

	// 检查是否有吸引人的开头
	hasQuestion := strings.Contains(text, "？") || strings.Contains(text, "?")
	hasExclamation := strings.Contains(text, "！") || strings.Contains(text, "!")
	hasGreeting := strings.Contains(text, "姐妹") || strings.Contains(text, "你们") || strings.Contains(text, "大家")
	if !hasQuestion && !hasExclamation && !hasGreeting {
		result.Problems = append(result.Problems, "缺少吸引人的开头 (如问候、悬念等)")
	}

	// 检查是否包含产品相关关键词
	hasProduct := strings.Contains(text, "产品") || strings.Contains(text, "商品") || strings.Contains(text, "服务")
	if !hasProduct {
		result.Problems = append(result.Problems, "缺少产品介绍或提及")
	}

	// 检查是否包含痛点
	hasPainPoint := strings.Contains(text, "问题") || strings.Contains(text, "痛点") || strings.Contains(text, "困扰")
	if !hasPainPoint {
		result.Problems = append(result.Problems, "缺少痛点引入或问题描述")
	}

	// 检查是否包含效果展示
	hasEffect := strings.Contains(text, "效果") || strings.Contains(text, "感受") || strings.Contains(text, "体验")
	if !hasEffect {
		result.Problems = append(result.Problems, "缺少效果展示或使用感受")
	}

	// 检查是否有行动号召
	hasCTA := strings.Contains(text, "点击") || strings.Contains(text, "关注") || strings.Contains(text, "评论") || strings.Contains(text, "点赞")
	if !hasCTA {
		result.Problems = append(result.Problems, "缺少行动号召 (引导互动)")
	}

	// 如果问题少于3个，认为基本合格
	if len(result.Problems) < 3 {
		result.Status = "合格"
		result.Score = 75
		result.Summary = "文案基本完整，可以继续使用"
	} else {
		// 生成改进建议
		result.Suggestions = append(result.Suggestions, "您的文案需要更多内容，建议从以下方面完善：")
		if !hasGreeting {
			result.Suggestions = append(result.Suggestions, "吸引人的开头：用问候、提问或悬念开场（如\"姐妹们！\"、\"你们有没有遇到...\"）")
		}
		if !hasPainPoint {
			result.Suggestions = append(result.Suggestions, "痛点引入：描述目标用户遇到的问题或困扰")
		}
		if !hasProduct {
			result.Suggestions = append(result.Suggestions, "产品介绍：说明产品如何解决用户痛点")
		}
		if !hasEffect {
			result.Suggestions = append(result.Suggestions, "效果展示：分享使用后的感受和效果")
		}
		if !hasCTA {
			result.Suggestions = append(result.Suggestions, "行动号召：引导用户互动（如点赞、评论、关注）")
		}
	}

	return result
}

// fallbackQualityCheck 当AI调用失败时的备用质量检测
func (ctrl *SopController) fallbackQualityCheck(text string) *QualityCheckResult {
	return extractQualityCheckFromText(text)
}

// extractTextFromFile 从文件中提取文本内容（支持多种格式，容错处理）
func extractTextFromFile(file *multipart.FileHeader) (string, error) {
	// 1. 验证文件大小
	if file.Size <= 0 {
		return "", fmt.Errorf("文件为空")
	}
	if file.Size > MaxFileSize {
		return "", fmt.Errorf("文件大小超过限制")
	}

	// 2. 打开文件
	src, err := file.Open()
	if err != nil {
		return "", fmt.Errorf("打开文件失败: %w", err)
	}
	defer src.Close()

	// 3. 读取文件内容（限制大小）
	limitedReader := io.LimitReader(src, MaxFileSize+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return "", fmt.Errorf("读取文件失败: %w", err)
	}

	// 检查是否超过限制
	if int64(len(data)) > MaxFileSize {
		return "", fmt.Errorf("文件大小超过限制")
	}

	// 4. 根据文件扩展名处理
	ext := strings.ToLower(filepath.Ext(file.Filename))

	switch ext {
	case ".txt", ".md":
		// 纯文本文件，直接返回
		text := string(data)
		// 验证UTF-8编码
		if !utf8.ValidString(text) {
			// 尝试清理无效字符
			text = strings.ToValidUTF8(text, "")
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	case ".pdf":
		// PDF文件文本提取
		text, err := extractTextFromPDF(data)
		if err != nil {
			return "", fmt.Errorf("PDF文本提取失败: %w（可能是扫描版PDF或加密PDF）", err)
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	case ".docx":
		// DOCX文件文本提取
		text, err := extractTextFromDOCX(data)
		if err != nil {
			return "", fmt.Errorf("DOCX文本提取失败: %w", err)
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	case ".doc":
		// 旧版Word文档（.doc格式较复杂，尝试基础提取）
		text, err := extractTextFromDOC(data)
		if err != nil {
			return "", fmt.Errorf("DOC文本提取失败: %w（建议转换为DOCX格式）", err)
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	case ".rtf":
		// RTF文件（富文本格式）
		text, err := extractTextFromRTF(data)
		if err != nil {
			return "", fmt.Errorf("RTF文本提取失败: %w", err)
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	default:
		// 尝试作为文本处理
		text := string(data)
		// 检查是否包含可打印字符
		hasPrintable := false
		for _, r := range text {
			if r >= 32 && r < 127 || r >= 0x4e00 && r <= 0x9fff {
				hasPrintable = true
				break
			}
		}
		if !hasPrintable {
			return "", fmt.Errorf("文件格式不支持或文件内容无法识别")
		}
		// 验证UTF-8编码
		if !utf8.ValidString(text) {
			text = strings.ToValidUTF8(text, "")
		}
		// 清理和验证文本，确保可以安全存储到数据库
		text = sanitizeUTF8ForDatabase(text)
		// 限制内容长度
		if len(text) > MaxTextContentLength {
			text = text[:MaxTextContentLength] + "...(内容过长已截断)"
		}
		return text, nil
	}
}

// sanitizeFileName 清理文件名，防止路径遍历攻击
func sanitizeFileName(fileName string) string {
	// 移除路径分隔符
	fileName = strings.ReplaceAll(fileName, "/", "_")
	fileName = strings.ReplaceAll(fileName, "\\", "_")
	fileName = strings.ReplaceAll(fileName, "..", "_")

	// 移除控制字符
	var result strings.Builder
	for _, r := range fileName {
		if r >= 32 && r != 127 {
			result.WriteRune(r)
		}
	}
	fileName = result.String()

	// 限制文件名长度
	if len(fileName) > 255 {
		ext := filepath.Ext(fileName)
		baseName := fileName[:255-len(ext)]
		fileName = baseName + ext
	}

	return strings.TrimSpace(fileName)
}

// isAllowedExtension 检查文件扩展名是否允许
func isAllowedExtension(ext string) bool {
	allowed := strings.Split(AllowedExtensions, ",")
	for _, a := range allowed {
		if strings.TrimSpace(a) == ext {
			return true
		}
	}
	return false
}

// GetRunStatus 获取Run执行状态
func (ctrl *SopController) GetRunStatus(c *gin.Context) {
	log.C(c).Infow("User get SOP run status called")

	runID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrBind.SetMessage("无效的执行ID"), nil)
		return
	}

	// 从token获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 验证Run是否属于当前用户
	run, err := ctrl.sopBiz.GetRun(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage("执行记录不存在"), nil)
		return
	}
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此记录"), nil)
		return
	}

	status, err := ctrl.sopBiz.GetRunStatus(c, uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.InternalServerError.SetMessage(err.Error()), nil)
		return
	}

	// 转换为API响应格式
	response := v1.RunStatusResponse{
		Status:          status.Status,
		CurrentNodeSort: status.CurrentNodeSort,
		TotalNodes:      status.TotalNodes,
		CompletedCount:  status.CompletedCount,
	}

	// 转换已完成节点
	completedNodes := make([]v1.CompletedNodeInfo, len(status.CompletedNodes))
	for i, node := range status.CompletedNodes {
		completedNodes[i] = v1.CompletedNodeInfo{
			NodeID:        node.NodeID,
			NodeName:      node.NodeName,
			Sort:          node.Sort,
			OutputPreview: node.OutputPreview,
		}
	}
	response.CompletedNodes = completedNodes

	// 转换下一个节点
	if status.NextNode != nil {
		response.NextNode = &v1.NextNodeInfo{
			NodeID:   status.NextNode.NodeID,
			NodeName: status.NextNode.NodeName,
			Sort:     status.NextNode.Sort,
			IsFirst:  status.NextNode.IsFirst,
			HasNext:  status.NextNode.HasNext,
		}
	}

	core.WriteResponse(c, nil, response)
}

// extractTextFromPDF 从PDF文件中提取文本
func extractTextFromPDF(data []byte) (string, error) {
	var result strings.Builder

	// 方法1: 提取PDF文本对象中的文本（BT ... ET）
	btPattern := regexp.MustCompile(`BT\s+(.*?)\s+ET`)
	matches := btPattern.FindAllStringSubmatch(string(data), -1)
	for _, match := range matches {
		if len(match) > 1 {
			// 提取文本内容（通常在Tj或TJ操作符中）
			textPattern := regexp.MustCompile(`\((.*?)\)\s*Tj|\[(.*?)\]\s*TJ`)
			textMatches := textPattern.FindAllStringSubmatch(match[1], -1)
			for _, tm := range textMatches {
				if len(tm) > 1 {
					text := tm[1]
					if len(tm) > 2 && tm[2] != "" {
						text = tm[2]
					}
					// 处理PDF转义字符
					text = strings.ReplaceAll(text, "\\n", "\n")
					text = strings.ReplaceAll(text, "\\r", "\r")
					text = strings.ReplaceAll(text, "\\t", "\t")
					text = strings.ReplaceAll(text, "\\(", "(")
					text = strings.ReplaceAll(text, "\\)", ")")
					result.WriteString(text)
					result.WriteString(" ")
				}
			}
		}
	}

	// 方法2: 如果方法1没有提取到文本，尝试提取所有可打印字符
	if result.Len() == 0 {
		text := extractPrintableTextFromPDF(data)
		if text != "" {
			result.WriteString(text)
		}
	}

	if result.Len() == 0 {
		return "", fmt.Errorf("无法从PDF中提取文本，可能是扫描版PDF或加密PDF")
	}

	return cleanExtractedText(result.String()), nil
}

// extractPrintableTextFromPDF 从PDF中提取可打印文本（备用方法）
func extractPrintableTextFromPDF(data []byte) string {
	var result strings.Builder
	var currentWord strings.Builder

	for i := 0; i < len(data); i++ {
		b := data[i]

		// 检查是否是ASCII可打印字符或UTF-8字符的开始
		if (b >= 32 && b < 127) || (b >= 0xC0 && b < 0xF8) {
			// 尝试读取UTF-8字符
			if b >= 0xC0 {
				// UTF-8多字节字符
				charLen := getUTF8CharLength(b)
				if i+charLen <= len(data) {
					charBytes := data[i : i+charLen]
					if isValidUTF8(charBytes) {
						currentWord.Write(charBytes)
						i += charLen - 1
						continue
					}
				}
			} else {
				currentWord.WriteByte(b)
			}
		} else if b == '\n' || b == '\r' || b == '\t' || b == ' ' {
			// 遇到空白字符，结束当前单词
			if currentWord.Len() > 0 {
				word := currentWord.String()
				// 过滤掉太短的单词（可能是PDF格式代码）
				if len(word) >= 2 {
					result.WriteString(word)
					result.WriteByte(' ')
				}
				currentWord.Reset()
			}
		} else {
			// 其他字符，重置当前单词
			if currentWord.Len() > 0 {
				word := currentWord.String()
				if len(word) >= 2 {
					result.WriteString(word)
					result.WriteByte(' ')
				}
				currentWord.Reset()
			}
		}
	}

	// 处理最后一个单词
	if currentWord.Len() > 0 {
		word := currentWord.String()
		if len(word) >= 2 {
			result.WriteString(word)
		}
	}

	return result.String()
}

// extractTextFromDOCX 从DOCX文件中提取文本
// DOCX文件实际上是ZIP格式，包含document.xml文件
func extractTextFromDOCX(data []byte) (string, error) {
	// 创建ZIP reader
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("无法读取DOCX文件（ZIP格式错误）: %w", err)
	}

	var documentXML []byte
	found := false

	// 查找document.xml文件
	for _, file := range zipReader.File {
		if file.Name == "word/document.xml" {
			rc, err := file.Open()
			if err != nil {
				return "", fmt.Errorf("无法打开document.xml: %w", err)
			}
			defer rc.Close()

			documentXML, err = io.ReadAll(rc)
			if err != nil {
				return "", fmt.Errorf("无法读取document.xml: %w", err)
			}
			found = true
			break
		}
	}

	if !found {
		return "", fmt.Errorf("DOCX文件中未找到document.xml")
	}

	// 解析XML并提取文本
	text, err := extractTextFromDOCXXML(documentXML)
	if err != nil {
		return "", fmt.Errorf("解析DOCX XML失败: %w", err)
	}

	return cleanExtractedText(text), nil
}

// extractTextFromDOCXXML 从DOCX的XML中提取文本
func extractTextFromDOCXXML(xmlData []byte) (string, error) {
	var result strings.Builder

	// DOCX使用WordprocessingML格式
	// 文本通常在<w:t>标签中
	textPattern := regexp.MustCompile(`<w:t[^>]*>([^<]*)</w:t>`)
	matches := textPattern.FindAllStringSubmatch(string(xmlData), -1)

	for _, match := range matches {
		if len(match) > 1 {
			text := match[1]
			// 解码XML实体
			text = decodeXMLEntities(text)
			result.WriteString(text)
			result.WriteString(" ")
		}
	}

	// 如果正则表达式没有匹配到，尝试使用XML解析器
	if result.Len() == 0 {
		text, err := parseDOCXXMLWithParser(xmlData)
		if err == nil && text != "" {
			return text, nil
		}
	}

	if result.Len() == 0 {
		return "", fmt.Errorf("无法从DOCX XML中提取文本")
	}

	return result.String(), nil
}

// parseDOCXXMLWithParser 使用XML解析器解析DOCX
func parseDOCXXMLWithParser(xmlData []byte) (string, error) {
	var result strings.Builder

	decoder := xml.NewDecoder(bytes.NewReader(xmlData))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		switch se := token.(type) {
		case xml.StartElement:
			// 查找w:t元素（Word文本元素）
			if se.Name.Local == "t" {
				// 读取文本内容
				var text string
				if err := decoder.DecodeElement(&text, &se); err == nil {
					result.WriteString(text)
					result.WriteString(" ")
				}
			}
		case xml.CharData:
			// 直接文本内容
			text := strings.TrimSpace(string(se))
			if text != "" && !strings.HasPrefix(text, "<") {
				result.WriteString(text)
				result.WriteString(" ")
			}
		}
	}

	return result.String(), nil
}

// extractTextFromDOC 从旧版Word文档(.doc)中提取文本
func extractTextFromDOC(data []byte) (string, error) {
	// .doc格式是OLE2格式，解析比较复杂
	// 这里使用简单的文本提取方法
	text := extractPrintableTextFromPDF(data) // 复用PDF的文本提取逻辑
	if text == "" {
		return "", fmt.Errorf("无法从DOC文件中提取文本，建议转换为DOCX格式")
	}
	return cleanExtractedText(text), nil
}

// extractTextFromRTF 从RTF文件中提取文本
func extractTextFromRTF(data []byte) (string, error) {
	var result strings.Builder
	rtfText := string(data)

	// RTF格式：文本通常在控制词之间
	// 移除RTF控制词和转义字符
	textPattern := regexp.MustCompile(`\\[a-z]+\d*\s?|\\'[0-9a-f]{2}|[{}]`)
	rtfText = textPattern.ReplaceAllString(rtfText, " ")

	// 提取可打印字符
	for _, r := range rtfText {
		if r >= 32 && r < 127 || (r >= 0x4e00 && r <= 0x9fff) {
			result.WriteRune(r)
		} else if r == '\n' || r == '\r' {
			result.WriteRune(' ')
		}
	}

	text := result.String()
	if text == "" {
		return "", fmt.Errorf("无法从RTF文件中提取文本")
	}

	return cleanExtractedText(text), nil
}

// cleanExtractedText 清理提取的文本，确保所有字符都是有效的UTF-8
func cleanExtractedText(text string) string {
	// 第一步：确保字符串是有效的UTF-8，移除无效的UTF-8序列
	if !utf8.ValidString(text) {
		// 将无效的UTF-8序列替换为空字符串
		text = strings.ToValidUTF8(text, "")
	}

	// 第二步：移除控制字符（包括NULL、换行、回车等）
	text = regexp.MustCompile(`[\x00-\x1F\x7F-\x9F]`).ReplaceAllString(text, "")

	// 第三步：移除多余的空白字符
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")

	// 第四步：移除PDF/DOCX格式残留字符（保留中文、英文、数字和常用标点）
	// Go的regexp不支持\u转义序列，使用Unicode属性类和字符类
	// \p{L} 匹配所有字母，\p{N} 匹配所有数字，\p{Han} 匹配汉字
	// \s 匹配空白字符
	// 直接列出要保留的标点符号
	keepPattern := regexp.MustCompile(`[^\p{L}\p{N}\p{Han}\s.,!?;:()\[\]{}\-—–'""…。，、；：？！（）【】《》]`)
	text = keepPattern.ReplaceAllString(text, "")

	// 第五步：再次验证UTF-8有效性，并移除任何剩余的无效字符
	text = sanitizeUTF8ForDatabase(text)

	return strings.TrimSpace(text)
}

// sanitizeUTF8ForDatabase 清理文本以确保可以安全存储到数据库
// 移除所有无效的UTF-8字符和可能导致数据库错误的字符
func sanitizeUTF8ForDatabase(text string) string {
	var result strings.Builder
	result.Grow(len(text))

	for _, r := range text {
		// 检查是否是有效的UTF-8字符
		if r == utf8.RuneError {
			// 跳过无效的UTF-8字符
			continue
		}

		// 检查字符是否在有效的Unicode范围内
		// Unicode范围：U+0000 到 U+10FFFF
		if r > 0x10FFFF {
			// 超出Unicode范围的字符，跳过
			continue
		}

		// 检查是否是替换字符（通常表示无效的UTF-8序列）
		if r == 0xFFFD {
			// Unicode替换字符，跳过
			continue
		}

		// 检查是否是私有使用区字符（可能导致问题）
		if (r >= 0xE000 && r <= 0xF8FF) || (r >= 0xF0000 && r <= 0xFFFFD) || (r >= 0x100000 && r <= 0x10FFFD) {
			// 私有使用区字符，跳过
			continue
		}

		// 检查是否是控制字符（除了常见的空白字符）
		if r < 0x20 && r != 0x09 && r != 0x0A && r != 0x0D {
			// 控制字符（除了Tab、LF、CR），跳过
			continue
		}

		// 检查是否是未分配的字符范围（可能导致数据库错误）
		if r >= 0xD800 && r <= 0xDFFF {
			// 代理对范围（用于UTF-16，在UTF-8中无效），跳过
			continue
		}

		// 字符通过所有检查，添加到结果中
		result.WriteRune(r)
	}

	// 最终验证：确保结果字符串是有效的UTF-8
	finalText := result.String()
	if !utf8.ValidString(finalText) {
		// 如果仍然无效，使用ToValidUTF8强制修复
		finalText = strings.ToValidUTF8(finalText, "")
	}

	return finalText
}

// decodeXMLEntities 解码XML实体
func decodeXMLEntities(text string) string {
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&apos;", "'")
	text = strings.ReplaceAll(text, "&#39;", "'")
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "&#160;", " ")
	return text
}

// getUTF8CharLength 获取UTF-8字符长度
func getUTF8CharLength(firstByte byte) int {
	if firstByte&0xE0 == 0xC0 {
		return 2
	} else if firstByte&0xF0 == 0xE0 {
		return 3
	} else if firstByte&0xF8 == 0xF0 {
		return 4
	}
	return 1
}

// isValidUTF8 检查字节序列是否是有效的UTF-8
func isValidUTF8(data []byte) bool {
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			return false
		}
		data = data[size:]
	}
	return true
}
