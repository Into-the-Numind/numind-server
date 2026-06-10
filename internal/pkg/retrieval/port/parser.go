package port

import (
	"context"
	"io"
	"numind-server/internal/pkg/retrieval/domain"
)

// DocumentParser 文档解析器接口
// 负责将二进制流解析为结构化的文本切片
type DocumentParser interface {
	Parse(ctx context.Context, file io.Reader, filename string) ([]domain.KnowledgeChunk, error)
}
