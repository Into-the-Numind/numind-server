package adapter

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/pkg/log"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

func init() {
	sqlite_vec.Auto()
}

// Ensure SQLiteVecStore implements VectorStore interface
var _ port.VectorStore = (*SQLiteVecStore)(nil)

// SQLiteVecStore 基于 sqlite-vec 的本地嵌入式向量数据库适配器
// 使用 SQLite + sqlite-vec 扩展实现向量搜索，适用于 < 50 万切片的场景
// 暴力精确搜索保证 100% 召回率，延迟 < 5ms（进程内计算）
type SQLiteVecStore struct {
	db       *sql.DB
	mu       sync.RWMutex
	embedder func(ctx context.Context, text string) ([]float32, error)
}

// NewSQLiteVecStore 创建新的 SQLiteVecStore
func NewSQLiteVecStore(dbPath string, embedder func(ctx context.Context, text string) ([]float32, error)) (*SQLiteVecStore, error) {
	// WAL 模式支持读写并发；busy_timeout 避免写竞争时报错；synchronous=NORMAL 提升写入性能
	dsn := fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	// SQLite 单写多读：虽然 WAL 模式支持并发读，但 sqlite-vec 虚拟表
	// 在多连接场景下可能存在可见性问题，因此使用单连接保证一致性。
	// 对于 < 1 万切片的暴力搜索（< 5ms），单连接不会成为瓶颈。
	db.SetMaxOpenConns(1)

	store := &SQLiteVecStore{
		db:       db,
		embedder: embedder,
	}

	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to init schema: %w", err)
	}

	log.Infow("Initialized SQLiteVecStore", "path", dbPath)
	return store, nil
}

func (s *SQLiteVecStore) initSchema() error {
	// 元数据表
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS chunks (
			id          TEXT PRIMARY KEY,
			document_id INTEGER NOT NULL,
			user_id     INTEGER NOT NULL,
			content     TEXT NOT NULL,
			summary     TEXT DEFAULT '',
			source_ref  TEXT DEFAULT '',
			tags        TEXT DEFAULT ''
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create chunks table: %w", err)
	}

	// 索引
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_chunks_doc ON chunks(document_id)`)
	if err != nil {
		return fmt.Errorf("failed to create doc index: %w", err)
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_chunks_user ON chunks(user_id)`)
	if err != nil {
		return fmt.Errorf("failed to create user index: %w", err)
	}

	// 向量虚拟表（sqlite-vec），使用 cosine 距离度量
	_, err = s.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS vec_chunks USING vec0(
			id TEXT PRIMARY KEY,
			embedding float[2048] distance_metric=cosine
		)
	`)
	if err != nil {
		return fmt.Errorf("failed to create vec_chunks table: %w", err)
	}

	return nil
}

// Upsert 批量插入或更新知识切片
func (s *SQLiteVecStore) Upsert(ctx context.Context, chunks []domain.KnowledgeChunk) error {
	if len(chunks) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	chunkStmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO chunks (id, document_id, user_id, content, summary, source_ref, tags)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare chunk stmt: %w", err)
	}
	defer chunkStmt.Close()

	// vec0 不支持 INSERT OR REPLACE，使用 DELETE + INSERT
	vecDeleteStmt, err := tx.PrepareContext(ctx, `DELETE FROM vec_chunks WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("failed to prepare vec delete stmt: %w", err)
	}
	defer vecDeleteStmt.Close()

	vecInsertStmt, err := tx.PrepareContext(ctx, `INSERT INTO vec_chunks (id, embedding) VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("failed to prepare vec insert stmt: %w", err)
	}
	defer vecInsertStmt.Close()

	for _, chunk := range chunks {
		// 如果没有预生成的向量，调用 embedder 生成
		vector := chunk.Vector
		if len(vector) == 0 && s.embedder != nil {
			vector, err = s.embedder(ctx, chunk.Content)
			if err != nil {
				return fmt.Errorf("failed to generate embedding for chunk %s: %w", chunk.ID, err)
			}
		}

		tagsStr := strings.Join(chunk.Tags, ",")

		// 写入元数据
		if _, err := chunkStmt.ExecContext(ctx, chunk.ID, chunk.DocumentID, chunk.UserID,
			chunk.Content, chunk.Summary, chunk.SourceRef, tagsStr); err != nil {
			return fmt.Errorf("failed to upsert chunk %s: %w", chunk.ID, err)
		}

		// 写入向量
		if len(vector) > 0 {
			serialized, serErr := sqlite_vec.SerializeFloat32(vector)
			if serErr != nil {
				return fmt.Errorf("failed to serialize vector for chunk %s: %w", chunk.ID, serErr)
			}
			if _, err := vecDeleteStmt.ExecContext(ctx, chunk.ID); err != nil {
				return fmt.Errorf("failed to delete old vector for chunk %s: %w", chunk.ID, err)
			}
			if _, err := vecInsertStmt.ExecContext(ctx, chunk.ID, serialized); err != nil {
				return fmt.Errorf("failed to insert vector for chunk %s: %w", chunk.ID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit tx: %w", err)
	}

	log.Infow("SQLiteVec Upsert complete", "count", len(chunks))
	return nil
}

// DeleteByDocumentID 删除指定文档的所有切片
func (s *SQLiteVecStore) DeleteByDocumentID(ctx context.Context, documentID uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 先查出要删除的 ID 列表
	rows, err := tx.QueryContext(ctx, `SELECT id FROM chunks WHERE document_id = ?`, documentID)
	if err != nil {
		return fmt.Errorf("failed to query chunk ids: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	// 批量删除向量
	if len(ids) > 0 {
		placeholders := make([]string, len(ids))
		args := make([]interface{}, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args[i] = id
		}
		query := fmt.Sprintf("DELETE FROM vec_chunks WHERE id IN (%s)", strings.Join(placeholders, ","))
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("failed to delete vectors: %w", err)
		}
	}

	// 删除元数据
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE document_id = ?`, documentID); err != nil {
		return fmt.Errorf("failed to delete chunks: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit tx: %w", err)
	}

	log.Infow("SQLiteVec DeleteByDocumentID complete", "documentID", documentID, "deleted", len(ids))
	return nil
}

// Search 根据语义检索相关切片
func (s *SQLiteVecStore) Search(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	if s.embedder == nil {
		return nil, fmt.Errorf("embedder is required for search")
	}

	// 严格模式：没有选知识库就不检索
	if len(filter.DocumentIDs) == 0 {
		return nil, nil
	}

	// 生成查询向量
	vector, err := s.embedder(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embedding failed: %w", err)
	}

	serialized, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize query vector: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Step 1: 获取向量总数，用于确定搜索 k 值
	// sqlite-vec 的 vec0 虚拟表在 k 远大于实际行数时会返回空结果（已知 bug），
	// 因此需要先获取实际行数，取 min(行数, 上限) 作为 k
	var vecCount int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM vec_chunks").Scan(&vecCount); err != nil {
		return nil, fmt.Errorf("failed to count vec_chunks: %w", err)
	}
	if vecCount == 0 {
		return nil, nil
	}

	// Step 2: 向量搜索获取候选集（暴力精确搜索）
	vecRows, err := s.db.QueryContext(ctx, `
		SELECT id, distance
		FROM vec_chunks
		WHERE embedding MATCH ?
		ORDER BY distance
		LIMIT ?
	`, serialized, vecCount)
	if err != nil {
		return nil, fmt.Errorf("vector search failed: %w", err)
	}

	type candidate struct {
		id       string
		distance float64
	}
	var candidates []candidate
	for vecRows.Next() {
		var c candidate
		if err := vecRows.Scan(&c.id, &c.distance); err != nil {
			vecRows.Close()
			return nil, fmt.Errorf("failed to scan vec row: %w", err)
		}
		candidates = append(candidates, c)
	}
	vecRows.Close()

	if len(candidates) == 0 {
		return nil, nil
	}

	// Step 3: 用候选 ID 查询元数据表并应用业务过滤
	placeholders := make([]string, len(candidates))
	args := make([]interface{}, len(candidates))
	for i, c := range candidates {
		placeholders[i] = "?"
		args[i] = c.id
	}

	// 构建过滤条件
	conditions := []string{
		fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ",")),
	}
	if len(filter.DocumentIDs) > 0 {
		docPlaceholders := make([]string, len(filter.DocumentIDs))
		for i, id := range filter.DocumentIDs {
			docPlaceholders[i] = "?"
			args = append(args, id)
		}
		conditions = append(conditions, fmt.Sprintf("document_id IN (%s)", strings.Join(docPlaceholders, ",")))
		// 纵深防御：biz 层白名单已按 user 过滤 docIDs，此处再叠加 user_id 隔离作为兜底
		// 避免未来新调用路径绕过白名单导致跨账户检索。系统文档 user_id=0 允许穿透。
		if filter.UserID > 0 {
			conditions = append(conditions, "(user_id = ? OR user_id = 0)")
			args = append(args, filter.UserID)
		}
	} else if filter.UserID > 0 {
		conditions = append(conditions, "user_id = ?")
		args = append(args, filter.UserID)
	}

	metaSQL := fmt.Sprintf(`
		SELECT id, document_id, user_id, content, summary, source_ref, tags
		FROM chunks
		WHERE %s
	`, strings.Join(conditions, " AND "))

	metaRows, err := s.db.QueryContext(ctx, metaSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("metadata query failed: %w", err)
	}
	defer metaRows.Close()

	// 构建 ID → metadata 映射
	type chunkMeta struct {
		chunk domain.KnowledgeChunk
	}
	metaMap := make(map[string]chunkMeta)
	for metaRows.Next() {
		var c domain.KnowledgeChunk
		var tagsStr string
		if err := metaRows.Scan(&c.ID, &c.DocumentID, &c.UserID, &c.Content, &c.Summary, &c.SourceRef, &tagsStr); err != nil {
			return nil, fmt.Errorf("failed to scan meta row: %w", err)
		}
		if tagsStr != "" {
			c.Tags = strings.Split(tagsStr, ",")
		}
		metaMap[c.ID] = chunkMeta{chunk: c}
	}

	// Step 4: 按向量距离排序合并结果
	var chunks []domain.KnowledgeChunk
	for _, cand := range candidates {
		meta, ok := metaMap[cand.id]
		if !ok {
			continue // 被过滤掉了
		}
		c := meta.chunk
		// cosine distance → cosine similarity: score = 1 - distance
		c.Score = float32(1 - cand.distance)
		chunks = append(chunks, c)
		if len(chunks) >= limit {
			break
		}
	}

	return chunks, nil
}

// FetchByDocumentID 直接获取指定文档的所有切片（纯 SQL 查询，无需向量搜索）
// 相比 DashVector 和 ChromemStore 无需构造假向量，天然支持纯过滤查询
func (s *SQLiteVecStore) FetchByDocumentID(ctx context.Context, documentID uint, limit int) ([]domain.KnowledgeChunk, error) {
	if limit <= 0 {
		limit = 10000
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, document_id, user_id, content, summary, source_ref, tags
		FROM chunks
		WHERE document_id = ?
		LIMIT ?
	`, documentID, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch query failed: %w", err)
	}
	defer rows.Close()

	var chunks []domain.KnowledgeChunk
	for rows.Next() {
		var c domain.KnowledgeChunk
		var tagsStr string
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.UserID, &c.Content, &c.Summary, &c.SourceRef, &tagsStr); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		if tagsStr != "" {
			c.Tags = strings.Split(tagsStr, ",")
		}
		chunks = append(chunks, c)
	}

	log.Infow("FetchByDocumentID completed", "documentID", documentID, "returned", len(chunks))
	return chunks, nil
}

// Close 关闭数据库连接
func (s *SQLiteVecStore) Close() error {
	return s.db.Close()
}
