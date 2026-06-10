package service

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
