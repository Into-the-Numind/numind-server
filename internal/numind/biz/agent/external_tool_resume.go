package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/cloudwego/eino/schema"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

const externalOperationIDExtraKey = "external_operation_id"

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
	store       store.IExternalToolResumeLease
	studentRuns *StudentRunService
}

// NewAgentRunResumer constructs the bridge used by the outer Feishu
// composition layer. Keeping the dependencies narrow avoids a feishu→agent
// package cycle and lets DataStore's concrete agent-run store be asserted to
// IExternalToolResumer at composition time.
func NewAgentRunResumer(runStore store.IExternalToolResumeLease, studentRuns *StudentRunService) *AgentRunResumer {
	return &AgentRunResumer{store: runStore, studentRuns: studentRuns}
}

// Resume appends the original tool result once and resumes the same run. A
// duplicate, cancelled, or deleted callback is a successful no-op.
func (r *AgentRunResumer) Resume(ctx context.Context, result ExternalToolResult) error {
	if r == nil || r.store == nil || r.studentRuns == nil || r.studentRuns.runner == nil {
		return fmt.Errorf("AgentRunResumer.Resume: resumer is not configured")
	}
	snapshot, err := r.studentRuns.runStore.Get(ctx, result.RunID)
	if err != nil {
		return fmt.Errorf("AgentRunResumer.Resume: load run snapshot: %w", err)
	}
	if snapshot.CancellationRequestedAt != nil || snapshot.IsDeleted {
		_, _, claimErr := r.store.ClaimExternalToolResume(ctx, result.RunID, result.OperationID, result.ToolCallID, result.Result)
		return claimErr
	}
	runReq, err := r.studentRuns.buildExternalResumeRequest(ctx, snapshot, result)
	if err != nil {
		return fmt.Errorf("AgentRunResumer.Resume: validate continuation before claim: %w", err)
	}
	leaseToken, claimed, err := r.store.ClaimExternalToolResume(
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
	return r.startClaimedContinuation(result, leaseToken, runReq)
}

type externalContinuationReadySignal struct {
	err error
	ack chan struct{}
}

func (r *AgentRunResumer) startClaimedContinuation(result ExternalToolResult, leaseToken string, runReq RunRequest) error {
	readyCh := make(chan externalContinuationReadySignal, 1)
	finishedCh := make(chan error, 1)
	abandoned := make(chan struct{})
	var accepted atomic.Bool
	var readyInvoked atomic.Bool
	runReq.ExternalContinuationReady = func(readyCtx context.Context) error {
		if !readyInvoked.CompareAndSwap(false, true) {
			return fmt.Errorf("external continuation readiness was already acknowledged")
		}
		err := r.store.CompleteExternalToolResume(
			readyCtx, result.RunID, result.OperationID, result.ToolCallID, leaseToken,
		)
		signal := externalContinuationReadySignal{err: err, ack: make(chan struct{})}
		select {
		case readyCh <- signal:
		case <-abandoned:
			return fmt.Errorf("external continuation readiness was abandoned")
		}
		select {
		case <-signal.ack:
		case <-abandoned:
			return fmt.Errorf("external continuation readiness was abandoned")
		}
		return err
	}

	go r.studentRuns.forwardNarration(result.RunID)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				panicErr := fmt.Errorf("runner preflight panic: %v", recovered)
				if accepted.Load() {
					log.Errorw("external tool continuation panicked after runner start", "run_id", result.RunID, "error", panicErr)
					return
				}
				select {
				case finishedCh <- panicErr:
				default:
				}
			}
		}()
		bgCtx := middleware.NewContextWithUserID(context.Background(), runReq.UserID)
		_, runErr := r.studentRuns.runner.Run(bgCtx, runReq)
		if accepted.Load() {
			if runErr != nil {
				log.Errorw("external tool continuation failed after runner start", "run_id", result.RunID, "error", runErr)
			}
			return
		}
		select {
		case finishedCh <- runErr:
		default:
		}
	}()

	select {
	case signal := <-readyCh:
		if signal.err == nil {
			accepted.Store(true)
		}
		close(signal.ack)
		if signal.err != nil {
			releaseErr := r.store.ReleaseExternalToolResume(context.Background(), result.RunID, result.OperationID, result.ToolCallID, leaseToken)
			return errors.Join(fmt.Errorf("AgentRunResumer.Resume: acknowledge runner start: %w", signal.err), releaseErr)
		}
		return nil
	case runErr := <-finishedCh:
		close(abandoned)
		if runErr == nil {
			runErr = fmt.Errorf("runner exited before external continuation readiness")
		}
		releaseErr := r.store.ReleaseExternalToolResume(context.Background(), result.RunID, result.OperationID, result.ToolCallID, leaseToken)
		return errors.Join(fmt.Errorf("AgentRunResumer.Resume: runner preflight: %w", runErr), releaseErr)
	}
}

func (s *StudentRunService) buildExternalResumeRequest(
	ctx context.Context,
	run *model.AgentRun,
	result ExternalToolResult,
) (RunRequest, error) {
	if s == nil || s.runStore == nil || run == nil || run.ID != result.RunID || result.RunID == 0 {
		return RunRequest{}, fmt.Errorf("external resume snapshot identity is invalid")
	}
	var turns []map[string]any
	if err := json.Unmarshal(run.Messages, &turns); err != nil {
		return RunRequest{}, fmt.Errorf("invalid external resume transcript: %w", err)
	}
	pendingPresent := hasPendingExternalAction(run.PendingExternalActionJSON)
	if pendingPresent {
		pending, err := ParsePendingExternalAction(run.PendingExternalActionJSON)
		if err != nil {
			return RunRequest{}, fmt.Errorf("invalid pending external identity: %w", err)
		}
		if pending.OperationID != result.OperationID || pending.ToolCallID != result.ToolCallID {
			return RunRequest{}, fmt.Errorf("pending external identity mismatch")
		}
	}
	found, err := findExternalToolResult(turns, result)
	if err != nil {
		return RunRequest{}, err
	}
	if !found {
		if !pendingPresent || run.StateReason != string(TerminalWaitingForUserChoice) {
			return RunRequest{}, fmt.Errorf("durable external result is missing")
		}
		compactResult, err := compactExternalResult(result.Result)
		if err != nil {
			return RunRequest{}, err
		}
		msg := schema.ToolMessage(string(compactResult), result.ToolCallID)
		msg.Extra = map[string]any{externalOperationIDExtraKey: result.OperationID}
		raw, err := json.Marshal(msg)
		if err != nil {
			return RunRequest{}, err
		}
		var turn map[string]any
		if err := json.Unmarshal(raw, &turn); err != nil {
			return RunRequest{}, err
		}
		turns = append(turns, turn)
	}
	history, err := turnsToExternalResumeHistoryMessages(turns)
	if err != nil {
		return RunRequest{}, err
	}
	resumeHistory := s.loadSessionHistory(context.WithoutCancel(ctx), run.SessionID, run.ID)
	resumeHistory = append(resumeHistory, history...)
	toolFlags := resolveToolFlags(context.WithoutCancel(ctx), s, run.AgentDefinitionID)
	return RunRequest{
		UserID: run.UserID, SessionID: run.SessionID, History: resumeHistory,
		ToolNames: toolNamesFromFlags(toolFlags), AgentDefinitionID: run.AgentDefinitionID,
		EnableMemory: true, ExistingRunID: run.ID, IsTest: run.IsTest,
		ContinueWithoutUserInput: true,
	}, nil
}

func findExternalToolResult(turns []map[string]any, result ExternalToolResult) (bool, error) {
	found := false
	for index, turn := range turns {
		role, _ := turn["role"].(string)
		if strings.TrimSpace(role) == "" {
			return false, fmt.Errorf("external resume transcript turn %d has no role", index)
		}
		if role != "tool" {
			continue
		}
		raw, err := json.Marshal(turn)
		if err != nil {
			return false, err
		}
		var msg schema.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			return false, err
		}
		if msg.ToolCallID != result.ToolCallID {
			continue
		}
		if found {
			return false, fmt.Errorf("duplicate external tool result")
		}
		operationID, _ := msg.Extra[externalOperationIDExtraKey].(string)
		if operationID != result.OperationID {
			return false, fmt.Errorf("consumed operation identity mismatch")
		}
		equal, err := externalJSONEqual([]byte(msg.Content), result.Result)
		if err != nil || !equal {
			if err == nil {
				err = fmt.Errorf("external tool result mismatch")
			}
			return false, err
		}
		found = true
	}
	return found, nil
}

func compactExternalResult(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || !json.Valid(raw) {
		return nil, fmt.Errorf("external tool result is invalid JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, err
	}
	return json.RawMessage(compact.Bytes()), nil
}

func externalJSONEqual(left, right []byte) (bool, error) {
	decode := func(raw []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("trailing JSON")
		}
		return value, nil
	}
	a, err := decode(left)
	if err != nil {
		return false, err
	}
	b, err := decode(right)
	if err != nil {
		return false, err
	}
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return bytes.Equal(aJSON, bJSON), nil
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
			content := historyTurnText(turn["content"])
			if strings.TrimSpace(content) != "" {
				out = append(out, schema.AssistantMessage(content, nil))
			}
		case "tool":
			toolCallID, _ := turn["tool_call_id"].(string)
			content, contentOK := turn["content"].(string)
			toolCallID = strings.TrimSpace(toolCallID)
			if toolCallID == "" || !contentOK || strings.TrimSpace(content) == "" || !json.Valid([]byte(content)) {
				return nil, fmt.Errorf("external resume transcript tool turn %d is invalid", index)
			}
			out = append(out, schema.AssistantMessage("", []schema.ToolCall{{
				ID:   toolCallID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "lark_execute",
					Arguments: `{}`,
				},
			}}))
			out = append(out, schema.ToolMessage(content, toolCallID, schema.WithToolName("lark_execute")))
		case "tool_group":
			// UI narration only; it is not a provider protocol message.
		default:
			return nil, fmt.Errorf("external resume transcript turn %d has unsupported role %q", index, role)
		}
	}
	return out, nil
}
