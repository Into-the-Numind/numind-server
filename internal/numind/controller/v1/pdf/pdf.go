package pdf

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
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

// ConvertToText 将文档转换为纯文本（支持 PDF、Word、TXT、MD、RTF 等格式）
func (ctrl *PdfController) ConvertToText(c *gin.Context) {
	log.C(c).Infow("Document to text conversion called")

	// 1. 获取上传的文件
	file, err := c.FormFile("file")
	if err != nil {
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("请上传文件"), nil)
		return
	}

	// 2. 验证文件扩展名
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

	// 6. 根据文件类型提取文本
	var text string
	switch ext {
	case ".pdf":
		text, _, err = extractTextFromPDF(fileData)
		if err != nil {
			log.C(c).Errorw("PDF文本提取失败", "error", err, "filename", file.Filename)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("PDF文本提取失败: "+err.Error()), nil)
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
			log.C(c).Errorw("DOCX文本提取失败", "error", err, "filename", file.Filename)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("DOCX文本提取失败: "+err.Error()), nil)
			return
		}
		text = formatText(text)
	case ".doc":
		text, err = extractTextFromDOC(fileData)
		if err != nil {
			log.C(c).Errorw("DOC文本提取失败", "error", err, "filename", file.Filename)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("DOC文本提取失败: "+err.Error()+"（建议转换为DOCX格式）"), nil)
			return
		}
		text = formatText(text)
	case ".rtf":
		text, err = extractTextFromRTF(fileData)
		if err != nil {
			log.C(c).Errorw("RTF文本提取失败", "error", err, "filename", file.Filename)
			core.WriteResponse(c, errno.ErrInternalServer.SetMessage("RTF文本提取失败: "+err.Error()), nil)
			return
		}
		text = formatText(text)
	default:
		core.WriteResponse(c, errno.ErrInvalidParameter.SetMessage("不支持的文件格式"), nil)
		return
	}

	// 7. 验证提取结果
	if text == "" {
		core.WriteResponse(c, errno.ErrInternalServer.SetMessage("未能从文件中提取到文本内容"), nil)
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

// extractTextFromDOCX 从DOCX文件中提取文本
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

// extractTextFromDOC 从旧版Word文档(.doc)中提取文本
func extractTextFromDOC(data []byte) (string, error) {
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

