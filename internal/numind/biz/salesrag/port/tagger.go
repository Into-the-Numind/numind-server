package port

import (
	"context"
	"numind-server/internal/numind/biz/salesrag/domain"
)

// ContentTagger 自动打标接口 (通常是大模型)
type ContentTagger interface {
	// TagChunk 为切片自动生成 DocType, SalesStage 和 Tags
	TagChunk(ctx context.Context, content string) (domain.DocType, []domain.SalesStage, []string, error)
}
