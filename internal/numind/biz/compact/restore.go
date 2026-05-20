package compact

import (
	"context"
	"encoding/json"
	"fmt"

	"numind-server/internal/pkg/model"
)

// RestorationNarration is appended to the system prompt when a session is
// resumed (blueprint §4.8.6 step 3). It nudges the LLM to summarize first
// rather than immediately invoking tools.
const RestorationNarration = `学员已重新打开这个会话。
请根据历史记录继续之前的工作，
第一条响应请简短总结上次进展，不要立即调用工具。`

// Restore reads agent_run.compact_summary + messages, applies the 3 cleansing
// passes (§4.8.6 step 2), prepends the compact summary as a system message (if
// any), and returns a RestoredSession the caller can feed into a new LLM turn.
//
// reinjector must not be nil; v1 callers pass &NullAttachmentReinjector{}.
func Restore(ctx context.Context, run *model.AgentRun, reinjector AttachmentReinjector) (*RestoredSession, error) {
	if reinjector == nil {
		return nil, fmt.Errorf("Restore: nil reinjector")
	}
	if run == nil {
		return nil, fmt.Errorf("Restore: nil run")
	}

	var raw []Message
	if len(run.Messages) > 0 {
		if err := json.Unmarshal(run.Messages, &raw); err != nil {
			return nil, fmt.Errorf("Restore: unmarshal messages: %w", err)
		}
	}

	cleaned := cleanseMessages(raw)

	if run.CompactSummary != "" {
		cleaned = append([]Message{{
			Role:          "system",
			Content:       run.CompactSummary,
			IsCompactMark: true,
		}}, cleaned...)
	}

	narration, err := reinjector.Reinject(ctx, RestorationNarration, run.ID)
	if err != nil {
		return nil, fmt.Errorf("Restore: reinjector: %w", err)
	}

	return &RestoredSession{
		Messages:         cleaned,
		SystemNarration:  narration,
		FirstTurnNoTools: true,
	}, nil
}

// cleanseMessages applies the 3 cleansing passes (blueprint §4.8.6 step 2):
//
//  1. drop dangling tool_use — assistant messages that contain tool_calls but
//     have no corresponding tool_result anywhere AND no content text;
//  2. drop orphan thinking blocks — role=="thinking" messages (v1 never
//     persists thinking across sessions);
//  3. drop empty assistant — content=="" with no tool_calls.
//
// Known limitation (S2 reviewer P2): when an assistant message has multiple
// tool_calls and only some have matching tool_results, the whole message is
// retained. v1 accepts this rare-edge-case hallucination risk; v2 will filter
// the ToolCalls JSON granularly.
func cleanseMessages(msgs []Message) []Message {
	// First pass: collect which tool_call_ids have results.
	haveResult := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			haveResult[m.ToolCallID] = true
		}
	}

	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		// (2) orphan thinking
		if m.Role == "thinking" {
			continue
		}
		// (1) dangling tool_use detection (assistant)
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			var calls []struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(m.ToolCalls, &calls)
			anyHasResult := false
			for _, c := range calls {
				if haveResult[c.ID] {
					anyHasResult = true
					break
				}
			}
			if !anyHasResult && m.Content == "" {
				continue
			}
		}
		// (3) empty assistant
		if m.Role == "assistant" && m.Content == "" && len(m.ToolCalls) == 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}
