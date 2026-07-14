package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"

	"numind-server/internal/numind/biz/agent/stream"
	storepkg "numind-server/internal/numind/store"
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
	reclaimer := NewExternalResumeReclaimer(lease, NewAgentRunResumer(lease, studentRuns), time.Hour)
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
	resumer := NewAgentRunResumer(resumeStore, NewStudentRunService(runner, runStore, nil, nil, nil, nil))
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
	resumer := NewAgentRunResumer(lease, studentRuns)
	resumer.preflightTimeout = time.Second
	reclaimer := NewExternalResumeReclaimer(lease, resumer, time.Hour)
	reclaimer.Start()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("reclaimer did not fill its bounded worker pool")
	}
	// Give an unbounded implementation enough time to start the rest of the
	// candidates before asserting the cap.
	time.Sleep(30 * time.Millisecond)
	assert.LessOrEqual(t, runner.maxSeen.Load(), int32(expectedExternalResumeWorkerLimit))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, reclaimer.Stop(ctx))
}
