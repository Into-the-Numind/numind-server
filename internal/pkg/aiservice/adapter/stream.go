package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/errno"
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
//
// traceMeta carries adapter-resolved routing decisions (reasoning effort,
// model family, temperature override). Only populated on the terminal
// IsFinal=true chunk; may be nil when the adapter did not populate trace
// metadata (e.g. the ali / volc adapters do not yet participate in the
// thinking gating that produces TraceMetadata — they pass nil and the field
// is omitted from downstream consumers).
func runOAIStream(
	r io.ReadCloser,
	ch chan<- aiservice.ChatChunk,
	provider string,
	defaultModel string,
	traceMeta *aiservice.TraceMetadata,
) {
	defer r.Close()
	defer close(ch)

	// Idle watchdog: a provider that sends headers then stalls would block the
	// scanner.Scan() loop below forever — hanging the whole agent run (dev run 138,
	// ~6.5 min). Close the body after `idle` of no new data so the read unblocks with
	// a clear, retryable error instead of waiting on the 10-min HTTP timeout.
	var idleWatcher *streamIdleWatcher
	if idle := streamIdleTimeout(); idle > 0 {
		var stopWatcher func()
		idleWatcher, stopWatcher = startStreamIdleWatcher(r, idle)
		defer stopWatcher()
	}

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
	// pendingToolCalls accumulates the per-index tool_call deltas. OpenAI-
	// compatible providers emit the id+name+type in the first chunk for a
	// given index, then split function.arguments JSON across subsequent
	// chunks. We concatenate the arguments fragments and surface the
	// assembled slice on the terminal chunk.
	pendingToolCalls := map[int]*aiservice.ToolCall{}

	for scanner.Scan() {
		if idleWatcher != nil {
			idleWatcher.mark() // got data → reset the idle clock
		}
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
				Index:         index,
				FinishReason:  fmt.Sprintf("parse_error: %v", err),
				IsFinal:       true,
				Usage:         lastUsage,
				Provider:      provider,
				Model:         resolvedModel,
				Err:           fmt.Errorf("aiservice stream parse error: %w", err),
				TraceMetadata: traceMeta,
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
				PromptTokens:       chunk.Usage.PromptTokens,
				CompletionTokens:   chunk.Usage.CompletionTokens,
				TotalTokens:        chunk.Usage.TotalTokens,
				ReasoningTokens:    chunk.Usage.extractReasoningTokens(),
				CachedPromptTokens: chunk.Usage.extractCachedPromptTokens(),
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

		// Accumulate tool_call deltas per index. The first chunk for a given
		// index carries id+name+type; subsequent chunks carry partial
		// function.arguments fragments that concatenate to the full JSON.
		//
		// argsDelta is an OPTIONAL side-channel: when a fragment carries an
		// arguments slice we surface it as a ToolCallArgsDelta on a non-final
		// chunk so the agent runner can stream a live "writing code" view. This
		// never affects the execution contract — execution still uses the fully
		// assembled ToolCall on the IsFinal=true terminal chunk below. The
		// runner applies its own allowlist gate (isCodeStreamingTool) by
		// FunctionName before emitting anything to the client; this layer fills
		// the field unconditionally and lets the runner decide.
		var argsDeltas []aiservice.ToolCallArgsDelta
		for _, tcd := range choice.Delta.ToolCalls {
			existing, ok := pendingToolCalls[tcd.Index]
			if !ok {
				existing = &aiservice.ToolCall{}
				pendingToolCalls[tcd.Index] = existing
			}
			if tcd.ID != "" {
				existing.ID = tcd.ID
			}
			if tcd.Type != "" {
				existing.Type = tcd.Type
			}
			if tcd.Function.Name != "" {
				existing.Function.Name = tcd.Function.Name
			}
			if tcd.Function.Arguments != "" {
				existing.Function.Arguments += tcd.Function.Arguments
				// Side-channel: carry the incremental arguments fragment with
				// the (possibly already-known) id + name for this tool-call
				// index. The name is read from the accumulated state so that
				// later fragments (which omit name) still carry it.
				argsDeltas = append(argsDeltas, aiservice.ToolCallArgsDelta{
					ToolCallID:   existing.ID,
					FunctionName: existing.Function.Name,
					ArgsDelta:    tcd.Function.Arguments,
				})
			}
		}

		// Emit content chunks with IsFinal=false; the terminal chunk is
		// emitted once below after the stream ends, carrying finish_reason,
		// final usage, and any assembled tool_calls together.
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

		// Emit tool-call arguments deltas as additional interim chunks (one per
		// fragment). Kept separate from the content emit above so a single SSE
		// chunk that carries both content and tool_call args (rare) still yields
		// both signals. These carry no Delta/ReasoningDelta — only the
		// side-channel field — so downstream content accumulation is untouched.
		for i := range argsDeltas {
			ad := argsDeltas[i]
			ch <- aiservice.ChatChunk{
				Index:             index,
				IsFinal:           false,
				Provider:          provider,
				Model:             resolvedModel,
				ToolCallArgsDelta: &ad,
			}
			index++
		}
	}

	// Idle-timeout: the watchdog closed the body after `idle` of no data. Surface a
	// clear, distinct error wrapping errno.ErrAIProviderTimeout — a provider stall IS
	// a provider timeout, and unlike context.DeadlineExceeded (which retryableError
	// treats as non-retryable) it classifies as a RETRYABLE provider timeout. So the
	// run fails fast (60s) instead of hanging on the 10-min HTTP timeout (dev run 138),
	// and a future streaming-retry wrapper can re-attempt the same provider before the
	// first chunk. (The current sync Retry middleware does not see this async stream
	// error — see retry.go; that wrapper is the deferred Part B.)
	if idleWatcher != nil && idleWatcher.tripped.Load() {
		ch <- aiservice.ChatChunk{
			Index:         index,
			FinishReason:  fmt.Sprintf("idle_timeout: no stream data for %s", idleWatcher.idle),
			IsFinal:       true,
			Usage:         lastUsage,
			Provider:      provider,
			Model:         resolvedModel,
			Err:           fmt.Errorf("aiservice stream idle timeout: no data for %s: %w", idleWatcher.idle, errno.ErrAIProviderTimeout),
			TraceMetadata: traceMeta,
		}
		return
	}

	if err := scanner.Err(); err != nil {
		ch <- aiservice.ChatChunk{
			Index:         index,
			FinishReason:  fmt.Sprintf("scan_error: %v", err),
			IsFinal:       true,
			Usage:         lastUsage,
			Provider:      provider,
			Model:         resolvedModel,
			Err:           fmt.Errorf("aiservice stream scan error: %w", err),
			TraceMetadata: traceMeta,
		}
		return
	}

	// Terminal chunk: always emit exactly one IsFinal=true chunk with the
	// aggregated finish_reason, usage, and any assembled tool_calls.
	terminal := aiservice.ChatChunk{
		Index:         index,
		FinishReason:  finishReason,
		IsFinal:       true,
		Usage:         lastUsage,
		Provider:      provider,
		Model:         resolvedModel,
		TraceMetadata: traceMeta,
	}
	if len(pendingToolCalls) > 0 {
		indexes := make([]int, 0, len(pendingToolCalls))
		for i := range pendingToolCalls {
			indexes = append(indexes, i)
		}
		sort.Ints(indexes)
		tcs := make([]aiservice.ToolCall, 0, len(indexes))
		for _, i := range indexes {
			tcs = append(tcs, *pendingToolCalls[i])
		}
		terminal.ToolCalls = tcs
	}
	ch <- terminal
}
