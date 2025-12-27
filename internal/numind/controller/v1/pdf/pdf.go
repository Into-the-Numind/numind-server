package pdf

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gen2brain/go-fitz"
	"github.com/gin-gonic/gin"

	"numind-server/internal/pkg/core"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// PdfController PDF处理控制器
type PdfController struct{}

// NewPdfController 创建PDF控制器
func NewPdfController() *PdfController {
	return &PdfController{}
}

// ConvertToText 将PDF转换为纯文本
func (ctrl *PdfController) ConvertToText(c *gin.Context) {
	log.C(c).Infow("PDF to text conversion called")

	// 1. 获取上传的文件
	file, err := c.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("请上传PDF文件"), nil)
		return
	}

	// 2. 验证文件扩展名
	ext := strings.ToLower(filepath.Ext(file.Filename))
	if ext != ".pdf" {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("只支持PDF文件"), nil)
		return
	}

	// 3. 验证文件大小（不为0即可，不限制最大大小）
	if file.Size <= 0 {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("文件为空"), nil)
		return
	}

	// 4. 打开文件
	src, err := file.Open()
	if err != nil {
		log.C(c).Errorw("打开文件失败", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("打开文件失败"), nil)
		return
	}
	defer src.Close()

	// 5. 读取文件内容（不限制大小，但使用流式读取避免内存溢出）
	fileData, err := io.ReadAll(src)
	if err != nil {
		log.C(c).Errorw("读取文件失败", "error", err)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("读取文件失败"), nil)
		return
	}

	// 6. 提取PDF文本
	text, pageCount, err := extractTextFromPDF(fileData)
	if err != nil {
		log.C(c).Errorw("PDF文本提取失败", "error", err, "filename", file.Filename)
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("PDF文本提取失败: "+err.Error()), nil)
		return
	}

	// 7. 验证和格式化文本，确保UTF-8编码正确并保持原格式
	text = formatPdfText(text)
	if text == "" {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("未能从PDF中提取到文本内容"), nil)
		return
	}

	// 8. 返回结果（最简化格式，直接返回文本字符串）
	core.WriteResponse(c, nil, text)
}

// extractTextFromPDF 使用go-fitz提取PDF文本
func extractTextFromPDF(data []byte) (string, int, error) {
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
			// 如果某一页提取失败，记录错误但继续处理其他页
			log.Infow("PDF页面文本提取失败", "page", i+1, "error", err)
			continue
		}

		if pageText != "" {
			// 清理页面文本（移除行尾空格）
			pageText = strings.TrimRight(pageText, " \t")
			if pageText != "" {
				result.WriteString(pageText)
				// 页面之间添加单个换行符分隔
				if i < numPages-1 {
					result.WriteString("\n")
				}
			}
		}
	}

	text := result.String()
	if text == "" {
		return "", 0, fmt.Errorf("未能从PDF中提取到文本，可能是扫描版PDF或加密PDF")
	}

	return text, numPages, nil
}

// formatPdfText 格式化PDF文本，保留原格式但清理多余空格和空行
func formatPdfText(text string) string {
	// 第一步：确保字符串是有效的UTF-8
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "")
	}

	// 第二步：移除控制字符（保留换行符、制表符等有用的空白字符）
	// 移除NULL、垂直制表符等控制字符，但保留换行符(\n)、回车符(\r)、制表符(\t)
	controlCharPattern := regexp.MustCompile(`[\x00-\x08\x0B-\x0C\x0E-\x1F\x7F-\x9F]`)
	text = controlCharPattern.ReplaceAllString(text, "")

	// 第三步：规范化换行符（统一为\n）
	// 将Windows换行符(\r\n)和Mac换行符(\r)统一为Unix换行符(\n)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	// 第四步：清理行内多余空格（多个连续空格合并为1个）
	// 但保留换行符，所以需要逐行处理
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

	// 第五步：清理多余空行（3个以上连续换行符合并为2个）
	// 保留段落间的空行（2个换行符），但移除过多的空行
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")

	// 第六步：移除无效字符，保留中文、英文、数字和常用标点符号
	// 保留换行符、空格、制表符等空白字符
	keepPattern := regexp.MustCompile(`[^\p{L}\p{N}\p{Han}\s.,!?;:()\[\]{}\-—–'""…。，、；：？！（）【】《》\n\r\t]`)
	text = keepPattern.ReplaceAllString(text, "")

	// 第七步：最终清理 - 移除行首行尾空白
	text = strings.TrimSpace(text)

	// 第八步：再次验证UTF-8有效性
	if !utf8.ValidString(text) {
		// 如果仍然无效，进行更激进的清理
		var result strings.Builder
		for _, r := range text {
			if utf8.ValidRune(r) {
				result.WriteRune(r)
			}
		}
		text = result.String()
	}

	return text
}

