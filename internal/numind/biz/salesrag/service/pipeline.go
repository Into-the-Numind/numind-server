package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
)

// PipelineParser defines interface for parsing file to markdown
type PipelineParser interface {
	Parse(ctx context.Context, file io.Reader, filename string) (string, error)
}

// Embedder 定义向量化函数类型
type Embedder func(ctx context.Context, text string) ([]float32, error)

// DocumentStatusUpdater 定义文档状态更新接口
type DocumentStatusUpdater interface {
	UpdateStatus(ctx context.Context, id uint, status string, errorMsg string) error
}

// IngestionPipeline manages the end-to-end ingestion process
type IngestionPipeline struct {
	parser   PipelineParser
	splitter *MarkdownSplitter
	tagger   *ContentTagger
	docStore DocumentStatusUpdater // 文档状态更新器
	store    port.VectorStore
	docChan  chan *domain.KnowledgeDocument
}

func NewIngestionPipeline(
	parser PipelineParser,
	splitter *MarkdownSplitter,
	tagger *ContentTagger,
	docStore DocumentStatusUpdater,
	store port.VectorStore,
) *IngestionPipeline {
	p := &IngestionPipeline{
		parser:   parser,
		splitter: splitter,
		tagger:   tagger,
		docStore: docStore,
		store:    store,
		docChan:  make(chan *domain.KnowledgeDocument, 10),
	}
	// Start worker
	go p.worker()
	return p
}

func (p *IngestionPipeline) Submit(doc *domain.KnowledgeDocument) {
	select {
	case p.docChan <- doc:
	default:
		log.Printf("Pipeline queue full, dropping doc %d", doc.ID)
		// Update status to FAILED in DB if possible
	}
}

func (p *IngestionPipeline) worker() {
	for doc := range p.docChan {
		ctx := context.Background()
		p.process(ctx, doc)
	}
}

func (p *IngestionPipeline) process(ctx context.Context, doc *domain.KnowledgeDocument) {
	startTime := time.Now()
	log.Printf("Starting processing for doc %d", doc.ID)

	// 更新状态为 PARSING
	if p.docStore != nil {
		if err := p.docStore.UpdateStatus(ctx, doc.ID, string(domain.DocStatusParsing), ""); err != nil {
			log.Printf("Failed to update status to PARSING: %v", err)
		}
	}

	// 1. Parsing
	var fileReader io.Reader
	// 检查是否为云端存储的 URL
	if strings.HasPrefix(doc.FilePath, "http://") || strings.HasPrefix(doc.FilePath, "https://") {
		// 下载文件内容
		resp, err := http.Get(doc.FilePath)
		if err != nil {
			p.fail(doc, fmt.Errorf("failed to download file from URL: %w", err))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			p.fail(doc, fmt.Errorf("failed to download file, status code: %d", resp.StatusCode))
			return
		}

		// 读取全部内容到内存（对于通常的文档，这还可以接受）
		// 如果文件很大，可以考虑流式处理，但 SimpleParser 也是读全部
		content, err := io.ReadAll(resp.Body)
		if err != nil {
			p.fail(doc, fmt.Errorf("failed to read downloaded content: %w", err))
			return
		}
		fileReader = bytes.NewReader(content)
	}

	markdown, err := p.parser.Parse(ctx, fileReader, doc.FilePath) // fileReader 可能为 nil，如果不是 URL 且 fileReader 为 nil，parser 会尝试读本地文件（兼容旧数据）
	if err != nil {
		p.fail(doc, fmt.Errorf("parsing failed: %w", err))
		return
	}

	// 2. Split
	chunks, err := p.splitter.Split(markdown)
	if err != nil {
		p.fail(doc, fmt.Errorf("splitting failed: %w", err))
		return
	}

	if len(chunks) == 0 {
		p.fail(doc, fmt.Errorf("no chunks generated from document"))
		return
	}

	// 3. Convert SplitChunk to KnowledgeChunk
	kChunks := make([]*domain.KnowledgeChunk, len(chunks))
	for i, sc := range chunks {
		// 合并 Doc Tags 和 Chunk Headers
		tags := append([]string{}, doc.Tags...)
		tags = append(tags, sc.Headers...)

		kChunks[i] = &domain.KnowledgeChunk{
			ID:         fmt.Sprintf("%d_%d", doc.ID, i), // Temp ID
			DocumentID: doc.ID,
			UserID:     doc.UserID, // 传递用户ID用于数据隔离
			Content:    sc.Content,
			Tags:       tags, // Use merged tags
			DocType:    doc.Type,
			SalesStage: []domain.SalesStage{domain.StageDiscovery},
		}
	}

	// 4. Tag
	err = p.tagger.TagChunks(ctx, kChunks)
	if err != nil {
		p.fail(doc, fmt.Errorf("tagging failed: %w", err))
		return
	}

	// 5. Managed Embedding & Storage（托管向量化与存储）
	// 更新状态为 EMBEDDING (在托管模式下，存储过程即包含向量化)
	if p.docStore != nil {
		if err := p.docStore.UpdateStatus(ctx, doc.ID, string(domain.DocStatusEmbedding), ""); err != nil {
			log.Printf("Failed to update status to EMBEDDING: %v", err)
		}
	}

	log.Printf("Submitting %d chunks to store (Managed Vectorization)", len(kChunks))

	// 6. Store（存储）
	kChunksVal := make([]domain.KnowledgeChunk, len(kChunks))
	for i, c := range kChunks {
		kChunksVal[i] = *c
	}

	err = p.store.Upsert(ctx, kChunksVal)
	if err != nil {
		p.fail(doc, fmt.Errorf("storage failed: %w", err))
		return
	}

	// 更新状态为 COMPLETED，并回写 ChunkCount
	if p.docStore != nil {
		// 尝试使用 UpdateColumns 回写 metadata
		if updater, ok := p.docStore.(interface {
			UpdateColumns(ctx context.Context, id uint, updates map[string]interface{}) error
		}); ok {
			updates := map[string]interface{}{
				"status":      string(domain.DocStatusCompleted),
				"chunk_count": len(kChunks),
				"error_msg":   "",
			}
			if err := updater.UpdateColumns(ctx, doc.ID, updates); err != nil {
				log.Printf("Failed to update metadata to COMPLETED: %v", err)
			}
		} else {
			// Fallback to UpdateStatus
			if err := p.docStore.UpdateStatus(ctx, doc.ID, string(domain.DocStatusCompleted), ""); err != nil {
				log.Printf("Failed to update status to COMPLETED: %v", err)
			}
		}
	}
	log.Printf("Finished processing doc %d in %v. Stored %d chunks.", doc.ID, time.Since(startTime), len(kChunksVal))
	log.Printf("Finished processing doc %d in %v. Stored %d chunks.", doc.ID, time.Since(startTime), len(kChunksVal))
}

func (p *IngestionPipeline) fail(doc *domain.KnowledgeDocument, err error) {
	log.Printf("Failed processing doc %d: %v", doc.ID, err)
	// 更新状态为 FAILED
	if p.docStore != nil {
		ctx := context.Background()
		if updateErr := p.docStore.UpdateStatus(ctx, doc.ID, string(domain.DocStatusFailed), err.Error()); updateErr != nil {
			log.Printf("Failed to update status to FAILED: %v", updateErr)
		}
	}
}
