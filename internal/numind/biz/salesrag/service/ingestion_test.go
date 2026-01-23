package service_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/numind/biz/salesrag/domain"
	"numind-server/internal/numind/biz/salesrag/port"
	"numind-server/internal/numind/biz/salesrag/service"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockParser
type MockParser struct {
	mock.Mock
}

func (m *MockParser) Parse(ctx context.Context, file io.Reader, filename string) ([]domain.KnowledgeChunk, error) {
	args := m.Called(ctx, file, filename)
	return args.Get(0).([]domain.KnowledgeChunk), args.Error(1)
}

// MockTagger
type MockTagger struct {
	mock.Mock
}

func (m *MockTagger) TagChunk(ctx context.Context, content string) (domain.DocType, []domain.SalesStage, []string, error) {
	args := m.Called(ctx, content)
	return args.Get(0).(domain.DocType), args.Get(1).([]domain.SalesStage), args.Get(2).([]string), args.Error(3)
}

func TestIngestDocument(t *testing.T) {
	// Setup
	store := adapter.NewMemoryStore()
	parser := new(MockParser)
	tagger := new(MockTagger)
	svc := service.NewIngestionService(parser, tagger, store)
	ctx := context.Background()

	// Mocks behavior
	rawContent := strings.NewReader("some pdf content")
	parsedChunks := []domain.KnowledgeChunk{
		{Content: "Chunk 1 Content"},
	}

	parser.On("Parse", ctx, rawContent, "test.pdf").Return(parsedChunks, nil)

	tagger.On("TagChunk", ctx, context.Background(), "Chunk 1 Content").Return( // Check context matching? simpliy mock
		domain.DocTypeFact,
		[]domain.SalesStage{domain.StageDiscovery},
		[]string{"tag1"},
		nil,
	).Maybe() // Loose matching for context

	// Re-setup tagger expectation carefully
	// The service calls `tagger.TagChunk(ctx, content)`
	tagger.On("TagChunk", ctx, "Chunk 1 Content").Return(
		domain.DocTypeFact,
		[]domain.SalesStage{domain.StageDiscovery},
		[]string{"tag1"},
		nil,
	)

	// Execute
	err := svc.IngestDocument(ctx, 1, "test.pdf", rawContent)
	assert.Nil(t, err)

	// Verify Store
	filter := port.SearchFilter{
		DocTypes: []domain.DocType{domain.DocTypeFact},
	}
	results, err := store.Search(ctx, "Chunk 1", filter, 10)
	assert.Nil(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "Chunk 1 Content", results[0].Content)
	assert.Equal(t, uint(1), results[0].DocumentID)
	assert.Equal(t, domain.StageDiscovery, results[0].SalesStage[0])
}
