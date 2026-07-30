package sandboxbroker

import (
	"container/list"
	"context"
	"errors"
	"time"
)

const (
	// RecoveryDefaultTimeout is the startup recovery ceiling required before
	// sandboxd can consider reopening admission.
	RecoveryDefaultTimeout = 60 * time.Second
	defaultRecoveryLimit   = 100
)

var (
	// ErrInvalidRecoveryConfig means startup recovery cannot safely reason
	// about both the journal and the dedicated Rootless runtime.
	ErrInvalidRecoveryConfig = errors.New("invalid sandbox recovery config")
	// ErrRecoveryIncomplete means bounded startup recovery left durable
	// compensation work instead of blocking sandboxd indefinitely.
	ErrRecoveryIncomplete = errors.New("sandbox recovery incomplete")
	// ErrRecoveryContainerMissing is the normalized missing-container signal
	// returned by runtime adapters.
	ErrRecoveryContainerMissing = errors.New("sandbox recovery container missing")
)

// RecoveryRuntime lists only containers from the dedicated Rootless daemon and
// fixed sandbox labels. It must never scan the production host Docker daemon.
type RecoveryRuntime interface {
	ListSandboxContainers(context.Context) ([]RecoveryContainer, error)
	Inspect(context.Context, string) (RuntimeInspect, error)
	Delete(context.Context, string) error
}

// RecoveryContainer is a fixed-label container observed in the dedicated
// Rootless runtime.
type RecoveryContainer struct {
	ContainerID    string
	LeaseID        string
	BrokerInstance string
}

// RecoveryConfig wires bounded startup recovery without product secrets.
type RecoveryConfig struct {
	Journal    *Journal
	Scheduler  *Scheduler
	Runtime    RecoveryRuntime
	Timeout    time.Duration
	BatchLimit int
	Now        func() time.Time
}

// RecoveryResult is a content-free startup recovery summary.
type RecoveryResult struct {
	CheckedLeases  int
	RestoredLeases int
	PendingLeases  int
	DeletedOrphans int
	Consistent     bool
}

// RecoverJournalAndRuntime reconciles the durable journal with the dedicated
// Rootless runtime before sandboxd reopens admission.
func RecoverJournalAndRuntime(
	ctx context.Context,
	cfg RecoveryConfig,
) (RecoveryResult, error) {
	if ctx == nil {
		return RecoveryResult{}, ErrInvalidRecoveryConfig
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = RecoveryDefaultTimeout
	}
	limit := cfg.BatchLimit
	if limit == 0 {
		limit = defaultRecoveryLimit
	}
	if cfg.Journal == nil ||
		cfg.Scheduler == nil ||
		cfg.Runtime == nil ||
		timeout <= 0 ||
		timeout > RecoveryDefaultTimeout {
		return RecoveryResult{}, ErrInvalidRecoveryConfig
	}
	if err := validateQueryLimit(limit); err != nil {
		return RecoveryResult{}, err
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	recoveryDeadline := time.Now().Add(timeout)
	runtimeDeadline := recoveryDeadline.Add(-2 * time.Second)
	if !runtimeDeadline.After(time.Now()) {
		runtimeDeadline = recoveryDeadline
	}
	recoveryCtx, cancelRecovery := context.WithDeadline(ctx, runtimeDeadline)
	defer cancelRecovery()
	compensationCtx, cancelCompensation := context.WithDeadline(
		context.Background(),
		recoveryDeadline,
	)
	defer cancelCompensation()

	leases, err := cfg.Journal.ListLive(recoveryCtx, limit)
	if err != nil {
		return RecoveryResult{}, err
	}
	containers, err := cfg.Runtime.ListSandboxContainers(recoveryCtx)
	if err != nil {
		result := RecoveryResult{CheckedLeases: len(leases)}
		result.PendingLeases = markRemainingRecoveryPending(
			compensationCtx,
			cfg.Journal,
			leases,
			"",
			now(),
			TerminationRecoveryTimeout,
		)
		return result, errors.Join(ErrRecoveryIncomplete, err)
	}

	result := RecoveryResult{CheckedLeases: len(leases)}
	liveLeases := make(map[string]Lease, len(leases))
	for _, lease := range leases {
		liveLeases[lease.LeaseID] = lease
	}

	runtimeContainerIDs := make(map[string]struct{}, len(containers))
	for _, container := range containers {
		if !safeRuntimeToken(container.ContainerID) {
			return result, ErrInvalidRecoveryConfig
		}
		runtimeContainerIDs[container.ContainerID] = struct{}{}
		lease, found := liveLeases[container.LeaseID]
		if found && lease.ContainerID == container.ContainerID {
			continue
		}
		if err := cfg.Runtime.Delete(recoveryCtx, container.ContainerID); err != nil {
			return result, errors.Join(ErrRecoveryIncomplete, err)
		}
		result.DeletedOrphans++
	}

	var incomplete error
	for _, lease := range leases {
		if err := recoveryCtx.Err(); err != nil {
			result.PendingLeases += markRemainingRecoveryPending(
				compensationCtx,
				cfg.Journal,
				leases,
				lease.LeaseID,
				now(),
				TerminationRecoveryTimeout,
			)
			incomplete = errors.Join(incomplete, ErrRecoveryIncomplete, err)
			break
		}
		if lease.State == LeaseTerminated {
			continue
		}
		if lease.State == LeaseRecoveryPending {
			result.PendingLeases++
			continue
		}
		if lease.ContainerID == "" {
			if markLeaseRecoveryPending(
				compensationCtx,
				cfg.Journal,
				lease,
				now(),
				TerminationContainerMissing,
			) {
				result.PendingLeases++
			}
			_ = cfg.Scheduler.Release(lease.LeaseID)
			continue
		}
		inspection, inspectErr := cfg.Runtime.Inspect(
			recoveryCtx,
			lease.ContainerID,
		)
		if inspectErr != nil {
			if markLeaseRecoveryPending(
				compensationCtx,
				cfg.Journal,
				lease,
				now(),
				TerminationContainerMissing,
			) {
				result.PendingLeases++
			}
			_ = cfg.Scheduler.Release(lease.LeaseID)
			if !errors.Is(inspectErr, ErrRecoveryContainerMissing) {
				incomplete = errors.Join(
					incomplete,
					ErrRecoveryIncomplete,
					inspectErr,
				)
			}
			continue
		}
		if inspection.Status != "running" {
			reason := terminationReasonForRecoveredExit(inspection)
			if err := cfg.Runtime.Delete(recoveryCtx, lease.ContainerID); err != nil {
				incomplete = errors.Join(
					incomplete,
					ErrRecoveryIncomplete,
					err,
				)
			}
			if markLeaseRecoveryPending(
				compensationCtx,
				cfg.Journal,
				lease,
				now(),
				reason,
			) {
				result.PendingLeases++
			}
			_ = cfg.Scheduler.Release(lease.LeaseID)
			continue
		}
		if _, found := runtimeContainerIDs[lease.ContainerID]; !found {
			if markLeaseRecoveryPending(
				compensationCtx,
				cfg.Journal,
				lease,
				now(),
				TerminationContainerMissing,
			) {
				result.PendingLeases++
			}
			incomplete = errors.Join(
				incomplete,
				ErrRecoveryIncomplete,
				ErrRecoveryContainerMissing,
			)
			continue
		}
		if err := cfg.Scheduler.recoverLeaseSlot(lease); err != nil {
			if markLeaseRecoveryPending(
				compensationCtx,
				cfg.Journal,
				lease,
				now(),
				TerminationBrokerShutdown,
			) {
				result.PendingLeases++
			}
			incomplete = errors.Join(incomplete, ErrRecoveryIncomplete, err)
			continue
		}
		result.RestoredLeases++
	}

	result.Consistent = incomplete == nil
	if incomplete != nil {
		return result, incomplete
	}
	return result, nil
}

func (s *Scheduler) recoverLeaseSlot(lease Lease) error {
	if s == nil ||
		!safeRuntimeToken(lease.LeaseID) ||
		!safeRuntimeToken(lease.RequestID) ||
		!safeRuntimeToken(lease.OwnerID) {
		return ErrInvalidRecoveryConfig
	}
	var state schedulerSlotState
	switch lease.State {
	case LeaseReady:
		state = schedulerSlotReady
	case LeaseActive, LeaseOutputPersisting:
		state = schedulerSlotActive
	default:
		return ErrInvalidRecoveryConfig
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slots == nil {
		s.slots = make(map[string]*schedulerSlot, s.totalContainerMax)
	}
	if s.requests == nil {
		s.requests = make(map[string]*schedulerRequestRecord)
	}
	if s.leaseRequests == nil {
		s.leaseRequests = make(map[string]string)
	}
	if s.finished == nil {
		s.finished = list.New()
	}
	if existingRequestID, found := s.leaseRequests[lease.LeaseID]; found &&
		existingRequestID != lease.RequestID {
		return ErrSchedulerIdempotencyConflict
	}
	request := SchedulerRequest{
		RequestID: lease.RequestID,
		LeaseID:   lease.LeaseID,
		OwnerID:   lease.OwnerID,
	}
	record := s.requests[lease.RequestID]
	if record == nil {
		record = &schedulerRequestRecord{
			request:  request,
			admitted: true,
		}
		s.requests[lease.RequestID] = record
	} else if record.request != request {
		return ErrSchedulerIdempotencyConflict
	}
	s.leaseRequests[lease.LeaseID] = lease.RequestID

	if slot := s.slots[lease.LeaseID]; slot != nil {
		if slot.state == schedulerSlotActive && state != schedulerSlotActive {
			s.active--
		}
		if slot.state != schedulerSlotActive && state == schedulerSlotActive {
			s.active++
		}
		slot.request = request
		slot.state = state
		return nil
	}
	if len(s.slots) >= s.totalContainerMax {
		return ErrSchedulerActiveLimit
	}
	s.slots[lease.LeaseID] = &schedulerSlot{
		request: request,
		state:   state,
	}
	if state == schedulerSlotActive {
		if s.active >= s.activeTaskMax {
			delete(s.slots, lease.LeaseID)
			return ErrSchedulerActiveLimit
		}
		s.active++
	}
	return nil
}

func markRemainingRecoveryPending(
	ctx context.Context,
	journal *Journal,
	leases []Lease,
	startLeaseID string,
	at time.Time,
	reason TerminationReason,
) int {
	count := 0
	started := false
	for _, lease := range leases {
		if startLeaseID == "" || lease.LeaseID == startLeaseID {
			started = true
		}
		if !started {
			continue
		}
		if markLeaseRecoveryPending(ctx, journal, lease, at, reason) {
			count++
		}
	}
	return count
}

func markLeaseRecoveryPending(
	ctx context.Context,
	journal *Journal,
	lease Lease,
	at time.Time,
	reason TerminationReason,
) bool {
	if journal == nil || lease.State == LeaseRecoveryPending {
		return lease.State == LeaseRecoveryPending
	}
	if !CanTransition(lease.State, LeaseRecoveryPending) {
		return false
	}
	reconcile := "pending"
	_, _, err := journal.Transition(ctx, TransitionParams{
		LeaseID:           lease.LeaseID,
		RequestID:         derivedRPCRequestID(lease.RequestID, "startup-recovery"),
		To:                LeaseRecoveryPending,
		At:                canonicalJournalTime(at),
		TerminationReason: &reason,
		ReconcileState:    &reconcile,
	})
	return err == nil
}

func terminationReasonForRecoveredExit(
	inspection RuntimeInspect,
) TerminationReason {
	switch {
	case inspection.OOMKilled:
		return TerminationOutOfMemory
	case inspection.ExitCode == 124:
		return TerminationExecTimeout
	case inspection.ExitCode != 0:
		return TerminationExecFailed
	default:
		return TerminationCompleted
	}
}
