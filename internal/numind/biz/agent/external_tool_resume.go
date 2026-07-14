package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/middleware"
	"numind-server/internal/pkg/model"
)

const externalOperationIDExtraKey = "external_operation_id"

const externalResumeWorkerLimit = 4

var errExternalContinuationFirstCall = errors.New("external continuation first model call did not start durably")

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
	store            store.IExternalToolResumeLease
	studentRuns      *StudentRunService
	preflightTimeout time.Duration
}

// ExternalResumeReclaimer makes durable completed external actions self-heal
// after a process crash. ClaimExternalToolResume supplies the cross-instance
// fence, so several application instances may scan the same candidates.
type ExternalResumeReclaimer struct {
	store       store.IExternalToolResumeLease
	resumer     *AgentRunResumer
	interval    time.Duration
	cancel      context.CancelFunc
	done        chan struct{}
	mu          sync.Mutex
	workers     sync.WaitGroup
	workerSlots chan struct{}
}

func NewExternalResumeReclaimer(s store.IExternalToolResumeLease, resumer *AgentRunResumer, interval time.Duration) *ExternalResumeReclaimer {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &ExternalResumeReclaimer{
		store: s, resumer: resumer, interval: interval,
		workerSlots: make(chan struct{}, externalResumeWorkerLimit),
	}
}

func (r *ExternalResumeReclaimer) Start() {
	if r == nil || r.store == nil || r.resumer == nil {
		return
	}
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	done := r.done
	r.mu.Unlock()
	go func() {
		defer close(done)
		r.scan(ctx)
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.scan(ctx)
			}
		}
	}()
}

func (r *ExternalResumeReclaimer) Stop(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	workersDone := make(chan struct{})
	go func() {
		r.workers.Wait()
		close(workersDone)
	}()
	select {
	case <-workersDone:
		r.mu.Lock()
		if r.done == done {
			r.cancel = nil
			r.done = nil
		}
		r.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *ExternalResumeReclaimer) scan(ctx context.Context) {
	runs, err := r.store.ListExternalToolResumeCandidates(ctx, time.Now().Add(-30*time.Second), 100)
	if err != nil {
		if ctx.Err() == nil {
			log.Warnw("external resume reclaimer scan failed", "error", err)
		}
		return
	}
	for i := range runs {
		if ctx.Err() != nil {
			return
		}
		result, err := reconstructExternalToolResult(&runs[i])
		if err != nil {
			log.Warnw("external resume reclaimer found invalid durable result", "agent_run_id", runs[i].ID, "error", err)
			continue
		}
		select {
		case r.workerSlots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		r.workers.Add(1)
		go func(candidate ExternalToolResult) {
			defer func() {
				<-r.workerSlots
				r.workers.Done()
			}()
			if err := r.resumer.Resume(ctx, candidate); err != nil && ctx.Err() == nil {
				log.Warnw("external resume reclaimer could not resume run", "agent_run_id", candidate.RunID, "error", err)
			}
		}(result)
	}
}

func reconstructExternalToolResult(run *model.AgentRun) (ExternalToolResult, error) {
	if run == nil || !hasPendingExternalAction(run.PendingExternalActionJSON) {
		return ExternalToolResult{}, fmt.Errorf("external recovery candidate has no pending identity")
	}
	pending, err := ParsePendingExternalAction(run.PendingExternalActionJSON)
	if err != nil {
		return ExternalToolResult{}, err
	}
	var turns []map[string]any
	if err := json.Unmarshal(run.Messages, &turns); err != nil {
		return ExternalToolResult{}, err
	}
	var result json.RawMessage
	for _, turn := range turns {
		if role, _ := turn["role"].(string); role != "tool" {
			continue
		}
		raw, err := json.Marshal(turn)
		if err != nil {
			return ExternalToolResult{}, err
		}
		var msg schema.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			return ExternalToolResult{}, err
		}
		opID, _ := msg.Extra[externalOperationIDExtraKey].(string)
		if msg.ToolCallID != pending.ToolCallID || opID != pending.OperationID {
			continue
		}
		if result != nil {
			return ExternalToolResult{}, fmt.Errorf("duplicate durable external result")
		}
		result, err = compactExternalResult(json.RawMessage(msg.Content))
		if err != nil {
			return ExternalToolResult{}, err
		}
	}
	if result == nil {
		return ExternalToolResult{}, fmt.Errorf("durable external result is missing")
	}
	return ExternalToolResult{RunID: run.ID, OperationID: pending.OperationID, ToolCallID: pending.ToolCallID, Result: result}, nil
}

// NewAgentRunResumer constructs the bridge used by the outer Feishu
// composition layer. Keeping the dependencies narrow avoids a feishu→agent
// package cycle and lets DataStore's concrete agent-run store be asserted to
// IExternalToolResumer at composition time.
func NewAgentRunResumer(runStore store.IExternalToolResumeLease, studentRuns *StudentRunService) *AgentRunResumer {
	return &AgentRunResumer{store: runStore, studentRuns: studentRuns, preflightTimeout: 10 * time.Second}
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
	return r.startClaimedContinuation(ctx, result, leaseToken, runReq)
}

func (r *AgentRunResumer) startClaimedContinuation(ctx context.Context, result ExternalToolResult, leaseToken string, runReq RunRequest) error {
	startedCh := make(chan error, 1)
	finishedCh := make(chan error, 1)
	gate := newExternalContinuationGate(r.store, result, leaseToken, startedCh)
	runReq.ExternalContinuationGate = gate
	preflightTimeout := r.preflightTimeout
	if preflightTimeout <= 0 {
		preflightTimeout = 10 * time.Second
	}
	waitCtx, stopWaiting := context.WithTimeout(ctx, preflightTimeout)
	defer stopWaiting()
	runnerCtx, cancelRunner := context.WithCancel(context.Background())

	go r.studentRuns.forwardNarration(result.RunID)
	go func() {
		defer cancelRunner()
		defer func() {
			if recovered := recover(); recovered != nil {
				panicErr := fmt.Errorf("runner preflight panic: %v", recovered)
				_ = gate.Finish(context.Background(), panicErr)
				select {
				case finishedCh <- panicErr:
				default:
				}
			}
		}()
		bgCtx := middleware.NewContextWithUserID(runnerCtx, runReq.UserID)
		_, runErr := r.studentRuns.runner.Run(bgCtx, runReq)
		if gate.Begun() && !gate.Finished() {
			_ = gate.Finish(context.Background(), runErr)
		}
		select {
		case finishedCh <- runErr:
		default:
		}
	}()

	select {
	case startErr := <-startedCh:
		if startErr != nil {
			return fmt.Errorf("AgentRunResumer.Resume: first model boundary: %w", startErr)
		}
		return nil
	case runErr := <-finishedCh:
		if gate.Begun() {
			select {
			case startErr := <-startedCh:
				if startErr == nil {
					return nil
				}
				return fmt.Errorf("AgentRunResumer.Resume: first model boundary: %w", startErr)
			default:
			}
		}
		if runErr == nil {
			runErr = fmt.Errorf("runner exited before the first model boundary")
		}
		releaseErr := gate.ReleaseIfUnstarted(context.Background())
		return errors.Join(fmt.Errorf("AgentRunResumer.Resume: runner preflight: %w", runErr), releaseErr)
	case <-waitCtx.Done():
		cancelRunner()
		abortErr := gate.Abort(waitCtx.Err())
		return errors.Join(fmt.Errorf("AgentRunResumer.Resume: runner preflight cancelled: %w", waitCtx.Err()), abortErr)
	}
}

type externalContinuationGate struct {
	store   store.IExternalToolResumeLease
	result  ExternalToolResult
	token   string
	started chan<- error

	mu                    sync.Mutex
	begun                 bool
	finished              bool
	completedSuccessfully bool
	finalErr              error
	hbCancel              context.CancelFunc
	hbDone                chan struct{}
	hbErr                 error
	heartbeatInterval     time.Duration
}

func newExternalContinuationGate(s store.IExternalToolResumeLease, result ExternalToolResult, token string, started chan<- error) *externalContinuationGate {
	return &externalContinuationGate{
		store: s, result: result, token: token, started: started,
		heartbeatInterval: 10 * time.Second,
	}
}

func (g *externalContinuationGate) BeginCall(ctx context.Context) (bool, context.Context, error) {
	if err := ctx.Err(); err != nil {
		g.failBeforeStart(err)
		return true, ctx, err
	}
	g.mu.Lock()
	if g.finished {
		finalErr := g.finalErr
		completedSuccessfully := g.completedSuccessfully
		g.mu.Unlock()
		if completedSuccessfully {
			return false, ctx, nil
		}
		if finalErr == nil {
			finalErr = errExternalContinuationFirstCall
		}
		return true, ctx, finalErr
	}
	if g.begun {
		g.mu.Unlock()
		return true, ctx, fmt.Errorf("external continuation first model boundary was already entered")
	}
	g.begun = true
	g.mu.Unlock()
	err := g.store.TouchExternalToolResume(ctx, g.result.RunID, g.result.OperationID, g.result.ToolCallID, g.token)
	if err != nil {
		g.failBeforeStart(err)
		return true, ctx, err
	}
	providerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	g.mu.Lock()
	if g.finished {
		finalErr := g.finalErr
		g.mu.Unlock()
		cancel()
		return true, providerCtx, finalErr
	}
	g.hbCancel, g.hbDone = cancel, done
	g.mu.Unlock()
	go g.runHeartbeat(providerCtx, cancel, done)
	g.signalStarted(nil)
	return true, providerCtx, nil
}

func (g *externalContinuationGate) runHeartbeat(ctx context.Context, cancel context.CancelFunc, done chan struct{}) {
	defer close(done)
	interval := g.heartbeatInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := g.store.TouchExternalToolResume(ctx, g.result.RunID, g.result.OperationID, g.result.ToolCallID, g.token); err != nil {
				if ctx.Err() != nil {
					return
				}
				g.mu.Lock()
				g.hbErr = err
				g.mu.Unlock()
				cancel()
				return
			}
		}
	}
}

func (g *externalContinuationGate) stopHeartbeat() error {
	g.mu.Lock()
	cancel, done := g.hbCancel, g.hbDone
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hbErr
}

func (g *externalContinuationGate) Finish(ctx context.Context, callErr error) error {
	g.mu.Lock()
	if g.finished {
		err := g.finalErr
		g.mu.Unlock()
		return err
	}
	g.finished = true
	g.mu.Unlock()
	if hbErr := g.stopHeartbeat(); callErr == nil && hbErr != nil {
		callErr = hbErr
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var transitionErr error
	if callErr == nil {
		transitionErr = g.store.CompleteExternalToolResume(persistCtx, g.result.RunID, g.result.OperationID, g.result.ToolCallID, g.token)
		if transitionErr == nil {
			g.mu.Lock()
			g.completedSuccessfully = true
			g.mu.Unlock()
		}
	} else {
		transitionErr = g.release(persistCtx)
	}
	if callErr != nil || transitionErr != nil {
		g.mu.Lock()
		g.finalErr = errors.Join(errExternalContinuationFirstCall, callErr, transitionErr)
		g.mu.Unlock()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.finalErr
}

func (g *externalContinuationGate) failBeforeStart(cause error) {
	_ = g.Abort(cause)
}

func (g *externalContinuationGate) Abort(cause error) error {
	g.mu.Lock()
	if g.finished {
		err := g.finalErr
		g.mu.Unlock()
		return err
	}
	g.finished = true
	cancelProvider := g.hbCancel
	g.mu.Unlock()
	if cancelProvider != nil {
		cancelProvider()
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	releaseErr := g.release(persistCtx)
	cancel()
	g.mu.Lock()
	g.finalErr = errors.Join(errExternalContinuationFirstCall, cause, releaseErr)
	finalErr := g.finalErr
	g.mu.Unlock()
	g.signalStarted(cause)
	return finalErr
}

func (g *externalContinuationGate) release(ctx context.Context) error {
	return g.store.ReleaseExternalToolResume(ctx, g.result.RunID, g.result.OperationID, g.result.ToolCallID, g.token)
}

func (g *externalContinuationGate) ReleaseIfUnstarted(ctx context.Context) error {
	g.mu.Lock()
	begun, finished := g.begun, g.finished
	g.mu.Unlock()
	if begun || finished {
		return nil
	}
	return g.release(ctx)
}

func (g *externalContinuationGate) signalStarted(err error) {
	select {
	case g.started <- err:
	default:
	}
}

func (g *externalContinuationGate) Begun() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.begun
}

func (g *externalContinuationGate) Finished() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.finished
}

func (g *externalContinuationGate) CompletedSuccessfully() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.completedSuccessfully
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
	history, err := turnsToExternalResumeHistoryMessages(turns, result.ToolCallID)
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
func turnsToExternalResumeHistoryMessages(turns []map[string]any, targetToolCallID string) ([]*schema.Message, error) {
	toolNames := make(map[string]string)
	for _, turn := range turns {
		if role, _ := turn["role"].(string); role != "assistant" {
			continue
		}
		calls, _ := turn["tool_calls"].([]any)
		for _, rawCall := range calls {
			call, _ := rawCall.(map[string]any)
			id, _ := call["id"].(string)
			fn, _ := call["function"].(map[string]any)
			name, _ := fn["name"].(string)
			id, name = strings.TrimSpace(id), strings.TrimSpace(name)
			if id != "" && isSafeExternalResumeToolName(name) {
				toolNames[id] = name
			}
		}
	}
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
			toolName := toolNames[toolCallID]
			if toolCallID == targetToolCallID {
				toolName = "lark_execute"
			}
			if toolName == "" {
				continue
			}
			out = append(out, schema.AssistantMessage("", []schema.ToolCall{{
				ID:   toolCallID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      toolName,
					Arguments: `{}`,
				},
			}}))
			out = append(out, schema.ToolMessage(content, toolCallID, schema.WithToolName(toolName)))
		case "tool_group":
			// UI narration only; it is not a provider protocol message.
		default:
			return nil, fmt.Errorf("external resume transcript turn %d has unsupported role %q", index, role)
		}
	}
	return out, nil
}

func isSafeExternalResumeToolName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}
