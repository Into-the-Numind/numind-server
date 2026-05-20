package memory

import "context"

// Embedder converts text strings into embedding vectors.
// v1 uses mockEmbedder (zero vectors); v2 will swap in a real aiservice.Embed call.
type Embedder interface {
	// Embed returns one embedding vector per input text.
	// The length of the returned slice equals len(texts).
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// mockEmbedder is a v1 placeholder that returns 1024-dimensional zero vectors.
// Dimensionality is chosen to match doubao-embedding-vision-250615 (spec §4.11).
type mockEmbedder struct{}

// NewMockEmbedder returns a v1 Embedder that always returns zero vectors.
func NewMockEmbedder() Embedder { return &mockEmbedder{} }

// Embed returns a 1024-dimensional zero vector for each input text.
func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, 1024) // zero vector — doubao-embedding-vision-250615 dim
	}
	return out, nil
}
