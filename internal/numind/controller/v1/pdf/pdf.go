package pdf

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/parser"
	"numind-server/internal/pkg/util"
)

// 常量定义
const (
	MaxFileSize          = 10 * 1024 * 1024 // 10MB
	MaxTextContentLength = 100000           // 文本内容最大长度
)

// PdfController PDF处理控制器
type PdfController struct{}

// NewPdfController 创建PDF控制器
func NewPdfController() *PdfController {
	return &PdfController{}
}

// ConvertToText 将文档转换为纯文本（支持 PDF、Word、TXT、MD、RTF 等格式）
// 同时保存文件到数据库和对象存储
// 必须提供 run_id 和 node_id 参数（通过 query 参数或 form 参数）
func (ctrl *PdfController) ConvertToText(c *gin.Context) {
	log.C(c).Infow("Document to text conversion called")

	// 1. 获取当前用户
	currentUser, exists := c.Get("current_user")
	if !exists {
		core.WriteResponse(c, errno.ErrUnauthorized.SetMessage("未找到用户信息"), nil)
		return
	}
	user := currentUser.(*model.User)

	// 2. 获取 run_id 和 node_id（优先从 form 参数获取，如果没有则从 query 参数获取）
	var runIDStr, nodeIDStr string
	if c.PostForm("run_id") != "" {
		runIDStr = c.PostForm("run_id")
	} else {
		runIDStr = c.Query("run_id")
	}
	if c.PostForm("node_id") != "" {
		nodeIDStr = c.PostForm("node_id")
	} else {
		nodeIDStr = c.Query("node_id")
	}

	// 3. 验证 run_id 和 node_id 参数
	if runIDStr == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("缺少必需参数: run_id"), nil)
		return
	}
	if nodeIDStr == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("缺少必需参数: node_id"), nil)
		return
	}

	runID, err := strconv.ParseUint(runIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的 run_id 参数"), nil)
		return
	}

	nodeID, err := strconv.ParseUint(nodeIDStr, 10, 32)
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的 node_id 参数"), nil)
		return
	}

	// 4. 验证 Run 是否属于当前用户
	ds := store.S
	run, err := ds.Sop().GetRun(uint(runID))
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("执行记录不存在"), nil)
		return
	}
	if run.UserID != user.ID {
		core.WriteResponse(c, errno.ErrForbidden.SetMessage("无权访问此执行记录"), nil)
		return
	}

	// 5. 获取上传的文件
	file, err := c.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("请上传文件"), nil)
		return
	}

	// 6. 获取文件扩展名（格式验证由 parser.DocumentParser 内部处理）
	ext := strings.ToLower(filepath.Ext(file.Filename))

	// 7. 验证文件大小
	if file.Size <= 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件为空"), nil)
		return
	}
	if file.Size > MaxFileSize {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件大小超过限制（最大%dMB）", MaxFileSize/(1024*1024)), nil)
		return
	}

	// 8. 验证文件名（防止路径遍历攻击）
	fileName := sanitizeFileName(file.Filename)
	if fileName == "" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("无效的文件名"), nil)
		return
	}

	// 9. 打开文件
	src, err := file.Open()
	if err != nil {
		log.C(c).Errorw("打开文件失败", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("打开文件失败"), nil)
		return
	}
	defer src.Close()

	// 10. 读取文件内容（限制大小，防止内存溢出）
	limitedReader := io.LimitReader(src, MaxFileSize+1)
	fileData, err := io.ReadAll(limitedReader)
	if err != nil {
		log.C(c).Errorw("读取文件失败", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("读取文件失败"), nil)
		return
	}

	// 检查是否超过限制
	if int64(len(fileData)) > MaxFileSize {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件大小超过限制"), nil)
		return
	}

	// 11. 生成安全的文件名和对象键（使用 run_id 和 node_id）
	timestamp := time.Now().UnixNano()
	safeFileName := fmt.Sprintf("pdf_file_%d_%d%s", user.ID, timestamp, ext)
	objectKey := fmt.Sprintf("pdf/%d/%d/%s", user.ID, runID, safeFileName)

	// 12. 上传到COS（带错误处理）
	var cosURL string
	if util.IsCOSEnabled() {
		cosURL, err = util.UploadBytesToCOS(c, objectKey, file.Header.Get("Content-Type"), fileData)
		if err != nil {
			log.C(c).Warnw("COS上传失败，继续处理", "error", err, "object_key", objectKey)
			cosURL = "" // 设置为空，表示未上传到COS
		} else {
			log.C(c).Infow("文件上传到COS成功", "cos_url", cosURL, "object_key", objectKey)
		}
	}

	// 13. 使用统一文档解析器提取文本（支持 pdf/txt/md/docx/doc/rtf/xlsx/pptx/html）
	dp := parser.NewDocumentParser()
	text, err := dp.Parse(c.Request.Context(), bytes.NewReader(fileData), file.Filename)
	if err != nil {
		log.C(c).Errorw("文档文本提取失败", "error", err, "filename", fileName)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("文档文本提取失败: %s", err.Error()), nil)
		return
	}
	var content string

	// 14. 验证提取结果
	if text == "" {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("未能从文件中提取到文本内容"), nil)
		return
	}

	// 15. 清理和验证文本，确保可以安全存储到数据库
	content = sanitizeUTF8ForDatabase(text)
	// 限制内容长度
	if len(content) > MaxTextContentLength {
		content = content[:MaxTextContentLength] + "...(内容过长已截断)"
	}

	// 最终验证：确保content是有效的UTF-8，防止数据库错误
	if content != "" {
		if !utf8.ValidString(content) {
			log.C(c).Warnw("Content包含无效UTF-8字符，进行清理", "filename", fileName)
			content = strings.ToValidUTF8(content, "")
			content = sanitizeUTF8ForDatabase(content)
		}
	}

	// 16. 创建数据库记录（绑定 user_id, run_id, node_id）
	runIDUint := uint(runID)
	nodeIDUint := uint(nodeID)
	sopFile := &model.SopFile{
		UserID:    user.ID,
		RunID:     &runIDUint,
		NodeID:    &nodeIDUint,
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

	// 17. 保存到数据库
	if err := ds.Sop().CreateFile(sopFile); err != nil {
		log.C(c).Errorw("创建文件记录失败", "error", err, "filename", fileName)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("保存文件记录失败: %s", err.Error()), nil)
		return
	}

	log.C(c).Infow("文件上传成功",
		"file_id", sopFile.ID,
		"filename", fileName,
		"run_id", runID,
		"node_id", nodeID,
		"size", file.Size,
		"cos_url", cosURL,
		"has_content", content != "")

	// 18. 返回结果（最简化格式，直接返回文本字符串）
	core.WriteResponse(c, nil, text)
}

// ExtractText 轻量文档转文本接口，不需要 run_id/node_id，不创建 SopFile 记录，不上传 COS。
// 支持格式: .pdf, .txt, .md, .docx, .doc
// 成功时直接返回提取的文本字符串。
func (ctrl *PdfController) ExtractText(c *gin.Context) {
	log.C(c).Infow("ExtractText called", "user_id", c.GetUint("userID"))

	// 1. 获取上传的文件
	file, err := c.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("请上传文件"), nil)
		return
	}

	// 2. 验证文件大小（格式验证由 parser.DocumentParser 内部处理）
	if file.Size <= 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件为空"), nil)
		return
	}
	if file.Size > MaxFileSize {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件大小超过限制（最大%dMB）", MaxFileSize/(1024*1024)), nil)
		return
	}

	// 4. 打开并读取文件内容
	src, err := file.Open()
	if err != nil {
		log.C(c).Errorw("打开文件失败", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("打开文件失败"), nil)
		return
	}
	defer src.Close()

	limitedReader := io.LimitReader(src, MaxFileSize+1)
	fileData, err := io.ReadAll(limitedReader)
	if err != nil {
		log.C(c).Errorw("读取文件失败", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("读取文件失败"), nil)
		return
	}
	if int64(len(fileData)) > MaxFileSize {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件大小超过限制"), nil)
		return
	}

	// 5. 使用统一文档解析器提取文本（支持 pdf/txt/md/docx/doc/rtf/xlsx/pptx/html）
	dp := parser.NewDocumentParser()
	text, err := dp.Parse(c.Request.Context(), bytes.NewReader(fileData), file.Filename)
	if err != nil {
		log.C(c).Errorw("文档文本提取失败", "error", err, "filename", file.Filename)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("文档文本提取失败: %s", err.Error()), nil)
		return
	}

	// 6. 验证提取结果
	if text == "" {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("未能从文件中提取到文本内容"), nil)
		return
	}

	// 7. 清理并截断文本
	text = sanitizeUTF8ForDatabase(text)
	if len(text) > MaxTextContentLength {
		text = text[:MaxTextContentLength] + "...(内容过长已截断)"
	}
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "")
		text = sanitizeUTF8ForDatabase(text)
	}

	log.C(c).Infow("ExtractText 成功", "filename", file.Filename, "text_len", len(text))
	core.WriteResponse(c, nil, text)
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

		// 保留其他有效字符
		result.WriteRune(r)
	}

	return result.String()
}
