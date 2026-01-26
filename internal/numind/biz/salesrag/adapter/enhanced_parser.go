package adapter

import (
	"archive/zip"
	"bytes"
	"context"
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
	"unicode"
	"unicode/utf8"

	"numind-server/internal/pkg/log"

	"github.com/gen2brain/go-fitz"
)

// EnhancedParser 增强型文档解析器
// 移植自 SOP PdfController，支持 Python 增强解析和文本清洗
type EnhancedParser struct{}

func NewEnhancedParser() *EnhancedParser {
	return &EnhancedParser{}
}

func (p *EnhancedParser) Parse(ctx context.Context, file io.Reader, filename string) (string, error) {
	// 1. 读取全部内容到内存
	var data []byte
	var err error

	if file != nil {
		data, err = io.ReadAll(file)
	} else if filename != "" {
		data, err = os.ReadFile(filename)
	} else {
		return "", fmt.Errorf("no input file or filename provided")
	}

	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	// 2. 根据扩展名分发
	ext := strings.ToLower(filepath.Ext(filename))
	var text string

	switch ext {
	case ".pdf":
		text, _, err = p.extractTextFromPDF(data)
		if err != nil {
			return "", fmt.Errorf("PDF parsing failed: %w", err)
		}
		// PDF 使用专门的清洗逻辑
		text = p.formatPdfText(text)

	case ".docx":
		text, err = p.extractTextFromDOCX(data)
		if err != nil {
			return "", fmt.Errorf("DOCX parsing failed: %w", err)
		}
		text = p.formatText(text)

	case ".doc":
		text, err = p.extractTextFromDOC(data)
		if err != nil {
			return "", fmt.Errorf("DOC parsing failed: %w", err)
		}
		text = p.formatText(text)

	case ".rtf":
		text, err = p.extractTextFromRTF(data)
		if err != nil {
			return "", fmt.Errorf("RTF parsing failed: %w", err)
		}
		text = p.formatText(text)

	case ".txt", ".md":
		text = string(data)
		text = p.formatText(text)

	default:
		// 默认当做文本尝试处理
		text = string(data)
		text = p.formatText(text)
	}

	// 3. 最终 UTF-8 清洗确保安全
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "")
	}

	return text, nil
}

// ====================================================================================
// 以下代码移植自 internal/numind/controller/v1/pdf/pdf.go
// ====================================================================================

// extractTextFromPDF 尝试使用 Python 增强解析，如果失败则降级到 go-fitz
func (p *EnhancedParser) extractTextFromPDF(data []byte) (string, int, error) {
	// 1. 尝试使用 Python 增强解析 (PyMuPDF blocks 模式)
	text, pages, err := p.extractTextFromPDFEnhanced(data)
	if err == nil && text != "" {
		log.Infow("Successfully extracted PDF using enhanced Python parser", "pages", pages)
		return text, pages, nil
	}

	// 2. 如果增强解析失败或未安装环境，降级到原有的 go-fitz 解析
	log.Warnw("Enhanced PDF parsing failed, falling back to legacy go-fitz", "error", err)
	return p.extractTextFromPDFLegacy(data)
}

// extractTextFromPDFEnhanced 使用外部 Python 脚本进行高质量解析
func (p *EnhancedParser) extractTextFromPDFEnhanced(data []byte) (string, int, error) {
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
	// 脚本路径通常在 scripts/document_parser.py
	scriptPath := "scripts/document_parser.py"
	// 尝试绝对路径或其他位置判定 (简单起见先假设在工作目录下或 app/scripts)
	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		// 尝试 Docker 容器内的常见路径
		scriptPath = "/app/scripts/document_parser.py"
		if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
			return "", 0, fmt.Errorf("python parser script not found")
		}
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
func (p *EnhancedParser) extractTextFromPDFLegacy(data []byte) (string, int, error) {
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
func (p *EnhancedParser) formatPdfText(text string) string {
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
		if p.isGarbageLine(line) {
			continue
		}

		cleanLines = append(cleanLines, line)
	}

	// 第五步：段落合并（核心优化：减少token占用的关键）
	text = p.mergeParagraphs(cleanLines)

	// 第六步：清理多余空行（3个以上合并为2个）
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")

	// 第七步：过滤非必要字符
	keepPattern := regexp.MustCompile(`[^\p{L}\p{N}\p{P}\p{S}\s]`)
	text = keepPattern.ReplaceAllString(text, "")

	// 第八步：最终清理
	text = strings.TrimSpace(text)

	return text
}

// isGarbageLine 判断是否为垃圾行（乱码、二进制残留）
func (p *EnhancedParser) isGarbageLine(line string) bool {
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
			// 标点符号中性
		} else if unicode.IsSymbol(r) {
			symbolCount++
		} else {
			symbolCount++
		}
	}

	if totalCount == 0 {
		return true
	}

	// 规则1：有效字符比例过低
	if totalCount > 10 && float64(validCount)/float64(totalCount) < 0.3 {
		return true
	}

	// 规则2：特殊符号占比过高
	if totalCount > 5 && float64(symbolCount)/float64(totalCount) > 0.5 {
		return true
	}

	// 规则3：检测 "cid:" 乱码
	if strings.Contains(line, "cid:") || strings.Contains(line, "(cid:") {
		return true
	}

	// 规则4：检测长串无空格的非CJK字符串
	hasCJK := false
	for _, r := range line {
		if unicode.Is(unicode.Han, r) {
			hasCJK = true
			break
		}
	}
	if !hasCJK && len(line) > 50 && !strings.Contains(line, " ") {
		if !strings.HasPrefix(line, "http") {
			return true
		}
	}

	// 规则5：检测连续的重复字符
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
func (p *EnhancedParser) mergeParagraphs(lines []string) string {
	var result strings.Builder
	var pendingLine string

	for _, line := range lines {
		if line == "" {
			if pendingLine != "" {
				result.WriteString(pendingLine)
				result.WriteString("\n\n")
				pendingLine = ""
			}
			continue
		}

		if p.isPageFooter(line) {
			continue
		}

		if pendingLine == "" {
			pendingLine = line
			continue
		}

		if p.shouldMerge(pendingLine, line) {
			sep := " "
			if p.isCJKEnd(pendingLine) && p.isCJKStart(line) {
				sep = ""
			}
			pendingLine += sep + line
		} else {
			result.WriteString(pendingLine)
			result.WriteString("\n")
			pendingLine = line
		}
	}

	if pendingLine != "" {
		result.WriteString(pendingLine)
	}

	return result.String()
}

func (p *EnhancedParser) shouldMerge(prev, next string) bool {
	if p.isListItem(next) {
		return false
	}
	if p.isSentenceEnd(prev) {
		return false
	}
	return true
}

func (p *EnhancedParser) isListItem(s string) bool {
	matched, _ := regexp.MatchString(`^(\d+[\.\)]|\u2022|\-|\*|·)\s`, s)
	return matched
}

func (p *EnhancedParser) isSentenceEnd(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	switch r {
	case '.', '!', '?', ';', ':':
		return true
	case '。', '！', '？', '；', '：':
		return true
	}
	return false
}

func (p *EnhancedParser) isCJKEnd(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	return unicode.Is(unicode.Han, r)
}

func (p *EnhancedParser) isCJKStart(s string) bool {
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.Is(unicode.Han, r)
}

func (p *EnhancedParser) isPageFooter(s string) bool {
	if _, err := strconv.Atoi(s); err == nil {
		return true
	}
	if strings.HasPrefix(strings.ToLower(s), "page") && len(s) < 20 {
		return true
	}
	return false
}

func (p *EnhancedParser) formatText(text string) string {
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "")
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := strings.Split(text, "\n")
	var formattedLines []string
	for _, line := range lines {
		line = regexp.MustCompile(`[ \t]+`).ReplaceAllString(line, " ")
		line = strings.TrimSpace(line)
		formattedLines = append(formattedLines, line)
	}
	text = strings.Join(formattedLines, "\n")
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")
	text = strings.TrimSpace(text)
	return text
}

// extractTextFromDOCX 从DOCX文件中提取文本，优先使用 Python 增强解析
func (p *EnhancedParser) extractTextFromDOCX(data []byte) (string, error) {
	text, _, err := p.extractTextFromPDFEnhanced(data)
	if err == nil && text != "" {
		log.Infow("Successfully extracted DOCX using enhanced Python parser")
		return text, nil
	}

	log.Warnw("Enhanced DOCX parsing failed, falling back to legacy XML parser", "error", err)
	return p.extractTextFromDOCXLegacy(data)
}

// extractTextFromDOCXLegacy 原有的 DOCX XML 解析逻辑
func (p *EnhancedParser) extractTextFromDOCXLegacy(data []byte) (string, error) {
	zipReader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("无法读取DOCX文件（ZIP格式错误）: %w", err)
	}

	var documentXML []byte
	found := false

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

	text, err := p.extractTextFromDOCXXML(documentXML)
	if err != nil {
		return "", fmt.Errorf("解析DOCX XML失败: %w", err)
	}
	return text, nil
}

func (p *EnhancedParser) extractTextFromDOCXXML(xmlData []byte) (string, error) {
	var result strings.Builder
	textPattern := regexp.MustCompile(`<w:t[^>]*>([^<]*)</w:t>`)
	matches := textPattern.FindAllStringSubmatch(string(xmlData), -1)

	for _, match := range matches {
		if len(match) > 1 {
			text := match[1]
			// 简单的XML实体解码（完整解码需要更复杂逻辑，这里暂时复用原逻辑思路）
			text = strings.ReplaceAll(text, "&lt;", "<")
			text = strings.ReplaceAll(text, "&gt;", ">")
			text = strings.ReplaceAll(text, "&amp;", "&")
			text = strings.ReplaceAll(text, "&apos;", "'")
			text = strings.ReplaceAll(text, "&quot;", "\"")
			result.WriteString(text)
			result.WriteString(" ")
		}
	}

	if result.Len() == 0 {
		text, err := p.parseDOCXXMLWithParser(xmlData)
		if err == nil && text != "" {
			return text, nil
		}
	}

	if result.Len() == 0 {
		return "", fmt.Errorf("无法从DOCX XML中提取文本")
	}

	return result.String(), nil
}

func (p *EnhancedParser) parseDOCXXMLWithParser(xmlData []byte) (string, error) {
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
		switch tok := token.(type) {
		case xml.StartElement:
			if tok.Name.Local == "t" {
				var text string
				if err := decoder.DecodeElement(&text, &tok); err == nil {
					result.WriteString(text)
					result.WriteString(" ")
				}
			}
		}
	}
	return result.String(), nil
}

// extractTextFromDOC 占位
func (p *EnhancedParser) extractTextFromDOC(data []byte) (string, error) {
	// 目前暂不支持，可以考虑调用 antiword 或 python textract，这里先返回错误或空
	return "", fmt.Errorf("DOC format not supported yet (convert to DOCX)")
}

// extractTextFromRTF 占位
func (p *EnhancedParser) extractTextFromRTF(data []byte) (string, error) {
	return "", fmt.Errorf("RTF format not supported yet")
}
