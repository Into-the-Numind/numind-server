package document

import (
	"bytes"
	"context"
	"path"
	"strings"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/parser"

	md "github.com/JohannesKaufmann/html-to-markdown"
)

// docxFallback 是 docx 解析的兜底 seam（v2 注入 qwen-long 实现；v1 注入 nil）。
type docxFallback interface {
	ParseDocx(ctx context.Context, data []byte, filename string) (string, error)
}

type contentKind int

const (
	kindText contentKind = iota // md / txt / 纯文本
	kindHTML
	kindDocx
	kindOther
)

// classifyContent 按 mime 优先、扩展名兜底判定内容类型。
func classifyContent(mime, filename string) contentKind {
	base := ""
	if mime != "" {
		base = strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0]))
	}
	ext := strings.ToLower(path.Ext(filename))

	switch {
	case base == "text/markdown" || base == "text/plain" || ext == ".md" || ext == ".markdown" || ext == ".txt":
		return kindText
	case base == "text/html" || ext == ".html" || ext == ".htm":
		return kindHTML
	case base == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || ext == ".docx":
		return kindDocx
	default:
		return kindOther
	}
}

// parseToMarkdown 把源文件字节解析成可编辑 markdown。
// 返回 (markdown, parseMethod, err)。parseMethod ∈ direct|html|markitdown|qwen_long。
//   - text: 直接当 markdown/文本（direct）
//   - html: html-to-markdown（html）
//   - docx: DocumentParser/MarkItDown（markitdown）；失败且 fb!=nil 走兜底（qwen_long，v2）
//   - 其它: ErrDocumentNotEditable
func parseToMarkdown(ctx context.Context, data []byte, filename, mime string, dp *parser.DocumentParser, fb docxFallback) (string, string, error) {
	switch classifyContent(mime, filename) {
	case kindText:
		return string(data), "direct", nil

	case kindHTML:
		converter := md.NewConverter("", true, nil)
		out, err := converter.ConvertString(string(data))
		if err != nil {
			return "", "", errno.ErrDocumentParseFailed
		}
		return out, "html", nil

	case kindDocx:
		if dp != nil {
			text, err := dp.Parse(ctx, bytes.NewReader(data), filename)
			if err == nil && strings.TrimSpace(text) != "" {
				return text, "markitdown", nil
			}
		}
		// v1: fb 注入 nil → 直接 ParseFailed。v2: 注入 qwen-long 实现兜底。
		if fb != nil {
			if out, err := fb.ParseDocx(ctx, data, filename); err == nil && strings.TrimSpace(out) != "" {
				return out, "qwen_long", nil
			}
		}
		return "", "", errno.ErrDocumentParseFailed

	default:
		return "", "", errno.ErrDocumentNotEditable
	}
}
