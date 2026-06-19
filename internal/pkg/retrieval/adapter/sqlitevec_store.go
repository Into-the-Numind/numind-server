package adapter

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/port"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
	"github.com/yanyiwu/gojieba"
)

func init() {
	sqlite_vec.Auto()
}

// Ensure SQLiteVecStore implements VectorStore interface
var _ port.VectorStore = (*SQLiteVecStore)(nil)

// Ensure SQLiteVecStore implements the optional KeywordSearcher interface
// （混合检索：retrieve.Service type-assert 该接口决定是否走 BM25 关键词通道）。
var _ port.KeywordSearcher = (*SQLiteVecStore)(nil)

// SQLiteVecStore 基于 sqlite-vec 的本地嵌入式向量数据库适配器
// 使用 SQLite + sqlite-vec 扩展实现向量搜索，适用于 < 50 万切片的场景
// 暴力精确搜索保证 100% 召回率，延迟 < 5ms（进程内计算）
type SQLiteVecStore struct {
	db       *sql.DB
	mu       sync.RWMutex
	embedder func(ctx context.Context, text string) ([]float32, error)

	// ftsAvailable 标记 FTS5 全文检索是否可用。仅当二进制以 -tags sqlite_fts5 构建
	// 且 fts_chunks 虚拟表建表成功时为 true。为 false 时所有关键词写入/检索路径降级为
	// no-op（双写跳过 fts_chunks、SearchKeyword 返回空），检索自动退回纯向量——绝不杀检索。
	ftsAvailable bool

	// jieba 中文分词器。写入与查询两端都用它把中文切成空格分隔 token，供 FTS5 默认
	// unicode61 tokenizer 做 BM25（中文 BM25 必须预分词，否则退化为逐字符匹配）。
	// 可能为 nil（极端环境字典缺失），segment 会优雅降级返回原文。
	jieba *gojieba.Jieba
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

	// 构建 jieba 实例一次（复用 enhanced_splitter.go 的字典路径模式：优先 Docker 内
	// /app/dict/*.utf8，否则用 gojieba 内置默认字典）。供 FTS5 写入/查询两端预分词。
	store.jieba = newJieba()

	// 尝试建 FTS5 全文检索表。失败（二进制无 -tags sqlite_fts5）→ 记警告 + 标记不可用，
	// 不让缺 FTS5 杀掉整个 store 初始化（降级守卫）。
	store.initFTS()

	log.Infow("Initialized SQLiteVecStore", "path", dbPath, "fts_available", store.ftsAvailable)
	return store, nil
}

// newJieba 构建 gojieba 分词器，复用 enhanced_splitter.go 的字典路径选择逻辑：
// Docker 环境优先用打进镜像的 /app/dict/*.utf8，本地开发回退 gojieba 内置默认字典。
func newJieba() *gojieba.Jieba {
	dictPath := "/app/dict/jieba.dict.utf8"
	if _, err := os.Stat(dictPath); err == nil {
		return gojieba.NewJieba(
			"/app/dict/jieba.dict.utf8",
			"/app/dict/hmm_model.utf8",
			"/app/dict/user.dict.utf8",
			"/app/dict/idf.utf8",
			"/app/dict/stop_words.utf8",
		)
	}
	return gojieba.NewJieba()
}

// initFTS 尝试创建 FTS5 虚拟表 fts_chunks。
//
// FTS5 是 SQLite 标准扩展，但 mattn/go-sqlite3 默认不编译它——必须 -tags sqlite_fts5。
// 无此 tag 时 CREATE VIRTUAL TABLE ... USING fts5 会报错 "no such module: fts5"。
//
// 降级守卫：建表失败**不返回错误**，只记警告并 s.ftsAvailable=false → 关键词写入/检索
// 全部 no-op，检索退回纯向量。这样同一份代码在带/不带 tag 的二进制上都能跑（项1 仍可用）。
func (s *SQLiteVecStore) initFTS() {
	// id UNINDEXED：id 列仅作 JOIN 回 chunks 用，不参与全文索引（省空间、避免被分词）。
	// content 列存 jieba 预分词后的正文，由 FTS5 默认 unicode61 tokenizer 做 BM25。
	_, err := s.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS fts_chunks USING fts5(id UNINDEXED, content)`)
	if err != nil {
		s.ftsAvailable = false
		log.Warnw("FTS5 unavailable, hybrid keyword retrieval disabled (build with -tags sqlite_fts5 to enable)",
			"error", err.Error())
		return
	}
	s.ftsAvailable = true
}

// segment 用 jieba 把文本切成空格分隔的 token，供 FTS5 unicode61 tokenizer **索引**。
// 写入端用：把干净正文切成 token，存进 fts_chunks.content。
// jieba 为 nil（极端环境）时优雅降级返回原文（FTS5 仍可按 unicode61 退化处理）。
func (s *SQLiteVecStore) segment(text string) string {
	if s.jieba == nil {
		return text
	}
	// hmm=true：对未登录词启用 HMM 新词发现，提升产品码/专名等的切分质量。
	return strings.Join(s.jieba.Cut(text, true), " ")
}

// buildFTSMatch 把用户 query 构造成**安全**的 FTS5 MATCH 表达式（查询端用）。
//
// 为什么不能直接把 segment 结果塞进 MATCH：jieba 会把 "ABC-123" 切成 "ABC - 123"，
// 而 FTS5 MATCH 语法里 '-' 是 NOT 操作符、':' 是列过滤符、裸 token 间是隐式 AND——
// 直接拼接会被解析成查询操作符（实测报 "no such column: 123"）甚至抛语法错。
//
// 做法：jieba 分词 → 丢掉不含字母/数字/CJK 的纯标点 token（如 "-"）→ 每个保留 token 用
// 双引号包成 FTS5 phrase（双引号转义为两个双引号），使其中的特殊字符被当字面量 →
// 用 " OR " 连接（任一 token 命中即贡献 BM25，更多命中排名更高；契合"补术语/编号命中"的目标）。
// 返回空串表示无可检索 token（调用方据此返回空、降级纯向量）。
func (s *SQLiteVecStore) buildFTSMatch(query string) string {
	var tokens []string
	if s.jieba != nil {
		tokens = s.jieba.Cut(query, true)
	} else {
		tokens = strings.Fields(query)
	}

	var quoted []string
	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" || !hasIndexableRune(tok) {
			continue // 跳过纯空白 / 纯标点 token（如 "-"、"," 等）
		}
		// FTS5 phrase：双引号包裹，内部双引号转义为两个双引号。
		escaped := strings.ReplaceAll(tok, `"`, `""`)
		quoted = append(quoted, `"`+escaped+`"`)
	}
	return strings.Join(quoted, " OR ")
}

// hasIndexableRune 判断 token 是否含至少一个可索引字符（字母/数字/非 ASCII，如 CJK）。
// 纯 ASCII 标点（'-'、','、'.' 等）会被 FTS5 unicode61 tokenizer 当分隔符丢弃，
// 作为 MATCH token 无意义且可能引入语法歧义，故过滤掉。
func hasIndexableRune(tok string) bool {
	for _, r := range tok {
		if r > 127 { // 非 ASCII（CJK 等）一律视为可索引
			return true
		}
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
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

	// FTS5 双写：FTS5 不支持 UPSERT，按 DELETE + INSERT 实现幂等覆盖。仅当 FTS5 可用时准备。
	var ftsDeleteStmt, ftsInsertStmt *sql.Stmt
	if s.ftsAvailable {
		ftsDeleteStmt, err = tx.PrepareContext(ctx, `DELETE FROM fts_chunks WHERE id = ?`)
		if err != nil {
			return fmt.Errorf("failed to prepare fts delete stmt: %w", err)
		}
		defer ftsDeleteStmt.Close()

		ftsInsertStmt, err = tx.PrepareContext(ctx, `INSERT INTO fts_chunks (id, content) VALUES (?, ?)`)
		if err != nil {
			return fmt.Errorf("failed to prepare fts insert stmt: %w", err)
		}
		defer ftsInsertStmt.Close()
	}

	for _, chunk := range chunks {
		// 如果没有预生成的向量，调用 embedder 生成
		vector := chunk.Vector
		if len(vector) == 0 && s.embedder != nil {
			vector, err = s.embedder(ctx, chunk.TextForEmbedding())
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

		// 写入 FTS5 全文索引（jieba 预分词后的**干净 Content**，非 EmbedText——关键词匹配应
		// 基于干净正文，不含结构感知切块器注入的标题面包屑噪声）。DELETE+INSERT 实现幂等覆盖。
		if s.ftsAvailable {
			if _, err := ftsDeleteStmt.ExecContext(ctx, chunk.ID); err != nil {
				return fmt.Errorf("failed to delete old fts row for chunk %s: %w", chunk.ID, err)
			}
			if _, err := ftsInsertStmt.ExecContext(ctx, chunk.ID, s.segment(chunk.Content)); err != nil {
				return fmt.Errorf("failed to insert fts row for chunk %s: %w", chunk.ID, err)
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

		// 同步删除 FTS5 全文索引行（仅当可用），避免删文档后关键词通道残留命中。
		if s.ftsAvailable {
			ftsQuery := fmt.Sprintf("DELETE FROM fts_chunks WHERE id IN (%s)", strings.Join(placeholders, ","))
			if _, err := tx.ExecContext(ctx, ftsQuery, args...); err != nil {
				return fmt.Errorf("failed to delete fts rows: %w", err)
			}
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

// SearchKeyword BM25 关键词检索（FTS5 全文索引），用于混合检索的关键词通道。
//
// 行为约束（与 Search 对齐）：
//   - FTS5 不可用 → 返回 (nil, nil)，调用方退回纯向量（降级守卫）；
//   - DocumentIDs 为空 → 返回 (nil, nil)（严格模式，不静默全量检索）；
//   - query jieba 分词后为空 → 返回 (nil, nil)；
//   - FTS5 MATCH 语法错误（分词产物含特殊字符等）→ 记日志 + 返回 (nil, nil)，不杀检索。
//
// Score：FTS5 bm25() 越小越相关（返回负值，越负越相关），用 1/(1-rank) 归一为
// (0,1] 正分（恒正、单调：越相关分越大；避免 1/(1+rank) 在 rank=-1 时 +Inf / 负值）。
// 仅供观测；RRF 融合按位次重新推导排名，故具体 Score 数值对融合结果无影响。
func (s *SQLiteVecStore) SearchKeyword(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	// 降级守卫：无 FTS5 → 纯向量。
	if !s.ftsAvailable {
		return nil, nil
	}

	// 严格模式：没有选知识库就不检索（与 Search 一致）。
	if len(filter.DocumentIDs) == 0 {
		return nil, nil
	}

	// 查询端构造安全的 FTS5 MATCH 表达式（jieba 分词 + 丢标点 + 双引号 phrase + OR 连接）。
	// 直接用空格拼接会被 FTS5 当查询操作符解析（'-'=NOT、隐式 AND），见 buildFTSMatch。
	matchExpr := s.buildFTSMatch(query)
	if matchExpr == "" {
		return nil, nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// 构建 WHERE 条件与参数。第一个参数始终是 FTS5 MATCH 表达式。
	args := []interface{}{matchExpr}

	docPlaceholders := make([]string, len(filter.DocumentIDs))
	for i, id := range filter.DocumentIDs {
		docPlaceholders[i] = "?"
		args = append(args, id)
	}
	conditions := []string{
		"fts_chunks MATCH ?",
		fmt.Sprintf("c.document_id IN (%s)", strings.Join(docPlaceholders, ",")),
	}
	// 纵深防御：与 Search 一致，叠加 user_id 隔离（系统文档 user_id=0 穿透）。
	if filter.UserID > 0 {
		conditions = append(conditions, "(c.user_id = ? OR c.user_id = 0)")
		args = append(args, filter.UserID)
	}
	args = append(args, limit)

	// bm25(fts_chunks) 越小越相关 → ORDER BY rank 升序取 top-limit。
	ftsSQL := fmt.Sprintf(`
		SELECT c.id, c.document_id, c.user_id, c.content, c.summary, c.source_ref, c.tags,
		       bm25(fts_chunks) AS rank
		FROM fts_chunks f
		JOIN chunks c ON c.id = f.id
		WHERE %s
		ORDER BY rank
		LIMIT ?
	`, strings.Join(conditions, " AND "))

	rows, err := s.db.QueryContext(ctx, ftsSQL, args...)
	if err != nil {
		// FTS5 MATCH 表达式可能因分词产物里的保留字符（如 "AND"/"*"/引号）解析失败。
		// 这是单次查询的软错误，不应杀掉整个检索——记日志后降级为纯向量。
		log.C(ctx).Warnw("FTS5 keyword search failed, degrading to dense-only",
			"error", err.Error(), "match_query", matchExpr)
		return nil, nil
	}
	defer rows.Close()

	var chunks []domain.KnowledgeChunk
	for rows.Next() {
		var c domain.KnowledgeChunk
		var tagsStr string
		var rank float64
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.UserID, &c.Content, &c.Summary, &c.SourceRef, &tagsStr, &rank); err != nil {
			log.C(ctx).Warnw("FTS5 keyword search scan failed, degrading to dense-only", "error", err.Error())
			return nil, nil
		}
		if tagsStr != "" {
			c.Tags = strings.Split(tagsStr, ",")
		}
		// bm25 rank 越负越相关；1/(1-rank) 归一为 (0,1] 正分仅供观测（RRF 按位次重排，不依赖此值）。
		c.Score = float32(1.0 / (1.0 - rank))
		chunks = append(chunks, c)
	}
	if err := rows.Err(); err != nil {
		log.C(ctx).Warnw("FTS5 keyword search rows error, degrading to dense-only", "error", err.Error())
		return nil, nil
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

// Close 关闭数据库连接，并释放 jieba 分词器持有的 C++ 资源（gojieba 是 cgo 包装，
// 不 Free 会泄漏 native 内存）。
func (s *SQLiteVecStore) Close() error {
	if s.jieba != nil {
		s.jieba.Free()
		s.jieba = nil
	}
	return s.db.Close()
}
