package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/util"
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
	parser     PipelineParser
	splitter   *MarkdownSplitter
	tagger     *ContentTagger
	docStore   DocumentStatusUpdater    // 文档状态更新器
	store      port.VectorStore         // 向量数据库
	chunkStore store.KnowledgeChunkStore // MySQL切片存储
	docChan    chan *domain.KnowledgeDocument
}

func NewIngestionPipeline(
	parser PipelineParser,
	splitter *MarkdownSplitter,
	tagger *ContentTagger,
	docStore DocumentStatusUpdater,
	store port.VectorStore,
	chunkStore store.KnowledgeChunkStore,
) *IngestionPipeline {
	p := &IngestionPipeline{
		parser:     parser,
		splitter:   splitter,
		tagger:     tagger,
		docStore:   docStore,
		store:      store,
		chunkStore: chunkStore,
		docChan:    make(chan *domain.KnowledgeDocument, 10),
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
		downloadURL := doc.FilePath

		// 尝试针对 COS 生成签名 URL，解决 403 问题
		// 解析 URL 获取 ObjectKey
		if u, err := url.Parse(doc.FilePath); err == nil {
			objectKey := strings.TrimPrefix(u.Path, "/")
			// 生成签名 URL (有效期 10 分钟)
			if signed, err := util.GenerateSignedURL(context.Background(), objectKey, 600); err == nil && signed != "" {
				downloadURL = signed
				log.Printf("Generated signed URL for doc %d: %s...", doc.ID, objectKey)
			}
		}

		// 下载文件内容
		resp, err := http.Get(downloadURL)
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

	// 使用原始文件名（而非 COS URL）进行解析，避免 URL 查询参数干扰扩展名识别
	log.Printf("DEBUG: Parsing document - ID: %d, Name: '%s', FilePath: '%s'", doc.ID, doc.Name, doc.FilePath)
	markdown, err := p.parser.Parse(ctx, fileReader, doc.Name)
	if err != nil {
		log.Printf("ERROR: Parsing failed for doc %d - Name: '%s', Error: %v", doc.ID, doc.Name, err)
		p.fail(doc, fmt.Errorf("parsing failed: %w", err))
		return
	}
	log.Printf("DEBUG: Parsing succeeded for doc %d, content length: %d", doc.ID, len(markdown))

	// 2. Split - 更新状态为 SPLITTING
	if p.docStore != nil {
		if err := p.docStore.UpdateStatus(ctx, doc.ID, string(domain.DocStatusSplitting), ""); err != nil {
			log.Printf("Failed to update status to SPLITTING: %v", err)
		}
	}

	log.Printf("Starting splitting for doc %d", doc.ID)

	chunks, err := p.splitter.Split(markdown)
	if err != nil {
		p.fail(doc, fmt.Errorf("splitting failed: %w", err))
		return
	}

	if len(chunks) == 0 {
		p.fail(doc, fmt.Errorf("no chunks generated from document"))
		return
	}

	log.Printf("Generated %d chunks for doc %d", len(chunks), doc.ID)

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
					}
	}

	// 4. Tag - 更新状态为 TAGGING
	if p.docStore != nil {
		if err := p.docStore.UpdateStatus(ctx, doc.ID, string(domain.DocStatusTagging), ""); err != nil {
			log.Printf("Failed to update status to TAGGING: %v", err)
		}
	}

	log.Printf("Starting tagging for %d chunks (doc %d)", len(kChunks), doc.ID)

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

	// 6. 先存储到MySQL（主数据源，优先写入）
	log.Printf("Storing %d chunks to MySQL for doc %d", len(kChunks), doc.ID)
	kChunksVal := make([]domain.KnowledgeChunk, len(kChunks))
	for i, c := range kChunks {
		kChunksVal[i] = *c
	}

	if err := p.storeChunksToMySQL(ctx, doc, kChunksVal); err != nil {
		p.fail(doc, fmt.Errorf("MySQL storage failed: %w", err))
		return
	}

	// 7. 再存储到向量数据库（用于语义搜索）
	log.Printf("Storing %d chunks to vector DB for doc %d", len(kChunks), doc.ID)
	err = p.store.Upsert(ctx, kChunksVal)
	if err != nil {
		// 标记向量化失败，但MySQL已有数据
		p.updateChunkEmbeddingStatus(ctx, doc.ID, "FAILED")
		p.fail(doc, fmt.Errorf("vector storage failed: %w", err))
		return
	}

	// 8. 标记向量化完成
	p.updateChunkEmbeddingStatus(ctx, doc.ID, "COMPLETED")

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

// storeChunksToMySQL 将切片存储到MySQL
func (p *IngestionPipeline) storeChunksToMySQL(ctx context.Context, doc *domain.KnowledgeDocument, chunks []domain.KnowledgeChunk) error {
	if p.chunkStore == nil {
		return fmt.Errorf("chunk store is not initialized")
	}

	modelChunks := make([]*model.KnowledgeChunk, len(chunks))
	for i, chunk := range chunks {
		modelChunks[i] = &model.KnowledgeChunk{
			DocumentID:      doc.ID,
			UserID:          doc.UserID,
			Sequence:        i,
			Content:         chunk.Content,
			Summary:         chunk.Summary,
			SourceRef:       chunk.SourceRef,
			Tags:            strings.Join(chunk.Tags, ","),
			VectorID:        chunk.ID, // 存储向量数据库ID
			EmbeddingStatus: "PENDING",
		}
	}
	return p.chunkStore.BatchCreate(ctx, modelChunks)
}

// updateChunkEmbeddingStatus 更新切片的向量化状态
func (p *IngestionPipeline) updateChunkEmbeddingStatus(ctx context.Context, docID uint, status string) {
	if p.chunkStore == nil {
		return
	}

	// 获取文档的所有切片
	chunks, err := p.chunkStore.ListByDocument(ctx, docID, 0)
	if err != nil {
		log.Printf("Failed to list chunks for status update: %v", err)
		return
	}

	// 批量更新状态
	for _, chunk := range chunks {
		updates := map[string]interface{}{"embedding_status": status}
		if err := p.chunkStore.UpdateColumns(ctx, chunk.ID, updates); err != nil {
			log.Printf("Failed to update chunk %d status: %v", chunk.ID, err)
		}
	}
}
