package ingest

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

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	aismw "numind-server/internal/pkg/aiservice/middleware"
	"numind-server/internal/pkg/billing"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/port"
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

// TextSplitter 定义文本切分器接口
type TextSplitter interface {
	Split(text string) ([]SplitChunk, error)
}

// IngestionPipeline manages the end-to-end ingestion process
type IngestionPipeline struct {
	parser     PipelineParser
	splitter   TextSplitter // 使用接口而非具体类型
	tagger     *ContentTagger
	docStore   DocumentStatusUpdater     // 文档状态更新器
	store      port.VectorStore          // 向量数据库
	chunkStore store.KnowledgeChunkStore // MySQL切片存储
	docChan    chan *domain.KnowledgeDocument
}

func NewIngestionPipeline(
	parser PipelineParser,
	splitter TextSplitter,
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

// Submit 把文档入队等待异步处理。返回 false 表示队列已满、文档被丢弃
// （调用方据此告知用户重试，而非误报成功）。
func (p *IngestionPipeline) Submit(doc *domain.KnowledgeDocument) bool {
	select {
	case p.docChan <- doc:
		return true
	default:
		log.Printf("Pipeline queue full, dropping doc %d", doc.ID)
		return false
	}
}

func (p *IngestionPipeline) worker() {
	for doc := range p.docChan {
		// Inject userID and skip-legacy-billing so that the embedder closure
		// routed through the AI Gateway can perform per-user billing correctly.
		ctx := context.Background()
		ctx = aismw.WithUserID(ctx, doc.UserID)
		ctx = aiservice.WithSkipLegacyBilling(ctx)
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
	parsePath := doc.Name // 默认用于识别扩展名

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
	} else {
		// 如果不是 URL，pass FilePath 给 Parser，因为它需要真实路径进行 ReadFile
		parsePath = doc.FilePath
	}

	// 使用原始文件名或路径进行解析
	log.Printf("DEBUG: Parsing document - ID: %d, Name: '%s', FilePath: '%s'", doc.ID, doc.Name, doc.FilePath)
	markdown, err := p.parser.Parse(ctx, fileReader, parsePath)
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

	// 切块 + 留痕：若 splitter 实现 StrategyAwareSplitter(可选接口,照搬 docStore.UpdateColumns
	// 的类型断言模式),拿到用了语义还是兜底 + 原因,便于"语义静默失效"可被发现/统计/定位;
	// 否则降级用旧 Split()。SplitWithStrategy 不变式:永不返回 err、非空文本必非空 chunk。
	var chunks []SplitChunk
	var splitStrategy, splitDetail string
	if sa, ok := p.splitter.(StrategyAwareSplitter); ok {
		chunks, splitStrategy, splitDetail, _ = sa.SplitWithStrategy(markdown)
		if splitStrategy == StrategyFallback {
			log.Printf("WARN: doc %d chunked via rule-fallback (semantic unavailable/failed); detail=%q", doc.ID, splitDetail)
		}
	} else {
		chunks, err = p.splitter.Split(markdown)
		if err != nil {
			p.fail(doc, fmt.Errorf("splitting failed: %w", err))
			return
		}
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
			EmbedText:  sc.EmbedText, // 结构感知切块器注入面包屑；空则 store 回退 Content
			Tags:       tags,         // Use merged tags
		}
	}

	// 4. Tag - 更新状态为 TAGGING
	if p.docStore != nil {
		if err := p.docStore.UpdateStatus(ctx, doc.ID, string(domain.DocStatusTagging), ""); err != nil {
			log.Printf("Failed to update status to TAGGING: %v", err)
		}
	}

	log.Printf("Starting tagging for %d chunks (doc %d)", len(kChunks), doc.ID)

	tagCtx := billing.WithBilling(ctx, doc.UserID, "salesrag_tagging")
	err = p.tagger.TagChunks(tagCtx, kChunks)
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

	// 5b. 删旧块（幂等自清，防孤儿残留）：chunk ID 是位置式 <docID>_<seq>，重切块产出更少
	// 块时旧尾块（如 doc 重切 24→18，残留 _18.._23）会留在向量库且**可被检索到**→旧切块内容
	// 泄漏。Upsert 只 REPLACE 当前批 ID，不会清掉超出范围的旧块。故写入前按 docID 整删一次。
	// 首次入库时为 no-op；重切块/重试时清干净。best-effort：删失败只 warn 不杀入库（与
	// ReindexDocument/DeleteDocument 一致）。
	if err := p.store.DeleteByDocumentID(ctx, doc.ID); err != nil {
		log.Printf("WARN: pre-store vector cleanup for doc %d failed (possible orphan residue): %v", doc.ID, err)
	}
	if p.chunkStore != nil {
		if err := p.chunkStore.DeleteByDocument(ctx, doc.ID); err != nil {
			log.Printf("WARN: pre-store MySQL chunk cleanup for doc %d failed: %v", doc.ID, err)
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
			// 留痕:记录本文档用了哪种切块策略 + 原因(仅当 splitter 暴露了策略时;
			// 写入失败只记日志、绝不阻断入库——留痕是辅助不能反害主流程)。
			if splitStrategy != "" {
				updates["split_strategy"] = splitStrategy
				if splitDetail != "" { // 语义/no_split 成功时 detail 为空,不必写空串
					updates["split_detail"] = splitDetail
				}
			}
			if err := updater.UpdateColumns(ctx, doc.ID, updates); err != nil {
				log.Printf("Failed to update metadata to COMPLETED: %v", err)
			}
		} else {
			// Fallback to UpdateStatus(该 docStore 不支持 UpdateColumns → 无法持久化 strategy)
			if splitStrategy != "" {
				log.Printf("WARN: doc %d split_strategy=%q not persisted: docStore lacks UpdateColumns", doc.ID, splitStrategy)
			}
			if err := p.docStore.UpdateStatus(ctx, doc.ID, string(domain.DocStatusCompleted), ""); err != nil {
				log.Printf("Failed to update status to COMPLETED: %v", err)
			}
		}
	}
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
