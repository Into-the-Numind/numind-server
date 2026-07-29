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
    termination_reason TEXT NOT NULL DEFAULT '',
    reconcile_state TEXT NOT NULL DEFAULT '',
    CHECK ((agent_run_id IS NULL) = (sandbox_session_id IS NULL))
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

// OpenJournal opens or creates a locked SQLite lease journal at an absolute path.
func OpenJournal(ctx context.Context, path string) (*Journal, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: journal path must be absolute", ErrInvalidLease)
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return nil, fmt.Errorf("inspect journal directory: %w", err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o077 != 0 {
		return nil, ErrUnsafeJournalPath
	}
	if pathInfo, statErr := os.Lstat(path); statErr == nil {
		if !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 {
			return nil, ErrUnsafeJournalPath
		}
	} else if !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("inspect journal database: %w", statErr)
	}

	lockFD, err := unix.Open(path+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, ErrUnsafeJournalPath
		}
		return nil, fmt.Errorf("open journal lock: %w", err)
	}
	lockFile := os.NewFile(uintptr(lockFD), "sandbox-journal-lock")
	lockInfo, err := lockFile.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() {
		_ = lockFile.Close()
		return nil, ErrUnsafeJournalPath
	}
	if err := lockFile.Chmod(0o600); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("secure journal lock: %w", err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lockFile.Close()
		return nil, ErrJournalLocked
	}
	releaseLock := func() {
		_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
		_ = lockFile.Close()
	}

	uri := (&url.URL{Scheme: "file", Path: path}).String()
	dsn := uri + "?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_foreign_keys=ON"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		releaseLock()
		return nil, fmt.Errorf("open journal database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		releaseLock()
		return nil, fmt.Errorf("ping journal database: %w", err)
	}
	if _, err := db.ExecContext(ctx, journalSchema); err != nil {
		_ = db.Close()
		releaseLock()
		return nil, fmt.Errorf("initialize journal schema: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		releaseLock()
		return nil, fmt.Errorf("secure journal database: %w", err)
	}
	return &Journal{db: db, lockFile: lockFile}, nil
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
	reason := ""
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
	if lease.State != LeaseActive || at.Before(lease.UpdatedAt) {
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
     OR (state = ? AND (last_heartbeat_at IS NULL OR last_heartbeat_at <= ?))
     OR (state IN (?, ?, ?) AND updated_at <= ?)
 )
 ORDER BY updated_at, lease_id LIMIT ?`,
		LeaseQueued,
		LeaseReady,
		toUnixMillis(expiresAtOrBefore),
		LeaseActive,
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
		var createdAt int64
		if err := rows.Scan(
			&event.EventID,
			&event.RequestID,
			&event.RequestHash,
			&event.LeaseID,
			&event.EventType,
			&from,
			&to,
			&event.Reason,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan lease event: %w", err)
		}
		event.StateFrom = LeaseState(from)
		event.StateTo = LeaseState(to)
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
		&lease.TerminationReason,
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
		&event.Reason,
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
	return operationFingerprint(
		"transition",
		params.LeaseID,
		params.To,
		uint64PointerValue(params.AgentRunID),
		uint64PointerValue(params.SandboxSessionID),
		stringPointerValue(params.ContainerID),
		timePointerValue(params.ExpiresAt),
		stringPointerValue(params.TerminationReason),
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
