package port

import (
	"context"
)

// ContentTagger 自动打标接口 (通常是大模型)
type ContentTagger interface {
	// TagChunk 为切片自动生成 Tags 和 Summary
	TagChunk(ctx context.Context, content string) (tags []string, summary string, err error)
}
