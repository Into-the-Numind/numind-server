package sandboxbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
	"golang.org/x/sys/unix"
)

var (
	// ErrPeerUnauthorized means a Unix peer is not an allowlisted API UID.
	ErrPeerUnauthorized = errors.New("sandbox broker peer unauthorized")
	// ErrRPCProtocol means a request or service response violates the v1 contract.
	ErrRPCProtocol = errors.New("sandbox broker RPC protocol violation")
	// ErrRPCReplayResultUnavailable prevents re-executing a completed side effect
	// whose user output is intentionally not stored in the journal.
	ErrRPCReplayResultUnavailable = errors.New("sandbox broker replay result unavailable")
)

// PeerCredentials are kernel-authenticated Unix socket credentials.
type PeerCredentials struct {
	PID int32
	UID uint32
	GID uint32
}

// PeerAuthorizer reads and validates credentials at accept time.
type PeerAuthorizer interface {
	Authorize(net.Conn) (PeerCredentials, error)
}

// RPCService is the content-free v1 broker operation boundary.
type RPCService interface {
	CreateLease(context.Context, PeerCredentials, CreateLeaseRPCRequest) (CreateLeaseRPCResponse, error)
	Activate(context.Context, PeerCredentials, string, ActivateRPCRequest) error
	Heartbeat(context.Context, PeerCredentials, string, MutationRPCRequest) error
	MarkPersisting(context.Context, PeerCredentials, string, MutationRPCRequest) error
	Exec(context.Context, PeerCredentials, string, ExecRPCRequest) (ExecRPCResponse, error)
	CopyIn(context.Context, PeerCredentials, string, string, string, io.Reader) error
	CopyOut(context.Context, PeerCredentials, string, string, string) (io.ReadCloser, error)
	Mkdir(context.Context, PeerCredentials, string, MkdirRPCRequest) error
	Inspect(context.Context, PeerCredentials, string) (InspectRPCResponse, error)
	ListLeases(context.Context, PeerCredentials, string) ([]string, error)
	Delete(context.Context, PeerCredentials, string, MutationRPCRequest) error
}

// ContainerRuntime performs only fixed-policy operations against the dedicated
// Rootless daemon. It never receives peer-controlled Docker options.
type ContainerRuntime interface {
	Spawn(context.Context, string) (string, error)
	Exec(context.Context, string, []string, []string) (ExecRPCResponse, error)
	CopyIn(context.Context, string, string, io.Reader) (int64, error)
	CopyOut(context.Context, string, CopyOutSource) (RuntimeCopyOut, error)
	Mkdir(context.Context, string, []string) error
	Inspect(context.Context, string) (RuntimeInspect, error)
	Delete(context.Context, string) error
}

// RuntimeCopyOut describes a pre-validated bounded tar stream.
type RuntimeCopyOut struct {
	Reader io.ReadCloser
	Files  int
	Bytes  int64
}

// RuntimeInspect is the normalized container state used by RPC responses.
type RuntimeInspect struct {
	Status    string
	ExitCode  int
	OOMKilled bool
}

type rpcOperationStatus uint8

const (
	rpcOperationFirst rpcOperationStatus = iota + 1
	rpcOperationPending
	rpcOperationCompleted
)

// JournalRPCService binds the v1 transport to the durable lease journal,
// global scheduler, and fixed Rootless runtime.
type JournalRPCService struct {
	journal   *Journal
	scheduler *Scheduler
	runtime   ContainerRuntime
	creates   singleflight.Group
}

// NewJournalRPCService builds the only production RPC operation service.
func NewJournalRPCService(
	journal *Journal,
	scheduler *Scheduler,
	runtime ContainerRuntime,
) (*JournalRPCService, error) {
	if journal == nil || scheduler == nil || runtime == nil {
		return nil, ErrInvalidServerConfig
	}
	return &JournalRPCService{
		journal:   journal,
		scheduler: scheduler,
		runtime:   runtime,
	}, nil
}

func (s *JournalRPCService) CreateLease(
	ctx context.Context,
	peer PeerCredentials,
	input CreateLeaseRPCRequest,
) (CreateLeaseRPCResponse, error) {
	flightKey := operationFingerprint(
		"create_flight",
		input.RequestID,
		peer.UID,
		input.OwnerID,
		input.OwnerBootID,
	)
	value, err, _ := s.creates.Do(flightKey, func() (any, error) {
		now := canonicalJournalTime(time.Now())
		expiresAt := now.Add(RuntimeSessionTimeout)
		candidateID := uuid.NewString()
		lease, replay, err := s.journal.CreateLease(ctx, CreateLeaseParams{
			LeaseID:          candidateID,
			RequestID:        input.RequestID,
			PeerUID:          int64(peer.UID),
			OwnerID:          input.OwnerID,
			OwnerBootID:      input.OwnerBootID,
			AgentRunID:       0,
			SandboxSessionID: 0,
			CreatedAt:        now,
			ExpiresAt:        expiresAt,
		})
		if err != nil {
			return CreateLeaseRPCResponse{}, err
		}
		if replay {
			if lease.State != LeaseReady {
				return CreateLeaseRPCResponse{}, ErrRPCReplayResultUnavailable
			}
			return createLeaseRPCResponse(lease), nil
		}
		if err := s.scheduler.Acquire(ctx, SchedulerRequest{
			RequestID: input.RequestID,
			LeaseID:   lease.LeaseID,
			OwnerID:   input.OwnerID,
		}); err != nil {
			s.finishFailedCreate(lease.LeaseID, "", TerminationQueueTimeout)
			return CreateLeaseRPCResponse{}, err
		}
		lease, _, err = s.journal.Transition(ctx, TransitionParams{
			LeaseID:   lease.LeaseID,
			RequestID: derivedRPCRequestID(input.RequestID, "creating"),
			To:        LeaseCreating,
			At:        canonicalJournalTime(time.Now()),
		})
		if err != nil {
			s.finishFailedCreate(lease.LeaseID, "", TerminationCreateFailed)
			return CreateLeaseRPCResponse{}, err
		}
		containerID, err := s.runtime.Spawn(ctx, lease.LeaseID)
		if err != nil || !safeRuntimeToken(containerID) {
			s.finishFailedCreate(lease.LeaseID, containerID, TerminationCreateFailed)
			if err != nil {
				return CreateLeaseRPCResponse{}, err
			}
			return CreateLeaseRPCResponse{}, ErrRPCProtocol
		}
		readyAt := canonicalJournalTime(time.Now())
		lease, _, err = s.journal.Transition(ctx, TransitionParams{
			LeaseID:     lease.LeaseID,
			RequestID:   derivedRPCRequestID(input.RequestID, "ready"),
			To:          LeaseReady,
			At:          readyAt,
			ContainerID: &containerID,
			ExpiresAt:   &expiresAt,
		})
		if err != nil {
			s.finishFailedCreate(lease.LeaseID, containerID, TerminationCreateFailed)
			return CreateLeaseRPCResponse{}, err
		}
		if err := s.scheduler.MarkReady(lease.LeaseID); err != nil {
			s.finishFailedCreate(lease.LeaseID, containerID, TerminationCreateFailed)
			return CreateLeaseRPCResponse{}, err
		}
		return createLeaseRPCResponse(lease), nil
	})
	if err != nil {
		return CreateLeaseRPCResponse{}, err
	}
	response, ok := value.(CreateLeaseRPCResponse)
	if !ok {
		return CreateLeaseRPCResponse{}, ErrRPCProtocol
	}
	return response, nil
}

func (s *JournalRPCService) Activate(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
	input ActivateRPCRequest,
) error {
	if _, err := s.leaseForPeer(ctx, peer, leaseID); err != nil {
		return err
	}
	runID := input.AgentRunID
	sessionID := input.SandboxSessionID
	lease, _, err := s.journal.Transition(ctx, TransitionParams{
		LeaseID:          leaseID,
		RequestID:        input.RequestID,
		To:               LeaseActive,
		At:               canonicalJournalTime(time.Now()),
		AgentRunID:       &runID,
		SandboxSessionID: &sessionID,
	})
	if err != nil {
		return err
	}
	if err := s.scheduler.Activate(leaseID); err != nil {
		s.finishLease(
			lease,
			derivedRPCRequestID(input.RequestID, "activation-failed"),
			TerminationActivationFailed,
		)
		return err
	}
	return nil
}

func (s *JournalRPCService) Heartbeat(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
	input MutationRPCRequest,
) error {
	if _, err := s.leaseForPeer(ctx, peer, leaseID); err != nil {
		return err
	}
	_, _, err := s.journal.RecordHeartbeat(
		ctx,
		leaseID,
		input.RequestID,
		canonicalJournalTime(time.Now()),
	)
	return err
}

func (s *JournalRPCService) MarkPersisting(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
	input MutationRPCRequest,
) error {
	if _, err := s.leaseForPeer(ctx, peer, leaseID); err != nil {
		return err
	}
	_, _, err := s.journal.Transition(ctx, TransitionParams{
		LeaseID:   leaseID,
		RequestID: input.RequestID,
		To:        LeaseOutputPersisting,
		At:        canonicalJournalTime(time.Now()),
	})
	return err
}

func (s *JournalRPCService) Exec(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
	input ExecRPCRequest,
) (ExecRPCResponse, error) {
	lease, err := s.leaseForPeerState(ctx, peer, leaseID, LeaseActive)
	if err != nil {
		return ExecRPCResponse{}, err
	}
	fingerprint := operationFingerprint("exec", leaseID, input.Argv, input.Env)
	status, err := s.journal.reserveRPCOperation(
		ctx,
		lease,
		input.RequestID,
		"exec",
		fingerprint,
	)
	if err != nil {
		return ExecRPCResponse{}, err
	}
	if status != rpcOperationFirst {
		return ExecRPCResponse{}, ErrRPCReplayResultUnavailable
	}
	response, err := s.runtime.Exec(
		ctx,
		lease.ContainerID,
		input.Argv,
		input.Env,
	)
	if err != nil {
		return ExecRPCResponse{}, err
	}
	if err := s.journal.completeRPCOperation(
		ctx,
		leaseID,
		input.RequestID,
		"exec",
		fingerprint,
		nil,
	); err != nil {
		return ExecRPCResponse{}, err
	}
	return response, nil
}

func (s *JournalRPCService) CopyIn(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
	rawPath string,
	requestID string,
	reader io.Reader,
) error {
	lease, err := s.leaseForPeerStates(
		ctx,
		peer,
		leaseID,
		LeaseReady,
		LeaseActive,
	)
	if err != nil {
		return err
	}
	if err := CheckCopyBudget(
		CopyInDirection,
		lease.CopyInFiles,
		lease.CopyInBytes,
		0,
	); err != nil {
		return err
	}
	remaining := MaxCopyInBytes - lease.CopyInBytes
	if remaining > MaxSingleFileBytes {
		remaining = MaxSingleFileBytes
	}
	fingerprint := operationFingerprint("copy_in", leaseID, rawPath)
	status, err := s.journal.reserveRPCOperation(
		ctx,
		lease,
		requestID,
		"copy_in",
		fingerprint,
	)
	if err != nil {
		return err
	}
	if status == rpcOperationCompleted {
		return nil
	}
	if status == rpcOperationPending {
		return ErrRPCReplayResultUnavailable
	}
	bytesCopied, err := s.runtime.CopyIn(
		ctx,
		lease.ContainerID,
		rawPath,
		&hardLimitReader{
			source:   reader,
			remain:   remaining,
			limitErr: ErrStreamInputTooLarge,
		},
	)
	if err != nil {
		return err
	}
	return s.journal.completeRPCOperation(
		ctx,
		leaseID,
		requestID,
		"copy_in",
		fingerprint,
		func(current *Lease) error {
			if err := CheckCopyBudget(
				CopyInDirection,
				current.CopyInFiles,
				current.CopyInBytes,
				bytesCopied,
			); err != nil {
				return err
			}
			current.CopyInFiles++
			current.CopyInBytes += bytesCopied
			return nil
		},
	)
}

func (s *JournalRPCService) CopyOut(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
	rawPath string,
	requestID string,
) (io.ReadCloser, error) {
	lease, err := s.leaseForPeerState(
		ctx,
		peer,
		leaseID,
		LeaseOutputPersisting,
	)
	if err != nil {
		return nil, err
	}
	source, err := CanonicalCopyOutPath(rawPath)
	if err != nil {
		return nil, err
	}
	fingerprint := operationFingerprint("copy_out", leaseID, source)
	status, err := s.journal.reserveRPCOperation(
		ctx,
		lease,
		requestID,
		"copy_out",
		fingerprint,
	)
	if err != nil {
		return nil, err
	}
	if status == rpcOperationPending {
		return nil, ErrRPCReplayResultUnavailable
	}
	output, err := s.runtime.CopyOut(ctx, lease.ContainerID, source)
	if err != nil {
		return nil, err
	}
	if output.Reader == nil ||
		output.Files < 0 ||
		output.Files > MaxCopyFiles ||
		output.Bytes < 0 {
		if output.Reader != nil {
			_ = output.Reader.Close()
		}
		return nil, ErrRPCProtocol
	}
	if status == rpcOperationFirst {
		if err := s.journal.completeRPCOperation(
			ctx,
			leaseID,
			requestID,
			"copy_out",
			fingerprint,
			func(current *Lease) error {
				if err := CheckCopyBudget(
					CopyOutDirection,
					current.CopyOutFiles,
					current.CopyOutBytes,
					output.Bytes,
				); err != nil {
					return err
				}
				if current.CopyOutFiles+output.Files > MaxCopyFiles {
					return ErrStreamOutputTooLarge
				}
				current.CopyOutFiles += output.Files
				current.CopyOutBytes += output.Bytes
				return nil
			},
		); err != nil {
			_ = output.Reader.Close()
			return nil, err
		}
	}
	return output.Reader, nil
}

func (s *JournalRPCService) Mkdir(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
	input MkdirRPCRequest,
) error {
	lease, err := s.leaseForPeerStates(
		ctx,
		peer,
		leaseID,
		LeaseReady,
		LeaseActive,
	)
	if err != nil {
		return err
	}
	fingerprint := operationFingerprint("mkdir", leaseID, input.Dirs)
	status, err := s.journal.reserveRPCOperation(
		ctx,
		lease,
		input.RequestID,
		"mkdir",
		fingerprint,
	)
	if err != nil {
		return err
	}
	if status == rpcOperationCompleted {
		return nil
	}
	if status == rpcOperationPending {
		return ErrRPCReplayResultUnavailable
	}
	if err := s.runtime.Mkdir(ctx, lease.ContainerID, input.Dirs); err != nil {
		return err
	}
	return s.journal.completeRPCOperation(
		ctx,
		leaseID,
		input.RequestID,
		"mkdir",
		fingerprint,
		nil,
	)
}

func (s *JournalRPCService) Inspect(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
) (InspectRPCResponse, error) {
	lease, err := s.leaseForPeer(ctx, peer, leaseID)
	if err != nil {
		return InspectRPCResponse{}, err
	}
	inspection, err := s.runtime.Inspect(ctx, lease.ContainerID)
	if err != nil {
		return InspectRPCResponse{}, err
	}
	return InspectRPCResponse{
		Status:      inspection.Status,
		ExitCode:    inspection.ExitCode,
		OOMKilled:   inspection.OOMKilled,
		OwnerID:     lease.OwnerID,
		OwnerBootID: lease.OwnerBootID,
	}, nil
}

func (s *JournalRPCService) ListLeases(
	ctx context.Context,
	peer PeerCredentials,
	ownerID string,
) ([]string, error) {
	leases, err := s.journal.ListByOwner(
		ctx,
		int64(peer.UID),
		ownerID,
		MaxJournalQueryLimit,
	)
	if err != nil {
		return nil, err
	}
	leaseIDs := make([]string, 0, len(leases))
	for _, lease := range leases {
		leaseIDs = append(leaseIDs, lease.LeaseID)
	}
	return leaseIDs, nil
}

func (s *JournalRPCService) Delete(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
	input MutationRPCRequest,
) error {
	lease, err := s.leaseForPeer(ctx, peer, leaseID)
	if err != nil {
		return err
	}
	if lease.State == LeaseTerminated {
		return s.journal.recordTerminalDelete(ctx, lease, input.RequestID)
	}
	reason := TerminationCompleted
	if lease.State != LeaseDestroying && lease.State != LeaseRecoveryPending {
		lease, _, err = s.journal.Transition(ctx, TransitionParams{
			LeaseID:           leaseID,
			RequestID:         input.RequestID,
			To:                LeaseDestroying,
			At:                canonicalJournalTime(time.Now()),
			TerminationReason: &reason,
		})
		if err != nil {
			return err
		}
	}
	if lease.ContainerID != "" {
		if err := s.runtime.Delete(ctx, lease.ContainerID); err != nil {
			s.markRecoveryPending(lease, input.RequestID, reason)
			return err
		}
	}
	if err := s.scheduler.Release(leaseID); err != nil &&
		!errors.Is(err, ErrSchedulerLeaseNotFound) {
		return err
	}
	_, _, err = s.journal.Transition(ctx, TransitionParams{
		LeaseID:           leaseID,
		RequestID:         derivedRPCRequestID(input.RequestID, "terminated"),
		To:                LeaseTerminated,
		At:                canonicalJournalTime(time.Now()),
		TerminationReason: &reason,
	})
	return err
}

func (s *JournalRPCService) leaseForPeer(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
) (Lease, error) {
	lease, err := s.journal.GetLease(ctx, leaseID)
	if err != nil {
		return Lease{}, err
	}
	if lease.PeerUID != int64(peer.UID) {
		return Lease{}, ErrLeaseNotFound
	}
	return lease, nil
}

func (s *JournalRPCService) leaseForPeerState(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
	state LeaseState,
) (Lease, error) {
	return s.leaseForPeerStates(ctx, peer, leaseID, state)
}

func (s *JournalRPCService) leaseForPeerStates(
	ctx context.Context,
	peer PeerCredentials,
	leaseID string,
	states ...LeaseState,
) (Lease, error) {
	lease, err := s.leaseForPeer(ctx, peer, leaseID)
	if err != nil {
		return Lease{}, err
	}
	for _, state := range states {
		if lease.State == state {
			if lease.ContainerID == "" {
				return Lease{}, ErrRPCProtocol
			}
			return lease, nil
		}
	}
	return Lease{}, ErrInvalidTransition
}

func (s *JournalRPCService) finishFailedCreate(
	leaseID string,
	containerID string,
	reason TerminationReason,
) {
	ctx, cancel := context.WithTimeout(context.Background(), RuntimeExecTimeout)
	defer cancel()
	lease, err := s.journal.GetLease(ctx, leaseID)
	if err != nil {
		return
	}
	if containerID != "" {
		_ = s.runtime.Delete(ctx, containerID)
	}
	s.finishLease(
		lease,
		derivedRPCRequestID(lease.RequestID, "create-failed"),
		reason,
	)
}

func (s *JournalRPCService) finishLease(
	lease Lease,
	requestID string,
	reason TerminationReason,
) {
	ctx, cancel := context.WithTimeout(context.Background(), RuntimeExecTimeout)
	defer cancel()
	if lease.State != LeaseDestroying {
		next, _, err := s.journal.Transition(ctx, TransitionParams{
			LeaseID:           lease.LeaseID,
			RequestID:         requestID,
			To:                LeaseDestroying,
			At:                canonicalJournalTime(time.Now()),
			TerminationReason: &reason,
		})
		if err != nil {
			s.markRecoveryPending(lease, requestID, reason)
			return
		}
		lease = next
	}
	if lease.ContainerID != "" {
		if err := s.runtime.Delete(ctx, lease.ContainerID); err != nil {
			s.markRecoveryPending(lease, requestID, reason)
			return
		}
	}
	_ = s.scheduler.Release(lease.LeaseID)
	_, _, _ = s.journal.Transition(ctx, TransitionParams{
		LeaseID:           lease.LeaseID,
		RequestID:         derivedRPCRequestID(requestID, "terminated"),
		To:                LeaseTerminated,
		At:                canonicalJournalTime(time.Now()),
		TerminationReason: &reason,
	})
}

func (s *JournalRPCService) markRecoveryPending(
	lease Lease,
	requestID string,
	reason TerminationReason,
) {
	if !CanTransition(lease.State, LeaseRecoveryPending) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), RuntimeExecTimeout)
	defer cancel()
	reconcile := "pending"
	_, _, _ = s.journal.Transition(ctx, TransitionParams{
		LeaseID:           lease.LeaseID,
		RequestID:         derivedRPCRequestID(requestID, "recovery"),
		To:                LeaseRecoveryPending,
		At:                canonicalJournalTime(time.Now()),
		TerminationReason: &reason,
		ReconcileState:    &reconcile,
	})
}

func createLeaseRPCResponse(lease Lease) CreateLeaseRPCResponse {
	return CreateLeaseRPCResponse{
		LeaseID:   lease.LeaseID,
		State:     string(LeaseReady),
		ExpiresAt: lease.ExpiresAt,
	}
}

func derivedRPCRequestID(requestID string, phase string) string {
	return uuid.NewSHA1(
		uuid.NameSpaceOID,
		[]byte(requestID+"\x00"+phase),
	).String()
}

func (j *Journal) reserveRPCOperation(
	ctx context.Context,
	lease Lease,
	requestID string,
	kind string,
	fingerprint string,
) (rpcOperationStatus, error) {
	if j == nil ||
		!validRequestID(requestID) ||
		!validRPCOperationKind(kind) ||
		len(fingerprint) != 64 ||
		lease.LeaseID == "" {
		return 0, ErrRPCProtocol
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.closed {
		return 0, ErrJournalClosed
	}
	transaction, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin RPC reservation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	event, found, err := eventByRequestTx(ctx, transaction, requestID)
	if err != nil {
		return 0, err
	}
	if found {
		if event.EventType != kind+"_reserved" ||
			event.LeaseID != lease.LeaseID ||
			event.RequestHash != fingerprint {
			return 0, ErrIdempotencyConflict
		}
		completionID := derivedRPCRequestID(requestID, kind+"-completed")
		completion, completed, err := eventByRequestTx(
			ctx,
			transaction,
			completionID,
		)
		if err != nil {
			return 0, err
		}
		if completed {
			if completion.EventType != kind+"_completed" ||
				completion.LeaseID != lease.LeaseID ||
				completion.RequestHash != fingerprint {
				return 0, ErrIdempotencyConflict
			}
			return rpcOperationCompleted, nil
		}
		return rpcOperationPending, nil
	}
	current, err := leaseByIDTx(ctx, transaction, lease.LeaseID)
	if err != nil {
		return 0, err
	}
	if current.State != lease.State ||
		current.ContainerID != lease.ContainerID ||
		current.PeerUID != lease.PeerUID {
		return 0, ErrInvalidTransition
	}
	now := canonicalJournalTime(time.Now())
	if err := insertEventTx(ctx, transaction, LeaseEvent{
		RequestID:   requestID,
		RequestHash: fingerprint,
		LeaseID:     lease.LeaseID,
		EventType:   kind + "_reserved",
		StateFrom:   current.State,
		StateTo:     current.State,
		CreatedAt:   now,
	}); err != nil {
		return 0, err
	}
	if err := transaction.Commit(); err != nil {
		return 0, fmt.Errorf("commit RPC reservation: %w", err)
	}
	return rpcOperationFirst, nil
}

func (j *Journal) completeRPCOperation(
	ctx context.Context,
	leaseID string,
	requestID string,
	kind string,
	fingerprint string,
	mutate func(*Lease) error,
) error {
	if j == nil ||
		!validRequestID(requestID) ||
		!validRPCOperationKind(kind) ||
		len(fingerprint) != 64 ||
		!safeRuntimeToken(leaseID) {
		return ErrRPCProtocol
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.closed {
		return ErrJournalClosed
	}
	transaction, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin RPC completion: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	reservation, found, err := eventByRequestTx(
		ctx,
		transaction,
		requestID,
	)
	if err != nil {
		return err
	}
	if !found ||
		reservation.EventType != kind+"_reserved" ||
		reservation.LeaseID != leaseID ||
		reservation.RequestHash != fingerprint {
		return ErrIdempotencyConflict
	}
	completionID := derivedRPCRequestID(requestID, kind+"-completed")
	completion, completed, err := eventByRequestTx(
		ctx,
		transaction,
		completionID,
	)
	if err != nil {
		return err
	}
	if completed {
		if completion.EventType != kind+"_completed" ||
			completion.LeaseID != leaseID ||
			completion.RequestHash != fingerprint {
			return ErrIdempotencyConflict
		}
		return nil
	}
	current, err := leaseByIDTx(ctx, transaction, leaseID)
	if err != nil {
		return err
	}
	if mutate != nil {
		if err := mutate(&current); err != nil {
			return err
		}
		current.UpdatedAt = canonicalJournalTime(time.Now())
		if err := updateLeaseTx(ctx, transaction, current); err != nil {
			return err
		}
	}
	if err := insertEventTx(ctx, transaction, LeaseEvent{
		RequestID:   completionID,
		RequestHash: fingerprint,
		LeaseID:     leaseID,
		EventType:   kind + "_completed",
		StateFrom:   current.State,
		StateTo:     current.State,
		CreatedAt:   canonicalJournalTime(time.Now()),
	}); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit RPC completion: %w", err)
	}
	return nil
}

func validRPCOperationKind(kind string) bool {
	switch kind {
	case "exec", "copy_in", "copy_out", "mkdir":
		return true
	default:
		return false
	}
}

func (j *Journal) recordTerminalDelete(
	ctx context.Context,
	lease Lease,
	requestID string,
) error {
	if j == nil || lease.State != LeaseTerminated || !validRequestID(requestID) {
		return ErrRPCProtocol
	}
	fingerprint := operationFingerprint("delete", lease.LeaseID)
	j.mu.RLock()
	defer j.mu.RUnlock()
	if j.closed {
		return ErrJournalClosed
	}
	transaction, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin terminal delete replay: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	event, found, err := eventByRequestTx(ctx, transaction, requestID)
	if err != nil {
		return err
	}
	if found {
		if event.LeaseID != lease.LeaseID {
			return ErrIdempotencyConflict
		}
		if event.EventType == "transition" &&
			event.StateTo == LeaseDestroying {
			return nil
		}
		if event.EventType == "delete_completed" &&
			event.RequestHash == fingerprint {
			return nil
		}
		return ErrIdempotencyConflict
	}
	if err := insertEventTx(ctx, transaction, LeaseEvent{
		RequestID:   requestID,
		RequestHash: fingerprint,
		LeaseID:     lease.LeaseID,
		EventType:   "delete_completed",
		StateFrom:   LeaseTerminated,
		StateTo:     LeaseTerminated,
		CreatedAt:   canonicalJournalTime(time.Now()),
	}); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit terminal delete replay: %w", err)
	}
	return nil
}

type CreateLeaseRPCRequest struct {
	RequestID        string `json:"request_id"`
	OwnerID          string `json:"owner_id"`
	OwnerBootID      string `json:"owner_boot_id"`
	AgentRunID       uint64 `json:"agent_run_id"`
	SandboxSessionID uint64 `json:"sandbox_session_id"`
}

type CreateLeaseRPCResponse struct {
	LeaseID   string    `json:"lease_id"`
	State     string    `json:"state"`
	ExpiresAt time.Time `json:"expires_at"`
}

type ActivateRPCRequest struct {
	RequestID        string `json:"request_id"`
	AgentRunID       uint64 `json:"agent_run_id"`
	SandboxSessionID uint64 `json:"sandbox_session_id"`
}

type MutationRPCRequest struct {
	RequestID string `json:"request_id"`
}

type ExecRPCRequest struct {
	RequestID string   `json:"request_id"`
	Argv      []string `json:"argv"`
	Env       []string `json:"env,omitempty"`
}

type ExecRPCResponse struct {
	Stdout   string        `json:"stdout,omitempty"`
	Stderr   string        `json:"stderr,omitempty"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
}

type MkdirRPCRequest struct {
	RequestID string   `json:"request_id"`
	Dirs      []string `json:"dirs"`
}

type InspectRPCResponse struct {
	Status      string `json:"status"`
	ExitCode    int    `json:"exit_code"`
	OOMKilled   bool   `json:"oom_killed"`
	OwnerID     string `json:"owner_id"`
	OwnerBootID string `json:"owner_boot_id"`
}

type rpcErrorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message,omitempty"`
		RequestID string `json:"request_id,omitempty"`
	} `json:"error"`
}

type peerContextKey struct{}

type socketIdentity struct {
	device uint64
	inode  uint64
}

var socketUmaskMu sync.Mutex

// Server exposes RPCService only through one authenticated Unix socket.
type Server struct {
	config     ServerConfig
	service    RPCService
	authorizer PeerAuthorizer
	copies     *copyStreamLimiter

	mu       sync.Mutex
	http     *http.Server
	listener net.Listener
	socket   socketIdentity
}

// NewServer validates all fixed ceilings without opening a socket.
func NewServer(
	config ServerConfig,
	service RPCService,
	authorizer PeerAuthorizer,
) (*Server, error) {
	if err := config.validate(); err != nil || service == nil || authorizer == nil {
		return nil, ErrInvalidServerConfig
	}
	copies, err := newCopyStreamLimiter(
		config.MaxCopyStreams,
		config.MaxLeaseDirectionStreams,
		config.AggregateCopyBytesPerSecond,
	)
	if err != nil {
		return nil, err
	}
	server := &Server{
		config:     config,
		service:    service,
		authorizer: authorizer,
		copies:     copies,
	}
	server.http = &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    int(config.MetadataMaxBytes),
		ConnContext: func(ctx context.Context, connection net.Conn) context.Context {
			authorized, ok := connection.(*authorizedConnection)
			if !ok {
				return ctx
			}
			return context.WithValue(ctx, peerContextKey{}, authorized.peer)
		},
	}
	return server, nil
}

// ListenAndServe is the only public serving path and always opens Unix.
func (s *Server) ListenAndServe() error {
	listener, identity, err := listenServerUnix(s.config)
	if err != nil {
		return err
	}
	limited, err := newLimitedListener(listener, s.config.MaxConnections)
	if err != nil {
		_ = listener.Close()
		_ = removeServerSocket(s.config, identity)
		return err
	}
	authorized := &authorizingListener{
		Listener:   limited,
		authorizer: s.authorizer,
	}

	s.mu.Lock()
	if s.listener != nil {
		s.mu.Unlock()
		_ = authorized.Close()
		_ = removeServerSocket(s.config, identity)
		return ErrInvalidServerConfig
	}
	s.listener = authorized
	s.socket = identity
	s.mu.Unlock()

	err = s.http.Serve(authorized)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// Shutdown stops accepts and removes only the socket inode this server created.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	listener := s.listener
	identity := s.socket
	s.mu.Unlock()
	shutdownErr := s.http.Shutdown(ctx)
	if listener != nil {
		_ = listener.Close()
	}
	removeErr := removeServerSocket(s.config, identity)
	if shutdownErr != nil {
		return shutdownErr
	}
	return removeErr
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	peer, ok := request.Context().Value(peerContextKey{}).(PeerCredentials)
	if !ok {
		s.writeError(writer, request, ErrPeerUnauthorized)
		return
	}
	if !headersWithinLimit(request.Header, s.config.MetadataMaxBytes) {
		s.writeError(writer, request, ErrRPCProtocol)
		return
	}
	if request.URL.Path == "/v1/leases" {
		s.handleLeaseCollection(writer, request, peer)
		return
	}
	const prefix = "/v1/leases/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		http.NotFound(writer, request)
		return
	}
	remainder := strings.TrimPrefix(request.URL.Path, prefix)
	parts := strings.Split(remainder, "/")
	if len(parts) == 0 || len(parts) > 2 {
		http.NotFound(writer, request)
		return
	}
	leaseID, err := url.PathUnescape(parts[0])
	if err != nil || !safeRuntimeToken(leaseID) {
		s.writeError(writer, request, ErrRPCProtocol)
		return
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	s.handleLease(writer, request, peer, leaseID, action)
}

func (s *Server) handleLeaseCollection(
	writer http.ResponseWriter,
	request *http.Request,
	peer PeerCredentials,
) {
	switch request.Method {
	case http.MethodPost:
		if request.URL.RawQuery != "" {
			s.writeError(writer, request, ErrRPCProtocol)
			return
		}
		var input CreateLeaseRPCRequest
		if err := s.decodeMutation(writer, request, &input, ""); err != nil {
			s.writeError(writer, request, err)
			return
		}
		if input.AgentRunID != 0 ||
			input.SandboxSessionID != 0 ||
			!safeRuntimeToken(input.OwnerID) ||
			!safeRuntimeToken(input.OwnerBootID) {
			s.writeError(writer, request, ErrRPCProtocol)
			return
		}
		response, err := s.service.CreateLease(request.Context(), peer, input)
		if err != nil {
			s.writeError(writer, request, err)
			return
		}
		if !safeRuntimeToken(response.LeaseID) ||
			response.State != string(LeaseReady) ||
			response.ExpiresAt.IsZero() ||
			!response.ExpiresAt.After(time.Now()) {
			s.writeError(writer, request, ErrRPCProtocol)
			return
		}
		s.writeJSON(writer, http.StatusCreated, response)
	case http.MethodGet:
		ownerID, ok := exactQueryValue(request.URL.Query(), "owner_id")
		if !ok || !safeRuntimeToken(ownerID) {
			s.writeError(writer, request, ErrRPCProtocol)
			return
		}
		leaseIDs, err := s.service.ListLeases(request.Context(), peer, ownerID)
		if err != nil {
			s.writeError(writer, request, err)
			return
		}
		if !validLeaseIDList(leaseIDs) {
			s.writeError(writer, request, ErrRPCProtocol)
			return
		}
		s.writeJSON(writer, http.StatusOK, struct {
			LeaseIDs []string `json:"lease_ids"`
		}{LeaseIDs: leaseIDs})
	default:
		writer.Header().Set("Allow", "GET, POST")
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLease(
	writer http.ResponseWriter,
	request *http.Request,
	peer PeerCredentials,
	leaseID string,
	action string,
) {
	if action == "" {
		s.handleLeaseRoot(writer, request, peer, leaseID)
		return
	}
	switch action {
	case "activate":
		if request.Method != http.MethodPost {
			s.methodNotAllowed(writer, "POST")
			return
		}
		var input ActivateRPCRequest
		if err := s.decodeMutation(writer, request, &input, ""); err != nil ||
			input.AgentRunID == 0 ||
			input.SandboxSessionID == 0 {
			s.writeError(writer, request, ErrRPCProtocol)
			return
		}
		s.writeNoContent(
			writer,
			request,
			s.service.Activate(request.Context(), peer, leaseID, input),
		)
	case "heartbeat", "persisting":
		if request.Method != http.MethodPost {
			s.methodNotAllowed(writer, "POST")
			return
		}
		var input MutationRPCRequest
		if err := s.decodeMutation(writer, request, &input, ""); err != nil {
			s.writeError(writer, request, err)
			return
		}
		var err error
		if action == "heartbeat" {
			err = s.service.Heartbeat(request.Context(), peer, leaseID, input)
		} else {
			err = s.service.MarkPersisting(request.Context(), peer, leaseID, input)
		}
		s.writeNoContent(writer, request, err)
	case "exec":
		s.handleExec(writer, request, peer, leaseID)
	case "files":
		s.handleFiles(writer, request, peer, leaseID)
	case "mkdir":
		s.handleMkdir(writer, request, peer, leaseID)
	default:
		http.NotFound(writer, request)
	}
}

func (s *Server) handleLeaseRoot(
	writer http.ResponseWriter,
	request *http.Request,
	peer PeerCredentials,
	leaseID string,
) {
	switch request.Method {
	case http.MethodGet:
		if request.URL.RawQuery != "" {
			s.writeError(writer, request, ErrRPCProtocol)
			return
		}
		response, err := s.service.Inspect(request.Context(), peer, leaseID)
		if err != nil {
			s.writeError(writer, request, err)
			return
		}
		if !validInspectResponse(response) {
			s.writeError(writer, request, ErrRPCProtocol)
			return
		}
		s.writeJSON(writer, http.StatusOK, response)
	case http.MethodDelete:
		var input MutationRPCRequest
		if err := s.decodeMutation(writer, request, &input, ""); err != nil {
			s.writeError(writer, request, err)
			return
		}
		s.writeNoContent(
			writer,
			request,
			s.service.Delete(request.Context(), peer, leaseID, input),
		)
	default:
		s.methodNotAllowed(writer, "GET, DELETE")
	}
}

func (s *Server) handleExec(
	writer http.ResponseWriter,
	request *http.Request,
	peer PeerCredentials,
	leaseID string,
) {
	if request.Method != http.MethodPost {
		s.methodNotAllowed(writer, "POST")
		return
	}
	var input ExecRPCRequest
	if err := s.decodeMutation(writer, request, &input, ""); err != nil ||
		len(input.Argv) == 0 {
		s.writeError(writer, request, ErrRPCProtocol)
		return
	}
	response, err := s.service.Exec(request.Context(), peer, leaseID, input)
	if err != nil {
		s.writeError(writer, request, err)
		return
	}
	if response.Duration < 0 ||
		len(response.Stdout)+len(response.Stderr) > 4<<20 {
		s.writeError(writer, request, ErrRPCProtocol)
		return
	}
	s.writeJSON(writer, http.StatusOK, response)
}

func (s *Server) handleFiles(
	writer http.ResponseWriter,
	request *http.Request,
	peer PeerCredentials,
	leaseID string,
) {
	copyContext, cancelCopy := context.WithTimeout(
		request.Context(),
		RuntimeSessionTimeout,
	)
	defer cancelCopy()
	path, ok := exactQueryValue(request.URL.Query(), "path")
	if !ok || path == "" {
		s.writeError(writer, request, ErrRPCProtocol)
		return
	}
	requestID := request.Header.Get("X-Numind-Request-ID")
	if !validRequestID(requestID) {
		s.writeError(writer, request, ErrRPCProtocol)
		return
	}
	switch request.Method {
	case http.MethodPut:
		mediaType, _, err := mime.ParseMediaType(
			request.Header.Get("Content-Type"),
		)
		if err != nil || mediaType != "application/octet-stream" {
			s.writeError(writer, request, ErrRPCProtocol)
			return
		}
		release, err := s.copies.acquire(leaseID, CopyInDirection)
		if err != nil {
			s.writeError(writer, request, err)
			return
		}
		defer release()
		request.Body = http.MaxBytesReader(
			writer,
			request.Body,
			DefaultCopyOutLimits().MaxSingleBytes,
		)
		err = s.service.CopyIn(
			copyContext,
			peer,
			leaseID,
			path,
			requestID,
			s.copies.reader(copyContext, request.Body),
		)
		s.writeNoContent(writer, request, err)
	case http.MethodGet:
		release, err := s.copies.acquire(leaseID, CopyOutDirection)
		if err != nil {
			s.writeError(writer, request, err)
			return
		}
		defer release()
		source, err := s.service.CopyOut(
			copyContext,
			peer,
			leaseID,
			path,
			requestID,
		)
		if err != nil {
			s.writeError(writer, request, err)
			return
		}
		defer source.Close()
		writer.Header().Set("Content-Type", "application/x-tar")
		writer.WriteHeader(http.StatusOK)
		buffer := make([]byte, ServerCopyBufferBytes)
		_, _ = io.CopyBuffer(
			&maxBytesWriter{
				dst:      s.copies.writer(copyContext, writer),
				remain:   DefaultCopyOutLimits().MaxTotalBytes + (2 << 20),
				limitErr: ErrStreamOutputTooLarge,
			},
			source,
			buffer,
		)
	default:
		s.methodNotAllowed(writer, "GET, PUT")
	}
}

func (s *Server) handleMkdir(
	writer http.ResponseWriter,
	request *http.Request,
	peer PeerCredentials,
	leaseID string,
) {
	if request.Method != http.MethodPost {
		s.methodNotAllowed(writer, "POST")
		return
	}
	var input MkdirRPCRequest
	if err := s.decodeMutation(writer, request, &input, ""); err != nil ||
		len(input.Dirs) == 0 ||
		len(input.Dirs) > DefaultCopyOutLimits().MaxFiles {
		s.writeError(writer, request, ErrRPCProtocol)
		return
	}
	s.writeNoContent(
		writer,
		request,
		s.service.Mkdir(request.Context(), peer, leaseID, input),
	)
}

func (s *Server) decodeMutation(
	writer http.ResponseWriter,
	request *http.Request,
	target any,
	headerRequestID string,
) error {
	if request.URL.RawQuery != "" {
		return ErrRPCProtocol
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return ErrRPCProtocol
	}
	if request.ContentLength > s.config.MetadataMaxBytes {
		return ErrRPCProtocol
	}
	request.Body = http.MaxBytesReader(
		writer,
		request.Body,
		s.config.MetadataMaxBytes,
	)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrRPCProtocol
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrRPCProtocol
	}
	requestID := mutationRequestID(target)
	if !validRequestID(requestID) {
		return ErrRPCProtocol
	}
	header := request.Header.Get("X-Numind-Request-ID")
	if headerRequestID != "" {
		header = headerRequestID
	}
	if header != requestID {
		return ErrRPCProtocol
	}
	return nil
}

func mutationRequestID(target any) string {
	switch value := target.(type) {
	case *CreateLeaseRPCRequest:
		return value.RequestID
	case *ActivateRPCRequest:
		return value.RequestID
	case *MutationRPCRequest:
		return value.RequestID
	case *ExecRPCRequest:
		return value.RequestID
	case *MkdirRPCRequest:
		return value.RequestID
	default:
		return ""
	}
}

func (s *Server) writeNoContent(
	writer http.ResponseWriter,
	request *http.Request,
	err error,
) {
	if err != nil {
		s.writeError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeJSON(
	writer http.ResponseWriter,
	status int,
	value any,
) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func (s *Server) writeError(
	writer http.ResponseWriter,
	request *http.Request,
	err error,
) {
	status, code := rpcErrorContract(err)
	var response rpcErrorBody
	response.Error.Code = code
	requestID := request.Header.Get("X-Numind-Request-ID")
	if validRequestID(requestID) {
		response.Error.RequestID = requestID
	}
	s.writeJSON(writer, status, response)
}

func rpcErrorContract(err error) (int, string) {
	var maxBytesError *http.MaxBytesError
	switch {
	case errors.Is(err, ErrPeerUnauthorized):
		return http.StatusForbidden, "policy_denied"
	case errors.Is(err, ErrSchedulerQueueTimeout),
		errors.Is(err, ErrSchedulerQueueFull),
		errors.Is(err, ErrSchedulerReplayCacheFull),
		errors.Is(err, ErrSchedulerActiveLimit),
		errors.Is(err, ErrCopyStreamLimit):
		return http.StatusTooManyRequests, "capacity"
	case errors.Is(err, ErrLeaseNotFound),
		errors.Is(err, ErrSchedulerLeaseNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, ErrRuntimePolicyDenied),
		errors.Is(err, ErrStreamPolicyDenied),
		errors.Is(err, ErrInvalidLease),
		errors.Is(err, ErrInvalidTransition),
		errors.Is(err, ErrRPCProtocol):
		return http.StatusBadRequest, "policy_denied"
	case errors.Is(err, ErrIdempotencyConflict):
		return http.StatusConflict, "protocol_error"
	case errors.Is(err, ErrStreamInputTooLarge):
		return http.StatusRequestEntityTooLarge, "input_too_large"
	case errors.As(err, &maxBytesError):
		return http.StatusRequestEntityTooLarge, "input_too_large"
	case errors.Is(err, ErrStreamOutputTooLarge):
		return http.StatusRequestEntityTooLarge, "output_too_large"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "timeout"
	default:
		return http.StatusServiceUnavailable, "unavailable"
	}
}

func (s *Server) methodNotAllowed(writer http.ResponseWriter, allow string) {
	writer.Header().Set("Allow", allow)
	writer.WriteHeader(http.StatusMethodNotAllowed)
}

func exactQueryValue(values url.Values, key string) (string, bool) {
	if len(values) != 1 {
		return "", false
	}
	items, found := values[key]
	if !found || len(items) != 1 {
		return "", false
	}
	return items[0], true
}

func validRequestID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validLeaseIDList(values []string) bool {
	if values == nil {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !safeRuntimeToken(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validInspectResponse(response InspectRPCResponse) bool {
	switch response.Status {
	case "running", "exited", "oom":
	default:
		return false
	}
	return safeRuntimeToken(response.OwnerID) &&
		safeRuntimeToken(response.OwnerBootID)
}

func headersWithinLimit(header http.Header, maximum int64) bool {
	var total int64
	for key, values := range header {
		total += int64(len(key))
		for _, value := range values {
			total += int64(len(value))
			if total > maximum {
				return false
			}
		}
	}
	return total <= maximum
}

type authorizedConnection struct {
	net.Conn
	peer PeerCredentials
}

type authorizingListener struct {
	net.Listener
	authorizer PeerAuthorizer
}

func (l *authorizingListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		peer, err := l.authorizer.Authorize(connection)
		if err != nil {
			_ = connection.Close()
			continue
		}
		return &authorizedConnection{Conn: connection, peer: peer}, nil
	}
}

func listenServerUnix(config ServerConfig) (*net.UnixListener, socketIdentity, error) {
	parent := filepath.Dir(config.SocketPath)
	parentFD, err := openExistingDirectoryNoFollow(parent, false)
	if err != nil {
		return nil, socketIdentity{}, ErrUnsafeServerSocket
	}
	var parentStat unix.Stat_t
	if err := unix.Fstat(parentFD, &parentStat); err != nil {
		_ = unix.Close(parentFD)
		return nil, socketIdentity{}, ErrUnsafeServerSocket
	}
	if uint32(parentStat.Mode)&unix.S_IFMT != unix.S_IFDIR ||
		parentStat.Uid != config.SocketDirectoryUID ||
		parentStat.Gid != config.SocketDirectoryGID ||
		uint32(parentStat.Mode)&0o7777 != ServerSocketDirectoryMode {
		_ = unix.Close(parentFD)
		return nil, socketIdentity{}, ErrUnsafeServerSocket
	}
	if err := removeSafeStaleSocket(config, parentFD); err != nil {
		_ = unix.Close(parentFD)
		return nil, socketIdentity{}, err
	}
	_ = unix.Close(parentFD)

	socketUmaskMu.Lock()
	previousUmask := unix.Umask(0o117)
	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{Name: config.SocketPath, Net: "unix"},
	)
	unix.Umask(previousUmask)
	socketUmaskMu.Unlock()
	if err != nil {
		return nil, socketIdentity{}, fmt.Errorf("listen broker Unix socket: %w", err)
	}
	identity, err := inspectServerSocket(config)
	if err != nil {
		_ = listener.Close()
		return nil, socketIdentity{}, ErrUnsafeServerSocket
	}
	return listener, identity, nil
}

func removeSafeStaleSocket(config ServerConfig, parentFD int) error {
	var stat unix.Stat_t
	err := unix.Lstat(config.SocketPath, &stat)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil ||
		uint32(stat.Mode)&unix.S_IFMT != unix.S_IFSOCK ||
		stat.Uid != config.SocketUID ||
		stat.Gid != config.SocketGID ||
		uint32(stat.Mode)&0o7777 != ServerSocketMode ||
		stat.Nlink != 1 {
		return ErrUnsafeServerSocket
	}
	if err := unix.Unlinkat(parentFD, filepath.Base(config.SocketPath), 0); err != nil {
		return ErrUnsafeServerSocket
	}
	return nil
}

func inspectServerSocket(config ServerConfig) (socketIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Lstat(config.SocketPath, &stat); err != nil {
		return socketIdentity{}, err
	}
	if uint32(stat.Mode)&unix.S_IFMT != unix.S_IFSOCK ||
		stat.Uid != config.SocketUID ||
		stat.Gid != config.SocketGID ||
		uint32(stat.Mode)&0o7777 != ServerSocketMode ||
		stat.Nlink != 1 {
		return socketIdentity{}, ErrUnsafeServerSocket
	}
	return socketIdentity{
		device: uint64(stat.Dev),
		inode:  uint64(stat.Ino),
	}, nil
}

func removeServerSocket(config ServerConfig, identity socketIdentity) error {
	if identity == (socketIdentity{}) {
		return nil
	}
	current, err := inspectServerSocket(config)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil || current != identity {
		return ErrUnsafeServerSocket
	}
	parentFD, err := openExistingDirectoryNoFollow(
		filepath.Dir(config.SocketPath),
		false,
	)
	if err != nil {
		return ErrUnsafeServerSocket
	}
	defer unix.Close(parentFD)
	if err := unix.Unlinkat(parentFD, filepath.Base(config.SocketPath), 0); err != nil {
		return ErrUnsafeServerSocket
	}
	return nil
}
