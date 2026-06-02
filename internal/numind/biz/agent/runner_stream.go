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

	var (
		currentMsgID  = uuid.NewString()
		currentText   strings.Builder
		currentReason strings.Builder
		toolCalls     = make(map[string]*toolCallStreamState)
		stepIdx       int
		seq           uint64
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
		stepIdx = state.StepIdx
	}

	// emit safely writes an event to ch. If ctx is already done, the send is
	// skipped to avoid blocking forever on a full or unread channel.
	//
	// P1 fix (T04): seq++ fires ONLY inside the successful send branch. Previously
	// seq++ ran unconditionally before the select, so a ctx.Done() win (dropped
	// event) still advanced seq — producing monotonic-sequence gaps that the SSE
	// client would interpret as lost events.
	emit := func(t stream.EventType, payload any) {
		ev, err := stream.Encode(t, payload, seq+1, run.ID, stepIdx)
		if err != nil {
			log.Warnw("consumeEinoStream: stream.Encode failed",
				"agent_run_id", run.ID, "event_type", t, "error", err)
			return
		}
		select {
		case ch <- ev:
			seq++
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
		payloadJSON, mErr := json.Marshal(p)
		if mErr != nil {
			log.Errorw("consumeEinoStream: marshal yield payload failed",
				"agent_run_id", run.ID, "error", mErr)
			st.TerminalReason = TerminalModelError
			emit(stream.EventTerminal, stream.TerminalPayload{
				Reason:     string(TerminalModelError),
				DurationMs: time.Since(startTime).Milliseconds(),
				StepCount:  st.StepCount,
			})
			return &RunResult{AgentRunID: run.ID, TerminalReason: TerminalModelError, StepCount: st.StepCount, Duration: time.Since(startTime)}, nil
		}
		if pErr := r.runStore.UpdatePendingQuestion(ctx, run.ID, payloadJSON); pErr != nil {
			// Non-fatal: still surface the question; the answer endpoint's
			// pending-json guard rejects resume if it truly failed to persist.
			log.Warnw("consumeEinoStream: UpdatePendingQuestion failed",
				"agent_run_id", run.ID, "error", pErr)
		}
		opts := make([]stream.QuestionOption, 0, len(p.Options))
		for _, o := range p.Options {
			opts = append(opts, stream.QuestionOption{Label: o.Label, Description: o.Description})
		}
		emit(stream.EventQuestionPrompt, stream.QuestionPromptPayload{
			Question:    p.Question,
			Options:     opts,
			Header:      p.Header,
			MultiSelect: p.MultiSelect,
		})
		st.TerminalReason = TerminalWaitingForUserChoice
		emit(stream.EventTerminal, stream.TerminalPayload{
			Reason:     string(TerminalWaitingForUserChoice),
			DurationMs: time.Since(startTime).Milliseconds(),
			StepCount:  st.StepCount,
		})
		return &RunResult{
			AgentRunID:     run.ID,
			TerminalReason: TerminalWaitingForUserChoice,
			StepCount:      st.StepCount,
			Duration:       time.Since(startTime),
		}, nil
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
				Reason:     string(TerminalAbortedStreaming),
				DurationMs: time.Since(startTime).Milliseconds(),
				StepCount:  st.StepCount,
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
				Message: err.Error(),
			})
			emit(stream.EventTerminal, stream.TerminalPayload{
				Reason:     string(st.TerminalReason),
				DurationMs: time.Since(startTime).Milliseconds(),
				StepCount:  st.StepCount,
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
			// Text delta.
			if msg.Content != "" {
				currentText.WriteString(msg.Content)
				emit(stream.EventTokenDelta, stream.TokenDeltaPayload{
					MessageID: currentMsgID,
					Text:      msg.Content,
				})
			}
			// Reasoning delta (thinking models).
			if msg.ReasoningContent != "" {
				currentReason.WriteString(msg.ReasoningContent)
				emit(stream.EventReasoningDelta, stream.ReasoningDeltaPayload{
					MessageID: currentMsgID,
					Text:      msg.ReasoningContent,
				})
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
				emit(stream.EventToolCallResult, stream.ToolCallResultPayload{
					ToolCallID: msg.ToolCallID,
					Preview:    truncateRunes(msg.Content, 500),
					DurationMs: durationMs,
				})
			}
		}

		// Detect step boundary: FinishReason populated means the assistant message is complete.
		if msg.ResponseMeta != nil && msg.ResponseMeta.FinishReason != "" {
			hasToolCalls := len(msg.ToolCalls) > 0

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

	finalReasoning := lastStepReasoning
	if finalReasoning == "" && currentReason.Len() > 0 {
		finalReasoning = currentReason.String()
	}

	if hasState {
		seq = state.Seq
	}

	// Emit terminal.
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
