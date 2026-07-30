package sandboxbroker

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// SchedulerTotalContainerMax is the global creating+ready+active ceiling.
	SchedulerTotalContainerMax = 5
	// SchedulerActiveTaskMax is the global active-task ceiling.
	SchedulerActiveTaskMax = 5
	// SchedulerQueueWaitTimeout is the fixed maximum FIFO wait.
	SchedulerQueueWaitTimeout = 30 * time.Second
	// schedulerReplayRetention covers the maximum live lease plus retry margin.
	// The journal remains the durable idempotency source across broker restarts.
	schedulerReplayRetention = 10 * time.Minute
)

var (
	// ErrInvalidSchedulerRequest means an admission identity is missing or unsafe.
	ErrInvalidSchedulerRequest = errors.New("invalid sandbox scheduler request")
	// ErrSchedulerIdempotencyConflict means one request or lease identity was reused.
	ErrSchedulerIdempotencyConflict = errors.New("sandbox scheduler idempotency conflict")
	// ErrSchedulerQueueTimeout means no global slot opened within the fixed wait.
	ErrSchedulerQueueTimeout = errors.New("sandbox scheduler queue timeout")
	// ErrSchedulerLeaseNotFound means the lease does not own a live scheduler slot.
	ErrSchedulerLeaseNotFound = errors.New("sandbox scheduler lease not found")
	// ErrInvalidSchedulerTransition means a slot lifecycle call is out of order.
	ErrInvalidSchedulerTransition = errors.New("invalid sandbox scheduler transition")
	// ErrSchedulerActiveLimit means all global active-task slots are occupied.
	ErrSchedulerActiveLimit = errors.New("sandbox scheduler active-task limit reached")
)

// SchedulerRequest is the immutable identity of one journal-backed admission.
type SchedulerRequest struct {
	RequestID string
	LeaseID   string
	OwnerID   string
}

// SchedulerSnapshot is a content-free point-in-time capacity view.
type SchedulerSnapshot struct {
	Containers      int
	Creating        int
	Ready           int
	Active          int
	Queued          int
	QueueRequestIDs []string
}

type schedulerSlotState uint8

const (
	schedulerSlotCreating schedulerSlotState = iota + 1
	schedulerSlotReady
	schedulerSlotActive
)

type schedulerSlot struct {
	request SchedulerRequest
	state   schedulerSlotState
}

type schedulerWaiter struct {
	done     chan struct{}
	deadline time.Time
}

type schedulerRequestRecord struct {
	request    SchedulerRequest
	waiter     *schedulerWaiter
	admitted   bool
	released   bool
	outcomeSet bool
	outcome    error
	finishedAt time.Time
}

// Scheduler owns the single broker-wide FIFO and live capacity counters.
// Owners identify rolling API slots but never receive separate quotas.
type Scheduler struct {
	mu sync.Mutex

	totalContainerMax int
	activeTaskMax     int
	queueWaitTimeout  time.Duration

	slots         map[string]*schedulerSlot
	requests      map[string]*schedulerRequestRecord
	leaseRequests map[string]string
	queue         []*schedulerRequestRecord
	active        int
}

// NewScheduler returns the fixed production five-container/five-task scheduler.
func NewScheduler() *Scheduler {
	return newScheduler(
		SchedulerTotalContainerMax,
		SchedulerActiveTaskMax,
		SchedulerQueueWaitTimeout,
	)
}

func newScheduler(
	totalContainerMax int,
	activeTaskMax int,
	queueWaitTimeout time.Duration,
) *Scheduler {
	if totalContainerMax <= 0 ||
		activeTaskMax <= 0 ||
		queueWaitTimeout <= 0 {
		panic("invalid sandbox scheduler limits")
	}
	return &Scheduler{
		totalContainerMax: totalContainerMax,
		activeTaskMax:     activeTaskMax,
		queueWaitTimeout:  queueWaitTimeout,
		slots:             make(map[string]*schedulerSlot, totalContainerMax),
		requests:          make(map[string]*schedulerRequestRecord),
		leaseRequests:     make(map[string]string),
	}
}

// Acquire reserves one global container slot in strict FIFO order.
// A replay with the same immutable identity observes the first outcome and
// never consumes another slot.
func (s *Scheduler) Acquire(
	ctx context.Context,
	request SchedulerRequest,
) error {
	if s == nil || ctx == nil || !validSchedulerRequest(request) {
		return ErrInvalidSchedulerRequest
	}

	now := time.Now()
	s.mu.Lock()
	s.purgeFinishedLocked(now)
	record, exists := s.requests[request.RequestID]
	if exists {
		if record.request != request {
			s.mu.Unlock()
			return ErrSchedulerIdempotencyConflict
		}
		if record.admitted || record.released {
			s.mu.Unlock()
			return nil
		}
		if record.outcomeSet {
			outcome := record.outcome
			s.mu.Unlock()
			return outcome
		}
	} else {
		if existingRequestID, duplicate := s.leaseRequests[request.LeaseID]; duplicate &&
			existingRequestID != request.RequestID {
			s.mu.Unlock()
			return ErrSchedulerIdempotencyConflict
		}
		record = &schedulerRequestRecord{
			request: request,
			waiter: &schedulerWaiter{
				done:     make(chan struct{}),
				deadline: now.Add(s.queueWaitTimeout),
			},
		}
		s.requests[request.RequestID] = record
		s.leaseRequests[request.LeaseID] = request.RequestID
		s.queue = append(s.queue, record)
		s.grantFIFOHeadLocked()
	}
	waiter := record.waiter
	if record.admitted {
		s.mu.Unlock()
		return nil
	}
	remaining := time.Until(waiter.deadline)
	s.mu.Unlock()

	if remaining <= 0 {
		return s.finishWaiting(record, ErrSchedulerQueueTimeout)
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()

	select {
	case <-waiter.done:
		return s.waitingOutcome(record)
	case <-ctx.Done():
		return s.finishWaiting(record, ctx.Err())
	case <-timer.C:
		return s.finishWaiting(record, ErrSchedulerQueueTimeout)
	}
}

// MarkReady records that fixed-policy container creation completed.
func (s *Scheduler) MarkReady(leaseID string) error {
	if s == nil || !safeRuntimeToken(leaseID) {
		return ErrSchedulerLeaseNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, found := s.slots[leaseID]
	if !found {
		return ErrSchedulerLeaseNotFound
	}
	switch slot.state {
	case schedulerSlotCreating:
		slot.state = schedulerSlotReady
		return nil
	case schedulerSlotReady:
		return nil
	default:
		return ErrInvalidSchedulerTransition
	}
}

// Activate converts one ready container into an active task.
func (s *Scheduler) Activate(leaseID string) error {
	if s == nil || !safeRuntimeToken(leaseID) {
		return ErrSchedulerLeaseNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	slot, found := s.slots[leaseID]
	if !found {
		return ErrSchedulerLeaseNotFound
	}
	switch slot.state {
	case schedulerSlotActive:
		return nil
	case schedulerSlotReady:
		if s.active >= s.activeTaskMax {
			return ErrSchedulerActiveLimit
		}
		slot.state = schedulerSlotActive
		s.active++
		return nil
	default:
		return ErrInvalidSchedulerTransition
	}
}

// Release frees capacity only after container destruction has completed.
func (s *Scheduler) Release(leaseID string) error {
	if s == nil || !safeRuntimeToken(leaseID) {
		return ErrSchedulerLeaseNotFound
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeFinishedLocked(now)

	slot, found := s.slots[leaseID]
	if !found {
		if requestID, recorded := s.leaseRequests[leaseID]; recorded {
			record := s.requests[requestID]
			if record != nil && record.released {
				return nil
			}
		}
		return ErrSchedulerLeaseNotFound
	}
	if slot.state == schedulerSlotActive {
		s.active--
	}
	delete(s.slots, leaseID)
	record := s.requests[slot.request.RequestID]
	if record != nil {
		record.released = true
		record.finishedAt = now
	}
	s.grantFIFOHeadLocked()
	return nil
}

// Snapshot returns counters and FIFO order without exposing task content.
func (s *Scheduler) Snapshot() SchedulerSnapshot {
	if s == nil {
		return SchedulerSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := SchedulerSnapshot{
		Containers:      len(s.slots),
		Active:          s.active,
		Queued:          len(s.queue),
		QueueRequestIDs: make([]string, 0, len(s.queue)),
	}
	for _, slot := range s.slots {
		switch slot.state {
		case schedulerSlotCreating:
			snapshot.Creating++
		case schedulerSlotReady:
			snapshot.Ready++
		}
	}
	for _, record := range s.queue {
		snapshot.QueueRequestIDs = append(
			snapshot.QueueRequestIDs,
			record.request.RequestID,
		)
	}
	return snapshot
}

func (s *Scheduler) finishWaiting(
	record *schedulerRequestRecord,
	outcome error,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.admitted {
		return nil
	}
	if record.outcomeSet {
		return record.outcome
	}
	for index, queued := range s.queue {
		if queued == record {
			s.queue = append(s.queue[:index], s.queue[index+1:]...)
			break
		}
	}
	record.outcomeSet = true
	record.outcome = outcome
	record.finishedAt = time.Now()
	close(record.waiter.done)
	s.grantFIFOHeadLocked()
	return outcome
}

func (s *Scheduler) waitingOutcome(record *schedulerRequestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.admitted {
		return nil
	}
	if record.outcomeSet {
		return record.outcome
	}
	return ErrInvalidSchedulerTransition
}

func (s *Scheduler) grantFIFOHeadLocked() {
	for len(s.queue) > 0 &&
		len(s.slots) < s.totalContainerMax &&
		s.active < s.activeTaskMax {
		record := s.queue[0]
		s.queue = s.queue[1:]
		if record.outcomeSet || record.admitted {
			continue
		}
		slot := &schedulerSlot{
			request: record.request,
			state:   schedulerSlotCreating,
		}
		s.slots[record.request.LeaseID] = slot
		record.admitted = true
		close(record.waiter.done)
	}
}

func (s *Scheduler) purgeFinishedLocked(now time.Time) {
	for requestID, record := range s.requests {
		if record.finishedAt.IsZero() ||
			now.Sub(record.finishedAt) < schedulerReplayRetention {
			continue
		}
		delete(s.requests, requestID)
		delete(s.leaseRequests, record.request.LeaseID)
	}
}

func validSchedulerRequest(request SchedulerRequest) bool {
	return safeRuntimeToken(request.RequestID) &&
		safeRuntimeToken(request.LeaseID) &&
		safeRuntimeToken(request.OwnerID)
}
