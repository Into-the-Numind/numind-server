package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"

	"numind-server/internal/numind/store"
)

// ExternalToolResult is the server-owned result of one persisted external
// operation. None of these fields comes from model tool input.
type ExternalToolResult struct {
	RunID       uint64
	ToolCallID  string
	OperationID string
	Result      json.RawMessage
}

// AgentRunResumer atomically installs an external result and continues the
// original run. The narrow store claim is the single source of concurrency
// ownership: only the callback receiving true may start the runner.
type AgentRunResumer struct {
	store       store.IExternalToolResumer
	studentRuns *StudentRunService
}

// NewAgentRunResumer constructs the bridge used by the outer Feishu
// composition layer. Keeping the dependencies narrow avoids a feishu→agent
// package cycle and lets DataStore's concrete agent-run store be asserted to
// IExternalToolResumer at composition time.
func NewAgentRunResumer(runStore store.IExternalToolResumer, studentRuns *StudentRunService) *AgentRunResumer {
	return &AgentRunResumer{store: runStore, studentRuns: studentRuns}
}

// Resume appends the original tool result once and resumes the same run. A
// duplicate, cancelled, or deleted callback is a successful no-op.
func (r *AgentRunResumer) Resume(ctx context.Context, result ExternalToolResult) error {
	if r == nil || r.store == nil || r.studentRuns == nil || r.studentRuns.runner == nil {
		return fmt.Errorf("AgentRunResumer.Resume: resumer is not configured")
	}
	claimed, err := r.store.ResumeExternalTool(
		ctx,
		result.RunID,
		result.OperationID,
		result.ToolCallID,
		result.Result,
	)
	if err != nil {
		return fmt.Errorf("AgentRunResumer.Resume: claim external result: %w", err)
	}
	if !claimed {
		return nil
	}
	runReq, err := r.studentRuns.resumeRunFromStoredHistory(ctx, result.RunID)
	if err != nil {
		return fmt.Errorf("AgentRunResumer.Resume: build continuation: %w", err)
	}
	if !runReq.ContinueWithoutUserInput {
		return fmt.Errorf("AgentRunResumer.Resume: claimed transcript does not end in an external tool result")
	}
	r.studentRuns.startDetachedResume(runReq)
	return nil
}

// turnsToExternalResumeHistoryMessages preserves the tool result required by
// provider protocols. Persisted UI transcripts do not necessarily contain the
// original assistant tool-call message, so a fixed minimal lark_execute call is
// reconstructed immediately before an orphan tool result. It carries only the
// original call ID and empty arguments; persisted argv is never replayed or
// exposed to the model.
func turnsToExternalResumeHistoryMessages(turns []map[string]any) ([]*schema.Message, error) {
	out := make([]*schema.Message, 0, len(turns)+1)
	for index, turn := range turns {
		role, ok := turn["role"].(string)
		if !ok || strings.TrimSpace(role) == "" {
			return nil, fmt.Errorf("external resume transcript turn %d has no role", index)
		}
		switch role {
		case "user":
			content := strings.TrimSpace(historyTurnText(turn["content"]))
			if content != "" {
				out = append(out, schema.UserMessage(content))
			}
		case "assistant":
			raw, err := json.Marshal(turn)
			if err != nil {
				return nil, fmt.Errorf("marshal assistant turn %d: %w", index, err)
			}
			var msg schema.Message
			if err := json.Unmarshal(raw, &msg); err != nil {
				return nil, fmt.Errorf("decode assistant turn %d: %w", index, err)
			}
			if strings.TrimSpace(msg.Content) != "" || len(msg.ToolCalls) > 0 {
				msg.Role = schema.Assistant
				out = append(out, &msg)
			}
		case "tool":
			toolCallID, _ := turn["tool_call_id"].(string)
			content, contentOK := turn["content"].(string)
			toolCallID = strings.TrimSpace(toolCallID)
			if toolCallID == "" || !contentOK || strings.TrimSpace(content) == "" || !json.Valid([]byte(content)) {
				return nil, fmt.Errorf("external resume transcript tool turn %d is invalid", index)
			}
			if !immediatelyPrecededByToolCall(out, toolCallID) {
				out = append(out, schema.AssistantMessage("", []schema.ToolCall{{
					ID:   toolCallID,
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "lark_execute",
						Arguments: `{}`,
					},
				}}))
			}
			out = append(out, schema.ToolMessage(content, toolCallID, schema.WithToolName("lark_execute")))
		case "tool_group":
			// UI narration only; it is not a provider protocol message.
		default:
			return nil, fmt.Errorf("external resume transcript turn %d has unsupported role %q", index, role)
		}
	}
	return out, nil
}

func immediatelyPrecededByToolCall(messages []*schema.Message, toolCallID string) bool {
	if len(messages) == 0 {
		return false
	}
	previous := messages[len(messages)-1]
	if previous == nil || previous.Role != schema.Assistant {
		return false
	}
	for _, call := range previous.ToolCalls {
		if call.ID == toolCallID && call.Function.Name == "lark_execute" {
			return true
		}
	}
	return false
}
