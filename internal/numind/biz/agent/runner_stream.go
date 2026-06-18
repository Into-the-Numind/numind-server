package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/biz/narration"
	"numind-server/internal/pkg/langfuse"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/model"
)

// toolCallStreamState tracks the start time and other per-call state for an
// in-flight tool invocation within a stream.
type toolCallStreamState struct {
	StartedAt time.Time
}

// consumeEinoStream drains a *schema.StreamReader[*schema.Message] produced by
// einoAgent.Stream, classifies each chunk, and emits stream.Event values onto ch.
//
// Responsibility boundaries (per spec §4.2):
//   - Handles token_delta / reasoning_delta / tool_call_start / tool_call_result
//     classification and emission.
//   - Emits assistant_message + step_done at every ResponseMeta.FinishReason boundary.
//   - Emits terminal(aborted_streaming) on ctx cancellation.
//   - Emits error + sets st.TerminalReason on stream errors via HandleLLMError.
//   - Does NOT touch the hook chain, does NOT call finalizeRun — those live in
//     RunStream (caller).
//
// sr ownership: sr.Close() is deferred here; caller must not close sr.
// ch ownership: the caller (RunStream) closes ch after consumeEinoStream returns.
func (r *agentRunner) consumeEinoStream(
	ctx context.Context,
	run *model.AgentRun,
	sr *schema.StreamReader[*schema.Message],
	ch chan<- stream.Event,
	st *LoopState,
	startTime time.Time,
) (*RunResult, error) {
	defer sr.Close()

	state, hasState := StreamStateFromContext(ctx)

	// T4 (#4) double-emission fix: when the live StreamToolCallChecker
	// (streamScanToolCallChecker) is active it already pumps the per-step ECHO
	// events (token_delta / reasoning_delta / assistant_message / step_done) to
	// the SAME shared channel for every model step. consumeEinoStream drains the
	// END copy of the same model output, so re-emitting those echoes here
	// doubles the user-visible text for a plain-text answer. checkerActive
	// mirrors the checker's own emit guard (`if !hasState || state.Ch == nil`):
	// under exactly that condition the checker pumps, so under it we must NOT
	// re-emit. We still ACCUMULATE content (for stash/terminal/RunResult) and
	// emit the events consumeEinoStream solely owns (stream_start, the single
	// terminal, tool_call_start/result, error, yield). When the checker is NOT
	// active (hypothetical no-checker path) consumeEinoStream emits everything,
	// preserving the fallback.
	checkerActive := hasState && state.Ch != nil

	var (
		currentMsgID  = uuid.NewString()
		currentText   strings.Builder
		currentReason strings.Builder
		toolCalls     = make(map[string]*toolCallStreamState)
		stepIdx       int
		// localSeq is the fallback seq source ONLY when there is no shared stream
		// state (hasState == false: the no-checker / unit-test path). When hasState
		// is true the shared, monotonic state.Seq counter is used instead so this
		// drain goroutine and the graph goroutine (checker / tool adapter) never
		// collide on a seq value.
		localSeq      uint64
		firstByteSent bool
		// lastStepContent stashes currentText.String() right before each
		// step-done Reset() so EOF emit terminal + RunResult.FinalOutput
		// carry the last step's accumulated content. Without this the
		// terminal payload + result read currentText AFTER reset (= ""),
		// agent_run.messages.assistant.content goes in empty, and reload
		// returns an empty history (dev 2026-05-28 multiple runs).
		lastStepContent   string
		lastStepReasoning string
	)
	if hasState && state.CurrentMsgID != "" {
		currentMsgID = state.CurrentMsgID
		stepIdx = int(state.StepIdx.Load())
	}

	// emit safely writes an event to ch. If ctx is already done, the send is
	// skipped to avoid blocking forever on a full or unread channel.
	//
	// Seq source (agent-event-seq-unify): the seq is drawn BEFORE the send from
	// the SINGLE shared, monotonic counter — state.Seq when a shared stream state
	// exists (the production path: this drain goroutine, the graph goroutine's
	// checker, and parallel tool-call emitters all advance the same atomic
	// counter so values never collide or go backwards), or the localSeq fallback
	// when there is no shared state (the no-checker / unit-test path).
	//
	// This intentionally increments BEFORE the send (was: increment-only-on-
	// successful-send). A dropped event on ctx.Done() therefore leaves a 1-gap in
	// the seq stream. That is ACCEPTABLE: seq is an ordering key for the client,
	// NOT a gap-detector (the FE does not gap-fill), and a gap can only occur at
	// ctx cancellation — i.e. the run is ending anyway.
	emit := func(t stream.EventType, payload any) {
		var nextSeq uint64
		if hasState {
			nextSeq = state.Seq.Add(1)
		} else {
			localSeq++
			nextSeq = localSeq
		}
		ev, err := stream.Encode(t, payload, nextSeq, run.ID, stepIdx)
		if err != nil {
			log.Warnw("consumeEinoStream: stream.Encode failed",
				"agent_run_id", run.ID, "event_type", t, "error", err)
			return
		}
		select {
		case ch <- ev:
			if !firstByteSent {
				firstByteSent = true
			}
		case <-ctx.Done():
		}
	}

	// handleYield surfaces an ask_user_question pause: persist the question,
	// emit a question_prompt + a waiting_for_user_choice terminal, and return a
	// non-error RunResult. Streaming counterpart of runner.go's Run yield
	// handler. Reached when the tool adapter captured a yield into stream state
	// (PendingYield) or the sentinel propagated as a stream error.
	handleYield := func(p YieldPayload) (*RunResult, error) {
		return r.persistAndEmitYield(ctx, run.ID, st, emit, startTime, p)
	}

	// Emit stream_start immediately.
	emit(stream.EventStreamStart, map[string]any{
		"session_id": run.SessionID,
		"run_id":     run.ID,
	})

	for {
		// Check ctx before blocking on Recv.
		select {
		case <-ctx.Done():
			emit(stream.EventTerminal, stream.TerminalPayload{
				Reason:      string(TerminalAbortedStreaming),
				DurationMs:  time.Since(startTime).Milliseconds(),
				StepCount:   st.StepCount,
				UserMessage: UserFacingTerminalMessage(TerminalAbortedStreaming),
			})
			st.TerminalReason = TerminalAbortedStreaming
			return &RunResult{
				AgentRunID:     run.ID,
				TerminalReason: TerminalAbortedStreaming,
				StepCount:      st.StepCount,
				Duration:       time.Since(startTime),
			}, nil
		default:
		}

		msg, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// ask_user_question pause takes priority over error classification:
			// the tool adapter captured the yield payload into stream state, and
			// the sentinel may also have propagated as this stream error. Surface
			// the question instead of a model_error terminal.
			if hasState && state.PendingYield != nil {
				return handleYield(*state.PendingYield)
			}
			var yErr *yieldError
			if errors.As(err, &yErr) {
				return handleYield(yErr.Payload)
			}
			// Classify via the same HandleLLMError logic as Run().
			event := HandleLLMError(st, err)

			// Map the event to a terminal reason string for the error payload.
			// P2 fix (T04): ErrorPayload.Code must be one of the spec §3.1 valid values
			// ("model_error" / "permission" / "internal"). "image_error" is not a valid
			// ErrorPayload.Code. Use "model_error" here; the EventTerminal below still
			// carries the precise TerminalImageError reason so the FE can distinguish.
			var errCode string
			switch event {
			case LoopEventLLMErrImage:
				st.TerminalReason = TerminalImageError
				errCode = "model_error"
			case LoopEventLLMErrPTL:
				st.TerminalReason = TerminalPromptTooLong
				errCode = "model_error"
			case LoopEventLLMErrMaxOutput:
				st.TerminalReason = TerminalErrorMaxBudget
				errCode = "model_error"
			default:
				st.TerminalReason = TerminalModelError
				errCode = "model_error"
			}

			log.Warnw("consumeEinoStream: sr.Recv error",
				"agent_run_id", run.ID, "terminal_reason", st.TerminalReason, "error", err)

			emit(stream.EventError, stream.ErrorPayload{
				Code:    errCode,
				Message: UserFacingErrorMessage(err), // raw err already logged above (line ~239)
			})
			emit(stream.EventTerminal, stream.TerminalPayload{
				Reason:      string(st.TerminalReason),
				DurationMs:  time.Since(startTime).Milliseconds(),
				StepCount:   st.StepCount,
				UserMessage: UserFacingTerminalMessage(st.TerminalReason),
			})
			return &RunResult{
				AgentRunID:     run.ID,
				TerminalReason: st.TerminalReason,
				FinalReasoning: currentReason.String(),
				StepCount:      st.StepCount,
				Duration:       time.Since(startTime),
			}, err
		}

		if msg == nil {
			continue
		}

		// Classify the message by role.
		switch msg.Role {
		case schema.Assistant:
			// Text delta. Accumulate ALWAYS (terminal/stash/RunResult depend on
			// it); only emit the echo when the live checker is NOT pumping (T4 #4).
			if msg.Content != "" {
				currentText.WriteString(msg.Content)
				if !checkerActive {
					emit(stream.EventTokenDelta, stream.TokenDeltaPayload{
						MessageID: currentMsgID,
						Text:      msg.Content,
					})
				}
			}
			// Reasoning delta (thinking models). Accumulate ALWAYS; only emit the
			// echo when the live checker is NOT pumping (T4 #4).
			if msg.ReasoningContent != "" {
				currentReason.WriteString(msg.ReasoningContent)
				if !checkerActive {
					emit(stream.EventReasoningDelta, stream.ReasoningDeltaPayload{
						MessageID: currentMsgID,
						Text:      msg.ReasoningContent,
					})
				}
			}
			// Tool calls — dedup by ID (same tool_call can arrive across multiple chunks).
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" {
					continue
				}
				if _, seen := toolCalls[tc.ID]; seen {
					continue
				}
				toolCalls[tc.ID] = &toolCallStreamState{StartedAt: time.Now()}

				// Build a short digest of the input arguments.
				inputDigest := inputSHA(tc.Function.Arguments)
				var inputPreview map[string]any
				if tc.Function.Arguments != "" {
					_ = json.Unmarshal([]byte(tc.Function.Arguments), &inputPreview)
					inputPreview = truncateMapValues(inputPreview, 500)
				}
				emit(stream.EventToolCallStart, stream.ToolCallStartPayload{
					ToolCallID:   tc.ID,
					ToolName:     tc.Function.Name,
					InputDigest:  inputDigest,
					InputPreview: inputPreview,
				})
			}

		case schema.Tool:
			// Tool result message.
			if msg.ToolCallID != "" {
				state, ok := toolCalls[msg.ToolCallID]
				var durationMs int64
				if ok {
					durationMs = time.Since(state.StartedAt).Milliseconds()
				}
				artURL, artName, artMime := artifactFromToolResult(msg.Content)
				emit(stream.EventToolCallResult, stream.ToolCallResultPayload{
					ToolCallID:       msg.ToolCallID,
					Preview:          truncateRunes(msg.Content, 500),
					ArtifactURL:      artURL,
					ArtifactFilename: artName,
					ArtifactMime:     artMime,
					DurationMs:       durationMs,
				})
			}
		}

		// Detect step boundary: FinishReason populated means the assistant message is complete.
		if msg.ResponseMeta != nil && msg.ResponseMeta.FinishReason != "" {
			hasToolCalls := len(msg.ToolCalls) > 0

			// Echo events: emit only when the live checker is NOT pumping them
			// (T4 #4). The stash + rotation below stay UNCONDITIONAL so the
			// terminal payload, RunResult.FinalOutput, and StepCount remain
			// correct regardless of who emits the echoes.
			if !checkerActive {
				emit(stream.EventAssistantMessage, stream.AssistantMessagePayload{
					MessageID:        currentMsgID,
					Content:          currentText.String(),
					ReasoningContent: currentReason.String(),
					HasToolCalls:     hasToolCalls,
				})
				emit(stream.EventStepDone, stream.StepDonePayload{
					StepIndex:  stepIdx,
					StopReason: msg.ResponseMeta.FinishReason,
				})
			}

			// Stash the just-emitted assistant content BEFORE Reset so EOF can
			// recover it as the final answer (single-step or last step of
			// multi-step ReAct). Without this stash, the terminal payload +
			// RunResult would read currentText AFTER reset and get an empty
			// string.
			lastStepContent = currentText.String()
			lastStepReasoning = currentReason.String()

			// Rotate state for the next step.
			stepIdx++
			st.StepCount = stepIdx
			currentMsgID = uuid.NewString()
			currentText.Reset()
			currentReason.Reset()
		}
	}

	// ask_user_question pause may surface as a clean EOF (the sentinel did not
	// propagate as a stream error) with the payload captured in stream state.
	if hasState && state.PendingYield != nil {
		return handleYield(*state.PendingYield)
	}

	// Normal EOF: stream completed successfully.
	st.TerminalReason = TerminalCompleted

	// Determine the final assistant content. Prefer lastStepContent (stashed
	// before every step-done Reset()). Fallback to currentText.String() in
	// the edge case where the LLM streamed content but never emitted
	// FinishReason — no step-done fired, so nothing was stashed and
	// currentText still holds the un-reset accumulation.
	finalContent := lastStepContent
	if finalContent == "" && hasState && state.LastStepContent != "" {
		finalContent = state.LastStepContent
	}
	if finalContent == "" && currentText.Len() > 0 {
		finalContent = currentText.String()
	}

	// Embed any tool-generated artifacts (images + documents/HTML) as markdown so they
	// are PERSISTED in the final answer (agent_run.messages) and render durably on
	// reload — the transient SSE artifact event alone vanishes when loadSessionSnapshot
	// rebuilds the conversation from the DB. finalizeInto drains so the subsequent
	// finalizeRun embed doesn't append a second time, AND excludes any image the model
	// already embedded (no double render) + appends documents as standalone card links
	// (问题五). dev 2026-06-18.
	finalContent = artifactCollectorFrom(ctx).finalizeInto(finalContent)

	finalReasoning := lastStepReasoning
	if finalReasoning == "" && currentReason.Len() > 0 {
		finalReasoning = currentReason.String()
	}

	// Emit terminal. (No pre-terminal seq sync needed anymore: the emit closure
	// reads/advances the shared state.Seq directly, so the terminal continues the
	// single unified counter without a manual hand-off.)
	emit(stream.EventTerminal, stream.TerminalPayload{
		Reason:      string(TerminalCompleted),
		DurationMs:  time.Since(startTime).Milliseconds(),
		StepCount:   st.StepCount,
		FinalOutput: finalContent,
	})

	return &RunResult{
		AgentRunID:     run.ID,
		TerminalReason: TerminalCompleted,
		FinalOutput:    finalContent,
		FinalReasoning: finalReasoning,
		StepCount:      st.StepCount,
		Duration:       time.Since(startTime),
	}, nil
}

// inputSHA returns a short hex digest of a JSON arguments string.
// Used as InputDigest in ToolCallStartPayload.
func inputSHA(args string) string {
	if args == "" {
		return ""
	}
	h := sha256.Sum256([]byte(args))
	return fmt.Sprintf("%x", h[:8])
}

// truncateMapValues returns a shallow copy of m where any string value longer
// than maxLen runes is truncated. Uses the package-level truncateRunes helper
// (defined in student_query.go).
func truncateMapValues(m map[string]any, maxLen int) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok && len([]rune(s)) > maxLen {
			out[k] = truncateRunes(s, maxLen)
		} else {
			out[k] = v
		}
	}
	return out
}

// persistAndEmitYield is the shared core of the streaming yield protocol:
// persist the pending question, record a Langfuse span, emit question_prompt +
// waiting_for_user_choice terminal via the supplied emitter, and drive the
// LoopState machine. Used by consumeEinoStream (yield arriving via the
// returned stream / PendingYield) and by RunStream's einoAgent.Stream error
// branch (yield raised mid-graph in multi-step ReAct surfaces as Stream()'s
// returned error — dev run #117). Keeping one core prevents the two paths
// from drifting.
func (r *agentRunner) persistAndEmitYield(ctx context.Context, runID uint64, st *LoopState,
	emit func(stream.EventType, any), startTime time.Time, p YieldPayload) (*RunResult, error) {
	payloadJSON, mErr := json.Marshal(p)
	if mErr != nil {
		log.Errorw("persistAndEmitYield: marshal yield payload failed",
			"agent_run_id", runID, "error", mErr)
		st.TerminalReason = TerminalModelError
		emit(stream.EventTerminal, stream.TerminalPayload{
			Reason:      string(TerminalModelError),
			DurationMs:  time.Since(startTime).Milliseconds(),
			StepCount:   st.StepCount,
			UserMessage: UserFacingTerminalMessage(TerminalModelError),
		})
		return &RunResult{AgentRunID: runID, TerminalReason: TerminalModelError, StepCount: st.StepCount, Duration: time.Since(startTime)}, nil
	}
	if pErr := r.runStore.UpdatePendingQuestion(ctx, runID, payloadJSON); pErr != nil {
		// Non-fatal: still surface the question; the answer endpoint's
		// pending-json guard rejects resume if it truly failed to persist.
		log.Warnw("persistAndEmitYield: UpdatePendingQuestion failed",
			"agent_run_id", runID, "error", pErr)
	}
	// Langfuse span for observability parity with runner.go's Run yield path.
	if tc := langfuse.FromContext(ctx); tc != nil {
		spanID := langfuse.SpanID()
		langfuse.CreateSpan(tc.TraceID, spanID, "tool.ask_user_question.yield",
			langfuse.WithSpanParent(tc.ParentObservationID),
			langfuse.WithSpanInput(p),
		)
		langfuse.EndSpan(tc.TraceID, spanID)
	}
	qs := make([]stream.QuestionPromptItem, 0, len(p.Questions))
	for _, q := range p.Questions {
		// The backend YieldOption carries a machine `key`, but the client
		// identifies options by label, so key is intentionally not forwarded.
		opts := make([]stream.QuestionOption, 0, len(q.Options))
		for _, o := range q.Options {
			opts = append(opts, stream.QuestionOption{Label: o.Label, Description: o.Description})
		}
		qs = append(qs, stream.QuestionPromptItem{
			Question:    q.Question,
			Options:     opts,
			Header:      q.Header,
			MultiSelect: q.MultiSelect,
		})
	}
	emit(stream.EventQuestionPrompt, stream.QuestionPromptPayload{Questions: qs})
	// Drive the state machine (parity with runner.go's Run yield path) —
	// this sets st.TerminalReason = TerminalWaitingForUserChoice.
	st.Transition(LoopEventAskUserPaused)
	emit(stream.EventTerminal, stream.TerminalPayload{
		Reason:      string(TerminalWaitingForUserChoice),
		DurationMs:  time.Since(startTime).Milliseconds(),
		StepCount:   st.StepCount,
		UserMessage: UserFacingTerminalMessage(TerminalWaitingForUserChoice),
	})
	return &RunResult{
		AgentRunID:     runID,
		TerminalReason: TerminalWaitingForUserChoice,
		StepCount:      st.StepCount,
		Duration:       time.Since(startTime),
	}, nil
}

// yieldFromStreamFailure extracts a yield payload from an einoAgent.Stream
// failure. Checks the adapter-captured stream state first (authoritative
// payload, set by fullToolEinoAdapter.InvokableRun), then walks the error
// chain — eino's internalError implements Unwrap and tool_node wraps the tool
// error with %w, so errors.As reaches the yieldError sentinel.
func yieldFromStreamFailure(state *StreamSessionState, err error) (*YieldPayload, bool) {
	if state != nil && state.PendingYield != nil {
		return state.PendingYield, true
	}
	var yErr *yieldError
	if errors.As(err, &yErr) {
		p := yErr.Payload
		return &p, true
	}
	return nil, false
}

// persistYieldTranscript writes the agent's pre-yield ReAct transcript (user
// input + assistant steps + tool groups it ran before pausing) into
// agent_run.messages. Before this the yield paths persisted nothing, so a
// waiting run held messages=[] and the resumed agent (which reloads context
// from persisted transcripts) forgot all prior work — it re-asked for facts it
// had already researched (HW-33 / dev run #119). Best-effort: a write failure
// is logged, not fatal — the pending question + answer flow still proceed.
func (r *agentRunner) persistYieldTranscript(ctx context.Context, runID uint64, userInput string, atts []displayAttachment) {
	turns := buildTranscriptTurns(
		userInput,
		stepCollectorFrom(ctx).list(),
		narration.CollectorFrom(ctx).Events(),
		"", "",
	)
	if turns == nil {
		// No assistant steps captured (e.g. agent asked on its very first turn,
		// or the stream-error yield path where consumeEinoStream never ran so
		// stepCollector is empty — only narration tool events exist). Persist at
		// least the user input + any tool groups so resume context and session
		// history are non-empty.
		turns = []map[string]any{{"role": "user", "content": userInput}}
		if groups := aggregateToolEvents(narration.CollectorFrom(ctx).Events()); len(groups) > 0 {
			turns = append(turns, map[string]any{"role": "tool_group", "tool_calls": groups})
		}
	}
	// 问题二: carry the user's uploaded attachments onto the user turn so a session
	// reloaded WHILE paused at ask_user_question still shows chips (before resume's
	// finalizeRun re-persists them). Runs before the multi-yield prior merge so it
	// targets this leg's user turn.
	setUserTurnAttachments(turns, atts)
	// HW-33 multi-yield: if a transcript already exists (this is a resumed run
	// that paused AGAIN — e.g. the agent asks a second clarifying question),
	// prepend it so the earlier yield's work is not clobbered by this overwrite.
	// AnswerAndClear already appended the prior answer as the existing tail user
	// turn, which duplicates this turn's leading user turn (userInput == that
	// answer), so drop the leading dup before merging.
	if prior := r.existingTranscriptTurns(ctx, runID); len(prior) > 0 {
		if len(turns) > 0 {
			if role, _ := turns[0]["role"].(string); role == "user" {
				turns = turns[1:]
			}
		}
		turns = append(prior, turns...)
	}
	finalMessages, mErr := json.Marshal(turns)
	if mErr != nil {
		log.Warnw("persistYieldTranscript: marshal failed", "agent_run_id", runID, "error", mErr)
		return
	}
	if err := r.runStore.WriteTurn(ctx, runID, json.RawMessage(finalMessages)); err != nil {
		log.Warnw("persistYieldTranscript: WriteTurn failed", "agent_run_id", runID, "error", err)
	}
}

// existingTranscriptTurns returns the already-persisted transcript turns for a
// run, or nil if the run has no usable transcript yet (the common single-yield
// case where messages is empty "[]"). Used by persistYieldTranscript to avoid
// clobbering a prior yield's context on a re-paused resume run.
func (r *agentRunner) existingTranscriptTurns(ctx context.Context, runID uint64) []map[string]any {
	existing, err := r.runStore.Get(ctx, runID)
	if err != nil || existing == nil || len(existing.Messages) == 0 || string(existing.Messages) == "[]" || string(existing.Messages) == "null" {
		return nil
	}
	var turns []map[string]any
	if uerr := json.Unmarshal(existing.Messages, &turns); uerr != nil {
		return nil
	}
	return turns
}
