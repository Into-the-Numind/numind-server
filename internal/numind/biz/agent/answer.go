package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

	// 3. Append user message.
	userMsg := buildAnswerMessage(req)
	if err := s.runStore.AppendUserMessage(ctx, runID, userMsg); err != nil {
		return nil, fmt.Errorf("Answer: append message: %w", err)
	}

	// 4. Clear pending question state.
	if err := s.runStore.ClearPendingQuestion(ctx, runID); err != nil {
		return nil, fmt.Errorf("Answer: clear pending question: %w", err)
	}

	// 5. Langfuse span for resume observability.
	emitAnswerSpan(ctx, run, req)

	// Capture snapshot for goroutine (avoid capturing s/run by pointer into closure
	// where they could be GC'd or mutated after the HTTP handler returns).
	agentDefID := run.AgentDefinitionID
	sessionID := run.SessionID
	toolFlags := resolveToolFlags(s, context.Background(), agentDefID)

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
// to be appended as a user message in the conversation history.
func buildAnswerMessage(req AnswerRequest) string {
	selectedJSON, _ := json.Marshal(req.Selected)
	var sb strings.Builder
	sb.WriteString("[user answered]\nSelected: ")
	sb.Write(selectedJSON)
	if req.FreeText != "" {
		sb.WriteString("\nFree text: ")
		sb.WriteString(req.FreeText)
	}
	return sb.String()
}

// emitAnswerSpan records a Langfuse span for the answer resume event.
// Gracefully no-ops if langfuse is not active in ctx.
func emitAnswerSpan(ctx context.Context, run *model.AgentRun, req AnswerRequest) {
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID := langfuse.SpanID()
		langfuse.CreateSpan(tc.TraceID, spanID, "tool.ask_user_question.resume",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(map[string]any{
				"run_id":    run.ID,
				"selected":  req.Selected,
				"free_text": req.FreeText,
			}),
		)
		langfuse.EndSpan(tc.TraceID, spanID)
	}
}

// resolveToolFlags looks up the agent definition's ToolFlags JSON for the
// given agentDefID. Returns nil on any failure — toolNamesFromFlags handles nil gracefully.
func resolveToolFlags(s *StudentRunService, ctx context.Context, agentDefID uint64) []byte {
	if agentDefID == 0 || s.skillStore == nil {
		return nil
	}
	ad, err := s.skillStore.GetByIDIncludeInactive(ctx, agentDefID)
	if err != nil {
		return nil
	}
	return ad.ToolFlags
}
