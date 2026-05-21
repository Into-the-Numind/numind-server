package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// AnswerRequest is the payload for POST /v1/agent-runs/:id/answer.
type AnswerRequest struct {
	// Selected contains the chosen option keys (1–4).
	Selected []string `json:"selected" binding:"required,min=1,max=4"`
	// FreeText is optional free-form text the user may add alongside option selection.
	FreeText string `json:"free_text,omitempty"`
}

// AnswerResponse is returned from POST /v1/agent-runs/:id/answer.
type AnswerResponse struct {
	RunID  uint64 `json:"run_id"`
	Status string `json:"status"` // "resumed"
}

// Answer handles POST /v1/agent-runs/:id/answer business logic.
//
// Steps:
//  1. Verify run ownership (returns 404 if cross-user).
//  2. Verify run is in state_reason == "waiting_for_user_choice".
//  3. Append the user's answer as a new user message to agent_run.messages.
//  4. Clear pending_question_json / pending_question_at and reset state_reason.
//  5. Emit a Langfuse span for observability (tool.ask_user_question.resume).
//  6. Restart runner.Run in a detached goroutine so the HTTP handler returns
//     immediately (same async pattern as StudentRunService.Create).
func (s *StudentRunService) Answer(ctx context.Context, userID uint, runID uint64, req AnswerRequest) (*AnswerResponse, error) {
	// 1. Ownership check (reuse existing helper).
	if err := s.verifyRunOwnership(ctx, userID, runID); err != nil {
		return nil, err
	}

	// 2. State check.
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		return nil, errno.ErrAgentRunNotFound.SetMessage("answer: get run: %s", err.Error())
	}
	if run.StateReason != string(TerminalWaitingForUserChoice) {
		return nil, errno.ErrInvalidInput.SetMessage(
			"answer: run is not waiting for user choice (state_reason=%s)", run.StateReason,
		)
	}

	// 2b. pending_question_json IS NOT NULL guard (spec §2.3c).
	// Defends against a corrupted row where state_reason was set but the JSON
	// is missing (partial UpdatePendingQuestion failure).
	if len(run.PendingQuestionJSON) == 0 || string(run.PendingQuestionJSON) == "null" {
		return nil, errno.ErrInvalidInput.SetMessage(
			"answer: no pending question json (data inconsistent)",
		)
	}

	// 3. Atomic answer: append user message + clear pending question in one tx.
	userMsg := buildAnswerMessage(run.PendingQuestionJSON, req.Selected, req.FreeText)
	if err := s.runStore.AnswerAndClear(ctx, runID, userMsg); err != nil {
		return nil, fmt.Errorf("Answer: atomic answer+clear: %w", err)
	}

	// 5. Langfuse span for resume observability.
	emitAnswerSpan(ctx, run, req)

	// Capture snapshot for goroutine (avoid capturing s/run by pointer into closure
	// where they could be GC'd or mutated after the HTTP handler returns).
	agentDefID := run.AgentDefinitionID
	sessionID := run.SessionID
	toolFlags := resolveToolFlags(context.Background(), s, agentDefID)

	// 6. Restart runner in a detached goroutine (detached ctx, same as Create).
	go func() {
		bgCtx := middleware.NewContextWithUserID(context.Background(), userID)
		toolNames := toolNamesFromFlags(toolFlags)
		runReq := RunRequest{
			UserID:            userID,
			SessionID:         sessionID,
			Input:             userMsg,
			ToolNames:         toolNames,
			AgentDefinitionID: agentDefID,
			EnableMemory:      true,
			ExistingRunID:     runID,
		}
		if _, runErr := s.runner.Run(bgCtx, runReq); runErr != nil {
			log.Errorw("Answer: restart runner failed", "run_id", runID, "error", runErr)
		}
	}()

	return &AnswerResponse{RunID: runID, Status: "resumed"}, nil
}

// buildAnswerMessage formats the user's answer into a human-readable string
// to be appended as a user message in the conversation history. Includes the
// original question (parsed from pendingJSON) per spec §2.3d so the LLM sees
// the full Q&A context on resume.
func buildAnswerMessage(pendingJSON []byte, selected []string, freeText string) string {
	var payload YieldPayload
	if err := json.Unmarshal(pendingJSON, &payload); err != nil {
		// Fallback: don't block answer even if JSON parsing fails.
		payload.Question = "<unparseable question>"
	}
	selectedJSON, _ := json.Marshal(selected)
	var sb strings.Builder
	sb.WriteString("[user answered]\nQuestion: ")
	sb.WriteString(payload.Question)
	sb.WriteString("\nSelected: ")
	sb.Write(selectedJSON)
	if freeText != "" {
		sb.WriteString("\nFree text: ")
		sb.WriteString(freeText)
	}
	return sb.String()
}

// emitAnswerSpan records a Langfuse span for the answer resume event.
// Gracefully no-ops if langfuse is not active in ctx. Includes wait_duration_ms
// (time since pending_question_at) per spec §6.2.
func emitAnswerSpan(ctx context.Context, run *model.AgentRun, req AnswerRequest) {
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID := langfuse.SpanID()
		spanInput := map[string]any{
			"run_id":    run.ID,
			"selected":  req.Selected,
			"free_text": req.FreeText,
		}
		if run.PendingQuestionAt != nil {
			spanInput["wait_duration_ms"] = time.Since(*run.PendingQuestionAt).Milliseconds()
		}
		langfuse.CreateSpan(tc.TraceID, spanID, "tool.ask_user_question.resume",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(spanInput),
		)
		langfuse.EndSpan(tc.TraceID, spanID)
	}
}

// resolveToolFlags looks up the agent definition's ToolFlags JSON for the
// given agentDefID. Returns nil on any failure — toolNamesFromFlags handles nil gracefully.
// ctx is the first parameter per Go idiom (T4 reviewer fix).
func resolveToolFlags(ctx context.Context, s *StudentRunService, agentDefID uint64) []byte {
	if agentDefID == 0 || s.skillStore == nil {
		return nil
	}
	ad, err := s.skillStore.GetByIDIncludeInactive(ctx, agentDefID)
	if err != nil {
		return nil
	}
	return ad.ToolFlags
}
