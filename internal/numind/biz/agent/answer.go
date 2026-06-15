package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

// AnswerRequest is the payload for POST /v1/agent-runs/:id/answer.
//
// agent-multi-question: Answers is keyed by the question text (Claude Code's
// model). A run may have posed 1-4 questions; the user answers each. Skipping a
// question = omitting its key. Cross-field rules are enforced in Answer (the
// answers must reference real pending questions and each be non-empty) since
// gin binding can't express them.
type AnswerRequest struct {
	// binding:required rejects a totally-missing/empty answers map at bind time
	// (a present-but-empty map and per-question rules are enforced in Answer).
	Answers map[string]AnswerItem `json:"answers" binding:"required"`
}

// AnswerItem is the user's answer to one question.
type AnswerItem struct {
	// Selected contains the chosen option labels (0–4). May be EMPTY when the
	// user answers only via the always-present free-text box. (Option keys are
	// not forwarded over SSE, so the client identifies options by label.)
	Selected []string `json:"selected"`
	// FreeText is free-form text the user may add alongside, or instead of, an
	// option selection (the "Other" box).
	FreeText string `json:"free_text,omitempty"`
}

// AnswerResponse is returned from POST /v1/agent-runs/:id/answer.
type AnswerResponse struct {
	RunID  uint64 `json:"run_id"`
	Status string `json:"status"` // "resumed"
}

// validateAndPersistAnswer is the shared validate + persist + resume-context
// build step used by BOTH the poll path (Answer) and the streaming path
// (AnswerStream), so the two never diverge in their ownership / state / answer
// validation (spec §4.2 R1).
//
// Steps:
//  1. Verify run ownership (returns 404 if cross-user) + at least one answer.
//  2. Verify run is in state_reason == "waiting_for_user_choice", with a
//     non-null pending_question_json, and validate each answer against the
//     pending questions.
//  3. Atomically append the answer turn (embedding question_answer for issue1
//     reload reconstruction) + clear the pending question.
//  4. Emit a Langfuse span for observability.
//  5. Rebuild the resume conversation context and return a fully-built
//     RunRequest (ExistingRunID = runID, Input = the human-readable answer
//     message) ready to hand to runner.Run (detached) or runner.RunStream (SSE).
//
// NOTE: AnswerAndClear flips the row's status to running and clears ended_at,
// so the helper has the side effect of taking over the paused run. The narration
// forwarder is NOT started here — the two call sites start it themselves so the
// helper has no goroutine ownership.
func (s *StudentRunService) validateAndPersistAnswer(ctx context.Context, userID uint, runID uint64, req AnswerRequest) (RunRequest, error) {
	// 1. Ownership check (reuse existing helper).
	if err := s.verifyRunOwnership(ctx, userID, runID); err != nil {
		return RunRequest{}, err
	}

	// 1b. At least one question must be answered (skipping all is not a resume).
	if len(req.Answers) == 0 {
		return RunRequest{}, errno.ErrInvalidInput.SetMessage("answer: must answer at least one question")
	}

	// 2. State check.
	run, err := s.runStore.Get(ctx, runID)
	if err != nil {
		return RunRequest{}, errno.ErrAgentRunNotFound.SetMessage("answer: get run: %s", err.Error())
	}
	if run.StateReason != string(TerminalWaitingForUserChoice) {
		return RunRequest{}, errno.ErrInvalidInput.SetMessage(
			"answer: run is not waiting for user choice (state_reason=%s)", run.StateReason,
		)
	}

	// 2b. pending_question_json IS NOT NULL guard (spec §2.3c).
	// Defends against a corrupted row where state_reason was set but the JSON
	// is missing (partial UpdatePendingQuestion failure).
	if len(run.PendingQuestionJSON) == 0 || string(run.PendingQuestionJSON) == "null" {
		return RunRequest{}, errno.ErrInvalidInput.SetMessage(
			"answer: no pending question json (data inconsistent)",
		)
	}

	// 2c. Validate each answer against the pending questions: every key must
	// reference a question that was actually asked, and each answer must carry
	// a selection or free text (skipping = omitting the key, not an empty entry).
	pending, perr := ParsePendingQuestion(run.PendingQuestionJSON)
	if perr != nil {
		return RunRequest{}, errno.ErrInvalidInput.SetMessage("answer: pending question json is corrupt: %s", perr.Error())
	}
	asked := make(map[string]YieldQuestion, len(pending.Questions))
	for _, q := range pending.Questions {
		asked[q.Question] = q
	}
	for qText, item := range req.Answers {
		q, ok := asked[qText]
		if !ok {
			return RunRequest{}, errno.ErrInvalidInput.SetMessage("answer: question %q was not asked", qText)
		}
		if len(item.Selected) == 0 && strings.TrimSpace(item.FreeText) == "" {
			return RunRequest{}, errno.ErrInvalidInput.SetMessage("answer: question %q has neither a selection nor free text", qText)
		}
		if len(item.Selected) > 4 {
			return RunRequest{}, errno.ErrInvalidInput.SetMessage("answer: question %q has too many selected options (got %d, max 4)", qText, len(item.Selected))
		}
		if !q.MultiSelect && len(item.Selected) > 1 {
			return RunRequest{}, errno.ErrInvalidInput.SetMessage("answer: question %q is single-select but got %d options", qText, len(item.Selected))
		}
	}

	// 3. Atomic answer: append the answer turn + clear pending question in one tx.
	// Reuse the already-parsed pending payload (no second parse). userMsg is the
	// human-readable content (also the resume Input below); the persisted turn
	// additionally embeds question_answer so a reloaded session reconstructs an
	// answered question_prompt card instead of an orphan user bubble (issue1).
	userMsg := buildAnswerMessage(pending, req.Answers)
	turn, err := buildAnswerTurn(pending, req.Answers, userMsg)
	if err != nil {
		return RunRequest{}, fmt.Errorf("validateAndPersistAnswer: build answer turn: %w", err)
	}
	if err := s.runStore.AnswerAndClear(ctx, runID, turn); err != nil {
		return RunRequest{}, fmt.Errorf("validateAndPersistAnswer: atomic answer+clear: %w", err)
	}

	// 4. Langfuse span for resume observability.
	emitAnswerSpan(ctx, run, req)

	// Snapshot the fields the resumed run needs (AnswerAndClear has already
	// mutated the row; read from the pre-resume copy captured above).
	agentDefID := run.AgentDefinitionID
	sessionID := run.SessionID
	toolFlags := resolveToolFlags(context.Background(), s, agentDefID)

	// 5. HW-33: rebuild the conversation context the resumed agent must see. The
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

	return RunRequest{
		UserID:            userID,
		SessionID:         sessionID,
		Input:             userMsg,
		History:           resumeHistory,
		ToolNames:         toolNamesFromFlags(toolFlags),
		AgentDefinitionID: agentDefID,
		EnableMemory:      true,
		ExistingRunID:     runID,
	}, nil
}

// Answer handles POST /v1/agent-runs/:id/answer business logic (poll path).
//
// It validates + persists the answer via the shared validateAndPersistAnswer
// helper, re-establishes the narration→buffer forwarder, then restarts
// runner.Run in a detached goroutine so the HTTP handler returns immediately
// (same async pattern as StudentRunService.Create). The frontend then polls
// narration + status. The streaming alternative is AnswerStream (issue4).
func (s *StudentRunService) Answer(ctx context.Context, userID uint, runID uint64, req AnswerRequest) (*AnswerResponse, error) {
	runReq, err := s.validateAndPersistAnswer(ctx, userID, runID, req)
	if err != nil {
		return nil, err
	}

	// Re-establish the narration→buffer forwarder for the resumed run. Phase 1's
	// forwarder exited when RunStream's defer CloseRun closed the channel on yield, so
	// without this the resumed runner's narration (web_search, chart, image_gen, …) is
	// emitted but never drained into the poll buffer → PollNarration returns the stale
	// pre-yield events forever and the UI looks frozen ("答完就停"). Same bridge as
	// Create/RunStream start (student_run_lifecycle.go). nil-safe (graceful degrade).
	go s.forwardNarration(runID)

	// Restart runner in a detached goroutine (detached ctx, same as Create).
	go func() {
		bgCtx := middleware.NewContextWithUserID(context.Background(), userID)
		if _, runErr := s.runner.Run(bgCtx, runReq); runErr != nil {
			log.Errorw("Answer: restart runner failed", "run_id", runID, "error", runErr)
		}
	}()

	return &AnswerResponse{RunID: runID, Status: "resumed"}, nil
}

// AnswerStream handles POST /v1/agent-runs/:id/answer-stream business logic
// (streaming resume path, issue4). It shares validateAndPersistAnswer with the
// poll path (Answer), then drives runner.RunStream on the CALLER's ctx (NOT
// detached — the controller's SSE pump goroutine owns the lifecycle and ch).
// RunStream streams token_delta / reasoning_delta / tool_call_* / question_prompt
// / terminal events onto ch so the resumed leg surfaces streamed assistant prose
// instead of poll-only tool narration. ch is buffered + owned by the caller
// (controller closes it after RunStream returns); AnswerStream does NOT close it.
func (s *StudentRunService) AnswerStream(ctx context.Context, userID uint, runID uint64, req AnswerRequest, ch chan<- stream.Event) (*RunResult, error) {
	runReq, err := s.validateAndPersistAnswer(ctx, userID, runID, req)
	if err != nil {
		return nil, err
	}

	// Re-establish the narration→buffer forwarder (same rationale as Answer);
	// streamed tool-call events still flow over ch, but the poll buffer stays
	// warm so a fallback/refresh mid-resume still sees narration. nil-safe.
	go s.forwardNarration(runID)

	// Stream the resumed leg on the caller's ctx so client disconnect propagates.
	return s.runner.RunStream(ctx, runReq, runID, ch)
}

// buildAnswerMessage formats the user's answers into a human-readable user
// message appended to the conversation history, so the resumed LLM sees the
// full Q&A context (Claude Code's "User has answered your questions" pattern).
// Questions render in the pending order; skipped questions are omitted.
func buildAnswerMessage(pending YieldPayload, answers map[string]AnswerItem) string {
	var sb strings.Builder
	sb.WriteString("用户已回答你的问题：")
	rendered := 0
	for _, q := range pending.Questions {
		item, ok := answers[q.Question]
		if !ok {
			continue
		}
		ans := resolveAnswer(item)
		if ans == "" {
			continue
		}
		sb.WriteString("\n- 「")
		sb.WriteString(q.Question)
		sb.WriteString("」→ ")
		sb.WriteString(ans)
		rendered++
	}
	// Defensive fallback: surface the answers keyed by their own text when no
	// pending question matched. Unreachable from Answer() (it rejects corrupt
	// pending and validates every key against the pending set, so rendered>=1);
	// kept so buildAnswerMessage is safe in isolation / on an empty payload.
	// Sort keys for deterministic output.
	if rendered == 0 {
		keys := make([]string, 0, len(answers))
		for k := range answers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ans := resolveAnswer(answers[k])
			if ans == "" {
				continue
			}
			sb.WriteString("\n- 「")
			sb.WriteString(k)
			sb.WriteString("」→ ")
			sb.WriteString(ans)
		}
	}
	sb.WriteString("\n请据此继续，不要重复已回答的问题。")
	return sb.String()
}

// buildAnswerTurn builds the persisted answer turn appended to agent_run.messages
// on the answer path. content is the human-readable Q&A text (buildAnswerMessage
// output) — the LLM resume history reads role+content only (turnsToHistoryMessages
// / mergeResumeTranscript ignore extra fields; verified). The embedded
// question_answer structure (questions + the user's resolved answer per question,
// only answered questions, in pending order) lets transformMessages reconstruct an
// answered question_prompt card on reload (issue1) instead of an orphan user
// bubble. question_answer is omitted when nothing was answered, so the turn
// degrades to a plain user bubble (matches buildAnswerMessage's fallback).
func buildAnswerTurn(pending YieldPayload, answers map[string]AnswerItem, content string) (json.RawMessage, error) {
	qs := make([]questionPromptItem, 0, len(pending.Questions))
	for _, q := range pending.Questions {
		item, ok := answers[q.Question]
		if !ok {
			continue
		}
		ans := resolveAnswer(item)
		if ans == "" {
			continue
		}
		qs = append(qs, questionPromptItem{
			Question:    q.Question,
			Options:     projectYieldOptions(q.Options),
			Header:      q.Header,
			MultiSelect: q.MultiSelect,
			Answer:      ans,
		})
	}
	turn := map[string]any{"role": "user", "content": content}
	if len(qs) > 0 {
		turn["question_answer"] = map[string]any{"questions": qs}
	}
	return json.Marshal(turn)
}

// resolveAnswer renders one AnswerItem as a single answer string: selected
// option labels joined, with any free text appended.
func resolveAnswer(item AnswerItem) string {
	parts := make([]string, 0, 2)
	if len(item.Selected) > 0 {
		parts = append(parts, strings.Join(item.Selected, "、"))
	}
	if ft := strings.TrimSpace(item.FreeText); ft != "" {
		parts = append(parts, ft)
	}
	return strings.Join(parts, "；")
}

// emitAnswerSpan records a Langfuse span for the answer resume event.
// Gracefully no-ops if langfuse is not active in ctx. Includes wait_duration_ms
// (time since pending_question_at) per spec §6.2.
func emitAnswerSpan(ctx context.Context, run *model.AgentRun, req AnswerRequest) {
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID := langfuse.SpanID()
		// Log only metadata — the question keys answered, not the user's answer
		// values (free text can carry private/proprietary content).
		answered := make([]string, 0, len(req.Answers))
		for q := range req.Answers {
			answered = append(answered, q)
		}
		sort.Strings(answered)
		spanInput := map[string]any{
			"run_id":             run.ID,
			"answered_count":     len(req.Answers),
			"answered_questions": answered,
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
