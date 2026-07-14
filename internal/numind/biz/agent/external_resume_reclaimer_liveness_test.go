package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent/stream"
	"numind-server/internal/numind/biz/narration"
	storepkg "numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/model"
)

type candidateIsolationRunner struct {
	badRunID    uint64
	goodStarted chan struct{}
	unblockBad  chan struct{}
}

func (r *candidateIsolationRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if req.ExistingRunID == r.badRunID {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-r.unblockBad:
			return nil, errors.New("test cleanup")
		}
	}
	if req.ExternalContinuationGate != nil {
		if _, _, err := req.ExternalContinuationGate.BeginCall(ctx); err != nil {
			return nil, err
		}
	}
	select {
	case <-r.goodStarted:
	default:
		close(r.goodStarted)
	}
	return &RunResult{AgentRunID: req.ExistingRunID, TerminalReason: TerminalCompleted}, nil
}

func (r *candidateIsolationRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}
func (r *candidateIsolationRunner) Cancel(uint64) bool { return false }

func seedReadyResumeCandidate(t *testing.T, runStore storepkg.IAgentRunStore, lease storepkg.IExternalToolResumeLease, session string) uint64 {
	t.Helper()
	run := &model.AgentRun{
		UserID: 7, SessionID: session, Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	token, claimed, err := lease.ClaimExternalToolResume(context.Background(), run.ID, "op-1", "tc-9", json.RawMessage(`{"ok":true}`))
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, lease.ReleaseExternalToolResume(context.Background(), run.ID, "op-1", "tc-9", token))
	return run.ID
}

func TestExternalResumeReclaimer_BlockedCandidateDoesNotBlockNextAndStopCancelsPreflight(t *testing.T) {
	db := newSQTestDB(t)
	ds := storepkg.NewTestStore(db)
	runStore := ds.AgentRuns()
	lease, ok := runStore.(storepkg.IExternalToolResumeLease)
	require.True(t, ok)
	badID := seedReadyResumeCandidate(t, runStore, lease, "bad")
	_ = seedReadyResumeCandidate(t, runStore, lease, "good")
	runner := &candidateIsolationRunner{
		badRunID: badID, goodStarted: make(chan struct{}), unblockBad: make(chan struct{}),
	}
	t.Cleanup(func() { close(runner.unblockBad) })
	studentRuns := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	reclaimer := NewExternalResumeReclaimer(lease, newTestAgentRunResumer(t, lease, studentRuns), time.Hour)
	reclaimer.Start()

	select {
	case <-runner.goodStarted:
	case <-time.After(300 * time.Millisecond):
		t.Error("one blocked preflight prevented a later recovery candidate from starting")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	require.NoError(t, reclaimer.Stop(ctx), "Stop must cancel and join blocked recovery preflights")
}

type cooperativeStopRunner struct {
	started chan struct{}
	exited  chan struct{}
}

func (r *cooperativeStopRunner) Run(ctx context.Context, _ RunRequest) (*RunResult, error) {
	close(r.started)
	<-ctx.Done()
	close(r.exited)
	return nil, ctx.Err()
}

func (r *cooperativeStopRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}
func (r *cooperativeStopRunner) Cancel(uint64) bool { return false }

func newExternalResumeNarrationProvider(t *testing.T) *narration.Provider {
	t.Helper()
	provider, err := narration.NewProvider(narration.Config{
		YAMLBytes: []byte(`tools: {}
defaults:
  verb: "执行"
  detail_template: "{{ .ToolName }}"
  use_template: "{{ .verb }}"
  result_template: "完成"
  error_template: "失败"
  rejected_template: "拒绝"
`),
		BufferSize: 8,
	})
	require.NoError(t, err)
	return provider
}

func TestExternalResumeReclaimer_StopJoinsCooperativeRunnerAndNarrationBeforeReturning(t *testing.T) {
	runStore := newMockStore()
	toolResult := schema.ToolMessage(`{"ok":true}`, "tc-9")
	toolResult.Extra = map[string]any{externalOperationIDExtraKey: "op-1"}
	toolRaw, err := json.Marshal(toolResult)
	require.NoError(t, err)
	run := &model.AgentRun{
		UserID: 7, SessionID: "managed", Status: "terminated", StateReason: "external_resume_ready",
		Messages:                  datatypes.JSON(fmt.Sprintf(`[{"role":"user","content":"写飞书"},%s]`, toolRaw)),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true, candidates: []model.AgentRun{*run}}
	runner := &cooperativeStopRunner{started: make(chan struct{}), exited: make(chan struct{})}
	provider := newExternalResumeNarrationProvider(t)
	buffer := NewNarrationBuffer(8, time.Minute)
	studentRuns := NewStudentRunService(runner, runStore, nil, nil, provider, buffer)
	reclaimer := NewExternalResumeReclaimer(resumeStore, newTestAgentRunResumer(t, resumeStore, studentRuns), time.Hour)
	reclaimer.Start()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("managed runner did not start")
	}
	provider.Emit(context.Background(), run.ID, "test", narration.StateUse, narration.EmitPayload{})
	require.Eventually(t, func() bool {
		return len(buffer.QuerySince(run.ID, time.Time{})) == 1
	}, time.Second, 5*time.Millisecond, "narration forwarder never subscribed")

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, reclaimer.Stop(stopCtx))
	select {
	case <-runner.exited:
	default:
		t.Fatal("Stop returned before the cooperative runner exited")
	}
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.releases)
	resumeStore.mu.Unlock()
	provider.Emit(context.Background(), run.ID, "test", narration.StateUse, narration.EmitPayload{})
	assert.Len(t, buffer.QuerySince(run.ID, time.Time{}), 1,
		"Stop returned while the narration forwarder was still subscribed")
}

type nonCooperativeStopRunner struct {
	started chan struct{}
	unblock chan struct{}
	exited  chan struct{}
}

func (r *nonCooperativeStopRunner) Run(context.Context, RunRequest) (*RunResult, error) {
	close(r.started)
	<-r.unblock
	close(r.exited)
	return nil, errors.New("unblocked non-cooperative runner")
}

func (r *nonCooperativeStopRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}
func (r *nonCooperativeStopRunner) Cancel(uint64) bool { return false }

func externalResumeStopWaiterCount() int {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return strings.Count(string(buf[:n]), "ExternalResumeReclaimer).Stop.func1")
}

func TestExternalResumeReclaimer_NonCooperativeRunnerRemainsOwnedUntilItExits(t *testing.T) {
	db := newSQTestDB(t)
	ds := storepkg.NewTestStore(db)
	runStore := ds.AgentRuns()
	lease, ok := runStore.(storepkg.IExternalToolResumeLease)
	require.True(t, ok)
	seedReadyResumeCandidate(t, runStore, lease, "non-cooperative")
	runner := &nonCooperativeStopRunner{
		started: make(chan struct{}), unblock: make(chan struct{}), exited: make(chan struct{}),
	}
	t.Cleanup(func() {
		select {
		case <-runner.unblock:
		default:
			close(runner.unblock)
		}
	})
	studentRuns := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	reclaimer := NewExternalResumeReclaimer(lease, newTestAgentRunResumer(t, lease, studentRuns), time.Hour)
	reclaimer.Start()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("non-cooperative runner did not start")
	}
	firstCtx, cancelFirst := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err := reclaimer.Stop(firstCtx)
	cancelFirst()
	require.ErrorIs(t, err, context.DeadlineExceeded,
		"Stop must not report success while an owned runner is still alive")

	secondResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		secondResult <- reclaimer.Stop(ctx)
	}()
	select {
	case err := <-secondResult:
		t.Fatalf("managed ownership was dropped before runner exit: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	assert.Zero(t, externalResumeStopWaiterCount(), "Stop must not create an unbounded Wait helper goroutine")
	close(runner.unblock)
	require.NoError(t, <-secondResult)
	select {
	case <-runner.exited:
	default:
		t.Fatal("managed runner did not exit before Stop completed")
	}
}

func TestAgentRunner_Run_CancellationInterruptsBlockingProvider(t *testing.T) {
	providerStarted := make(chan struct{})
	original := chatFn
	t.Cleanup(func() { chatFn = original })
	chatFn = func(ctx context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		close(providerStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	runStore := newMockStore()
	runner, toolName := newReActRunner(runStore)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := runner.Run(ctx, newReActRequest(toolName, "block"))
		result <- err
	}()
	select {
	case <-providerStarted:
	case <-time.After(time.Second):
		t.Fatal("production runner did not reach provider boundary")
	}
	cancel()
	select {
	case err := <-result:
		require.NoError(t, err, "runner converts provider cancellation into a terminal result")
	case <-time.After(time.Second):
		t.Fatal("production AgentRunner.Run ignored provider ctx cancellation")
	}
}

type lateBoundaryRunner struct {
	allowLate chan struct{}
	attempted chan error
}

func (r *lateBoundaryRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	<-ctx.Done()
	<-r.allowLate
	_, _, err := req.ExternalContinuationGate.BeginCall(context.Background())
	r.attempted <- err
	return nil, err
}

func (r *lateBoundaryRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}
func (r *lateBoundaryRunner) Cancel(uint64) bool { return false }

func TestExternalToolResume_CancelledPreflightRejectsLateProviderBoundary(t *testing.T) {
	runStore := newMockStore()
	run := &model.AgentRun{
		UserID: 7, SessionID: "late", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-1","session_id":"auth-1","tool_call_id":"tc-9","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), run))
	runner := &lateBoundaryRunner{allowLate: make(chan struct{}), attempted: make(chan error, 1)}
	resumeStore := &externalResumeStoreStub{runStore: runStore, returnOK: true}
	resumer := newTestAgentRunResumer(t, resumeStore, NewStudentRunService(runner, runStore, nil, nil, nil, nil))
	resumer.preflightTimeout = 20 * time.Millisecond

	err := resumer.Resume(context.Background(), ExternalToolResult{
		RunID: run.ID, OperationID: "op-1", ToolCallID: "tc-9", Result: json.RawMessage(`{"ok":true}`),
	})
	require.Error(t, err)
	close(runner.allowLate)
	select {
	case lateErr := <-runner.attempted:
		require.Error(t, lateErr, "an aborted lease must never admit a late provider boundary")
	case <-time.After(time.Second):
		t.Fatal("late provider boundary was not attempted")
	}
	resumeStore.mu.Lock()
	assert.Equal(t, 1, resumeStore.releases)
	assert.Zero(t, resumeStore.completes)
	resumeStore.mu.Unlock()
}

type boundedPreflightRunner struct {
	active  atomic.Int32
	maxSeen atomic.Int32
	started chan struct{}
	once    sync.Once
}

type durableCapacityRunner struct {
	active  atomic.Int32
	maxSeen atomic.Int32
	started chan uint64
	release chan struct{}
	mu      sync.Mutex
	calls   map[uint64]int
}

func (r *durableCapacityRunner) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	if _, _, err := req.ExternalContinuationGate.BeginCall(ctx); err != nil {
		return nil, err
	}
	active := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		previous := r.maxSeen.Load()
		if active <= previous || r.maxSeen.CompareAndSwap(previous, active) {
			break
		}
	}
	r.mu.Lock()
	r.calls[req.ExistingRunID]++
	r.mu.Unlock()
	r.started <- req.ExistingRunID
	select {
	case <-r.release:
		return &RunResult{AgentRunID: req.ExistingRunID, TerminalReason: TerminalCompleted}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *durableCapacityRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}
func (r *durableCapacityRunner) Cancel(uint64) bool { return false }

func TestExternalResumeCapacity_FifthResultIsDurableAndAutomaticallyRetried(t *testing.T) {
	db := newSQTestDB(t)
	ds := storepkg.NewTestStore(db)
	runStore := ds.AgentRuns()
	lease, ok := runStore.(storepkg.IExternalToolResumeLease)
	require.True(t, ok)
	for i := 0; i < ExternalContinuationLimit; i++ {
		seedReadyResumeCandidate(t, runStore, lease, fmt.Sprintf("capacity-managed-%d", i))
	}

	runner := &durableCapacityRunner{
		started: make(chan uint64, ExternalContinuationLimit+1),
		release: make(chan struct{}, ExternalContinuationLimit+1),
		calls:   make(map[uint64]int),
	}
	supervisor := NewExternalContinuationSupervisor(ExternalContinuationLimit)
	studentRuns := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	resumer := NewAgentRunResumer(lease, studentRuns, supervisor)
	reclaimer := NewExternalResumeReclaimer(lease, resumer, time.Millisecond)
	reclaimer.Start()
	t.Cleanup(func() {
		supervisor.BeginStop()
		for i := 0; i < ExternalContinuationLimit+1; i++ {
			select {
			case runner.release <- struct{}{}:
			default:
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = reclaimer.Stop(ctx)
		_ = supervisor.Wait(ctx)
	})

	for i := 0; i < ExternalContinuationLimit; i++ {
		select {
		case <-runner.started:
		case <-time.After(time.Second):
			t.Fatal("reclaimer did not occupy all shared continuation slots")
		}
	}

	fifth := &model.AgentRun{
		UserID: 7, SessionID: "capacity-fifth", Status: "terminated", StateReason: string(TerminalWaitingForUserChoice),
		Messages:                  datatypes.JSON(`[{"role":"user","content":"写飞书"}]`),
		PendingExternalActionJSON: datatypes.JSON(`{"provider":"feishu","operation_id":"op-5","session_id":"auth-5","tool_call_id":"tc-5","phase":"user_auth","expires_at":"2026-07-13T09:30:00Z"}`),
		StartedAt:                 time.Now(),
	}
	require.NoError(t, runStore.Create(context.Background(), fifth))
	result := json.RawMessage(`{"ok":true,"operation_id":"op-5","state":"succeeded"}`)
	require.NoError(t, resumer.Resume(context.Background(), ExternalToolResult{
		RunID: fifth.ID, OperationID: "op-5", ToolCallID: "tc-5", Result: result,
	}), "a completed external operation must be accepted even while continuation capacity is full")

	queued, err := runStore.Get(context.Background(), fifth.ID)
	require.NoError(t, err)
	assert.Equal(t, "terminated", queued.Status)
	assert.Equal(t, "external_resume_ready", queued.StateReason)
	assert.Equal(t, string(fifth.PendingExternalActionJSON), string(queued.PendingExternalActionJSON))
	assert.Contains(t, string(queued.Messages), `\"operation_id\":\"op-5\"`)

	runner.release <- struct{}{}
	select {
	case resumedID := <-runner.started:
		assert.Equal(t, fifth.ID, resumedID, "the durable fifth result must resume without another external callback")
	case <-time.After(time.Second):
		t.Fatal("freeing one slot did not automatically resume the durable fifth result")
	}
	runner.mu.Lock()
	assert.Equal(t, 1, runner.calls[fifth.ID])
	runner.mu.Unlock()
	assert.LessOrEqual(t, runner.maxSeen.Load(), int32(ExternalContinuationLimit))
}

const expectedExternalResumeWorkerLimit = 4

func (r *boundedPreflightRunner) Run(ctx context.Context, _ RunRequest) (*RunResult, error) {
	active := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		previous := r.maxSeen.Load()
		if active <= previous || r.maxSeen.CompareAndSwap(previous, active) {
			break
		}
	}
	if active >= expectedExternalResumeWorkerLimit {
		r.once.Do(func() { close(r.started) })
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (r *boundedPreflightRunner) RunStream(context.Context, RunRequest, uint64, chan<- stream.Event) (*RunResult, error) {
	return nil, errors.New("not used")
}
func (r *boundedPreflightRunner) Cancel(uint64) bool { return false }

func TestExternalResumeReclaimer_BoundsConcurrentRecoveryWorkers(t *testing.T) {
	db := newSQTestDB(t)
	ds := storepkg.NewTestStore(db)
	runStore := ds.AgentRuns()
	lease, ok := runStore.(storepkg.IExternalToolResumeLease)
	require.True(t, ok)
	for i := 0; i < expectedExternalResumeWorkerLimit*3; i++ {
		seedReadyResumeCandidate(t, runStore, lease, fmt.Sprintf("blocked-%d", i))
	}
	runner := &boundedPreflightRunner{started: make(chan struct{})}
	studentRuns := NewStudentRunService(runner, runStore, nil, nil, nil, nil)
	resumer := newTestAgentRunResumer(t, lease, studentRuns)
	resumer.preflightTimeout = time.Second
	reclaimer := NewExternalResumeReclaimer(lease, resumer, time.Hour)
	reclaimer.Start()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("reclaimer did not fill its bounded worker pool")
	}
	assert.LessOrEqual(t, runner.maxSeen.Load(), int32(expectedExternalResumeWorkerLimit))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, reclaimer.Stop(ctx))
}
