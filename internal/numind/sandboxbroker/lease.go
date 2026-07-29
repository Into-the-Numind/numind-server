package sandboxbroker

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// LeaseState identifies one durable phase in a Sandbox lease lifecycle.
type LeaseState string

const (
	// LeaseQueued means the request is durable but no container creation has started.
	LeaseQueued LeaseState = "queued"
	// LeaseCreating means container creation is in progress.
	LeaseCreating LeaseState = "creating"
	// LeaseReady means the container exists but is not bound to a product run.
	LeaseReady LeaseState = "ready"
	// LeaseActive means the container is bound to a product run and accepting work.
	LeaseActive LeaseState = "active"
	// LeaseOutputPersisting means output is being copied to durable product storage.
	LeaseOutputPersisting LeaseState = "output_persisting"
	// LeaseDestroying means container teardown is in progress.
	LeaseDestroying LeaseState = "destroying"
	// LeaseTerminated means teardown and required reconciliation are complete.
	LeaseTerminated LeaseState = "terminated"
	// LeaseRecoveryPending means an interrupted lifecycle requires reconciliation.
	LeaseRecoveryPending LeaseState = "recovery_pending"
)

// MaxJournalQueryLimit bounds every journal list operation.
const MaxJournalQueryLimit = 1000

var (
	// ErrJournalClosed is returned when an operation targets a closed journal.
	ErrJournalClosed = errors.New("sandbox broker journal closed")
	// ErrJournalLocked is returned when another broker owns the journal lock.
	ErrJournalLocked = errors.New("sandbox broker journal already locked")
	// ErrUnsafeJournalPath is returned when journal filesystem isolation is unsafe.
	ErrUnsafeJournalPath = errors.New("unsafe sandbox broker journal path")
	// ErrLeaseNotFound is returned when the requested lease does not exist.
	ErrLeaseNotFound = errors.New("sandbox broker lease not found")
	// ErrInvalidLease is returned when lease input violates journal invariants.
	ErrInvalidLease = errors.New("invalid sandbox broker lease")
	// ErrInvalidTransition is returned when a lifecycle transition is not allowed.
	ErrInvalidTransition = errors.New("invalid sandbox broker lease transition")
	// ErrIdempotencyConflict is returned when a request ID is reused for different work.
	ErrIdempotencyConflict = errors.New("sandbox broker idempotency conflict")
	// ErrInvalidQueryLimit is returned when a list request is not safely bounded.
	ErrInvalidQueryLimit = errors.New("invalid sandbox broker query limit")
)

var allowedTransitions = map[LeaseState]map[LeaseState]struct{}{
	LeaseQueued: {
		LeaseCreating:   {},
		LeaseDestroying: {},
	},
	LeaseCreating: {
		LeaseReady:           {},
		LeaseDestroying:      {},
		LeaseRecoveryPending: {},
	},
	LeaseReady: {
		LeaseActive:          {},
		LeaseDestroying:      {},
		LeaseRecoveryPending: {},
	},
	LeaseActive: {
		LeaseOutputPersisting: {},
		LeaseDestroying:       {},
		LeaseRecoveryPending:  {},
	},
	LeaseOutputPersisting: {
		LeaseDestroying:      {},
		LeaseRecoveryPending: {},
	},
	LeaseDestroying: {
		LeaseTerminated:      {},
		LeaseRecoveryPending: {},
	},
	LeaseRecoveryPending: {
		LeaseDestroying: {},
		LeaseTerminated: {},
	},
}

// Lease is the durable, content-free record of one Sandbox container lifecycle.
type Lease struct {
	LeaseID           string
	RequestID         string
	PeerUID           int64
	OwnerID           string
	OwnerBootID       string
	AgentRunID        uint64
	SandboxSessionID  uint64
	ContainerID       string
	State             LeaseState
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ExpiresAt         time.Time
	LastHeartbeatAt   time.Time
	CopyInFiles       int
	CopyInBytes       int64
	CopyOutFiles      int
	CopyOutBytes      int64
	TerminationReason string
	ReconcileState    string
}

// LeaseEvent is one append-only lifecycle or reconciliation audit entry.
type LeaseEvent struct {
	EventID     int64
	RequestID   string
	RequestHash string
	LeaseID     string
	EventType   string
	StateFrom   LeaseState
	StateTo     LeaseState
	Reason      string
	CreatedAt   time.Time
}

// CreateLeaseParams contains the immutable identity and lifetime of a new lease.
type CreateLeaseParams struct {
	LeaseID          string
	RequestID        string
	PeerUID          int64
	OwnerID          string
	OwnerBootID      string
	AgentRunID       uint64
	SandboxSessionID uint64
	CreatedAt        time.Time
	ExpiresAt        time.Time
}

// TransitionParams contains one idempotent, validated lifecycle transition.
type TransitionParams struct {
	LeaseID           string
	RequestID         string
	To                LeaseState
	At                time.Time
	AgentRunID        *uint64
	SandboxSessionID  *uint64
	ContainerID       *string
	ExpiresAt         *time.Time
	TerminationReason *string
	ReconcileState    *string
}

// IsValidLeaseState reports whether state is part of the durable state machine.
func IsValidLeaseState(state LeaseState) bool {
	switch state {
	case LeaseQueued, LeaseCreating, LeaseReady, LeaseActive,
		LeaseOutputPersisting, LeaseDestroying, LeaseTerminated,
		LeaseRecoveryPending:
		return true
	default:
		return false
	}
}

// CanTransition reports whether the explicit state table permits the requested move.
func CanTransition(from LeaseState, to LeaseState) bool {
	next, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	_, ok = next[to]
	return ok
}

func validateCreateLeaseParams(params CreateLeaseParams) error {
	if strings.TrimSpace(params.LeaseID) == "" ||
		strings.TrimSpace(params.RequestID) == "" ||
		strings.TrimSpace(params.OwnerID) == "" ||
		strings.TrimSpace(params.OwnerBootID) == "" ||
		params.PeerUID < 0 {
		return ErrInvalidLease
	}
	if len(params.LeaseID) > 128 || len(params.RequestID) > 128 ||
		len(params.OwnerID) > 128 || len(params.OwnerBootID) > 128 {
		return ErrInvalidLease
	}
	if params.AgentRunID != 0 || params.SandboxSessionID != 0 {
		return ErrInvalidLease
	}
	if params.CreatedAt.IsZero() {
		params.CreatedAt = time.Now()
	}
	if params.ExpiresAt.IsZero() || !params.ExpiresAt.After(params.CreatedAt) {
		return ErrInvalidLease
	}
	return nil
}

func validateTransitionParams(current Lease, params TransitionParams) error {
	if strings.TrimSpace(params.LeaseID) == "" ||
		strings.TrimSpace(params.RequestID) == "" ||
		params.LeaseID != current.LeaseID ||
		!IsValidLeaseState(params.To) ||
		!CanTransition(current.State, params.To) {
		return ErrInvalidTransition
	}
	if len(params.RequestID) > 128 {
		return ErrInvalidTransition
	}
	at := params.At
	if at.IsZero() {
		at = time.Now()
	}
	if at.Before(current.UpdatedAt) {
		return ErrInvalidTransition
	}
	if params.AgentRunID != nil && *params.AgentRunID > math.MaxInt64 {
		return ErrInvalidTransition
	}
	if params.SandboxSessionID != nil && *params.SandboxSessionID > math.MaxInt64 {
		return ErrInvalidTransition
	}
	if (params.AgentRunID == nil) != (params.SandboxSessionID == nil) {
		return ErrInvalidTransition
	}
	if params.ContainerID != nil && (strings.TrimSpace(*params.ContainerID) == "" || len(*params.ContainerID) > 128) {
		return ErrInvalidTransition
	}
	if params.TerminationReason != nil && (strings.TrimSpace(*params.TerminationReason) == "" ||
		len(*params.TerminationReason) > 256) {
		return ErrInvalidTransition
	}
	if params.ReconcileState != nil && *params.ReconcileState != "" &&
		*params.ReconcileState != "pending" &&
		*params.ReconcileState != "completed" &&
		*params.ReconcileState != "failed" {
		return ErrInvalidTransition
	}
	switch params.To {
	case LeaseCreating:
		if params.AgentRunID != nil || params.ContainerID != nil ||
			params.ExpiresAt != nil || params.TerminationReason != nil ||
			params.ReconcileState != nil {
			return ErrInvalidTransition
		}
	case LeaseReady:
		if params.ContainerID == nil ||
			params.ExpiresAt == nil || !params.ExpiresAt.After(at) {
			return ErrInvalidTransition
		}
		if params.AgentRunID != nil || params.TerminationReason != nil ||
			params.ReconcileState != nil || current.AgentRunID != 0 ||
			current.SandboxSessionID != 0 || current.ContainerID != "" {
			return ErrInvalidTransition
		}
	case LeaseActive:
		if params.AgentRunID == nil || params.SandboxSessionID == nil ||
			*params.AgentRunID == 0 || *params.SandboxSessionID == 0 {
			return ErrInvalidTransition
		}
		if params.ContainerID != nil || params.ExpiresAt != nil ||
			params.TerminationReason != nil || params.ReconcileState != nil ||
			current.AgentRunID != 0 || current.SandboxSessionID != 0 ||
			current.ContainerID == "" {
			return ErrInvalidTransition
		}
	case LeaseOutputPersisting:
		if params.AgentRunID != nil || params.ContainerID != nil ||
			params.ExpiresAt != nil || params.TerminationReason != nil ||
			params.ReconcileState != nil {
			return ErrInvalidTransition
		}
	case LeaseDestroying, LeaseRecoveryPending, LeaseTerminated:
		if params.TerminationReason == nil {
			return ErrInvalidTransition
		}
		if params.AgentRunID != nil || params.ContainerID != nil ||
			params.ExpiresAt != nil {
			return ErrInvalidTransition
		}
	}
	return nil
}

func validateQueryLimit(limit int) error {
	if limit <= 0 || limit > MaxJournalQueryLimit {
		return fmt.Errorf("%w: must be between 1 and %d", ErrInvalidQueryLimit, MaxJournalQueryLimit)
	}
	return nil
}
