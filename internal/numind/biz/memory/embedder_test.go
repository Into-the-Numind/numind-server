package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMockEmbedder_DimAndCount verifies the v1 mock still returns 1024-dim zero vectors
// matching len(texts). Used by store layer tests where calling aiservice is unwanted.
func TestMockEmbedder_DimAndCount(t *testing.T) {
	e := NewMockEmbedder()
	out, err := e.Embed(context.Background(), []string{"a", "b", "c"})
	assert.NoError(t, err)
	assert.Len(t, out, 3)
	for i, v := range out {
		assert.Len(t, v, 1024, "row %d should be 1024-dim", i)
		assert.Equal(t, float32(0), v[0], "row %d should be zero vector", i)
	}
}

// TestMockEmbedder_EmptyNilInput verifies the mock embedder handles nil input cleanly.
func TestMockEmbedder_EmptyNilInput(t *testing.T) {
	e := NewMockEmbedder()
	out, err := e.Embed(context.Background(), nil)
	assert.NoError(t, err)
	assert.Len(t, out, 0)
}

// TestAIServiceEmbedder_EmptyInput verifies the aiservice embedder short-circuits
// on empty input without calling the network. Avoids spurious empty-batch RPC.
func TestAIServiceEmbedder_EmptyInput(t *testing.T) {
	e := NewAIServiceEmbedder()
	out, err := e.Embed(context.Background(), nil)
	assert.NoError(t, err)
	assert.Nil(t, out)
}

// TestRetriever_WithEmbedderOption_Override verifies the WithEmbedder option
// successfully overrides the default mockEmbedder. Real network call paths
// are covered by integration tests in Phase D.
func TestRetriever_WithEmbedderOption_Override(t *testing.T) {
	customCalled := false
	custom := &customEmbedderForTest{onEmbed: func() { customCalled = true }}
	r := NewRetriever(WithEmbedder(custom))
	// Cast to concrete type to inspect embedder; type assertion is acceptable in test code.
	impl, ok := r.(*retrieverImpl)
	assert.True(t, ok)
	assert.NotNil(t, impl.embedder)

	// Sanity: calling Embed on the custom triggers our spy.
	_, _ = impl.embedder.Embed(context.Background(), []string{"test"})
	assert.True(t, customCalled, "custom embedder should be invoked when set via WithEmbedder")
}

type customEmbedderForTest struct {
	onEmbed func()
}

func (c *customEmbedderForTest) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if c.onEmbed != nil {
		c.onEmbed()
	}
	out := make([][]float32, len(texts))
	return out, nil
}
