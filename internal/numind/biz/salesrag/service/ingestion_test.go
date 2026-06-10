package service_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"numind-server/internal/numind/biz/salesrag/adapter"
	"numind-server/internal/pkg/retrieval/domain"
	"numind-server/internal/pkg/retrieval/port"
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

func (m *MockTagger) TagChunk(ctx context.Context, content string) ([]string, string, error) {
	args := m.Called(ctx, content)
	return args.Get(0).([]string), args.Get(1).(string), args.Error(2)
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

	tagger.On("TagChunk", ctx, "Chunk 1 Content").Return(
		[]string{"tag1"},
		"Test summary",
		nil,
	)

	// Execute
	err := svc.IngestDocument(ctx, 1, "test.pdf", rawContent)
	assert.Nil(t, err)

	// Verify Store
	filter := port.SearchFilter{DocumentIDs: []uint{1}}
	results, err := store.Search(ctx, "Chunk 1", filter, 10)
	assert.Nil(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "Chunk 1 Content", results[0].Content)
	assert.Equal(t, uint(1), results[0].DocumentID)
	assert.Equal(t, []string{"tag1"}, results[0].Tags)
	assert.Equal(t, "Test summary", results[0].Summary)
}
