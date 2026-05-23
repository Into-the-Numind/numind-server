package search

import (
	"encoding/json"

	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// maxIndexableMessagesBytes caps run.Messages size before extractor will
// attempt full JSON unmarshal. Above this, we skip indexing (search remains
// best-effort; the search index missing one outlier run is acceptable, but
// allocating tens of MB on the request hot path is not).
//
// Chosen at 10 MiB: a normal multi-turn run is < 200 KB; runs above this
// threshold are almost certainly pathological (runaway tool output, etc.).
const maxIndexableMessagesBytes = 10 << 20 // 10 MiB

// messageEnvelope is the minimal shape of one entry in agent_run.messages JSON.
// We only deserialize the fields needed for indexing. Unknown fields are ignored.
type messageEnvelope struct {
	Role        string          `json:"role"`
	Content     json.RawMessage `json:"content"`
	MessageUUID string          `json:"message_uuid"`
	// reasoning_content is intentionally NOT indexed (spec §Content 提取规则).
	// tool_calls is intentionally NOT indexed (search would surface JSON noise).
}

// extractSearchRows walks an AgentRun's messages JSON and emits one
// AgentMessageSearch row per indexable message.
//
// Rules (spec §Content 提取规则):
//   - role=user / role=assistant: index the textual content
//   - role=tool: skip (tool results are JSON, low search value)
//   - assistant with tool_calls: still index content (we only deserialize
//     content, tool_calls is ignored by the envelope)
//   - reasoning_content: not indexed (envelope doesn't expose it)
//   - content_length is computed from the extracted text
//
// A message without a usable UUID is skipped — without a UUID we cannot dedupe
// on diff, so indexing it would create duplicates on every WriteTurn.
//
// The returned slice is safe to feed to IAgentMessageSearchStore.BulkInsert.
func extractSearchRows(run model.AgentRun) []model.AgentMessageSearch {
	if len(run.Messages) == 0 {
		return nil
	}
	// Guard: full JSON unmarshal of run.Messages can allocate hundreds of MB
	// for pathological runs (runaway tool output). Skip indexing above the
	// size cap — log so operators notice the outlier run.
	if len(run.Messages) > maxIndexableMessagesBytes {
		log.Warnw("agent_message_search: run.Messages exceeds index size limit; skipping",
			"agent_run_id", run.ID,
			"user_id", run.UserID,
			"size_bytes", len(run.Messages),
			"limit_bytes", maxIndexableMessagesBytes)
		return nil
	}
	var envelopes []messageEnvelope
	if err := json.Unmarshal(run.Messages, &envelopes); err != nil {
		// Messages JSON may be malformed (legacy) — just skip extraction.
		return nil
	}
	out := make([]model.AgentMessageSearch, 0, len(envelopes))
	for i, m := range envelopes {
		if m.MessageUUID == "" {
			continue
		}
		// Skip non-indexable roles.
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		text := decodeContent(m.Content)
		if text == "" {
			continue
		}
		out = append(out, model.AgentMessageSearch{
			AgentRunID:    run.ID,
			UserID:        run.UserID,
			SessionID:     run.SessionID,
			MessageUUID:   m.MessageUUID,
			Role:          m.Role,
			Content:       text,
			ContentLength: lenRunes(text),
			MessageIndex:  i,
		})
	}
	return out
}

// decodeContent handles the two valid shapes of message content:
//   - plain string (most common)
//   - array of typed blocks: [{"type":"text","text":"..."}, ...] (multimodal)
//
// Returns the concatenated text content; multimodal blocks of type != "text"
// (image_url, file, etc.) are skipped. Returns "" for unparsable content.
func decodeContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try array of blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var buf []byte
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				if len(buf) > 0 {
					buf = append(buf, '\n')
				}
				buf = append(buf, []byte(b.Text)...)
			}
		}
		return string(buf)
	}
	return ""
}

// lenRunes returns the rune (character) count of s.
// For Chinese text this is more meaningful than byte length (len(s)).
func lenRunes(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// filterByNewUUID returns the subset of rows whose MessageUUID is not in known.
// Used by the WriteTurn hook to diff against already-indexed messages.
func filterByNewUUID(rows []model.AgentMessageSearch, known []string) []model.AgentMessageSearch {
	if len(rows) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(known))
	for _, u := range known {
		seen[u] = struct{}{}
	}
	out := make([]model.AgentMessageSearch, 0, len(rows))
	for _, r := range rows {
		if _, ok := seen[r.MessageUUID]; ok {
			continue
		}
		out = append(out, r)
	}
	return out
}
