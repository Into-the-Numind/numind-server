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
// aiservice.ChatChunk values to ch.  It closes ch when the stream ends or an
// error occurs.
//
// The last chunk before [DONE] typically carries a non-nil usage object
// (when stream_options.include_usage=true was sent in the request).  That
// usage is attached to the final sentinel chunk (IsFinal=true).
//
// provider and defaultModel are informational; the actual model name is read
// from the first non-empty chunk.Model field returned by the provider.
func runOAIStream(r io.ReadCloser, ch chan<- aiservice.ChatChunk, _ string, _ string) {
	defer r.Close()
	defer close(ch)

	scanner := bufio.NewScanner(r)
	// Increase scanner buffer for large payloads.
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1*1024*1024)

	index := 0
	var lastUsage *aiservice.TokenUsage

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
			ch <- aiservice.ChatChunk{
				Index:        index,
				FinishReason: fmt.Sprintf("parse_error: %v", err),
				IsFinal:      true,
			}
			return
		}

		// Capture usage from chunks that include it (final chunk).
		if chunk.Usage != nil {
			lastUsage = &aiservice.TokenUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}

		if len(chunk.Choices) == 0 {
			// Pure usage chunk — no content delta.
			continue
		}

		choice := chunk.Choices[0]
		delta := choice.Delta.Content
		reasoningDelta := choice.Delta.ReasoningContent
		finishReason := choice.FinishReason

		isFinal := finishReason != ""

		c := aiservice.ChatChunk{
			Delta:          delta,
			ReasoningDelta: reasoningDelta,
			Index:          index,
			FinishReason:   finishReason,
			IsFinal:        isFinal,
		}
		if isFinal && lastUsage != nil {
			c.Usage = lastUsage
		}
		ch <- c
		index++
	}

	if err := scanner.Err(); err != nil {
		ch <- aiservice.ChatChunk{
			Index:        index,
			FinishReason: fmt.Sprintf("scan_error: %v", err),
			IsFinal:      true,
		}
		return
	}

	// If no finish_reason chunk was emitted (some providers skip it),
	// emit a terminal chunk with the accumulated usage.
	ch <- aiservice.ChatChunk{
		Index:   index,
		IsFinal: true,
		Usage:   lastUsage,
		Delta:   "",
	}
}
