package pdf

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gen2brain/go-fitz"
	"github.com/gin-gonic/gin"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
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

	// 6. 验证文件扩展名
	ext := strings.ToLower(filepath.Ext(file.Filename))
	supportedExts := []string{".pdf", ".txt", ".md", ".docx", ".doc", ".rtf"}
	isSupported := false
	for _, supportedExt := range supportedExts {
		if ext == supportedExt {
			isSupported = true
			break
		}
	}
	if !isSupported {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("不支持的文件格式，支持格式: .pdf, .txt, .md, .docx, .doc, .rtf"), nil)
		return
	}

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

	// 13. 根据文件类型提取文本
	var text string
	var content string
	switch ext {
	case ".pdf":
		text, _, err = extractTextFromPDF(fileData)
		if err != nil {
			log.C(c).Errorw("PDF文本提取失败", "error", err, "filename", fileName)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("PDF文本提取失败: %s", err.Error()), nil)
			return
		}
		// PDF 使用专门的格式化函数
		text = formatPdfText(text)
	case ".txt", ".md":
		// 纯文本文件，直接读取
		text = string(fileData)
		// 验证UTF-8编码
		if !utf8.ValidString(text) {
			text = strings.ToValidUTF8(text, "")
		}
		text = formatText(text)
	case ".docx":
		text, err = extractTextFromDOCX(fileData)
		if err != nil {
			log.C(c).Errorw("DOCX文本提取失败", "error", err, "filename", fileName)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("DOCX文本提取失败: %s", err.Error()), nil)
			return
		}
		text = formatText(text)
	case ".doc":
		text, err = extractTextFromDOC(fileData)
		if err != nil {
			log.C(c).Errorw("DOC文本提取失败", "error", err, "filename", fileName)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("DOC文本提取失败: %s（建议转换为DOCX格式）", err.Error()), nil)
			return
		}
		text = formatText(text)
	case ".rtf":
		text, err = extractTextFromRTF(fileData)
		if err != nil {
			log.C(c).Errorw("RTF文本提取失败", "error", err, "filename", fileName)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("RTF文本提取失败: %s", err.Error()), nil)
			return
		}
		text = formatText(text)
	default:
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("不支持的文件格式"), nil)
		return
	}

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

	// 2. 验证文件扩展名
	ext := strings.ToLower(filepath.Ext(file.Filename))
	supportedExts := []string{".pdf", ".txt", ".md", ".docx", ".doc"}
	isSupported := false
	for _, supportedExt := range supportedExts {
		if ext == supportedExt {
			isSupported = true
			break
		}
	}
	if !isSupported {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("不支持的文件格式，支持格式: .pdf, .txt, .md, .docx, .doc"), nil)
		return
	}

	// 3. 验证文件大小
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

	// 5. 根据文件类型提取文本
	var text string
	switch ext {
	case ".pdf":
		text, _, err = extractTextFromPDF(fileData)
		if err != nil {
			log.C(c).Errorw("PDF文本提取失败", "error", err, "filename", file.Filename)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("PDF文本提取失败: %s", err.Error()), nil)
			return
		}
		text = formatPdfText(text)
	case ".txt", ".md":
		text = string(fileData)
		if !utf8.ValidString(text) {
			text = strings.ToValidUTF8(text, "")
		}
		text = formatText(text)
	case ".docx":
		text, err = extractTextFromDOCX(fileData)
		if err != nil {
			log.C(c).Errorw("DOCX文本提取失败", "error", err, "filename", file.Filename)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("DOCX文本提取失败: %s", err.Error()), nil)
			return
		}
		text = formatText(text)
	case ".doc":
		text, err = extractTextFromDOC(fileData)
		if err != nil {
			log.C(c).Errorw("DOC文本提取失败", "error", err, "filename", file.Filename)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("DOC文本提取失败: %s（建议转换为DOCX格式）", err.Error()), nil)
			return
		}
		text = formatText(text)
	default:
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("不支持的文件格式"), nil)
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

// extractTextFromPDF 尝试使用 Python 增强解析，如果失败则降级到 go-fitz
func extractTextFromPDF(data []byte) (string, int, error) {
	// 1. 尝试使用 Python 增强解析 (PyMuPDF blocks 模式)
	text, pages, err := extractTextFromPDFEnhanced(data)
	if err == nil && text != "" {
		log.Infow("Successfully extracted PDF using enhanced Python parser", "pages", pages)
		return text, pages, nil
	}

	// 2. 如果增强解析失败或未安装环境，降级到原有的 go-fitz 解析
	log.Warnw("Enhanced PDF parsing failed, falling back to legacy go-fitz", "error", err)
	return extractTextFromPDFLegacy(data)
}

// extractTextFromPDFEnhanced 使用外部 Python 脚本进行高质量解析
func extractTextFromPDFEnhanced(data []byte) (string, int, error) {
	// 创建临时文件
	tmpFile, err := os.CreateTemp("", "pdf_upload_*.pdf")
	if err != nil {
		return "", 0, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := tmpFile.Write(data); err != nil {
		return "", 0, fmt.Errorf("failed to write data to temp file: %w", err)
	}

	// 执行 Python 脚本
	// 注意：脚本路径改为通用解析器
	scriptPath := "/app/scripts/document_parser.py"
	// 如果是本地开发环境，尝试相对路径
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		scriptPath = "scripts/document_parser.py"
	}

	cmd := exec.Command("python3", scriptPath, tmpFile.Name())
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", 0, fmt.Errorf("python script execution failed: %v, stderr: %s", err, stderr.String())
	}

	// 解析输出的 JSON
	var result struct {
		Success   bool   `json:"success"`
		Content   string `json:"content"`
		PageCount int    `json:"page_count"`
		Error     string `json:"error"`
	}

	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		return "", 0, fmt.Errorf("failed to parse python output: %w", err)
	}

	if !result.Success {
		return "", 0, fmt.Errorf("python extraction error: %s", result.Error)
	}

	return result.Content, result.PageCount, nil
}

// extractTextFromPDFLegacy 使用原本的 go-fitz 提取 PDF 文本（作为降级方案）
func extractTextFromPDFLegacy(data []byte) (string, int, error) {
	// 从内存中打开PDF文档
	doc, err := fitz.NewFromMemory(data)
	if err != nil {
		return "", 0, fmt.Errorf("无法打开PDF文档: %w", err)
	}
	defer doc.Close()

	var result strings.Builder
	numPages := doc.NumPage()

	// 逐页提取文本
	for i := 0; i < numPages; i++ {
		pageText, err := doc.Text(i)
		if err != nil {
			log.Infow("PDF页面文本提取失败", "page", i+1, "error", err)
			continue
		}

		if pageText != "" {
			pageText = strings.TrimRight(pageText, " \t")
			if pageText != "" {
				result.WriteString(pageText)
				if i < numPages-1 {
					result.WriteString("\n")
				}
			}
		}
	}

	text := result.String()
	if text == "" {
		return "", 0, fmt.Errorf("未能从PDF中提取到文本")
	}

	return text, numPages, nil
}

// formatPdfText 格式化PDF文本，保留原格式但清理多余空格和空行，并合并被截断的段落
func formatPdfText(text string) string {
	// 第一步：确保字符串是有效的UTF-8
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "")
	}

	// 第二步：移除控制字符（保留换行符、制表符等有用的空白字符）
	controlCharPattern := regexp.MustCompile(`[\x00-\x08\x0B-\x0C\x0E-\x1F\x7F-\x9F]`)
	text = controlCharPattern.ReplaceAllString(text, "")

	// 第三步：规范化换行符
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	// 第四步：初步清理行 & 垃圾行过滤（核心修复：去除二进制乱码）
	lines := strings.Split(text, "\n")
	var cleanLines []string
	for _, line := range lines {
		// 清理行内多个连续空格
		line = regexp.MustCompile(`[ \t]+`).ReplaceAllString(line, " ")
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		// 过滤垃圾行
		if isGarbageLine(line) {
			continue
		}

		cleanLines = append(cleanLines, line)
	}

	// 第五步：段落合并（核心优化：减少token占用的关键）
	text = mergeParagraphs(cleanLines)

	// 第六步：清理多余空行（3个以上合并为2个）
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")

	// 第七步：过滤非必要字符
	// 扩大范围保留更多有意义的符号，但去除纯乱码
	// 保留所有 Unicode 字母(L)、数字(N)、标点(P)、符号(S)和空白(Z/C)
	keepPattern := regexp.MustCompile(`[^\p{L}\p{N}\p{P}\p{S}\s]`)
	text = keepPattern.ReplaceAllString(text, "")

	// 第八步：最终清理
	text = strings.TrimSpace(text)

	return text
}

// isGarbageLine 判断是否为垃圾行（乱码、二进制残留）
func isGarbageLine(line string) bool {
	totalCount := 0
	validCount := 0  // CJK, Letters, Numbers
	symbolCount := 0 // Crazy symbols

	for _, r := range line {
		if unicode.IsSpace(r) {
			continue
		}
		totalCount++

		if unicode.Is(unicode.Han, r) || unicode.IsLetter(r) || unicode.IsNumber(r) {
			validCount++
		} else if unicode.IsPunct(r) {
			// 标点符号中性，不计数为有效也不计数为垃圾，除非纯标点
		} else if unicode.IsSymbol(r) {
			symbolCount++
		} else {
			// 其他未识别字符视为潜在垃圾
			symbolCount++
		}
	}

	if totalCount == 0 {
		return true
	}

	// 规则1：如果有效字符（字母/数字/中文）比例过低 (< 30%) 且行有一定长度
	// 适用于：大量的乱码符号行，例如 "#@!$%^&*()..."
	if totalCount > 10 && float64(validCount)/float64(totalCount) < 0.3 {
		return true
	}

	// 规则2：如果特殊符号占比过高 (> 50%)
	if totalCount > 5 && float64(symbolCount)/float64(totalCount) > 0.5 {
		return true
	}

	// 规则3：检测 "cid:" 乱码（PDF字体映射错误常见特征）
	if strings.Contains(line, "cid:") || strings.Contains(line, "(cid:") {
		return true
	}

	// 规则4：检测长串无空格的非CJK字符串（类似于base64或二进制dump）
	// 仅在不包含任何中文的情况下应用
	hasCJK := false
	for _, r := range line {
		if unicode.Is(unicode.Han, r) {
			hasCJK = true
			break
		}
	}

	if !hasCJK && len(line) > 50 && !strings.Contains(line, " ") {
		// 例外：保留可能的长URL
		if !strings.HasPrefix(line, "http") {
			return true
		}
	}

	// 规则5：检测连续的重复字符（如 ".............." 或 "__________"）
	// 这种情况常用于排版，但在LLM上下文中通常是噪音
	// 简单的检测：如果去重后的字符种类很少且长度很长
	uniqueChars := make(map[rune]struct{})
	for _, r := range line {
		uniqueChars[r] = struct{}{}
	}
	if len(line) > 20 && len(uniqueChars) < 3 {
		return true
	}

	return false
}

// mergeParagraphs 智能合并被断行的段落
func mergeParagraphs(lines []string) string {
	var result strings.Builder
	var pendingLine string // 当前正在构建的段落（可能包含多行）

	for _, line := range lines {
		// 1. 如果是空行，表示段落分隔
		if line == "" {
			if pendingLine != "" {
				result.WriteString(pendingLine)
				result.WriteString("\n\n") // 段落结束
				pendingLine = ""
			}
			continue
		}

		// 2. 简单的页码/页眉页脚过滤（数字或极短且包含Page等关键词）
		// 比如 "Page 1", "1 / 20"
		if isPageFooter(line) {
			continue
		}

		// 3. 处理 pendingLine
		if pendingLine == "" {
			pendingLine = line
			continue
		}

		// 4. 判断是否合并
		// 查看 pendingLine 的结尾和 line 的开头
		if shouldMerge(pendingLine, line) {
			// 需要合并
			// 中文与中文之间不需要空格，其他情况通常需要空格
			sep := " "
			if isCJKEnd(pendingLine) && isCJKStart(line) {
				sep = ""
			}
			pendingLine += sep + line
		} else {
			// 不需要合并，当前 pendingLine 结束
			result.WriteString(pendingLine)
			result.WriteString("\n") // 换行，但不一定是段落结束，可能是列表项
			pendingLine = line
		}
	}

	// 处理最后遗留的 line
	if pendingLine != "" {
		result.WriteString(pendingLine)
	}

	return result.String()
}

// shouldMerge 判断两行是否应该合并
func shouldMerge(prev, next string) bool {
	// 这里是一些启发式规则

	// 1. 如果下一行是列表项（数字或符号开头），不合并
	if isListItem(next) {
		return false
	}

	// 2. 如果上一行以结束性标点结尾，不合并
	// 英文: . ? ! : ;
	// 中文: 。 ？ ！ ： ；
	if isSentenceEnd(prev) {
		return false
	}

	// 3. 如果上一行非常短（可能是标题），不合并
	// 这个阈值需要斟酌，比如 "Chapter 1"
	// 但如果是长句被切断，上一行（切断部分）可能很长，下一行可能短
	// 这里的prev是"已经累积的行"，可能会很长。
	// 我们只应该看"prev原本的最后一行"。但在当前逻辑里，prev是累积的。
	// 风险：如果标题很长被切分了，会被合并。这通常是可以接受的。
	// 风险：如果标题短，且没有标点（通常如此），会被合并到正文。
	// 例如： "Introduction" (换行) "This is..." -> "Introduction This is..."
	// 这样其实还好，比断行好。

	// 4. 如果下一行是大写字母开头，且上一行不是以连字符结尾
	// 英文中，虽然句子开头大写，但专有名词也大写。
	// 如果 prev 没有结束符，且 next 大写，有可能是新句子（如果prev漏了标点？），也有可能只是专有名词。
	// 倾向于合并，除非看起来像标题。

	return true
}

func isListItem(s string) bool {
	// 匹配 "1.", "1)", "•", "-", "* "
	matched, _ := regexp.MatchString(`^(\d+[\.\)]|•|\-|\*|·)\s`, s)
	return matched
}

func isSentenceEnd(s string) bool {
	if s == "" {
		return false
	}
	// 获取最后一个字符（注意 rune）
	r, _ := utf8.DecodeLastRuneInString(s)
	switch r {
	case '.', '!', '?', ';', ':': // 英文
		return true
	case '。', '！', '？', '；', '：': // 中文
		return true
		// 注意：不包括逗号，逗号应该合并
	}
	return false
}

func isCJKEnd(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.Is(unicode.Han, r)
}

func isCJKStart(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.Is(unicode.Han, r)
}

func isPageFooter(s string) bool {
	// 纯数字
	if _, err := strconv.Atoi(s); err == nil {
		return true
	}
	// Page X of Y
	if strings.HasPrefix(strings.ToLower(s), "page") && len(s) < 20 {
		return true
	}
	// 极短的非标点内容?
	return false
}

// formatText 格式化文本（用于非PDF格式），保留原格式但清理多余空格和空行
func formatText(text string) string {
	// 第一步：确保字符串是有效的UTF-8
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "")
	}

	// 第二步：规范化换行符（统一为\n）
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	// 第三步：清理行内多余空格（多个连续空格合并为1个）
	lines := strings.Split(text, "\n")
	var formattedLines []string
	for _, line := range lines {
		// 清理行内多个连续空格
		line = regexp.MustCompile(`[ \t]+`).ReplaceAllString(line, " ")
		// 移除行首行尾空格
		line = strings.TrimSpace(line)
		formattedLines = append(formattedLines, line)
	}
	text = strings.Join(formattedLines, "\n")

	// 第四步：清理多余空行（3个以上连续换行符合并为2个）
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")

	// 第五步：最终清理 - 移除行首行尾空白
	text = strings.TrimSpace(text)

	return text
}

// extractTextFromDOCX 从DOCX文件中提取文本，优先使用 Python 增强解析
func extractTextFromDOCX(data []byte) (string, error) {
	// 1. 尝试使用 Python 增强解析 (python-docx)
	text, _, err := extractTextFromPDFEnhanced(data) // 复用已有的外部脚本执行逻辑
	if err == nil && text != "" {
		log.Infow("Successfully extracted DOCX using enhanced Python parser")
		return text, nil
	}

	// 2. 降级方案
	log.Warnw("Enhanced DOCX parsing failed, falling back to legacy XML parser", "error", err)
	return extractTextFromDOCXLegacy(data)
}

// extractTextFromDOCXLegacy 原有的 DOCX XML 解析逻辑
func extractTextFromDOCXLegacy(data []byte) (string, error) {
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

	return text, nil
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

// decodeXMLEntities 解码XML实体
func decodeXMLEntities(text string) string {
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&apos;", "'")
	return text
}

// extractTextFromDOC 从旧版Word文档(.doc)中提取文本，优先使用 Python 增强解析
func extractTextFromDOC(data []byte) (string, error) {
	// 1. 尝试使用 Python 增强解析 (antiword)
	text, _, err := extractTextFromPDFEnhanced(data) // 复用已有的外部脚本执行逻辑
	if err == nil && text != "" {
		log.Infow("Successfully extracted DOC using enhanced Python parser (antiword)")
		return text, nil
	}

	// 2. 降级方案
	log.Warnw("Enhanced DOC parsing failed, falling back to legacy printable text extraction", "error", err)
	return extractTextFromDOCLegacy(data)
}

// extractTextFromDOCLegacy 原有的简单二进制文本提取逻辑
func extractTextFromDOCLegacy(data []byte) (string, error) {
	// .doc格式是OLE2格式，解析比较复杂
	// 这里使用简单的文本提取方法
	text := extractPrintableText(data)
	if text == "" {
		return "", fmt.Errorf("无法从DOC文件中提取文本，建议转换为DOCX格式")
	}
	return text, nil
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

	return text, nil
}

// extractPrintableText 从二进制数据中提取可打印文本（用于DOC等格式）
func extractPrintableText(data []byte) string {
	var result strings.Builder
	var currentWord strings.Builder

	for i := 0; i < len(data); i++ {
		b := data[i]

		// 检查是否是ASCII可打印字符或UTF-8字符的开始
		if (b >= 32 && b < 127) || (b >= 0xC0 && b < 0xF8) {
			// 尝试读取UTF-8字符
			if b >= 0xC0 {
				// UTF-8多字节字符
				var utf8Bytes []byte
				if b < 0xE0 {
					// 2字节UTF-8
					if i+1 < len(data) {
						utf8Bytes = data[i : i+2]
						i++
					}
				} else if b < 0xF0 {
					// 3字节UTF-8
					if i+2 < len(data) {
						utf8Bytes = data[i : i+3]
						i += 2
					}
				} else {
					// 4字节UTF-8
					if i+3 < len(data) {
						utf8Bytes = data[i : i+4]
						i += 3
					}
				}

				if len(utf8Bytes) > 0 {
					if utf8.Valid(utf8Bytes) {
						currentWord.Write(utf8Bytes)
					}
				}
			} else {
				// ASCII字符
				currentWord.WriteByte(b)
			}
		} else if b == 0x20 || b == 0x0A || b == 0x0D || b == 0x09 {
			// 空白字符，结束当前词
			if currentWord.Len() > 0 {
				result.WriteString(currentWord.String())
				currentWord.Reset()
			}
			if b == 0x20 {
				result.WriteByte(' ')
			} else if b == 0x0A {
				result.WriteByte('\n')
			}
		} else {
			// 其他字符，结束当前词
			if currentWord.Len() > 0 {
				result.WriteString(currentWord.String())
				currentWord.Reset()
			}
		}
	}

	// 添加最后一个词
	if currentWord.Len() > 0 {
		result.WriteString(currentWord.String())
	}

	return result.String()
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
