// Package aiservice provides the unified AI Gateway for all AI capability calls
// (Chat, Embed, Rerank, OCR, ASR). Business layers call this package rather than
// individual provider packages directly.
//
// Usage:
//
//	// Startup (once, in main):
//	g := aiservice.Build(aiservice.Deps{DB: db, Langfuse: langfuse.C})
//	aiservice.SetDefault(g)
//
//	// Business layer (any goroutine):
//	resp, err := aiservice.Chat(ctx, profile.SopText, req)
package aiservice

import "context"

// Chat performs a non-streaming chat completion via the process-wide Gateway.
// taskID must be one of the constants in the profile package (e.g., profile.SopText).
func Chat(ctx context.Context, taskID string, req ChatRequest) (*ChatResponse, error) {
	return Default().Chat(ctx, taskID, req)
}

// ChatStream starts a streaming chat completion via the process-wide Gateway.
// Each chunk is delivered on the returned channel; the channel is closed after
// the final chunk (IsFinal==true) or on error.
func ChatStream(ctx context.Context, taskID string, req ChatRequest) (<-chan ChatChunk, error) {
	return Default().ChatStream(ctx, taskID, req)
}

// Embed converts a batch of texts into float32 vectors via the process-wide Gateway.
func Embed(ctx context.Context, taskID string, req EmbedRequest) (*EmbedResponse, error) {
	return Default().Embed(ctx, taskID, req)
}

// Rerank re-scores and re-orders the provided documents relative to the query
// via the process-wide Gateway.
func Rerank(ctx context.Context, taskID string, req RerankRequest) (*RerankResponse, error) {
	return Default().Rerank(ctx, taskID, req)
}

// OCR extracts text from an image via the process-wide Gateway.
func OCR(ctx context.Context, taskID string, req OCRRequest) (*OCRResponse, error) {
	return Default().OCR(ctx, taskID, req)
}

// ASR transcribes an audio clip to text via the process-wide Gateway.
func ASR(ctx context.Context, taskID string, req ASRRequest) (*ASRResponse, error) {
	return Default().ASR(ctx, taskID, req)
}
