package sandboxbroker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/sys/unix"
)

const journalSchema = `
CREATE TABLE IF NOT EXISTS lease (
    lease_id TEXT PRIMARY KEY,
    request_id TEXT UNIQUE NOT NULL,
    peer_uid INTEGER NOT NULL CHECK (peer_uid >= 0),
    owner_id TEXT NOT NULL,
    owner_boot_id TEXT NOT NULL,
    agent_run_id INTEGER CHECK (agent_run_id IS NULL OR agent_run_id > 0),
    sandbox_session_id INTEGER CHECK (sandbox_session_id IS NULL OR sandbox_session_id > 0),
    container_id TEXT,
    state TEXT NOT NULL CHECK (state IN (
        'queued','creating','ready','active','output_persisting',
        'destroying','terminated','recovery_pending'
    )),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    last_heartbeat_at INTEGER,
    copy_in_files INTEGER NOT NULL DEFAULT 0 CHECK (copy_in_files >= 0),
    copy_in_bytes INTEGER NOT NULL DEFAULT 0 CHECK (copy_in_bytes >= 0),
    copy_out_files INTEGER NOT NULL DEFAULT 0 CHECK (copy_out_files >= 0),
    copy_out_bytes INTEGER NOT NULL DEFAULT 0 CHECK (copy_out_bytes >= 0),
    termination_reason TEXT NOT NULL DEFAULT '' CHECK (termination_reason IN (
        '','completed','cancelled','queue_timeout','lease_expired',
        'heartbeat_timeout','create_failed','activation_failed','exec_failed',
        'exec_timeout','out_of_memory','resource_limit','output_persist_failed',
        'container_missing','broker_shutdown','recovery_timeout','orphaned',
        'reconcile_failed'
    )),
    reconcile_state TEXT NOT NULL DEFAULT ''
        CHECK (reconcile_state IN ('','pending','completed','failed')),
    CHECK ((agent_run_id IS NULL) = (sandbox_session_id IS NULL)),
    CHECK (agent_run_id IS NULL OR container_id IS NOT NULL),
    CHECK (created_at <= updated_at),
    CHECK (expires_at > created_at),
    CHECK (
        last_heartbeat_at IS NULL OR
        (agent_run_id IS NOT NULL AND
         last_heartbeat_at >= created_at AND
         last_heartbeat_at <= updated_at)
    ),
    CHECK (
        (state IN ('queued','creating') AND
         container_id IS NULL AND agent_run_id IS NULL) OR
        (state = 'ready' AND
         container_id IS NOT NULL AND agent_run_id IS NULL) OR
        (state IN ('active','output_persisting') AND
         container_id IS NOT NULL AND agent_run_id IS NOT NULL AND
         last_heartbeat_at IS NOT NULL) OR
        state IN ('destroying','terminated','recovery_pending')
    ),
    CHECK (
        (state IN ('destroying','terminated','recovery_pending') AND
         termination_reason != '') OR
        (state NOT IN ('destroying','terminated','recovery_pending') AND
         termination_reason = '')
    )
);

CREATE INDEX IF NOT EXISTS idx_lease_state_updated
    ON lease(state, updated_at, lease_id);
CREATE INDEX IF NOT EXISTS idx_lease_owner
    ON lease(peer_uid, owner_id, created_at, lease_id);

CREATE TABLE IF NOT EXISTS lease_event (
    event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT UNIQUE NOT NULL,
    request_hash TEXT NOT NULL,
    lease_id TEXT NOT NULL REFERENCES lease(lease_id),
    event_type TEXT NOT NULL,
    state_from TEXT NOT NULL DEFAULT '',
    state_to TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_lease_event_lease
    ON lease_event(lease_id, event_id);

CREATE TRIGGER IF NOT EXISTS lease_event_no_update
BEFORE UPDATE ON lease_event
BEGIN
    SELECT RAISE(ABORT, 'lease_event is append-only');
END;

CREATE TRIGGER IF NOT EXISTS lease_event_no_delete
BEFORE DELETE ON lease_event
BEGIN
    SELECT RAISE(ABORT, 'lease_event is append-only');
END;
`

// Journal is the single-writer durable fact source for Sandbox lease lifecycles.
type Journal struct {
	mu       sync.RWMutex
	db       *sql.DB
	lockFile *os.File
	closed   bool
}

type privateFileIdentity struct {
	device uint64
	inode  uint64
}

// OpenJournal opens or creates a locked SQLite lease journal at an absolute path.
func OpenJournal(ctx context.Context, path string) (*Journal, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: journal path must be absolute", ErrInvalidLease)
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}
	if err := validatePrivateDirectory(parent); err != nil {
		return nil, ErrUnsafeJournalPath
	}

	lockPath := path + ".lock"
	lockFile, lockIdentity, err := openPrivateRegular(lockPath, unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		return nil, fmt.Errorf("open journal lock: %w", err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lockFile.Close()
		return nil, ErrJournalLocked
	}
	releaseLock := func() {
		_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
		_ = lockFile.Close()
	}
	reopenedLock, reopenedLockIdentity, err := openPrivateRegular(lockPath, unix.O_RDWR)
	if err != nil || reopenedLockIdentity != lockIdentity {
		if reopenedLock != nil {
			_ = reopenedLock.Close()
		}
		releaseLock()
		return nil, ErrUnsafeJournalPath
	}
	_ = reopenedLock.Close()

	dbGuard, dbIdentity, err := openPrivateRegular(path, unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		releaseLock()
		return nil, fmt.Errorf("open journal database guard: %w", err)
	}
	releaseDBGuard := func() {
		_ = dbGuard.Close()
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := validatePrivateRegularIfExists(sidecar); err != nil {
			releaseDBGuard()
			releaseLock()
			return nil, fmt.Errorf("inspect journal sidecar: %w", err)
		}
	}

	uri := (&url.URL{Scheme: "file", Path: path}).String()
	dsn := uri + "?mode=rw&_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_foreign_keys=ON"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		releaseDBGuard()
		releaseLock()
		return nil, fmt.Errorf("open journal database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		releaseDBGuard()
		releaseLock()
		return nil, fmt.Errorf("ping journal database: %w", err)
	}
	if _, err := db.ExecContext(ctx, journalSchema); err != nil {
		_ = db.Close()
		releaseDBGuard()
		releaseLock()
		return nil, fmt.Errorf("initialize journal schema: %w", err)
	}
	reopenedDB, reopenedIdentity, err := openPrivateRegular(path, unix.O_RDWR)
	if err != nil || reopenedIdentity != dbIdentity {
		if reopenedDB != nil {
			_ = reopenedDB.Close()
		}
		_ = db.Close()
		releaseDBGuard()
		releaseLock()
		return nil, ErrUnsafeJournalPath
	}
	_ = reopenedDB.Close()
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if err := validatePrivateRegularIfExists(sidecar); err != nil {
			_ = db.Close()
			releaseDBGuard()
			releaseLock()
			return nil, fmt.Errorf("validate journal sidecar: %w", err)
		}
	}
	releaseDBGuard()
	return &Journal{db: db, lockFile: lockFile}, nil
}

func validatePrivateDirectory(path string) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return fmt.Errorf("inspect private directory: %w", err)
	}
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Uid != uint32(os.Geteuid()) ||
		os.FileMode(mode).Perm()&0o077 != 0 {
		return ErrUnsafeJournalPath
	}
	return nil
}

func openPrivateRegular(path string, flags int) (*os.File, privateFileIdentity, error) {
	fd, err := unix.Open(path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, privateFileIdentity{}, ErrUnsafeJournalPath
		}
		return nil, privateFileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, privateFileIdentity{}, fmt.Errorf("inspect private file: %w", err)
	}
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Uid != uint32(os.Geteuid()) ||
		uint64(stat.Nlink) != 1 ||
		os.FileMode(mode).Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, privateFileIdentity{}, ErrUnsafeJournalPath
	}
	return file, privateFileIdentity{
		device: uint64(stat.Dev),
		inode:  uint64(stat.Ino),
	}, nil
}

func validatePrivateRegularIfExists(path string) error {
	file, _, err := openPrivateRegular(path, unix.O_RDONLY)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	return file.Close()
}

// Close drains in-process operations and releases the single-instance file lock.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	var errs []error
	if j.db != nil {
		if err := j.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if j.lockFile != nil {
		if err := unix.Flock(int(j.lockFile.Fd()), unix.LOCK_UN); err != nil {
			errs = append(errs, err)
		}
		if err := j.lockFile.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CreateLease durably creates a queued lease or replays the original result.
func (j *Journal) CreateLease(ctx context.Context, params CreateLeaseParams) (Lease, bool, error) {
	if params.CreatedAt.IsZero() {
		params.CreatedAt = canonicalJournalTime(time.Now())
	} else {
		params.CreatedAt = canonicalJournalTime(params.CreatedAt)
	}
	params.ExpiresAt = canonicalJournalTime(params.ExpiresAt)
	if err := validateCreateLeaseParams(params); err != nil {
		return Lease{}, false, err
	}

	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.closed {
		return Lease{}, false, ErrJournalClosed
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, false, fmt.Errorf("begin create lease: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	event, found, err := eventByRequestTx(ctx, tx, params.RequestID)
	if err != nil {
		return Lease{}, false, err
	}
	if found {
		lease, getErr := leaseByIDTx(ctx, tx, event.LeaseID)
		if getErr != nil {
			return Lease{}, false, getErr
		}
		if event.EventType != "create" || event.StateTo != LeaseQueued ||
			event.RequestHash != createLeaseFingerprint(params) ||
			!sameCreateIdentity(lease, params) {
			return Lease{}, false, ErrIdempotencyConflict
		}
		return lease, true, nil
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO lease (
    lease_id, request_id, peer_uid, owner_id, owner_boot_id,
    agent_run_id, sandbox_session_id, state, created_at, updated_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		params.LeaseID,
		params.RequestID,
		params.PeerUID,
		params.OwnerID,
		params.OwnerBootID,
		nullableUint64(params.AgentRunID),
		nullableUint64(params.SandboxSessionID),
		LeaseQueued,
		toUnixMillis(params.CreatedAt),
		toUnixMillis(params.CreatedAt),
		toUnixMillis(params.ExpiresAt),
	)
	if err != nil {
		return Lease{}, false, fmt.Errorf("insert lease: %w", err)
	}
	if err := insertEventTx(ctx, tx, LeaseEvent{
		RequestID:   params.RequestID,
		RequestHash: createLeaseFingerprint(params),
		LeaseID:     params.LeaseID,
		EventType:   "create",
		StateTo:     LeaseQueued,
		CreatedAt:   params.CreatedAt,
	}); err != nil {
		return Lease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, false, fmt.Errorf("commit create lease: %w", err)
	}
	lease, err := j.getLeaseLocked(ctx, params.LeaseID)
	return lease, false, err
}

// LookupCreateReplay checks only existing idempotency state and never writes.
// It lets a closed admission gate reject new work before growing the journal.
func (j *Journal) LookupCreateReplay(
	ctx context.Context,
	params CreateLeaseParams,
) (Lease, bool, error) {
	if params.CreatedAt.IsZero() {
		params.CreatedAt = canonicalJournalTime(time.Now())
	} else {
		params.CreatedAt = canonicalJournalTime(params.CreatedAt)
	}
	params.ExpiresAt = canonicalJournalTime(params.ExpiresAt)
	if err := validateCreateLeaseParams(params); err != nil {
		return Lease{}, false, err
	}

	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.closed {
		return Lease{}, false, ErrJournalClosed
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, false, fmt.Errorf("begin create replay lookup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	event, found, err := eventByRequestTx(ctx, tx, params.RequestID)
	if err != nil || !found {
		return Lease{}, false, err
	}
	lease, err := leaseByIDTx(ctx, tx, event.LeaseID)
	if err != nil {
		return Lease{}, false, err
	}
	if event.EventType != "create" ||
		event.StateTo != LeaseQueued ||
		event.RequestHash != createLeaseFingerprint(params) ||
		!sameCreateIdentity(lease, params) {
		return Lease{}, false, ErrIdempotencyConflict
	}
	return lease, true, nil
}

// Transition applies one legal lifecycle transition or replays an earlier request.
func (j *Journal) Transition(ctx context.Context, params TransitionParams) (Lease, bool, error) {
	if params.At.IsZero() {
		params.At = canonicalJournalTime(time.Now())
	} else {
		params.At = canonicalJournalTime(params.At)
	}
	if params.ExpiresAt != nil {
		expires := canonicalJournalTime(*params.ExpiresAt)
		params.ExpiresAt = &expires
	} else if params.To == LeaseActive {
		expires := params.At.Add(MaxActiveLeaseDuration)
		params.ExpiresAt = &expires
	}

	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.closed {
		return Lease{}, false, ErrJournalClosed
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, false, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	event, found, err := eventByRequestTx(ctx, tx, params.RequestID)
	if err != nil {
		return Lease{}, false, err
	}
	if found {
		if event.EventType != "transition" || event.LeaseID != params.LeaseID ||
			event.StateTo != params.To || event.RequestHash != transitionFingerprint(params) {
			return Lease{}, false, ErrIdempotencyConflict
		}
		lease, getErr := leaseByIDTx(ctx, tx, params.LeaseID)
		return lease, true, getErr
	}

	current, err := leaseByIDTx(ctx, tx, params.LeaseID)
	if err != nil {
		return Lease{}, false, err
	}
	if err := validateTransitionParams(current, params); err != nil {
		return Lease{}, false, err
	}
	next := applyTransition(current, params)
	if err := updateLeaseTx(ctx, tx, next); err != nil {
		return Lease{}, false, err
	}
	reason := TerminationReason("")
	if params.TerminationReason != nil {
		reason = *params.TerminationReason
	}
	if err := insertEventTx(ctx, tx, LeaseEvent{
		RequestID:   params.RequestID,
		RequestHash: transitionFingerprint(params),
		LeaseID:     params.LeaseID,
		EventType:   "transition",
		StateFrom:   current.State,
		StateTo:     params.To,
		Reason:      reason,
		CreatedAt:   params.At,
	}); err != nil {
		return Lease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, false, fmt.Errorf("commit transition: %w", err)
	}
	return next, false, nil
}

// RecordHeartbeat durably refreshes an active lease without changing its binding.
func (j *Journal) RecordHeartbeat(
	ctx context.Context,
	leaseID string,
	requestID string,
	at time.Time,
) (Lease, bool, error) {
	if strings.TrimSpace(leaseID) == "" || strings.TrimSpace(requestID) == "" ||
		len(leaseID) > 128 || len(requestID) > 128 {
		return Lease{}, false, ErrInvalidLease
	}
	if at.IsZero() {
		at = canonicalJournalTime(time.Now())
	} else {
		at = canonicalJournalTime(at)
	}

	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.closed {
		return Lease{}, false, ErrJournalClosed
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, false, fmt.Errorf("begin heartbeat: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	event, found, err := eventByRequestTx(ctx, tx, requestID)
	if err != nil {
		return Lease{}, false, err
	}
	if found {
		if event.EventType != "heartbeat" || event.LeaseID != leaseID ||
			event.RequestHash != operationFingerprint("heartbeat", leaseID) {
			return Lease{}, false, ErrIdempotencyConflict
		}
		lease, getErr := leaseByIDTx(ctx, tx, leaseID)
		return lease, true, getErr
	}
	lease, err := leaseByIDTx(ctx, tx, leaseID)
	if err != nil {
		return Lease{}, false, err
	}
	if lease.State != LeaseActive || at.Before(lease.UpdatedAt) || !at.Before(lease.ExpiresAt) {
		return Lease{}, false, ErrInvalidTransition
	}
	lease.LastHeartbeatAt = at
	lease.UpdatedAt = at
	if err := updateLeaseTx(ctx, tx, lease); err != nil {
		return Lease{}, false, err
	}
	if err := insertEventTx(ctx, tx, LeaseEvent{
		RequestID:   requestID,
		RequestHash: operationFingerprint("heartbeat", leaseID),
		LeaseID:     leaseID,
		EventType:   "heartbeat",
		StateFrom:   LeaseActive,
		StateTo:     LeaseActive,
		CreatedAt:   at,
	}); err != nil {
		return Lease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, false, fmt.Errorf("commit heartbeat: %w", err)
	}
	return lease, false, nil
}

// GetLease returns one lease by its durable identifier.
func (j *Journal) GetLease(ctx context.Context, leaseID string) (Lease, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.closed {
		return Lease{}, ErrJournalClosed
	}
	return j.getLeaseLocked(ctx, leaseID)
}

func (j *Journal) getLeaseLocked(ctx context.Context, leaseID string) (Lease, error) {
	return scanLease(j.db.QueryRowContext(ctx, leaseSelect+` WHERE lease_id = ?`, leaseID))
}

// ListByOwner returns bounded, non-terminated leases for one authenticated owner.
func (j *Journal) ListByOwner(
	ctx context.Context,
	peerUID int64,
	ownerID string,
	limit int,
) ([]Lease, error) {
	if peerUID < 0 || strings.TrimSpace(ownerID) == "" {
		return nil, ErrInvalidLease
	}
	if err := validateQueryLimit(limit); err != nil {
		return nil, err
	}
	return j.list(ctx, leaseSelect+`
 WHERE peer_uid = ? AND owner_id = ? AND state != ?
 ORDER BY created_at, lease_id LIMIT ?`, peerUID, ownerID, LeaseTerminated, limit)
}

// ListLive returns bounded leases that have not reached the terminated state.
func (j *Journal) ListLive(ctx context.Context, limit int) ([]Lease, error) {
	if err := validateQueryLimit(limit); err != nil {
		return nil, err
	}
	return j.list(ctx, leaseSelect+`
 WHERE state != ?
 ORDER BY updated_at, lease_id LIMIT ?`, LeaseTerminated, limit)
}

// ListStale returns bounded leases whose state-specific deadline is at or before its cutoff.
func (j *Journal) ListStale(
	ctx context.Context,
	expiresAtOrBefore time.Time,
	heartbeatAtOrBefore time.Time,
	updatedAtOrBefore time.Time,
	limit int,
) ([]Lease, error) {
	if expiresAtOrBefore.IsZero() || heartbeatAtOrBefore.IsZero() || updatedAtOrBefore.IsZero() {
		return nil, ErrInvalidLease
	}
	if err := validateQueryLimit(limit); err != nil {
		return nil, err
	}
	return j.list(ctx, leaseSelect+`
 WHERE (
        (state IN (?, ?) AND expires_at <= ?)
     OR (state = ? AND (
            expires_at <= ? OR
            last_heartbeat_at IS NULL OR
            last_heartbeat_at <= ?
        ))
     OR (state IN (?, ?, ?) AND updated_at <= ?)
 )
 ORDER BY updated_at, lease_id LIMIT ?`,
		LeaseQueued,
		LeaseReady,
		toUnixMillis(expiresAtOrBefore),
		LeaseActive,
		toUnixMillis(expiresAtOrBefore),
		toUnixMillis(heartbeatAtOrBefore),
		LeaseCreating,
		LeaseOutputPersisting,
		LeaseDestroying,
		toUnixMillis(updatedAtOrBefore),
		limit,
	)
}

// ListRecoveryPending returns bounded leases that still require reconciliation.
func (j *Journal) ListRecoveryPending(ctx context.Context, limit int) ([]Lease, error) {
	if err := validateQueryLimit(limit); err != nil {
		return nil, err
	}
	return j.list(ctx, leaseSelect+`
 WHERE state = ? OR reconcile_state = 'pending'
 ORDER BY updated_at, lease_id LIMIT ?`, LeaseRecoveryPending, limit)
}

// MarkReconcileCompleted marks one recovery-pending lease as fully reconciled.
// It is idempotent across retries: if another run already completed the same
// reconciliation, the current terminal lease is returned without changing user
// data or re-opening the lifecycle.
func (j *Journal) MarkReconcileCompleted(
	ctx context.Context,
	leaseID string,
	requestID string,
	at time.Time,
) (Lease, bool, error) {
	if strings.TrimSpace(leaseID) == "" ||
		strings.TrimSpace(requestID) == "" ||
		len(leaseID) > 128 ||
		len(requestID) > 128 {
		return Lease{}, false, ErrInvalidLease
	}
	if at.IsZero() {
		at = canonicalJournalTime(time.Now())
	} else {
		at = canonicalJournalTime(at)
	}

	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.closed {
		return Lease{}, false, ErrJournalClosed
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return Lease{}, false, fmt.Errorf("begin reconcile complete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	fingerprint := operationFingerprint("reconcile_completed", leaseID)
	event, found, err := eventByRequestTx(ctx, tx, requestID)
	if err != nil {
		return Lease{}, false, err
	}
	if found {
		if event.EventType != "reconcile_completed" ||
			event.LeaseID != leaseID ||
			event.RequestHash != fingerprint {
			return Lease{}, false, ErrIdempotencyConflict
		}
		lease, getErr := leaseByIDTx(ctx, tx, leaseID)
		return lease, true, getErr
	}

	current, err := leaseByIDTx(ctx, tx, leaseID)
	if err != nil {
		return Lease{}, false, err
	}
	if current.ReconcileState == "completed" && current.State == LeaseTerminated {
		return current, false, nil
	}
	if current.ReconcileState != "pending" && current.State != LeaseRecoveryPending {
		return Lease{}, false, ErrInvalidTransition
	}
	if current.TerminationReason == "" || !IsValidTerminationReason(current.TerminationReason) {
		return Lease{}, false, ErrInvalidTransition
	}
	if at.Before(current.UpdatedAt) {
		return Lease{}, false, ErrInvalidTransition
	}
	next := current
	if next.State == LeaseRecoveryPending {
		next.State = LeaseTerminated
	}
	next.ReconcileState = "completed"
	next.UpdatedAt = at
	if err := updateLeaseTx(ctx, tx, next); err != nil {
		return Lease{}, false, err
	}
	if err := insertEventTx(ctx, tx, LeaseEvent{
		RequestID:   requestID,
		RequestHash: fingerprint,
		LeaseID:     leaseID,
		EventType:   "reconcile_completed",
		StateFrom:   current.State,
		StateTo:     next.State,
		Reason:      current.TerminationReason,
		CreatedAt:   at,
	}); err != nil {
		return Lease{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, false, fmt.Errorf("commit reconcile complete: %w", err)
	}
	return next, false, nil
}

// ListEvents returns a bounded chronological audit trail for one lease.
func (j *Journal) ListEvents(ctx context.Context, leaseID string, limit int) ([]LeaseEvent, error) {
	if strings.TrimSpace(leaseID) == "" {
		return nil, ErrInvalidLease
	}
	if err := validateQueryLimit(limit); err != nil {
		return nil, err
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.closed {
		return nil, ErrJournalClosed
	}
	rows, err := j.db.QueryContext(ctx, `
SELECT event_id, request_id, request_hash, lease_id, event_type, state_from, state_to, reason, created_at
FROM lease_event WHERE lease_id = ? ORDER BY event_id LIMIT ?`, leaseID, limit)
	if err != nil {
		return nil, fmt.Errorf("list lease events: %w", err)
	}
	defer rows.Close()
	var events []LeaseEvent
	for rows.Next() {
		var event LeaseEvent
		var from string
		var to string
		var reason string
		var createdAt int64
		if err := rows.Scan(
			&event.EventID,
			&event.RequestID,
			&event.RequestHash,
			&event.LeaseID,
			&event.EventType,
			&from,
			&to,
			&reason,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan lease event: %w", err)
		}
		event.StateFrom = LeaseState(from)
		event.StateTo = LeaseState(to)
		event.Reason = TerminationReason(reason)
		if event.Reason != "" && !IsValidTerminationReason(event.Reason) {
			return nil, ErrInvalidLease
		}
		event.CreatedAt = fromUnixMillis(createdAt)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate lease events: %w", err)
	}
	return events, nil
}

func (j *Journal) list(ctx context.Context, query string, args ...any) ([]Lease, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.closed {
		return nil, ErrJournalClosed
	}
	rows, err := j.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list leases: %w", err)
	}
	defer rows.Close()
	var leases []Lease
	for rows.Next() {
		lease, scanErr := scanLease(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		leases = append(leases, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate leases: %w", err)
	}
	return leases, nil
}

const leaseSelect = `
SELECT lease_id, request_id, peer_uid, owner_id, owner_boot_id,
       agent_run_id, sandbox_session_id, container_id, state,
       created_at, updated_at, expires_at, last_heartbeat_at,
       copy_in_files, copy_in_bytes, copy_out_files, copy_out_bytes,
       termination_reason, reconcile_state
FROM lease`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanLease(row rowScanner) (Lease, error) {
	var lease Lease
	var agentRunID sql.NullInt64
	var sandboxSessionID sql.NullInt64
	var containerID sql.NullString
	var state string
	var createdAt int64
	var updatedAt int64
	var expiresAt int64
	var lastHeartbeatAt sql.NullInt64
	var terminationReason string
	err := row.Scan(
		&lease.LeaseID,
		&lease.RequestID,
		&lease.PeerUID,
		&lease.OwnerID,
		&lease.OwnerBootID,
		&agentRunID,
		&sandboxSessionID,
		&containerID,
		&state,
		&createdAt,
		&updatedAt,
		&expiresAt,
		&lastHeartbeatAt,
		&lease.CopyInFiles,
		&lease.CopyInBytes,
		&lease.CopyOutFiles,
		&lease.CopyOutBytes,
		&terminationReason,
		&lease.ReconcileState,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Lease{}, ErrLeaseNotFound
	}
	if err != nil {
		return Lease{}, fmt.Errorf("scan lease: %w", err)
	}
	if agentRunID.Valid {
		lease.AgentRunID = uint64(agentRunID.Int64)
	}
	if sandboxSessionID.Valid {
		lease.SandboxSessionID = uint64(sandboxSessionID.Int64)
	}
	if containerID.Valid {
		lease.ContainerID = containerID.String
	}
	lease.State = LeaseState(state)
	if !IsValidLeaseState(lease.State) {
		return Lease{}, ErrInvalidLease
	}
	lease.TerminationReason = TerminationReason(terminationReason)
	if lease.TerminationReason != "" && !IsValidTerminationReason(lease.TerminationReason) {
		return Lease{}, ErrInvalidLease
	}
	lease.CreatedAt = fromUnixMillis(createdAt)
	lease.UpdatedAt = fromUnixMillis(updatedAt)
	lease.ExpiresAt = fromUnixMillis(expiresAt)
	if lastHeartbeatAt.Valid {
		lease.LastHeartbeatAt = fromUnixMillis(lastHeartbeatAt.Int64)
	}
	return lease, nil
}

func leaseByIDTx(ctx context.Context, tx *sql.Tx, leaseID string) (Lease, error) {
	return scanLease(tx.QueryRowContext(ctx, leaseSelect+` WHERE lease_id = ?`, leaseID))
}

func eventByRequestTx(ctx context.Context, tx *sql.Tx, requestID string) (LeaseEvent, bool, error) {
	var event LeaseEvent
	var from string
	var to string
	var reason string
	var createdAt int64
	err := tx.QueryRowContext(ctx, `
SELECT event_id, request_id, request_hash, lease_id, event_type, state_from, state_to, reason, created_at
FROM lease_event WHERE request_id = ?`, requestID).Scan(
		&event.EventID,
		&event.RequestID,
		&event.RequestHash,
		&event.LeaseID,
		&event.EventType,
		&from,
		&to,
		&reason,
		&createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return LeaseEvent{}, false, nil
	}
	if err != nil {
		return LeaseEvent{}, false, fmt.Errorf("lookup idempotency event: %w", err)
	}
	event.StateFrom = LeaseState(from)
	event.StateTo = LeaseState(to)
	event.Reason = TerminationReason(reason)
	if event.Reason != "" && !IsValidTerminationReason(event.Reason) {
		return LeaseEvent{}, false, ErrInvalidLease
	}
	event.CreatedAt = fromUnixMillis(createdAt)
	return event, true, nil
}

func insertEventTx(ctx context.Context, tx *sql.Tx, event LeaseEvent) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO lease_event (
    request_id, request_hash, lease_id, event_type, state_from, state_to, reason, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RequestID,
		event.RequestHash,
		event.LeaseID,
		event.EventType,
		event.StateFrom,
		event.StateTo,
		event.Reason,
		toUnixMillis(event.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("append lease event: %w", err)
	}
	return nil
}

func updateLeaseTx(ctx context.Context, tx *sql.Tx, lease Lease) error {
	_, err := tx.ExecContext(ctx, `
UPDATE lease SET
    agent_run_id = ?,
    sandbox_session_id = ?,
    container_id = ?,
    state = ?,
    updated_at = ?,
    expires_at = ?,
    last_heartbeat_at = ?,
    copy_in_files = ?,
    copy_in_bytes = ?,
    copy_out_files = ?,
    copy_out_bytes = ?,
    termination_reason = ?,
    reconcile_state = ?
WHERE lease_id = ?`,
		nullableUint64(lease.AgentRunID),
		nullableUint64(lease.SandboxSessionID),
		nullableString(lease.ContainerID),
		lease.State,
		toUnixMillis(lease.UpdatedAt),
		toUnixMillis(lease.ExpiresAt),
		nullableTime(lease.LastHeartbeatAt),
		lease.CopyInFiles,
		lease.CopyInBytes,
		lease.CopyOutFiles,
		lease.CopyOutBytes,
		lease.TerminationReason,
		lease.ReconcileState,
		lease.LeaseID,
	)
	if err != nil {
		return fmt.Errorf("update lease: %w", err)
	}
	return nil
}

func applyTransition(current Lease, params TransitionParams) Lease {
	next := current
	next.State = params.To
	next.UpdatedAt = params.At
	if params.AgentRunID != nil {
		next.AgentRunID = *params.AgentRunID
	}
	if params.SandboxSessionID != nil {
		next.SandboxSessionID = *params.SandboxSessionID
	}
	if params.ContainerID != nil {
		next.ContainerID = *params.ContainerID
	}
	if params.ExpiresAt != nil {
		next.ExpiresAt = *params.ExpiresAt
	}
	if params.TerminationReason != nil {
		next.TerminationReason = *params.TerminationReason
	}
	if params.ReconcileState != nil {
		next.ReconcileState = *params.ReconcileState
	}
	if params.To == LeaseActive {
		next.LastHeartbeatAt = params.At
	}
	return next
}

func sameCreateIdentity(lease Lease, params CreateLeaseParams) bool {
	return lease.RequestID == params.RequestID &&
		lease.PeerUID == params.PeerUID &&
		lease.OwnerID == params.OwnerID &&
		lease.OwnerBootID == params.OwnerBootID
}

func createLeaseFingerprint(params CreateLeaseParams) string {
	return operationFingerprint(
		"create",
		params.PeerUID,
		params.OwnerID,
		params.OwnerBootID,
		params.ExpiresAt.Sub(params.CreatedAt).Milliseconds(),
	)
}

func transitionFingerprint(params TransitionParams) string {
	expiresAt := timePointerValue(params.ExpiresAt)
	if params.To == LeaseActive {
		// Activation expiry is a fixed broker result, not caller-supplied
		// request semantics. Excluding it lets response-loss retries replay the
		// first binding and its original absolute deadline.
		expiresAt = nil
	}
	return operationFingerprint(
		"transition",
		params.LeaseID,
		params.To,
		uint64PointerValue(params.AgentRunID),
		uint64PointerValue(params.SandboxSessionID),
		stringPointerValue(params.ContainerID),
		expiresAt,
		terminationReasonPointerValue(params.TerminationReason),
		stringPointerValue(params.ReconcileState),
	)
}

func operationFingerprint(values ...any) string {
	payload, _ := json.Marshal(values)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func uint64PointerValue(value *uint64) any {
	if value == nil {
		return nil
	}
	return *value
}

func stringPointerValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func terminationReasonPointerValue(value *TerminationReason) any {
	if value == nil {
		return nil
	}
	return *value
}

func timePointerValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return toUnixMillis(*value)
}

func nullableUint64(value uint64) any {
	if value == 0 {
		return nil
	}
	return int64(value)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return toUnixMillis(value)
}

func toUnixMillis(value time.Time) int64 {
	return value.UTC().UnixMilli()
}

func fromUnixMillis(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}

func canonicalJournalTime(value time.Time) time.Time {
	return fromUnixMillis(toUnixMillis(value))
}
