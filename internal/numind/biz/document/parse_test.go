package document

import (
	"context"
	"errors"
	"testing"

	"numind-server/internal/pkg/errno"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClassifyContent(t *testing.T) {
	docxMime := "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	assert.Equal(t, kindText, classifyContent("text/markdown", "a.md"))
	assert.Equal(t, kindText, classifyContent("", "a.txt"))
	assert.Equal(t, kindHTML, classifyContent("text/html", "a.html"))
	assert.Equal(t, kindDocx, classifyContent(docxMime, "a.docx"))
	assert.Equal(t, kindOther, classifyContent("image/png", "a.png"))
}

func TestParseToMarkdown_Text(t *testing.T) {
	md, method, err := parseToMarkdown(context.Background(), []byte("# 标题\n正文"), "a.md", "text/markdown", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "direct", method)
	assert.Equal(t, "# 标题\n正文", md)
}

func TestParseToMarkdown_HTML(t *testing.T) {
	md, method, err := parseToMarkdown(context.Background(), []byte("<h1>Title</h1><p><b>bold</b></p>"), "a.html", "text/html", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "html", method)
	assert.Contains(t, md, "# Title")
	assert.Contains(t, md, "**bold**")
}

func TestParseToMarkdown_Docx_NoParserNoFallback_Fails(t *testing.T) {
	// v1: dp=nil + fb=nil → docx 无法解析 → ErrDocumentParseFailed（优雅领域错误，不 panic/500）。
	_, _, err := parseToMarkdown(context.Background(), []byte("PK..."), "a.docx",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil, nil)
	assert.True(t, errors.Is(err, errno.ErrDocumentParseFailed))
}

func TestParseToMarkdown_Other_NotEditable(t *testing.T) {
	_, _, err := parseToMarkdown(context.Background(), []byte("\x89PNG"), "a.png", "image/png", nil, nil)
	assert.True(t, errors.Is(err, errno.ErrDocumentNotEditable))
}
