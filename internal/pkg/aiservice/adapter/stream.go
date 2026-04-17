package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"numind-server/internal/pkg/aiservice"
)

// runOAIStream reads an OpenAI-compatible SSE stream from r and sends
// aiservice.ChatChunk values to ch. It closes ch when the stream ends or an
// error occurs.
//
// Emit contract:
//   - Content and reasoning deltas are streamed as-is with IsFinal=false.
//   - Exactly one IsFinal=true chunk is emitted, after the stream ends, and it
//     carries the finish_reason + the most recent usage (nil if the provider
//     never sent one).
//
// Why a single terminal chunk: some providers (e.g. DMXAPI-proxied DeepSeek)
// send the finish_reason chunk BEFORE an independent usage-only chunk
// (choices=[], usage populated). Other providers bundle them. Collecting the
// finish_reason and usage separately inside the loop and emitting a single
// terminal chunk handles both patterns uniformly and avoids the previous bug
// where usage on a separate late chunk was silently dropped, making
// sop_node_run.total_tokens stay 0.
//
// provider is the adapter name (e.g. "ali", "volc") and defaultModel is the
// configured ProviderModelID used as fallback when the provider omits
// chunk.Model. The actual model name is read from each chunk's Model field
// when non-empty. Both Provider and Model are propagated to every emitted
// ChatChunk including the terminal one.
func runOAIStream(r io.ReadCloser, ch chan<- aiservice.ChatChunk, provider string, defaultModel string) {
	defer r.Close()
	defer close(ch)

	scanner := bufio.NewScanner(r)
	// Increase scanner buffer for large payloads.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1*1024*1024)

	index := 0
	var lastUsage *aiservice.TokenUsage
	// resolvedModel tracks the most recent non-empty model name from the provider.
	resolvedModel := defaultModel
	// finishReason is captured from whichever chunk reports it; emitted on the
	// terminal chunk after the loop.
	finishReason := ""

	for scanner.Scan() {
		line := scanner.Text()

		// Skip blank lines and SSE comment lines.
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk oaiStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Parse error: emit a terminal chunk with any usage captured so far
			// and return. Content chunks emitted before this point stay valid.
			ch <- aiservice.ChatChunk{
				Index:        index,
				FinishReason: fmt.Sprintf("parse_error: %v", err),
				IsFinal:      true,
				Usage:        lastUsage,
				Provider:     provider,
				Model:        resolvedModel,
			}
			return
		}

		// Track the most recent non-empty model name from the provider.
		if chunk.Model != "" {
			resolvedModel = chunk.Model
		}

		// Capture usage from any chunk that includes it (may be the finish
		// chunk, may be a later usage-only chunk with choices=[]).
		if chunk.Usage != nil {
			lastUsage = &aiservice.TokenUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}

		if len(chunk.Choices) == 0 {
			// Pure usage/metadata chunk — no content to emit, state already
			// captured above.
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta.Content
		reasoningDelta := choice.Delta.ReasoningContent
		if choice.FinishReason != "" {
			finishReason = choice.FinishReason
		}

		// Emit content chunks with IsFinal=false; the terminal chunk is
		// emitted once below after the stream ends, carrying finish_reason
		// and the final usage together.
		if delta != "" || reasoningDelta != "" {
			ch <- aiservice.ChatChunk{
				Delta:          delta,
				ReasoningDelta: reasoningDelta,
				Index:          index,
				IsFinal:        false,
				Provider:       provider,
				Model:          resolvedModel,
			}
			index++
		}
	}

	if err := scanner.Err(); err != nil {
		ch <- aiservice.ChatChunk{
			Index:        index,
			FinishReason: fmt.Sprintf("scan_error: %v", err),
			IsFinal:      true,
			Usage:        lastUsage,
			Provider:     provider,
			Model:        resolvedModel,
		}
		return
	}

	// Terminal chunk: always emit exactly one IsFinal=true chunk with the
	// aggregated finish_reason and usage.
	ch <- aiservice.ChatChunk{
		Index:        index,
		FinishReason: finishReason,
		IsFinal:      true,
		Usage:        lastUsage,
		Provider:     provider,
		Model:        resolvedModel,
	}
}
