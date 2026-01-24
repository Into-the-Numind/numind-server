package service

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"

	"github.com/stretchr/testify/assert"
)

// MockStore
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

func TestIngestionPipeline_Process(t *testing.T) {
	// 1. Setup Parser
	parser := adapter.NewSimpleParser()

	// 2. Setup Splitter
	splitter := NewMarkdownSplitter(SplitterConfig{MaxChunkSize: 100})

	// 3. Setup Tagger (Mock)
	mockJSON := "```json\n" + `{"sales_stage": ["DISCOVERY"], "tags": ["test"]}` + "\n```"
	tagger := NewContentTagger(&MockAliBiz{MockResponse: mockJSON})

	// 4. Setup Store (Mock)
	var wg sync.WaitGroup
	wg.Add(1)
	store := &MockStore{
		UpsertFunc: func(ctx context.Context, chunks []domain.KnowledgeChunk) error {
			defer wg.Done()
			assert.NotEmpty(t, chunks)
			// 验证向量已生成
			// 验证向量 (Empty because we don't have embedder here anymore, store handles embedding?)
			// Pipeline calls tagger, then store. Store Upsert logic handles embedding usually in adapter/dashvector.
			// But here we are mocking store.
			// Pipeline struct doesn't have embedder anymore? It assumes store does it?
			// Wait, Pipeline struct in service depends on who?
			// Let's check pipeline.go again. It doesn't have embedder.
			// And we removed embedder from KnowledgeChunk in pipeline?
			// Checking pipeline.go step 124.
			// No embedding logic in pipeline.go. It's done by store or before.
			// If store does it, then chunks here won't have vector yet IF pipeline doesn't call embedder.
			// Pipeline.go does NOT call embedder.
			// So chunks[0].Vector is empty here.
			// I should remove the vector assertion or expect empty.
			assert.Empty(t, chunks[0].Vector, "Vector should be empty before store handles it")
			return nil
		},
	}

	// 6. Setup Mock DocumentStatusUpdater (nil for test simplicity)
	var mockDocStore DocumentStatusUpdater = nil

	// 7. Setup Pipeline
	pipeline := NewIngestionPipeline(parser, splitter, tagger, mockDocStore, store)

	// 6. Create temp file
	tmpFile, err := os.CreateTemp("", "test_doc_*.md")
	assert.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	content := "# Test Doc\nThis is a test document."
	tmpFile.WriteString(content)
	tmpFile.Close()

	// 7. Submit Doc
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
	case <-time.After(2 * time.Second):
		t.Fatal("Pipeline processing timed out")
	}
}
