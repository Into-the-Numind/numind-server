package sandboxbroker

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestRecoveryRestoresRunningReadyAndActiveLeases(t *testing.T) {
	ctx := context.Background()
	journal := openTestJournal(t, testJournalPath(t))
	now := time.Unix(1700000000, 0).UTC()
	ready := createRecoveryLease(
		t,
		journal,
		"lease-ready",
		"request-ready",
		"container-ready",
		LeaseReady,
		now,
	)
	active := createRecoveryLease(
		t,
		journal,
		"lease-active",
		"request-active",
		"container-active",
		LeaseActive,
		now.Add(time.Second),
	)
	runtime := newFakeRecoveryRuntime(
		RecoveryContainer{ContainerID: ready.ContainerID, LeaseID: ready.LeaseID},
		RecoveryContainer{ContainerID: active.ContainerID, LeaseID: active.LeaseID},
	)
	scheduler := NewScheduler()

	result, err := RecoverJournalAndRuntime(ctx, RecoveryConfig{
		Journal:   journal,
		Scheduler: scheduler,
		Runtime:   runtime,
		Now:       func() time.Time { return now.Add(10 * time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Consistent ||
		result.CheckedLeases != 2 ||
		result.RestoredLeases != 2 ||
		result.PendingLeases != 0 {
		t.Fatalf("result = %#v", result)
	}
	snapshot := scheduler.Snapshot()
	if snapshot.Containers != 2 || snapshot.Ready != 1 || snapshot.Active != 1 {
		t.Fatalf("scheduler snapshot = %#v", snapshot)
	}
}

func TestRecoveryMarksMissingContainerPending(t *testing.T) {
	ctx := context.Background()
	journal := openTestJournal(t, testJournalPath(t))
	now := time.Unix(1700000100, 0).UTC()
	lease := createRecoveryLease(
		t,
		journal,
		"lease-missing",
		"request-missing",
		"container-missing",
		LeaseActive,
		now,
	)
	runtime := newFakeRecoveryRuntime()
	runtime.inspectErr[lease.ContainerID] = ErrRecoveryContainerMissing

	result, err := RecoverJournalAndRuntime(ctx, RecoveryConfig{
		Journal:   journal,
		Scheduler: NewScheduler(),
		Runtime:   runtime,
		Now:       func() time.Time { return now.Add(10 * time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Consistent || result.PendingLeases != 1 {
		t.Fatalf("result = %#v err=%v", result, err)
	}
	recovered, err := journal.GetLease(ctx, lease.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != LeaseRecoveryPending ||
		recovered.TerminationReason != TerminationContainerMissing ||
		recovered.ReconcileState != "pending" {
		t.Fatalf("recovered lease = %#v", recovered)
	}
}

func TestRecoveryDeletesDedicatedDaemonOrphans(t *testing.T) {
	ctx := context.Background()
	journal := openTestJournal(t, testJournalPath(t))
	now := time.Unix(1700000200, 0).UTC()
	lease := createRecoveryLease(
		t,
		journal,
		"lease-live",
		"request-live",
		"container-live",
		LeaseReady,
		now,
	)
	runtime := newFakeRecoveryRuntime(
		RecoveryContainer{ContainerID: lease.ContainerID, LeaseID: lease.LeaseID},
		RecoveryContainer{ContainerID: "container-stale", LeaseID: lease.LeaseID},
		RecoveryContainer{ContainerID: "container-orphan", LeaseID: "lease-orphan"},
	)

	result, err := RecoverJournalAndRuntime(ctx, RecoveryConfig{
		Journal:   journal,
		Scheduler: NewScheduler(),
		Runtime:   runtime,
		Now:       func() time.Time { return now.Add(10 * time.Second) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedOrphans != 2 {
		t.Fatalf("result = %#v", result)
	}
	if deleted := runtime.deletedIDs(); !reflect.DeepEqual(deleted, []string{"container-stale", "container-orphan"}) {
		t.Fatalf("deleted = %#v", deleted)
	}
}

func TestRecoveryInspectFailureLeavesPendingCompensation(t *testing.T) {
	ctx := context.Background()
	journal := openTestJournal(t, testJournalPath(t))
	now := time.Unix(1700000300, 0).UTC()
	lease := createRecoveryLease(
		t,
		journal,
		"lease-failed-inspect",
		"request-failed-inspect",
		"container-failed-inspect",
		LeaseActive,
		now,
	)
	runtime := newFakeRecoveryRuntime(
		RecoveryContainer{ContainerID: lease.ContainerID, LeaseID: lease.LeaseID},
	)
	runtime.inspectErr[lease.ContainerID] = errors.New("daemon unavailable")

	result, err := RecoverJournalAndRuntime(ctx, RecoveryConfig{
		Journal:   journal,
		Scheduler: NewScheduler(),
		Runtime:   runtime,
		Now:       func() time.Time { return now.Add(10 * time.Second) },
	})
	if !errors.Is(err, ErrRecoveryIncomplete) {
		t.Fatalf("err = %v", err)
	}
	if result.Consistent || result.PendingLeases != 1 {
		t.Fatalf("result = %#v", result)
	}
	recovered, err := journal.GetLease(ctx, lease.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != LeaseRecoveryPending ||
		recovered.ReconcileState != "pending" {
		t.Fatalf("recovered lease = %#v", recovered)
	}
}

func TestRecoveryListFailureMarksLiveLeasesPending(t *testing.T) {
	ctx := context.Background()
	journal := openTestJournal(t, testJournalPath(t))
	now := time.Unix(1700000350, 0).UTC()
	lease := createRecoveryLease(
		t,
		journal,
		"lease-list-fail",
		"request-list-fail",
		"container-list-fail",
		LeaseActive,
		now,
	)
	runtime := newFakeRecoveryRuntime()
	runtime.listErr = errors.New("daemon list failed")

	result, err := RecoverJournalAndRuntime(ctx, RecoveryConfig{
		Journal:   journal,
		Scheduler: NewScheduler(),
		Runtime:   runtime,
		Now:       func() time.Time { return now.Add(10 * time.Second) },
	})
	if !errors.Is(err, ErrRecoveryIncomplete) {
		t.Fatalf("err = %v", err)
	}
	if result.CheckedLeases != 1 || result.PendingLeases != 1 {
		t.Fatalf("result = %#v", result)
	}
	recovered, err := journal.GetLease(ctx, lease.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != LeaseRecoveryPending ||
		recovered.TerminationReason != TerminationRecoveryTimeout ||
		recovered.ReconcileState != "pending" {
		t.Fatalf("recovered lease = %#v", recovered)
	}
}

func TestRecoverySecondRunIsIdempotent(t *testing.T) {
	ctx := context.Background()
	journal := openTestJournal(t, testJournalPath(t))
	now := time.Unix(1700000400, 0).UTC()
	lease := createRecoveryLease(
		t,
		journal,
		"lease-idempotent",
		"request-idempotent",
		"container-idempotent",
		LeaseActive,
		now,
	)
	runtime := newFakeRecoveryRuntime(
		RecoveryContainer{ContainerID: lease.ContainerID, LeaseID: lease.LeaseID},
	)
	cfg := RecoveryConfig{
		Journal:   journal,
		Scheduler: NewScheduler(),
		Runtime:   runtime,
		Now:       func() time.Time { return now.Add(10 * time.Second) },
	}
	first, err := RecoverJournalAndRuntime(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RecoverJournalAndRuntime(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Consistent || !second.Consistent ||
		first.RestoredLeases != 1 ||
		second.RestoredLeases != 1 {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	events, err := journal.ListEvents(ctx, lease.LeaseID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("events len = %d events=%#v", len(events), events)
	}
}

func createRecoveryLease(
	t *testing.T,
	journal *Journal,
	leaseID string,
	requestID string,
	containerID string,
	state LeaseState,
	createdAt time.Time,
) Lease {
	t.Helper()
	ctx := context.Background()
	lease, _, err := journal.CreateLease(
		ctx,
		testCreateParams(leaseID, requestID, createdAt),
	)
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err = journal.Transition(ctx, TransitionParams{
		LeaseID:   lease.LeaseID,
		RequestID: requestID + "-creating",
		To:        LeaseCreating,
		At:        createdAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err = journal.Transition(ctx, TransitionParams{
		LeaseID:     lease.LeaseID,
		RequestID:   requestID + "-ready",
		To:          LeaseReady,
		At:          createdAt.Add(2 * time.Second),
		ContainerID: &containerID,
		ExpiresAt:   &lease.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state == LeaseReady {
		return lease
	}
	runID := uint64(7)
	sessionID := uint64(8)
	lease, _, err = journal.Transition(ctx, TransitionParams{
		LeaseID:          lease.LeaseID,
		RequestID:        requestID + "-active",
		To:               LeaseActive,
		At:               createdAt.Add(3 * time.Second),
		AgentRunID:       &runID,
		SandboxSessionID: &sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state == LeaseOutputPersisting {
		lease, _, err = journal.Transition(ctx, TransitionParams{
			LeaseID:   lease.LeaseID,
			RequestID: requestID + "-persisting",
			To:        LeaseOutputPersisting,
			At:        createdAt.Add(4 * time.Second),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return lease
}

type fakeRecoveryRuntime struct {
	mu          sync.Mutex
	containers  []RecoveryContainer
	inspections map[string]RuntimeInspect
	inspectErr  map[string]error
	deleteErr   map[string]error
	listErr     error
	deleted     []string
}

func newFakeRecoveryRuntime(
	containers ...RecoveryContainer,
) *fakeRecoveryRuntime {
	inspections := make(map[string]RuntimeInspect, len(containers))
	for _, container := range containers {
		inspections[container.ContainerID] = RuntimeInspect{Status: "running"}
	}
	return &fakeRecoveryRuntime{
		containers:  append([]RecoveryContainer(nil), containers...),
		inspections: inspections,
		inspectErr:  make(map[string]error),
		deleteErr:   make(map[string]error),
	}
}

func (r *fakeRecoveryRuntime) ListSandboxContainers(
	context.Context,
) ([]RecoveryContainer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]RecoveryContainer(nil), r.containers...), nil
}

func (r *fakeRecoveryRuntime) Inspect(
	_ context.Context,
	containerID string,
) (RuntimeInspect, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.inspectErr[containerID]; err != nil {
		return RuntimeInspect{}, err
	}
	inspection, found := r.inspections[containerID]
	if !found {
		return RuntimeInspect{}, ErrRecoveryContainerMissing
	}
	return inspection, nil
}

func (r *fakeRecoveryRuntime) Delete(
	_ context.Context,
	containerID string,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.deleteErr[containerID]; err != nil {
		return err
	}
	r.deleted = append(r.deleted, containerID)
	return nil
}

func (r *fakeRecoveryRuntime) deletedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.deleted...)
}
