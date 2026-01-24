package service

import (
	"context"
	"fmt"
	"io"

	"numind-server/internal/numind/biz/salesrag/port"
)

type IngestionService struct {
	parser port.DocumentParser
	tagger port.ContentTagger
	store  port.VectorStore
}

func NewIngestionService(p port.DocumentParser, t port.ContentTagger, s port.VectorStore) *IngestionService {
	return &IngestionService{
		parser: p,
		tagger: t,
		store:  s,
	}
}

// IngestDocument 处理文档导入: 解析 -> 打标 -> 存储
func (s *IngestionService) IngestDocument(ctx context.Context, docID uint, filename string, reader io.Reader) error {
	// 1. 解析
	chunks, err := s.parser.Parse(ctx, reader, filename)
	if err != nil {
		return fmt.Errorf("parse validation failed: %w", err)
	}

	// 2. 遍历切片进行打标 (串行或并行优化)
	for i := range chunks {
		chunks[i].DocumentID = docID
		chunks[i].ID = fmt.Sprintf("%d_c%d", docID, i) // Generate Chunk ID

		// Call LLM for tagging
		stages, tags, err := s.tagger.TagChunk(ctx, chunks[i].Content)
		if err != nil {
			// Log warning but maybe fallback to default?
			// For now, return error
			return fmt.Errorf("auto-tagging failed for chunk %d: %w", i, err)
		}
		chunks[i].SalesStage = stages
		chunks[i].Tags = tags
	}

	// 3. 存入向量库
	if err := s.store.Upsert(ctx, chunks); err != nil {
		return fmt.Errorf("vector store upsert failed: %w", err)
	}

	return nil
}
