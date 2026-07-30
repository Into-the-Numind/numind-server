package sandboxbroker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenJournalAppliesDurableSettingsAndSecureFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "leases.db")
	journal := openTestJournal(t, path)

	var journalMode string
	if err := journal.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode = %q; want wal", journalMode)
	}
	var synchronous int
	if err := journal.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 {
		t.Fatalf("synchronous = %d; want FULL(2)", synchronous)
	}
	var busyTimeout int
	if err := journal.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d; want 5000", busyTimeout)
	}
	for _, target := range []string{
		filepath.Dir(path),
		path,
		path + ".lock",
		path + "-wal",
		path + "-shm",
	} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %o; want %o", filepath.Base(target), got, want)
		}
	}

	rows, err := journal.db.Query(`PRAGMA table_info(lease)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	forbidden := map[string]bool{
		"file": true, "payload": true, "prompt": true, "command": true,
		"stdout": true, "stderr": true, "secret": true, "token": true,
	}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if forbidden[strings.ToLower(name)] {
			t.Fatalf("journal schema contains forbidden business-data column %q", name)
		}
	}
}

func TestJournalSchemaRejectsInvalidLeaseInvariants(t *testing.T) {
	journal := openTestJournal(t, testJournalPath(t))
	now := time.Now().UTC().Truncate(time.Millisecond)
	params := testCreateParams("lease-1", "create-1", now)
	if _, _, err := journal.CreateLease(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"active without container or binding": `
UPDATE lease SET state='active' WHERE lease_id='lease-1'`,
		"unknown reconciliation state": `
UPDATE lease SET reconcile_state='arbitrary' WHERE lease_id='lease-1'`,
		"non-future expiry": `
UPDATE lease SET expires_at=created_at WHERE lease_id='lease-1'`,
		"free-text termination reason": `
UPDATE lease SET termination_reason='prompt-or-secret' WHERE lease_id='lease-1'`,
		"terminal state without reason code": `
UPDATE lease SET state='destroying' WHERE lease_id='lease-1'`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := journal.db.Exec(statement); err == nil {
				t.Fatalf("invalid direct SQL succeeded: %s", statement)
			}
		})
	}
}

func TestJournalSingleInstanceLock(t *testing.T) {
	path := testJournalPath(t)
	first := openTestJournal(t, path)
	if _, err := OpenJournal(context.Background(), path); !errors.Is(err, ErrJournalLocked) {
		t.Fatalf("second OpenJournal err = %v; want locked", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen after unlock: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenJournalRejectsUnsafeDirectoryWithoutChangingPermissions(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	_, err := OpenJournal(context.Background(), filepath.Join(parent, "leases.db"))
	if !errors.Is(err, ErrUnsafeJournalPath) {
		t.Fatalf("OpenJournal err = %v; want unsafe path", err)
	}
	info, statErr := os.Stat(parent)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("OpenJournal changed existing directory mode to %o", got)
	}
}

func TestOpenJournalRejectsSymlinkDatabaseLockAndSidecars(t *testing.T) {
	for _, target := range []string{"database", "lock", "wal", "shm"} {
		t.Run(target, func(t *testing.T) {
			parent := secureTestJournalParent(t)
			path := filepath.Join(parent, "leases.db")
			victim := filepath.Join(parent, "victim")
			if err := os.WriteFile(victim, []byte("do not touch"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := path
			if target == "lock" {
				link = path + ".lock"
			} else if target == "wal" {
				link = path + "-wal"
			} else if target == "shm" {
				link = path + "-shm"
			}
			if err := os.Symlink(victim, link); err != nil {
				t.Fatal(err)
			}
			_, err := OpenJournal(context.Background(), path)
			if !errors.Is(err, ErrUnsafeJournalPath) {
				t.Fatalf("OpenJournal err = %v; want unsafe path", err)
			}
			body, readErr := os.ReadFile(victim)
			if readErr != nil || string(body) != "do not touch" {
				t.Fatalf("victim changed: %q err=%v", body, readErr)
			}
		})
	}
}

func TestOpenJournalRejectsHardlinkedDatabaseLockAndSidecars(t *testing.T) {
	for _, target := range []string{"database", "lock", "wal", "shm"} {
		t.Run(target, func(t *testing.T) {
			parent := secureTestJournalParent(t)
			path := filepath.Join(parent, "leases.db")
			victim := filepath.Join(parent, "victim")
			if err := os.WriteFile(victim, []byte("do not touch"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := path
			if target == "lock" {
				link = path + ".lock"
			} else if target == "wal" {
				link = path + "-wal"
			} else if target == "shm" {
				link = path + "-shm"
			}
			if err := os.Link(victim, link); err != nil {
				t.Fatal(err)
			}
			_, err := OpenJournal(context.Background(), path)
			if !errors.Is(err, ErrUnsafeJournalPath) {
				t.Fatalf("OpenJournal err = %v; want unsafe path", err)
			}
			body, readErr := os.ReadFile(victim)
			if readErr != nil || string(body) != "do not touch" {
				t.Fatalf("victim changed: %q err=%v", body, readErr)
			}
		})
	}
}

func TestOpenJournalRejectsPermissiveDatabaseLockAndSidecars(t *testing.T) {
	for _, target := range []string{"database", "lock", "wal", "shm"} {
		t.Run(target, func(t *testing.T) {
			parent := secureTestJournalParent(t)
			path := filepath.Join(parent, "leases.db")
			unsafePath := path
			if target == "lock" {
				unsafePath = path + ".lock"
			} else if target == "wal" {
				unsafePath = path + "-wal"
			} else if target == "shm" {
				unsafePath = path + "-shm"
			}
			if err := os.WriteFile(unsafePath, []byte("do not touch"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(unsafePath, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := OpenJournal(context.Background(), path)
			if !errors.Is(err, ErrUnsafeJournalPath) {
				t.Fatalf("OpenJournal err = %v; want unsafe path", err)
			}
			info, statErr := os.Stat(unsafePath)
			if statErr != nil {
				t.Fatal(statErr)
			}
			if got := info.Mode().Perm(); got != 0o644 {
				t.Fatalf("OpenJournal changed unsafe target mode to %o", got)
			}
		})
	}
}

func TestJournalCreateLeaseConcurrentIdempotency(t *testing.T) {
	journal := openTestJournal(t, testJournalPath(t))
	params := testCreateParams("lease-1", "create-1", time.Now().UTC().Truncate(time.Millisecond))

	const goroutines = 32
	var wg sync.WaitGroup
	var firstCount atomic.Int32
	var replayCount atomic.Int32
	errs := make(chan error, goroutines)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, replay, err := journal.CreateLease(context.Background(), params)
			if err != nil {
				errs <- err
				return
			}
			if lease.LeaseID != params.LeaseID || lease.State != LeaseQueued {
				errs <- ErrInvalidLease
				return
			}
			if replay {
				replayCount.Add(1)
			} else {
				firstCount.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("CreateLease: %v", err)
	}
	if firstCount.Load() != 1 || replayCount.Load() != goroutines-1 {
		t.Fatalf("first/replay = %d/%d", firstCount.Load(), replayCount.Load())
	}
	var leaseCount int
	if err := journal.db.QueryRow(`SELECT COUNT(*) FROM lease`).Scan(&leaseCount); err != nil {
		t.Fatal(err)
	}
	var eventCount int
	if err := journal.db.QueryRow(`SELECT COUNT(*) FROM lease_event`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if leaseCount != 1 || eventCount != 1 {
		t.Fatalf("lease/event count = %d/%d; want 1/1", leaseCount, eventCount)
	}

	retry := params
	retry.LeaseID = "lease-newly-generated-on-retry"
	replayedLease, replay, err := journal.CreateLease(context.Background(), retry)
	if err != nil || !replay || replayedLease.LeaseID != params.LeaseID {
		t.Fatalf("generated-id retry lease=%#v replay=%v err=%v", replayedLease, replay, err)
	}

	conflict := params
	conflict.ExpiresAt = conflict.ExpiresAt.Add(time.Minute)
	if _, _, err := journal.CreateLease(context.Background(), conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting create replay err = %v", err)
	}
}

func TestJournalTransitionsHeartbeatAndAppendOnlyEvents(t *testing.T) {
	journal := openTestJournal(t, testJournalPath(t))
	now := time.Now().UTC().Truncate(time.Millisecond)
	params := testCreateParams("lease-1", "create-1", now)
	if _, _, err := journal.CreateLease(context.Background(), params); err != nil {
		t.Fatal(err)
	}

	creating := TransitionParams{
		LeaseID: params.LeaseID, RequestID: "transition-creating",
		To: LeaseCreating, At: now.Add(time.Second),
	}
	if _, replay, err := journal.Transition(context.Background(), creating); err != nil || replay {
		t.Fatalf("creating replay=%v err=%v", replay, err)
	}
	containerID := "rootless-container-1"
	readyExpiry := now.Add(10 * time.Minute)
	ready := TransitionParams{
		LeaseID: params.LeaseID, RequestID: "transition-ready",
		To: LeaseReady, At: now.Add(2 * time.Second),
		ContainerID: &containerID, ExpiresAt: &readyExpiry,
	}
	if _, _, err := journal.Transition(context.Background(), ready); err != nil {
		t.Fatal(err)
	}
	runID := uint64(77)
	sessionID := uint64(88)
	activeAt := now.Add(3 * time.Second)
	activeExpiry := activeAt.Add(MaxActiveLeaseDuration)
	active := TransitionParams{
		LeaseID: params.LeaseID, RequestID: "transition-active",
		To: LeaseActive, At: activeAt,
		AgentRunID: &runID, SandboxSessionID: &sessionID,
		ExpiresAt: &activeExpiry,
	}
	lease, _, err := journal.Transition(context.Background(), active)
	if err != nil {
		t.Fatal(err)
	}
	if lease.AgentRunID != runID || lease.SandboxSessionID != sessionID ||
		!lease.LastHeartbeatAt.Equal(active.At) {
		t.Fatalf("active lease = %#v", lease)
	}
	heartbeatAt := now.Add(4 * time.Second)
	lease, replay, err := journal.RecordHeartbeat(
		context.Background(), params.LeaseID, "heartbeat-1", heartbeatAt,
	)
	if err != nil || replay || !lease.LastHeartbeatAt.Equal(heartbeatAt) {
		t.Fatalf("heartbeat replay=%v lease=%#v err=%v", replay, lease, err)
	}
	if _, replay, err := journal.RecordHeartbeat(
		context.Background(), params.LeaseID, "heartbeat-1", heartbeatAt.Add(time.Second),
	); err != nil || !replay {
		t.Fatalf("heartbeat replay = %v err=%v", replay, err)
	}
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: params.LeaseID, RequestID: "transition-output",
		To: LeaseOutputPersisting, At: now.Add(5 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	reason := TerminationCompleted
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: params.LeaseID, RequestID: "transition-destroying",
		To: LeaseDestroying, At: now.Add(6 * time.Second),
		TerminationReason: &reason,
	}); err != nil {
		t.Fatal(err)
	}
	lease, _, err = journal.Transition(context.Background(), TransitionParams{
		LeaseID: params.LeaseID, RequestID: "transition-terminated",
		To: LeaseTerminated, At: now.Add(7 * time.Second),
		TerminationReason: &reason,
	})
	if err != nil || lease.State != LeaseTerminated {
		t.Fatalf("terminated lease=%#v err=%v", lease, err)
	}

	replayedLease, replay, err := journal.Transition(context.Background(), creating)
	if err != nil || !replay || replayedLease.State != LeaseTerminated {
		t.Fatalf("late transition replay lease=%#v replay=%v err=%v", replayedLease, replay, err)
	}
	conflictingReplay := creating
	conflictingReplay.To = LeaseActive
	if _, _, err := journal.Transition(context.Background(), conflictingReplay); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting transition replay err = %v", err)
	}
	conflictingReadyReplay := ready
	otherExpiry := readyExpiry.Add(time.Minute)
	conflictingReadyReplay.ExpiresAt = &otherExpiry
	if _, _, err := journal.Transition(context.Background(), conflictingReadyReplay); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting ready expiry replay err = %v", err)
	}

	events, err := journal.ListEvents(context.Background(), params.LeaseID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 8 {
		t.Fatalf("event count = %d; want 8", len(events))
	}
	if _, err := journal.db.Exec(`UPDATE lease_event SET reason='tamper' WHERE event_id=1`); err == nil {
		t.Fatal("append-only event update succeeded")
	}
	if _, err := journal.db.Exec(`DELETE FROM lease_event WHERE event_id=1`); err == nil {
		t.Fatal("append-only event delete succeeded")
	}
}

func TestJournalRejectsIllegalTransitionsAndRequestReuse(t *testing.T) {
	journal := openTestJournal(t, testJournalPath(t))
	now := time.Now().UTC().Truncate(time.Millisecond)
	params := testCreateParams("lease-1", "create-1", now)
	if _, _, err := journal.CreateLease(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	runID := uint64(1)
	sessionID := uint64(2)
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: params.LeaseID, RequestID: "skip-to-active",
		To: LeaseActive, At: now.Add(time.Second),
		AgentRunID: &runID, SandboxSessionID: &sessionID,
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("illegal transition err = %v", err)
	}
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: params.LeaseID, RequestID: params.RequestID,
		To: LeaseCreating, At: now.Add(time.Second),
	}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("cross-operation request reuse err = %v", err)
	}
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: "missing", RequestID: "missing-transition",
		To: LeaseCreating, At: now.Add(time.Second),
	}); !errors.Is(err, ErrLeaseNotFound) {
		t.Fatalf("missing lease transition err = %v", err)
	}

	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: params.LeaseID, RequestID: "creating-valid",
		To: LeaseCreating, At: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	sensitiveFreeText := TerminationReason("prompt=customer-data api_key=secret stdout=payload")
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: params.LeaseID, RequestID: "destroy-sensitive",
		To: LeaseDestroying, At: now.Add(3 * time.Second),
		TerminationReason: &sensitiveFreeText,
	}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("sensitive free-text reason err = %v", err)
	}
	var storedReason string
	if err := journal.db.QueryRow(
		`SELECT termination_reason FROM lease WHERE lease_id = ?`,
		params.LeaseID,
	).Scan(&storedReason); err != nil {
		t.Fatal(err)
	}
	if storedReason != "" {
		t.Fatalf("sensitive termination reason persisted: %q", storedReason)
	}
	var unsafeEventCount int
	if err := journal.db.QueryRow(
		`SELECT COUNT(*) FROM lease_event WHERE reason != ''`,
	).Scan(&unsafeEventCount); err != nil {
		t.Fatal(err)
	}
	if unsafeEventCount != 0 {
		t.Fatalf("unexpected reason-bearing events = %d", unsafeEventCount)
	}
}

func TestJournalCrashReopenPreservesLeaseAndEvents(t *testing.T) {
	if path := os.Getenv("NUMIND_JOURNAL_CRASH_TEST_PATH"); path != "" {
		runJournalCrashWriter(t, path)
		os.Exit(0)
	}

	path := testJournalPath(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestJournalCrashReopenPreservesLeaseAndEvents$")
	cmd.Env = append(os.Environ(), "NUMIND_JOURNAL_CRASH_TEST_PATH="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash writer failed: %v\n%s", err, output)
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	params := testCreateParams("lease-1", "create-1", now)
	reopened, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	lease, err := reopened.GetLease(context.Background(), params.LeaseID)
	if err != nil || lease.State != LeaseCreating {
		t.Fatalf("reopened lease=%#v err=%v", lease, err)
	}
	events, err := reopened.ListEvents(context.Background(), params.LeaseID, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("reopened events=%v err=%v", events, err)
	}
}

func runJournalCrashWriter(t *testing.T, path string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	params := testCreateParams("lease-1", "create-1", now)
	journal, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.CreateLease(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: params.LeaseID, RequestID: "creating-1",
		To: LeaseCreating, At: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestJournalStaleAndRecoveryQueriesAreBoundedAndDeterministic(t *testing.T) {
	journal := openTestJournal(t, testJournalPath(t))
	base := time.Now().UTC().Truncate(time.Millisecond)
	created := []time.Time{
		base.Add(-2 * time.Minute),
		base.Add(-90 * time.Second),
		base,
	}
	for i, at := range created {
		params := testCreateParams(
			"lease-"+string(rune('a'+i)),
			"create-"+string(rune('a'+i)),
			at,
		)
		if i == 0 {
			params.ExpiresAt = base.Add(-time.Minute)
		}
		if _, _, err := journal.CreateLease(context.Background(), params); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: "lease-b", RequestID: "creating-b",
		To: LeaseCreating, At: base.Add(-80 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	containerID := "container-b"
	readyExpiry := base.Add(10 * time.Minute)
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: "lease-b", RequestID: "ready-b",
		To: LeaseReady, At: base.Add(-70 * time.Second),
		ContainerID: &containerID, ExpiresAt: &readyExpiry,
	}); err != nil {
		t.Fatal(err)
	}
	runID := uint64(2)
	sessionID := uint64(3)
	activeAt := base.Add(-time.Minute)
	activeExpiry := activeAt.Add(MaxActiveLeaseDuration)
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: "lease-b", RequestID: "active-b",
		To: LeaseActive, At: activeAt,
		AgentRunID: &runID, SandboxSessionID: &sessionID,
		ExpiresAt: &activeExpiry,
	}); err != nil {
		t.Fatal(err)
	}

	absoluteParams := testCreateParams("lease-d", "create-d", base.Add(-6*time.Minute))
	if _, _, err := journal.CreateLease(context.Background(), absoluteParams); err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: "lease-d", RequestID: "creating-d",
		To: LeaseCreating, At: base.Add(-5*time.Minute - 2*time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	absoluteContainerID := "container-d"
	absoluteReadyExpiry := base.Add(10 * time.Minute)
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: "lease-d", RequestID: "ready-d",
		To: LeaseReady, At: base.Add(-5*time.Minute - time.Second),
		ContainerID: &absoluteContainerID, ExpiresAt: &absoluteReadyExpiry,
	}); err != nil {
		t.Fatal(err)
	}
	absoluteActiveAt := base.Add(-MaxActiveLeaseDuration)
	absoluteActiveExpiry := absoluteActiveAt.Add(MaxActiveLeaseDuration)
	absoluteRunID := uint64(4)
	absoluteSessionID := uint64(5)
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: "lease-d", RequestID: "active-d",
		To: LeaseActive, At: absoluteActiveAt,
		AgentRunID: &absoluteRunID, SandboxSessionID: &absoluteSessionID,
		ExpiresAt: &absoluteActiveExpiry,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.RecordHeartbeat(
		context.Background(), "lease-d", "heartbeat-d", base.Add(-30*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.RecordHeartbeat(
		context.Background(), "lease-d", "heartbeat-at-expiry-d", absoluteActiveExpiry,
	); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("heartbeat at absolute expiry err = %v", err)
	}

	cutoff := base.Add(-time.Minute)
	stale, err := journal.ListStale(context.Background(), base, cutoff, cutoff, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 3 ||
		stale[0].LeaseID != "lease-a" ||
		stale[1].LeaseID != "lease-b" ||
		stale[2].LeaseID != "lease-d" {
		t.Fatalf("stale leases = %v", leaseIDs(stale))
	}
	stale, err = journal.ListStale(context.Background(), base, base, base, 1)
	if err != nil || len(stale) != 1 || stale[0].LeaseID != "lease-a" {
		t.Fatalf("bounded stale leases = %v err=%v", leaseIDs(stale), err)
	}

	reason := TerminationContainerMissing
	reconcile := "pending"
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: "lease-a", RequestID: "creating-a",
		To: LeaseCreating, At: base.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.Transition(context.Background(), TransitionParams{
		LeaseID: "lease-a", RequestID: "recovery-a",
		To: LeaseRecoveryPending, At: base.Add(2 * time.Second),
		TerminationReason: &reason, ReconcileState: &reconcile,
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := journal.ListRecoveryPending(context.Background(), 10)
	if err != nil || len(pending) != 1 || pending[0].LeaseID != "lease-a" {
		t.Fatalf("pending leases = %v err=%v", leaseIDs(pending), err)
	}
	for _, limit := range []int{0, MaxJournalQueryLimit + 1} {
		if _, err := journal.ListLive(context.Background(), limit); !errors.Is(err, ErrInvalidQueryLimit) {
			t.Fatalf("ListLive limit %d err = %v", limit, err)
		}
	}
}

func TestJournalClosedMethodsFailAndCloseIsIdempotent(t *testing.T) {
	journal := openTestJournal(t, testJournalPath(t))
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.GetLease(context.Background(), "lease-1"); !errors.Is(err, ErrJournalClosed) {
		t.Fatalf("GetLease after Close err = %v", err)
	}
}

func TestJournalCloseWaitsForActiveOperation(t *testing.T) {
	journal := openTestJournal(t, testJournalPath(t))
	now := time.Now().UTC().Truncate(time.Millisecond)
	params := testCreateParams("lease-1", "create-1", now)
	if _, _, err := journal.CreateLease(context.Background(), params); err != nil {
		t.Fatal(err)
	}

	blockingTx, err := journal.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	getResult := make(chan error, 1)
	go func() {
		_, getErr := journal.GetLease(context.Background(), params.LeaseID)
		getResult <- getErr
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if !journal.mu.TryLock() {
			break
		}
		journal.mu.Unlock()
		if time.Now().After(deadline) {
			_ = blockingTx.Rollback()
			t.Fatal("GetLease did not enter an active journal operation")
		}
		runtime.Gosched()
	}

	closeResult := make(chan error, 1)
	go func() { closeResult <- journal.Close() }()
	select {
	case err := <-closeResult:
		_ = blockingTx.Rollback()
		t.Fatalf("Close returned before active operation drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	if err := blockingTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-getResult:
		if err != nil {
			t.Fatalf("active GetLease failed while Close waited: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active GetLease did not finish")
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish after active operation drained")
	}
	if _, err := journal.GetLease(context.Background(), params.LeaseID); !errors.Is(err, ErrJournalClosed) {
		t.Fatalf("GetLease after concurrent Close err = %v", err)
	}
}

func openTestJournal(t *testing.T, path string) *Journal {
	t.Helper()
	journal, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	return journal
}

func testJournalPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state", "leases.db")
}

func secureTestJournalParent(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	return parent
}

func testCreateParams(leaseID string, requestID string, createdAt time.Time) CreateLeaseParams {
	return CreateLeaseParams{
		LeaseID:     leaseID,
		RequestID:   requestID,
		PeerUID:     1000,
		OwnerID:     "api-primary",
		OwnerBootID: "boot-1",
		CreatedAt:   createdAt,
		ExpiresAt:   createdAt.Add(15 * time.Minute),
	}
}

func leaseIDs(leases []Lease) []string {
	ids := make([]string, 0, len(leases))
	for _, lease := range leases {
		ids = append(ids, lease.LeaseID)
	}
	return ids
}
