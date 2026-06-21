package ingest

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/pkg/model"
	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/port"

	"github.com/stretchr/testify/assert"
)

// MockStore 用于测试的 Mock VectorStore
type MockStore struct {
	UpsertFunc func(ctx context.Context, chunks []domain.KnowledgeChunk) error
}

func (m *MockStore) Upsert(ctx context.Context, chunks []domain.KnowledgeChunk) error {
	if m.UpsertFunc != nil {
		return m.UpsertFunc(ctx, chunks)
	}
	return nil
}
func (m *MockStore) Search(ctx context.Context, query string, filter port.SearchFilter, limit int) ([]domain.KnowledgeChunk, error) {
	return nil, nil
}
func (m *MockStore) DeleteByDocumentID(ctx context.Context, documentID uint) error {
	return nil
}
func (m *MockStore) FetchByDocumentID(ctx context.Context, documentID uint, limit int) ([]domain.KnowledgeChunk, error) {
	return nil, nil
}

// MockChunkStore 用于测试的 Mock KnowledgeChunkStore
type MockChunkStore struct{}

func (m *MockChunkStore) Create(ctx context.Context, chunk *model.KnowledgeChunk) error {
	return nil
}
func (m *MockChunkStore) BatchCreate(ctx context.Context, chunks []*model.KnowledgeChunk) error {
	return nil
}
func (m *MockChunkStore) GetByID(ctx context.Context, id uint) (*model.KnowledgeChunk, error) {
	return nil, nil
}
func (m *MockChunkStore) ListByDocument(ctx context.Context, documentID uint, limit int) ([]*model.KnowledgeChunk, error) {
	return nil, nil
}
func (m *MockChunkStore) ListByDocumentAndUser(ctx context.Context, documentID uint, userID uint, limit int) ([]*model.KnowledgeChunk, error) {
	return nil, nil
}
func (m *MockChunkStore) Update(ctx context.Context, chunk *model.KnowledgeChunk) error {
	return nil
}
func (m *MockChunkStore) UpdateColumns(ctx context.Context, id uint, updates map[string]interface{}) error {
	return nil
}
func (m *MockChunkStore) DeleteByDocument(ctx context.Context, documentID uint) error {
	return nil
}
func (m *MockChunkStore) GetByVectorID(ctx context.Context, vectorID string) (*model.KnowledgeChunk, error) {
	return nil, nil
}
func (m *MockChunkStore) CountByDocument(ctx context.Context, documentID uint) (int64, error) {
	return 0, nil
}

// recordingStore 有状态向量 store：按 docID 记录 chunk id 集合，Upsert 增/替、
// DeleteByDocumentID 整删，每次 Upsert 发信号。用于验证"重切块不留孤儿"。
type recordingStore struct {
	mu          sync.Mutex
	byDoc       map[uint]map[string]bool
	deleteCalls []uint
	upsertCh    chan struct{}
}

func newRecordingStore() *recordingStore {
	return &recordingStore{byDoc: map[uint]map[string]bool{}, upsertCh: make(chan struct{}, 8)}
}
func (s *recordingStore) Upsert(ctx context.Context, chunks []domain.KnowledgeChunk) error {
	s.mu.Lock()
	for _, c := range chunks {
		if s.byDoc[c.DocumentID] == nil {
			s.byDoc[c.DocumentID] = map[string]bool{}
		}
		s.byDoc[c.DocumentID][c.ID] = true
	}
	s.mu.Unlock()
	s.upsertCh <- struct{}{}
	return nil
}
func (s *recordingStore) Search(ctx context.Context, q string, f port.SearchFilter, l int) ([]domain.KnowledgeChunk, error) {
	return nil, nil
}
func (s *recordingStore) DeleteByDocumentID(ctx context.Context, documentID uint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalls = append(s.deleteCalls, documentID)
	delete(s.byDoc, documentID)
	return nil
}
func (s *recordingStore) FetchByDocumentID(ctx context.Context, documentID uint, l int) ([]domain.KnowledgeChunk, error) {
	return nil, nil
}
func (s *recordingStore) countDoc(docID uint) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byDoc[docID])
}

// 回归测试（项A1）：同一 docID 先灌多块、再灌单块，向量库必须只剩单块（无孤儿残留）。
// 修复前 Upsert 只 REPLACE 当前批 id，旧尾块 _1.._N-1 会残留且可被检索。
func TestIngestionPipeline_ReingestNoOrphans(t *testing.T) {
	store := newRecordingStore()
	pipeline := NewIngestionPipeline(adapter.NewSimpleParser(),
		NewMarkdownSplitter(SplitterConfig{MaxChunkSize: 60}),
		NewContentTagger(), nil, store, &MockChunkStore{})

	ingest := func(content string) {
		f, err := os.CreateTemp("", "reingest_*.md")
		assert.NoError(t, err)
		defer os.Remove(f.Name())
		f.WriteString(content)
		f.Close()
		pipeline.Submit(&domain.KnowledgeDocument{ID: 42, Name: "x.md", FilePath: f.Name(), Status: domain.DocStatusPending})
		select {
		case <-store.upsertCh:
		case <-time.After(8 * time.Second):
			t.Fatal("ingest timed out")
		}
	}

	// 第一次：长内容 → 多块。
	long := "# Doc\n第一段内容足够长以便切成多个块。第二段也很长继续填充。第三段同样冗长。" +
		"第四段更多文字。第五段还在写。第六段继续。第七段结束部分。"
	ingest(long)
	n1 := store.countDoc(42)
	if n1 < 2 {
		t.Fatalf("expected multiple chunks on first ingest, got %d", n1)
	}

	// 第二次：同 docID，短内容 → 单块。修复后必须整删旧块再写。
	ingest("# Doc\n短内容。")
	n2 := store.countDoc(42)
	if n2 != 1 {
		t.Fatalf("re-ingest left orphans: expected 1 chunk for doc 42, got %d (n1=%d)", n2, n1)
	}
	// 必须真的调用了 DeleteByDocumentID(42)。
	store.mu.Lock()
	var deletedDoc42 bool
	for _, d := range store.deleteCalls {
		if d == 42 {
			deletedDoc42 = true
		}
	}
	store.mu.Unlock()
	if !deletedDoc42 {
		t.Error("expected pipeline to call DeleteByDocumentID(42) before storing")
	}
}

func TestIngestionPipeline_Process(t *testing.T) {
	// 1. Setup Parser
	parser := adapter.NewSimpleParser()

	// 2. Setup Splitter
	splitter := NewMarkdownSplitter(SplitterConfig{MaxChunkSize: 100})

	// 3. Setup Tagger (使用真实的 DMXAPI Tagger)
	tagger := NewContentTagger()

	// 4. Setup Store (Mock)
	var wg sync.WaitGroup
	wg.Add(1)
	store := &MockStore{
		UpsertFunc: func(ctx context.Context, chunks []domain.KnowledgeChunk) error {
			defer wg.Done()
			assert.NotEmpty(t, chunks)
			// Vector 在 store.Upsert 时由 DashVector 处理，这里 mock 不生成
			assert.Empty(t, chunks[0].Vector, "Vector should be empty before store handles it")
			return nil
		},
	}

	// 5. Setup Mock DocumentStatusUpdater (nil for test simplicity)
	var mockDocStore DocumentStatusUpdater = nil

	// 6. Setup Mock ChunkStore
	mockChunkStore := &MockChunkStore{}

	// 7. Setup Pipeline
	pipeline := NewIngestionPipeline(parser, splitter, tagger, mockDocStore, store, mockChunkStore)

	// 8. Create temp file
	tmpFile, err := os.CreateTemp("", "test_doc_*.md")
	assert.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	content := "# Test Doc\nThis is a test document."
	tmpFile.WriteString(content)
	tmpFile.Close()

	// 9. Submit Doc
	doc := &domain.KnowledgeDocument{
		ID:       1,
		Name:     "test.md",
		FilePath: tmpFile.Name(), // Use temp file path
		Status:   domain.DocStatusPending,
	}

	pipeline.Submit(doc)

	// Wait for processing
	// We use wg in store mock to know when it's done
	// Add timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Pipeline processing timed out")
	}
}
