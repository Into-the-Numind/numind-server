package memory

import (
	"context"
	"fmt"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
)

// Embedder converts text strings into embedding vectors.
// v1 uses mockEmbedder (zero vectors); v1.0-final swaps in aiserviceEmbedder.
type Embedder interface {
	// Embed returns one embedding vector per input text.
	// The length of the returned slice equals len(texts).
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// mockEmbedder is a v1 placeholder that returns 1024-dimensional zero vectors.
// Retained for unit tests where calling real aiservice is unwanted.
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

// aiserviceEmbedder is the production Embedder backed by aiservice.Embed
// (Agent Mode #14/14 e2e rollout — A2).
type aiserviceEmbedder struct{}

// NewAIServiceEmbedder returns an Embedder that calls aiservice.Embed with the
// agent.embed task profile route. Dimension is fixed at 1024 to match
// doubao-embedding-vision-250615 and text-embedding-v4 (configurable v2).
func NewAIServiceEmbedder() Embedder { return &aiserviceEmbedder{} }

// Embed delegates to aiservice.Embed; the returned slice length matches len(texts).
func (e *aiserviceEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	resp, err := aiservice.Embed(ctx, profile.AgentEmbed, aiservice.EmbedRequest{
		Texts:     texts,
		Dimension: 1024,
	})
	if err != nil {
		return nil, fmt.Errorf("aiserviceEmbedder.Embed: %w", err)
	}
	return resp.Embeddings, nil
}
