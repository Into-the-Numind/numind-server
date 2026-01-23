package adapter

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SimpleParser 基本文件解析器
type SimpleParser struct{}

func NewSimpleParser() *SimpleParser {
	return &SimpleParser{}
}

func (p *SimpleParser) Parse(ctx context.Context, file io.Reader, filename string) (string, error) {
	var content []byte
	var err error

	if file != nil {
		content, err = io.ReadAll(file)
	} else if filename != "" {
		content, err = os.ReadFile(filename)
	} else {
		return "", fmt.Errorf("no input file or filename provided")
	}

	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	// 简单扩展名检查
	switch filepath.Ext(filename) {
	case ".pdf":
		return "", fmt.Errorf("pdf parsing not implemented in SimpleParser")
	default:
		// 默认当做文本处理 (Markdown, Txt)
		return string(content), nil
	}
}
