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
	// Selected contains the chosen option keys (0–4). May be EMPTY when the user
	// answers only via the always-present free-text box (ask-question-freetext).
	Selected []string `json:"selected" binding:"max=4"`
	// FreeText is free-form text the user may add alongside, or instead of, an
	// option selection. At least one of Selected / FreeText must be non-empty
	// (enforced in Answer — binding can't express the cross-field rule).
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

	// 1b. Cross-field guard: at least one of Selected / FreeText must be present.
	// binding only validates max=4 on Selected; free-text-only answers are valid
	// (ask-question-freetext), but an empty-empty answer is not.
	if len(req.Selected) == 0 && strings.TrimSpace(req.FreeText) == "" {
		return nil, errno.ErrInvalidInput.SetMessage(
			"answer: must select an option or provide free text",
		)
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

	// HW-33: rebuild the conversation context the resumed agent must see. The
	// run paused mid-ReAct persisted its pre-yield transcript in run.Messages
	// (captured here BEFORE AnswerAndClear appended the answer). loadSessionHistory
	// excludes the current run by design, so the agent would otherwise resume with
	// ZERO memory of the research it did before pausing (dev run #119: it re-asked
	// for the company name it was already researching). Inject prior runs' history
	// PLUS this run's pre-yield transcript as History; the answer becomes Input.
	resumeHistory := s.loadSessionHistory(context.Background(), sessionID, runID)
	if len(run.Messages) > 0 && string(run.Messages) != "null" {
		var turns []map[string]any
		if uerr := json.Unmarshal(run.Messages, &turns); uerr == nil {
			resumeHistory = append(resumeHistory, turnsToHistoryMessages(turns)...)
		}
	}

	// 6. Restart runner in a detached goroutine (detached ctx, same as Create).
	go func() {
		bgCtx := middleware.NewContextWithUserID(context.Background(), userID)
		toolNames := toolNamesFromFlags(toolFlags)
		runReq := RunRequest{
			UserID:            userID,
			SessionID:         sessionID,
			Input:             userMsg,
			History:           resumeHistory,
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
	// T1 bridge: parse the now-array pending payload but keep the existing
	// single-question answer semantics (first question). agent-multi-question T2
	// replaces this with per-question answers from the answers map.
	question := "<unparseable question>"
	if payload, err := ParsePendingQuestion(pendingJSON); err == nil && len(payload.Questions) > 0 {
		question = payload.Questions[0].Question
	}
	selectedJSON, _ := json.Marshal(selected)
	var sb strings.Builder
	sb.WriteString("[user answered]\nQuestion: ")
	sb.WriteString(question)
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
