# Agent Run Survives Exit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let user-facing Agent runs continue server-side after the browser refreshes, closes, or leaves the chat page, while preserving explicit cancel semantics and durable results on return.

**Architecture:** Backend execution moves into a supervised streaming service that drains `RunStream` events to the replayable run event broker independently of HTTP subscribers. Frontend SSE becomes an observer channel that can reattach to `/events` or fall back to polling/snapshot whenever an active run is restored or an observer stream ends before terminal.

**Tech Stack:** Go 1.24, Gin SSE, Redis Streams/PubSub run event broker, GORM-backed `agent_run` SOT, Vue 3, Pinia, Vitest, Playwright.

---

## References

- S2 spec: `numind-server/docs/superpowers/specs/2026-08-03-agent-run-survives-exit-design.md`
- Requirement: `numind-server/requirements/agent-run-survives-exit.md`
- Proposal: `numind-server/proposals/agent-run-survives-exit-proposal.md`
- AI service rule: `.claude/rules/ai-service.md`
- Server worktree: `/private/tmp/wt-agent-run-survives-exit-numind-server`
- Web worktree: `/private/tmp/wt-agent-run-survives-exit-numind-web-v3`

## File Structure

Backend:

- Create `numind-server/internal/numind/biz/agent/stream/execution_registry.go`: process-local active execution registry.
- Create `numind-server/internal/numind/biz/agent/stream/execution_registry_test.go`: registry race/cancel tests.
- Create `numind-server/internal/numind/biz/agent/student_run_stream_prepare.go`: `PreparedStreamRun` and pre-create extraction from `AcquireStreamLock`.
- Create `numind-server/internal/numind/biz/agent/student_run_stream_supervisor.go`: supervised start and detached event publisher.
- Modify `numind-server/internal/numind/biz/agent/student_run_lifecycle.go`: service fields, constructor wiring, cancel bridge.
- Modify `numind-server/internal/numind/biz/agent/answer.go`: answer-stream supervised resume wiring.
- Modify `numind-server/internal/numind/controller/v1/agent/student_run_stream.go`: observer-only create/answer SSE handlers.
- Modify tests in `numind-server/internal/numind/biz/agent/`, `numind-server/internal/numind/biz/agent/stream/`, and `numind-server/internal/numind/controller/v1/agent/`.

Frontend:

- Modify `numind-web-v3/src/stores/agentChat.ts`: restore ordinary active `running/pending` runs from snapshots.
- Modify `numind-web-v3/src/composables/useAgentStream.ts`: unified event reattach and early-ended observer fallback.
- Modify `numind-web-v3/src/composables/useAgentRun.ts`: expose polling state.
- Modify `numind-web-v3/src/views/agent/AgentChatView.vue`: auto-observe restored active runs.
- Modify related Vitest specs under `src/stores/__tests__`, `src/composables/__tests__`, and `src/views/agent/__tests__`.

## Dependency Graph

```text
T1 registry
  -> T2 prepare/pre-create extraction
       -> T3 supervised publisher + initial stream start
            -> T4 supervised answer resume
                 -> T5 observer-only controller + API contract
                      -> T6 cancel + observability + AI-rule verification
                           -> T7 frontend active snapshot restore
                                -> T8 frontend unified event reattach
                                     -> T9 frontend auto-observer wiring
                                          -> T10 verification + browser QA
```

All implementation writes are serial. Backend API/contract tasks T1-T6 finish before frontend tasks T7-T9. Read-only review subagents may run after T10.

---

### Task 1: Backend Execution Registry

**Description:** Add the process-local registry that records active supervised executions by run ID, prevents duplicate starts in one process, and exposes parent-context cancellation.

**Files:**

- Create: `numind-server/internal/numind/biz/agent/stream/execution_registry.go`
- Create: `numind-server/internal/numind/biz/agent/stream/execution_registry_test.go`

- [ ] **Step 1: Write failing tests**

Use package `stream_test` and import `numind-server/internal/numind/biz/agent/stream`. Add:

```go
func TestStreamExecutionRegistry_StartCancelFinish(t *testing.T) {
	registry := stream.NewStreamExecutionRegistry()
	cancelled := false
	done := make(chan struct{})

	require.True(t, registry.Start(42, func() { cancelled = true }, done))
	require.True(t, registry.IsActive(42))
	require.True(t, registry.Cancel(42))
	require.True(t, cancelled)

	close(done)
	registry.Finish(42)
	require.False(t, registry.IsActive(42))
	require.False(t, registry.Cancel(42))
}

func TestStreamExecutionRegistry_StartIsSingleFlightPerRun(t *testing.T) {
	registry := stream.NewStreamExecutionRegistry()
	require.True(t, registry.Start(42, func() {}, make(chan struct{})))
	require.False(t, registry.Start(42, func() {}, make(chan struct{})))
	require.True(t, registry.Start(43, func() {}, make(chan struct{})))
}

func TestStreamExecutionRegistry_ConcurrentStartOnlyOneWins(t *testing.T) {
	registry := stream.NewStreamExecutionRegistry()
	var wins atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if registry.Start(42, func() {}, make(chan struct{})) {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int64(1), wins.Load())
}
```

- [ ] **Step 2: Verify RED**

Run:

```bash
cd /private/tmp/wt-agent-run-survives-exit-numind-server
go test ./internal/numind/biz/agent/stream -run TestStreamExecutionRegistry -count=1
```

Expected: FAIL because `NewStreamExecutionRegistry` does not exist.

- [ ] **Step 3: Implement registry**

Create:

```go
package stream

import (
	"context"
	"sync"
	"time"
)

type StreamExecution struct {
	RunID     uint64
	StartedAt time.Time
	Cancel    context.CancelFunc
	Done      <-chan struct{}
}

type StreamExecutionRegistry struct {
	mu     sync.Mutex
	active map[uint64]*StreamExecution
}

func NewStreamExecutionRegistry() *StreamExecutionRegistry {
	return &StreamExecutionRegistry{active: make(map[uint64]*StreamExecution)}
}

func (r *StreamExecutionRegistry) Start(runID uint64, cancel context.CancelFunc, done <-chan struct{}) bool {
	if r == nil || runID == 0 || cancel == nil || done == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.active[runID]; exists {
		return false
	}
	r.active[runID] = &StreamExecution{RunID: runID, StartedAt: time.Now(), Cancel: cancel, Done: done}
	return true
}

func (r *StreamExecutionRegistry) Cancel(runID uint64) bool {
	if r == nil || runID == 0 {
		return false
	}
	r.mu.Lock()
	exec, exists := r.active[runID]
	r.mu.Unlock()
	if !exists || exec.Cancel == nil {
		return false
	}
	exec.Cancel()
	return true
}

func (r *StreamExecutionRegistry) Finish(runID uint64) {
	if r == nil || runID == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.active, runID)
}

func (r *StreamExecutionRegistry) IsActive(runID uint64) bool {
	if r == nil || runID == 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, exists := r.active[runID]
	return exists
}
```

- [ ] **Step 4: Verify GREEN**

```bash
go test -race ./internal/numind/biz/agent/stream -run TestStreamExecutionRegistry -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/numind/biz/agent/stream/execution_registry.go internal/numind/biz/agent/stream/execution_registry_test.go
git commit -m "feat(agent): add stream execution registry"
```

**Acceptance Conditions:**

- Registry tests pass with race detector.
- `Start` is single-flight per run ID.
- `Cancel` calls the registered parent cancel.
- `Finish` is idempotent.

---

### Task 2: Backend Prepare/Pre-Create Extraction

**Description:** Extract stream run pre-creation out of the old SSE subscription lock path, so a run can be created once and then started by a supervised executor.

**Files:**

- Create: `numind-server/internal/numind/biz/agent/student_run_stream_prepare.go`
- Modify: `numind-server/internal/numind/biz/agent/student_run_lifecycle.go`
- Modify: `numind-server/internal/numind/biz/agent/student_run_lifecycle_test.go`

- [ ] **Step 1: Write failing tests**

Add:

```go
func TestPrepareStreamRun_PrecreatesRunningRunWithoutSubscriptionLock(t *testing.T) {
	svc, runs := setupStudentRunServiceForTest(t)
	prepared, err := svc.PrepareStreamRun(context.Background(), 123, CreateRunRequest{
		AgentDefinitionID: 1,
		Message:           "hello",
	})
	require.NoError(t, err)
	require.NotZero(t, prepared.RunID)
	require.Equal(t, uint(123), prepared.UserID)
	require.Equal(t, "hello", prepared.Request.Message)
	run, err := runs.Get(context.Background(), prepared.RunID)
	require.NoError(t, err)
	require.Equal(t, "running", run.Status)
	require.NotEmpty(t, run.SessionID)
}

func TestAcquireStreamLock_RemainsCompatibilityWrapper(t *testing.T) {
	svc, _ := setupStudentRunServiceForTest(t)
	runID, acquired, err := svc.AcquireStreamLock(context.Background(), 123, CreateRunRequest{
		AgentDefinitionID: 1,
		Message:           "hello",
	})
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotZero(t, runID)
	svc.ReleaseStreamLock(runID)
}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/numind/biz/agent -run 'TestPrepareStreamRun|TestAcquireStreamLock_RemainsCompatibilityWrapper' -count=1
```

Expected: FAIL because `PrepareStreamRun` does not exist.

- [ ] **Step 3: Add DTO and prepare method**

Create `student_run_stream_prepare.go`:

```go
type PreparedStreamRun struct {
	RunID     uint64
	SessionID string
	UserID    uint
	Request   CreateRunRequest
}

func (s *StudentRunService) PrepareStreamRun(ctx context.Context, userID uint, req CreateRunRequest) (*PreparedStreamRun, error) {
	// Move the existing validation, attachment readiness, definition resolution,
	// session id generation, inherited pin/name lookup, display attachment
	// composition, and preRun Create from AcquireStreamLock into this method.
	// Return PreparedStreamRun from the created row.
}
```

The implementation must preserve all current `AcquireStreamLock` behavior except taking `s.streamLock`.

- [ ] **Step 4: Convert `AcquireStreamLock` into wrapper**

Replace its body with:

```go
prepared, err := s.PrepareStreamRun(ctx, userID, req)
if err != nil {
	return 0, false, err
}
if !s.streamLock.Acquire(prepared.RunID) {
	return prepared.RunID, false, nil
}
return prepared.RunID, true, nil
```

- [ ] **Step 5: Verify GREEN**

```bash
go test ./internal/numind/biz/agent -run 'TestPrepareStreamRun|TestAcquireStreamLock|TestAcquireStreamLock_PersistsInitialUserTurnWithAttachment|TestAcquireStreamLock_RejectsPendingAttachment' -count=1
```

- [ ] **Step 6: Commit**

```bash
git add internal/numind/biz/agent/student_run_stream_prepare.go internal/numind/biz/agent/student_run_lifecycle.go internal/numind/biz/agent/student_run_lifecycle_test.go
git commit -m "refactor(agent): extract stream run preparation"
```

**Acceptance Conditions:**

- `PrepareStreamRun` pre-creates the same DB row shape as current streaming create.
- `AcquireStreamLock` compatibility tests still pass.
- No controller or runner behavior changes yet.

---

### Task 3: Backend Supervised Publisher and Initial Run Start

**Description:** Add supervised initial stream execution. The service starts `RunStream` on a detached runner context, drains all events, publishes them best-effort, and publishes a best-effort error event if runner exits with an error before terminal/error.

**Files:**

- Create: `numind-server/internal/numind/biz/agent/student_run_stream_supervisor.go`
- Create: `numind-server/internal/numind/biz/agent/student_run_stream_supervisor_test.go`
- Modify: `numind-server/internal/numind/biz/agent/student_run_lifecycle.go`

- [ ] **Step 1: Write failing tests**

Add:

```go
func TestStartPreparedStreamRun_DrainsEventsWithoutSubscriber(t *testing.T) {}
func TestStartPreparedStreamRun_PublishUsesDetachedContext(t *testing.T) {}
func TestStartPreparedStreamRun_PublishFailureDoesNotStopRunner(t *testing.T) {}
func TestStartPreparedStreamRun_PublishesErrorWhenRunnerFailsBeforeTerminal(t *testing.T) {}
```

The last test is required by S2 §4.3. The runner stub returns `errors.New("boom")` without sending terminal/error. The broker stub must receive one `stream.EventError` for that run.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/numind/biz/agent -run 'TestStartPreparedStreamRun_' -count=1
```

- [ ] **Step 3: Initialize registry on service**

Add field:

```go
streamExecutions *stream.StreamExecutionRegistry
```

Initialize in `NewStudentRunService`:

```go
streamExecutions: stream.NewStreamExecutionRegistry(),
```

- [ ] **Step 4: Implement supervised start**

Add:

```go
func (s *StudentRunService) StartPreparedStreamRun(prepared *PreparedStreamRun) bool {
	if prepared == nil || prepared.RunID == 0 {
		return false
	}
	return s.startSupervisedRun(prepared.RunID, prepared.UserID, func(ctx context.Context, ch chan<- stream.Event) (*RunResult, error) {
		return s.RunStream(ctx, prepared.UserID, prepared.Request, prepared.RunID, ch)
	})
}

func (s *StudentRunService) startSupervisedRun(runID uint64, userID uint, run func(context.Context, chan<- stream.Event) (*RunResult, error)) bool {
	done := make(chan struct{})
	bgCtx := middleware.NewContextWithUserID(context.Background(), userID)
	runnerCtx, cancel := context.WithCancel(bgCtx)
	if !s.streamExecutions.Start(runID, cancel, done) {
		cancel()
		return false
	}
	go func() {
		defer close(done)
		defer s.streamExecutions.Finish(runID)
		events := make(chan stream.Event, 256)
		drained := make(chan struct{})
		observedTerminal := make(chan bool, 1)
		go func() {
			defer close(drained)
			observedTerminal <- s.publishDetachedRunEvents(bgCtx, runID, events)
		}()
		_, runErr := run(runnerCtx, events)
		close(events)
		<-drained
		terminalSeen := <-observedTerminal
		if runErr != nil && !terminalSeen {
			s.publishDetachedRunError(bgCtx, runID, runErr)
		}
	}()
	return true
}
```

- [ ] **Step 5: Implement detached publisher**

Add:

```go
func (s *StudentRunService) publishDetachedRunEvents(ctx context.Context, runID uint64, events <-chan stream.Event) bool {
	terminalSeen := false
	for ev := range events {
		if ev.Type == stream.EventTerminal || ev.Type == stream.EventError {
			terminalSeen = true
		}
		if _, err := s.PublishRunEvent(ctx, runID, ev); err != nil {
			log.Warnw("agent supervised event publish failed", "run_id", runID, "event_type", ev.Type, "error", err)
		}
	}
	return terminalSeen
}

func (s *StudentRunService) publishDetachedRunError(ctx context.Context, runID uint64, runErr error) {
	ev, encodeErr := stream.Encode(stream.EventError, stream.ErrorPayload{
		Code:    "internal",
		Message: "Agent 运行中断，请稍后重试。",
	}, 0, runID, 0)
	if encodeErr != nil {
		return
	}
	_, _ = s.PublishRunEvent(ctx, runID, ev)
}
```

- [ ] **Step 6: Verify GREEN**

```bash
go test ./internal/numind/biz/agent -run 'TestStartPreparedStreamRun_' -count=1
```

- [ ] **Step 7: Commit**

```bash
git add internal/numind/biz/agent/student_run_stream_supervisor.go internal/numind/biz/agent/student_run_stream_supervisor_test.go internal/numind/biz/agent/student_run_lifecycle.go
git commit -m "feat(agent): supervise initial stream execution"
```

**Acceptance Conditions:**

- Initial supervised stream drains without an HTTP subscriber.
- Publish uses a detached/non-canceled context.
- Publish failure does not stop runner.
- Runner error before terminal/error publishes a best-effort `error` event.
- Duplicate starts for same run are prevented by registry.

---

### Task 4: Backend Supervised Answer Resume

**Description:** Route `answer-stream` execution through the same supervisor while keeping answer validation and answer persistence synchronous.

**Files:**

- Modify: `numind-server/internal/numind/biz/agent/answer.go`
- Modify: `numind-server/internal/numind/biz/agent/answer_stream_test.go`
- Modify: `numind-server/internal/numind/biz/agent/student_run_stream_supervisor.go`
- Modify: `numind-server/internal/numind/biz/agent/student_run_stream_supervisor_test.go`

- [ ] **Step 1: Write failing tests**

Add:

```go
func TestStartPreparedAnswerStream_ValidationIsSynchronous(t *testing.T) {}
func TestStartPreparedAnswerStream_StartsSupervisedResumeAfterPersistingAnswer(t *testing.T) {}
func TestStartPreparedAnswerStream_InvalidAnswerDoesNotStartSupervisor(t *testing.T) {}
```

Invalid cases must include cross-user, non-waiting run, and empty answers.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/numind/biz/agent -run 'TestStartPreparedAnswerStream_' -count=1
```

- [ ] **Step 3: Implement supervised answer method**

Add:

```go
func (s *StudentRunService) StartPreparedAnswerStream(ctx context.Context, userID uint, runID uint64, req AnswerRequest) (bool, error) {
	runReq, err := s.validateAndPersistAnswer(ctx, userID, runID, req)
	if err != nil {
		return false, err
	}
	return s.startSupervisedRun(runID, userID, func(runCtx context.Context, ch chan<- stream.Event) (*RunResult, error) {
		go s.forwardNarration(runID)
		return s.runner.RunStream(runCtx, runReq, runID, ch)
	}), nil
}
```

Keep existing `AnswerStream(ctx, ..., ch)` for compatibility tests, but update comments to state browser controller uses supervised answer stream.

- [ ] **Step 4: Verify GREEN**

```bash
go test ./internal/numind/biz/agent -run 'TestStartPreparedAnswerStream_|TestAnswerStream|TestAnswerStream_HappyPath_DrivesRunStream' -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/numind/biz/agent/answer.go internal/numind/biz/agent/answer_stream_test.go internal/numind/biz/agent/student_run_stream_supervisor.go internal/numind/biz/agent/student_run_stream_supervisor_test.go
git commit -m "feat(agent): supervise answer stream resume"
```

**Acceptance Conditions:**

- Invalid answer-stream requests return synchronously and do not start execution.
- Valid answer-stream persists the answer before supervised resume starts.
- Resume execution uses detached supervisor context, not HTTP request ctx.

---

### Task 5: Backend Observer-Only SSE Controllers and API Contract

**Description:** Convert create/answer SSE handlers into observer-only handlers and implement S2 §6 API behavior without adding routes.

**Files:**

- Modify: `numind-server/internal/numind/controller/v1/agent/student_run_stream.go`
- Modify: `numind-server/internal/numind/controller/v1/agent/student_run_stream_test.go`
- Modify: `numind-server/internal/numind/controller/v1/agent/answer_stream_test.go`
- Modify: `numind-server/internal/numind/controller/v1/agent/run_events_test.go`

- [ ] **Step 1: Write failing controller tests**

Add:

```go
func TestCreateStream_ClientDisconnectDoesNotCancelSupervisedRun(t *testing.T) {}
func TestCreateStream_ClientDisconnectBeforeFirstWriteStillStartsPreparedRun(t *testing.T) {}
func TestCreateStream_StartsPreparedRunBeforeObserving(t *testing.T) {}
func TestCreateStream_BrokerUnavailableEmitsObserverFallbackStart(t *testing.T) {}
func TestAnswerStream_ValidationErrorReturnsJSONBeforeSSE(t *testing.T) {}
func TestAnswerStream_DisconnectDoesNotCancelSupervisedResume(t *testing.T) {}
```

The fallback test must assert the S2 §6 frame:

```json
{"type":"stream_start","run_id":123,"data":{"session_id":"sess-1","run_id":123,"observer_fallback":true}}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/numind/controller/v1/agent -run 'TestCreateStream_|TestAnswerStream_' -count=1
```

- [ ] **Step 3: Expand streaming controller service interface**

Use:

```go
PrepareStreamRun(ctx context.Context, userID uint, req agentbiz.CreateRunRequest) (*agentbiz.PreparedStreamRun, error)
StartPreparedStreamRun(prepared *agentbiz.PreparedStreamRun) bool
StartPreparedAnswerStream(ctx context.Context, userID uint, runID uint64, req agentbiz.AnswerRequest) (bool, error)
SubscribeRunEvents(ctx context.Context, userID uint, runID uint64, after string) (<-chan stream.PublishedEvent, error)
```

- [ ] **Step 4: Add observer helpers**

Add `switchToSSE`, `observeRunEvents`, `writePublishedRunEvents`, and `writeObserverFallbackStart`. `writePublishedRunEvents` returns on request ctx done or write error without canceling runner.

- [ ] **Step 5: Rewrite `CreateStream`**

Required order:

```go
prepared, err := h.runSvc.PrepareStreamRun(c.Request.Context(), user.ID, req)
if err != nil {
	core.WriteResponse(c, err, nil)
	return
}
h.runSvc.StartPreparedStreamRun(prepared)
h.switchToSSE(c)
h.observeRunEvents(c, user.ID, prepared.RunID, "")
```

`StartPreparedStreamRun` must happen before `switchToSSE` writes or flushes the first byte. This closes the S2 hard constraint that a successfully pre-created run can never be left `running` without a background runner if the browser disconnects before the first SSE write.

- [ ] **Step 6: Rewrite `AnswerStream`**

Required order:

```go
_, err := h.runSvc.StartPreparedAnswerStream(c.Request.Context(), user.ID, runID, req)
if err != nil {
	core.WriteResponse(c, err, nil)
	return
}
h.switchToSSE(c)
h.observeRunEvents(c, user.ID, runID, "")
```

- [ ] **Step 7: Verify GREEN**

```bash
go test ./internal/numind/controller/v1/agent -run 'TestCreateStream_|TestAnswerStream_|TestSubscribeEvents' -count=1
```

- [ ] **Step 8: Commit**

```bash
git add internal/numind/controller/v1/agent/student_run_stream.go internal/numind/controller/v1/agent/*_test.go
git commit -m "feat(agent): decouple stream observers from execution"
```

**Acceptance Conditions:**

- HTTP/SSE disconnect is observer-only.
- Controller does not call runner with request-owned ctx.
- A run created by `PrepareStreamRun` is started before any possible SSE write/flush failure.
- Broker unavailable emits optional `observer_fallback` stream_start and closes cleanly.
- S2 §6 API paths remain unchanged.
- Answer validation errors are returned before switching to SSE.

---

### Task 6: Backend Cancel, Observability, and AI-Service Rule Verification

**Description:** Wire explicit cancel through the supervisor and runner, update observer disconnect metadata, and document `.claude/rules/ai-service.md` compliance.

**Files:**

- Modify: `numind-server/internal/numind/biz/agent/student_run_lifecycle.go`
- Modify: `numind-server/internal/numind/biz/agent/student_run_lifecycle_test.go`
- Modify: `numind-server/internal/numind/controller/v1/agent/student_run_stream.go`
- Modify: `numind-server/internal/numind/controller/v1/agent/student_run_stream_test.go`
- Create: `numind-server/.ndf/decisions/agent-run-survives-exit/0001-ai-observability.md`

- [ ] **Step 1: Write failing tests**

Add:

```go
func TestCancel_CancelsSupervisorBeforeRunnerRegistered(t *testing.T) {}
func TestObserveRunEvents_RecordsObserverDisconnectNotAbort(t *testing.T) {}
func TestObserveRunEvents_DoesNotCreateNewGeneration(t *testing.T) {}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/numind/biz/agent ./internal/numind/controller/v1/agent -run 'TestCancel_CancelsSupervisorBeforeRunnerRegistered|TestObserveRunEvents_' -count=1
```

- [ ] **Step 3: Extend `Cancel`**

Modify:

```go
if s.streamExecutions != nil {
	s.streamExecutions.Cancel(runID)
}
s.runner.Cancel(runID)
```

- [ ] **Step 4: Update observer metadata**

Use observer-only reasons:

```go
disconnectReason := "run_complete"
// request ctx done:
disconnectReason = "observer_disconnect"
// write error:
disconnectReason = "write_error"
```

Do not write `client_disconnect` as an execution abort reason.

- [ ] **Step 5: Run AI service audits**

From `numind-server`:

```bash
rg -n '"numind-server/internal/numind/biz/(ali|volc|baidu)|internal/service/bailian_http|http\\.Post|resty\\.New' internal/numind/biz/agent internal/numind/controller/v1/agent
rg -n 'CreateGeneration|CreateTrace|agent-runtime-run|tool.ask_user_question.resume' internal/numind/biz/agent internal/numind/controller/v1/agent
```

Expected:

- No new direct provider imports or raw AI HTTP calls in modified files.
- No new controller generation call.
- `agent-runtime-run` remains runner-owned.
- `tool.ask_user_question.resume` remains in `answer.go`.

- [ ] **Step 6: Record AI observability decision**

Create `0001-ai-observability.md`:

```markdown
# AI Observability Decision

This feature adds no new LLM or AI service call site. Agent generations remain under the existing `agent-runtime-run` trace created by `agentRunner.Run` / `agentRunner.RunStream`.

Controller SSE spans are observer spans only. Browser disconnect is recorded as `observer_disconnect` and does not represent execution abort.

`.claude/rules/ai-service.md` audit result: no direct provider imports, no raw AI HTTP calls, and no missing generation for a new LLM call because no new LLM call exists.
```

- [ ] **Step 7: Verify GREEN**

```bash
go test ./internal/numind/biz/agent ./internal/numind/controller/v1/agent -run 'TestCancel_|TestObserveRunEvents_' -count=1
task lint
```

- [ ] **Step 8: Commit**

```bash
git add internal/numind/biz/agent/student_run_lifecycle.go internal/numind/biz/agent/student_run_lifecycle_test.go internal/numind/controller/v1/agent/student_run_stream.go internal/numind/controller/v1/agent/student_run_stream_test.go .ndf/decisions/agent-run-survives-exit/0001-ai-observability.md
git commit -m "fix(agent): preserve explicit cancel and observer tracing"
```

**Acceptance Conditions:**

- Explicit cancel reaches supervisor and runner.
- Observer disconnect is not execution abort.
- `.claude/rules/ai-service.md` audit result is recorded in `.ndf/decisions`.
- `task lint` passes in `numind-server`.

---

### Task 7: Frontend Restores Ordinary Active Runs From Snapshot

**Description:** Make historical/reloaded AgentChat sessions restore `currentRun` for ordinary `running/pending` runs.

**Files:**

- Modify: `numind-web-v3/src/stores/agentChat.ts`
- Modify: `numind-web-v3/src/stores/__tests__/agentChat.spec.ts`
- Modify: `numind-web-v3/src/stores/__tests__/agentChat-session-epoch.spec.ts`

- [ ] **Step 1: Write failing store tests**

Add:

```ts
it('loadSessionSnapshot restores an ordinary running run as currentRun', async () => {
  vi.mocked(api.getSessionSnapshot).mockResolvedValueOnce({
    session_id: 'sess-running',
    status: 'running',
    run: { id: 501, session_id: 'sess-running', status: 'running', state_reason: 'running', created_at: '', updated_at: '' },
    messages: []
  } as never)
  const store = useAgentChatStore()
  await store.loadSessionSnapshot('sess-running', false)
  expect(store.currentRun).toMatchObject({ id: 501, status: 'running' })
})

it('loadSessionSnapshot keeps terminal runs inactive', async () => {
  vi.mocked(api.getSessionSnapshot).mockResolvedValueOnce({
    session_id: 'sess-done',
    status: 'completed',
    run: { id: 502, session_id: 'sess-done', status: 'completed', state_reason: 'completed', created_at: '', updated_at: '' },
    messages: []
  } as never)
  const store = useAgentChatStore()
  await store.loadSessionSnapshot('sess-done', false)
  expect(store.currentRun).toBeNull()
})
```

- [ ] **Step 2: Verify RED**

```bash
cd /private/tmp/wt-agent-run-survives-exit-numind-web-v3
npm run test:unit -- src/stores/__tests__/agentChat.spec.ts src/stores/__tests__/agentChat-session-epoch.spec.ts
```

- [ ] **Step 3: Implement restore**

In `loadSessionSnapshot`, include ordinary active run:

```ts
const restoredOrdinaryActiveRun =
  snap.run &&
  (snap.run.status === 'running' || snap.run.status === 'pending')
```

Include it in the current `if (snap.run && ...)` branch that restores waiting/external runs.

- [ ] **Step 4: Verify GREEN**

```bash
npm run test:unit -- src/stores/__tests__/agentChat.spec.ts src/stores/__tests__/agentChat-session-epoch.spec.ts
```

- [ ] **Step 5: Commit**

```bash
git add src/stores/agentChat.ts src/stores/__tests__/agentChat.spec.ts src/stores/__tests__/agentChat-session-epoch.spec.ts
git commit -m "feat(agent): restore active runs from snapshots"
```

**Acceptance Conditions:**

- Ordinary `running/pending` snapshot runs restore `currentRun`.
- Terminal snapshot runs do not become active.
- Waiting question and external continuation snapshot tests still pass.

---

### Task 8: Frontend Unified Event Reattach and Early-Ended Stream Fallback

**Description:** Add a single event reattach path for ordinary active runs and external continuations. Treat a stream that ends before terminal as observer loss.

**Files:**

- Modify: `numind-web-v3/src/composables/useAgentStream.ts`
- Modify: `numind-web-v3/src/composables/__tests__/useAgentStream.spec.ts`
- Modify: `numind-web-v3/src/types/agent-stream.ts`

- [ ] **Step 1: Write failing composable tests**

Add:

```ts
it('attachRunEvents attaches ordinary active run from the beginning when no cursor exists', async () => {
  mockStore.transportCursorForRun.mockReturnValueOnce('')
  mockStreamAgentRunEvents.mockImplementationOnce(async (_runId, after, onEvent) => {
    expect(after).toBe('')
    onEvent({ ...makeEvent('terminal'), transport_cursor: '10-0', data: { reason: 'completed' } })
  })
  const { attachRunEvents } = useAgentStream()
  await attachRunEvents(42)
  expect(mockStreamAgentRunEvents).toHaveBeenCalledWith(42, '', expect.any(Function), expect.any(AbortSignal))
})

it('start falls back to attachRunEvents when initial stream ends before terminal', async () => {
  mockStreamAgentRun.mockImplementationOnce(async (_req, onEvent) => {
    onEvent({ ...makeEvent('stream_start'), transport_cursor: '1-0' })
  })
  mockStreamAgentRunEvents.mockImplementationOnce(async (_runId, _after, onEvent) => {
    onEvent({ ...makeEvent('terminal'), transport_cursor: '2-0', data: { reason: 'completed' } })
  })
  const { start } = useAgentStream()
  await start(baseReq)
  expect(mockStreamAgentRunEvents).toHaveBeenCalled()
  expect(mockStartStatusPolling).not.toHaveBeenCalled()
})
```

- [ ] **Step 2: Verify RED**

```bash
npm run test:unit -- src/composables/__tests__/useAgentStream.spec.ts
```

- [ ] **Step 3: Add `attachRunEvents` API**

Update `UseAgentStreamApi` and implement:

```ts
const resolveAttachAfter = (
  runId: number,
  opts?: { after?: string; baseline?: 'cursor' | 'from_start' | 'pause' }
): string => {
  if (opts?.after !== undefined) return opts.after
  if (opts?.baseline === 'pause') return 'pause'
  if (opts?.baseline === 'from_start') return ''
  return store.transportCursorForRun(runId) || ''
}
```

`attachContinuation(runId, after)` becomes a wrapper around pause baseline.

- [ ] **Step 4: Add early-ended fallback**

When `start` or `startResume` returns without final terminal and `store.currentRun?.id` is active, call:

```ts
await attachRunEvents(store.currentRun.id)
```

If attach fails before terminal, set `fallbackPolling.value = true` and call `startStatusPolling()`.

- [ ] **Step 5: Add optional payload field**

If `StreamStartPayload` exists, add:

```ts
observer_fallback?: boolean
```

- [ ] **Step 6: Verify GREEN**

```bash
npm run test:unit -- src/composables/__tests__/useAgentStream.spec.ts
```

- [ ] **Step 7: Commit**

```bash
git add src/composables/useAgentStream.ts src/composables/__tests__/useAgentStream.spec.ts src/types/agent-stream.ts
git commit -m "feat(agent): reattach active run events"
```

**Acceptance Conditions:**

- Ordinary active runs attach with saved cursor or `after=""`.
- External continuation keeps `after="pause"`.
- Streams ending before terminal attach or poll instead of showing run failure.
- Terminal clears cursor and existing DB reconcile remains active.

---

### Task 9: Frontend Auto-Observer Wiring and Polling State

**Description:** Wire restored active runs in `AgentChatView` to observe events automatically while avoiding duplicate stream/poll loops.

**Files:**

- Modify: `numind-web-v3/src/composables/useAgentRun.ts`
- Create or modify: `numind-web-v3/src/composables/__tests__/useAgentRun.spec.ts`
- Modify: `numind-web-v3/src/views/agent/AgentChatView.vue`
- Modify: `numind-web-v3/src/views/agent/__tests__/AgentChatView.spec.ts`

- [ ] **Step 1: Write failing tests**

Add `useAgentRun` polling-state test:

```ts
it('tracks status polling lifecycle', () => {
  vi.useFakeTimers()
  const { startStatusPolling, stopStatusPolling, isStatusPolling } = useAgentRun()
  expect(isStatusPolling.value).toBe(false)
  startStatusPolling()
  expect(isStatusPolling.value).toBe(true)
  stopStatusPolling()
  expect(isStatusPolling.value).toBe(false)
})
```

Add `AgentChatView` test:

```ts
it('observes an ordinary reloaded running run', async () => {
  vi.mocked(api.getSessionSnapshot).mockResolvedValueOnce({
    session_id: 'sess-running',
    status: 'running',
    run: { id: 777, session_id: 'sess-running', status: 'running', state_reason: 'running', created_at: '', updated_at: '' },
    messages: []
  } as never)
  shallowMount(AgentChatView, { props: { sessionId: 'sess-running', agentId: null, readOnly: false } })
  await flushPromises()
  expect(mockAttachRunEvents).toHaveBeenCalledWith(777)
})
```

- [ ] **Step 2: Verify RED**

```bash
npm run test:unit -- src/composables/__tests__/useAgentRun.spec.ts src/views/agent/__tests__/AgentChatView.spec.ts
```

- [ ] **Step 3: Expose polling state**

In `useAgentRun.ts`:

```ts
const isStatusPolling = ref(false)
```

Set it true in `startStatusPolling`, false in `stopStatusPolling`, and return it.

- [ ] **Step 4: Add auto-observer watcher**

In `AgentChatView.vue`, use `attachRunEvents` and add watcher:

```ts
watch(
  () =>
    [
      !!store.currentRun &&
        (store.currentRun.status === 'running' || store.currentRun.status === 'pending'),
      isStreaming.value,
      fallbackPolling.value,
      runCtrl.isStatusPolling.value,
      store.currentRun?.id
    ] as const,
  ([active, hasLiveStream, hasPollingFallback, hasStatusPolling, runId]) => {
    if (!active || hasLiveStream || hasPollingFallback || hasStatusPolling || !runId) return
    if (store.isWaitingForAuth || store.isQueuedExternalContinuationActive) return
    void attachRunEvents(runId)
  }
)
```

- [ ] **Step 5: Verify GREEN**

```bash
npm run test:unit -- src/composables/__tests__/useAgentRun.spec.ts src/views/agent/__tests__/AgentChatView.spec.ts
```

- [ ] **Step 6: Commit**

```bash
git add src/composables/useAgentRun.ts src/composables/__tests__/useAgentRun.spec.ts src/views/agent/AgentChatView.vue src/views/agent/__tests__/AgentChatView.spec.ts
git commit -m "feat(agent): observe restored active runs"
```

**Acceptance Conditions:**

- Restored ordinary running runs attach automatically.
- External continuation still uses pause-baseline observation.
- No duplicate attach while streaming or polling is active.
- Unmount stops local observation only.
- Stop button still calls cancel API.

---

### Task 10: Full Verification, Browser QA, and Review Prep

**Description:** Run required gates, verify browser behavior, record QA evidence, and prepare for S4/S5 review.

**Files:**

- Create: `numind-server/docs/superpowers/qa/2026-08-03-agent-run-survives-exit-s5.md`
- Modify: `numind-server/.ndf/manifest.yaml`

- [ ] **Step 1: Backend focused tests**

```bash
cd /private/tmp/wt-agent-run-survives-exit-numind-server
go test ./internal/numind/biz/agent/stream -run TestStreamExecutionRegistry -count=1
go test ./internal/numind/biz/agent -run 'TestPrepareStreamRun|TestStartPrepared|TestCancel_|TestAnswerStream|TestAcquireStreamLock' -count=1
go test ./internal/numind/controller/v1/agent -run 'TestCreateStream_|TestAnswerStream_|TestSubscribeEvents|TestObserveRunEvents_' -count=1
```

- [ ] **Step 2: Backend required gates**

```bash
go test ./... -count=1
task lint
```

- [ ] **Step 3: Frontend focused tests**

```bash
cd /private/tmp/wt-agent-run-survives-exit-numind-web-v3
npm run test:unit -- src/stores/__tests__/agentChat.spec.ts src/stores/__tests__/agentChat-session-epoch.spec.ts
npm run test:unit -- src/composables/__tests__/useAgentStream.spec.ts src/composables/__tests__/useAgentRun.spec.ts
npm run test:unit -- src/views/agent/__tests__/AgentChatView.spec.ts
```

- [ ] **Step 4: Frontend required gates**

```bash
npm run lint
npm run type-check
```

- [ ] **Step 5: Browser QA**

Verify:

```text
1. Start Agent run, wait for stream_start, refresh page, return to same session, final answer appears.
2. Start Agent run, navigate away before terminal, return later, final answer appears from snapshot.
3. Start Agent run, click stop, run becomes cancelled and does not continue to completed.
4. Open two tabs on the same running session; both observe or one observes while the other polls, with no duplicate execution.
```

If adding a Playwright spec:

```bash
npm run test:e2e -- e2e/agent-run-survives-exit.spec.ts
```

- [ ] **Step 6: Write QA summary**

Create:

```markdown
# Agent Run Survives Exit S5 Verification

## Backend
- Focused tests:
- go test ./...:
- task lint:

## Frontend
- Focused Vitest:
- npm run lint:
- npm run type-check:
- Playwright/browser QA:

## AI Observability
- No new LLM/generation point:
- `.claude/rules/ai-service.md` audit:

## Result
- PASS/FAIL:
- Remaining risks:
```

- [ ] **Step 7: Commit verification notes**

```bash
git add docs/superpowers/qa/2026-08-03-agent-run-survives-exit-s5.md .ndf/manifest.yaml
git commit -m "docs(qa): verify agent run survives exit"
```

**Acceptance Conditions:**

- Server `task lint` passes after Go changes.
- Web `npm run lint && npm run type-check` passes after Vue/TS changes.
- Browser QA proves exit/refresh does not cancel, explicit stop still cancels.
- QA records no new LLM/generation point and `.claude/rules/ai-service.md` audit.
- Diff contains no DB schema migration, no new API path, no prod config change, and no secret.

---

## S3 Gate Checklist

| Requirement | Covered By |
|---|---|
| Initial stream disconnect does not create `aborted_streaming` | T3, T5, T10 |
| `answer-stream` disconnect continues resume leg | T4, T5, T10 |
| Explicit stop still cancels | T1, T6, T9, T10 |
| Return to completed session shows final result | T7, T8, T10 |
| Return to still-running session restores observer/polling | T7, T8, T9 |
| Multi-tab does not steal events or duplicate execution | T1, T3, T5, T10 |
| Redis broker failure is observational only | T3, T5, T8, T10 |
| Runner error before terminal/error emits best-effort error event | T3 |
| Owner-before-broker authorization remains | T5 and existing `SubscribeRunEvents` tests |
| Billing/reconcile not confused with disconnect | T3, T6, existing runner billing path |
| S2 §6 API contract is implemented without new routes | T5 |
| Trace topology and `.claude/rules/ai-service.md` compliance | T6, T10 |

## Dependency and Atomicity Check

- Dependency chain is acyclic: `T1 -> T2 -> T3 -> T4 -> T5 -> T6 -> T7 -> T8 -> T9 -> T10`.
- Backend API/contract work completes before frontend work begins.
- T6 is the dedicated AI/observability task required by S3 because S2 defines trace topology.
- Each task has a focused file set and an explicit verification command.
- All writes are serial to keep lifecycle semantics easy to review.
