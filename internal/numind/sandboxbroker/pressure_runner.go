package sandboxbroker

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// SandboxDrainTimeout is the maximum graceful drain on SIGTERM.
	SandboxDrainTimeout = 300 * time.Second
)

var (
	// ErrInvalidPressureRunnerConfig means sandboxd cannot safely wire the
	// pressure/readiness loop.
	ErrInvalidPressureRunnerConfig = errors.New("invalid sandbox pressure runner config")
	// ErrSandboxDrainDeadline means graceful shutdown reached its fixed ceiling.
	ErrSandboxDrainDeadline = errors.New("sandbox drain deadline reached")
)

// PressureSampler returns one trusted host/cgroup sample.
type PressureSampler interface {
	Sample(context.Context) (PressureSample, error)
}

// PressureLeaseReclaimer terminates one lease selected by pressure control.
type PressureLeaseReclaimer interface {
	ReclaimLease(context.Context, string, TerminationReason) error
}

// PressureRunnerConfig wires the production sampler loop without business
// service credentials.
type PressureRunnerConfig struct {
	Scheduler *Scheduler
	Pressure  *PressureController
	Readiness *ReadinessChecker
	Sampler   PressureSampler
	Reclaimer PressureLeaseReclaimer
	Interval  time.Duration
	Watchdog  time.Duration
	Now       func() time.Time
}

// PressureRunner owns the T10 sampling cadence and watchdog.
type PressureRunner struct {
	scheduler *Scheduler
	pressure  *PressureController
	readiness *ReadinessChecker
	sampler   PressureSampler
	reclaimer PressureLeaseReclaimer
	interval  time.Duration
	watchdog  time.Duration
	now       func() time.Time

	mu          sync.Mutex
	lastRefresh time.Time
}

// NewPressureRunner starts closed and admits only after pressure and readiness
// have both published a fresh successful sample.
func NewPressureRunner(cfg PressureRunnerConfig) (*PressureRunner, error) {
	interval := cfg.Interval
	if interval == 0 {
		interval = PressureSampleInterval
	}
	watchdog := cfg.Watchdog
	if watchdog == 0 {
		watchdog = PressureMaximumSampleGap
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	if cfg.Scheduler == nil ||
		cfg.Pressure == nil ||
		cfg.Readiness == nil ||
		cfg.Sampler == nil ||
		cfg.Reclaimer == nil ||
		interval != PressureSampleInterval ||
		watchdog != PressureMaximumSampleGap ||
		now().IsZero() {
		return nil, ErrInvalidPressureRunnerConfig
	}
	cfg.Scheduler.SetAdmission(false, ErrPressureSamplingGap)
	return &PressureRunner{
		scheduler: cfg.Scheduler,
		pressure:  cfg.Pressure,
		readiness: cfg.Readiness,
		sampler:   cfg.Sampler,
		reclaimer: cfg.Reclaimer,
		interval:  interval,
		watchdog:  watchdog,
		now:       now,
	}, nil
}

// Run samples immediately and then every two seconds until ctx is cancelled.
// It always runs readiness sync after an Observe result, including sampling-gap
// errors, so the scheduler's actual gate follows the latest safe state.
func (r *PressureRunner) Run(ctx context.Context) error {
	if r == nil || ctx == nil {
		return ErrInvalidPressureRunnerConfig
	}
	watchdogCtx, stopWatchdog := context.WithCancel(ctx)
	defer stopWatchdog()
	go r.runWatchdog(watchdogCtx)

	_ = r.sampleAndSync(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.scheduler.SetAdmission(false, ctx.Err())
			return ctx.Err()
		case <-ticker.C:
			_ = r.sampleAndSync(ctx)
		}
	}
}

// Start launches Run in the background and returns a stop function for the
// sandboxd composition root.
func (r *PressureRunner) Start(ctx context.Context) func() {
	runCtx, cancel := context.WithCancel(ctx)
	go func() {
		_ = r.Run(runCtx)
	}()
	return cancel
}

func (r *PressureRunner) sampleAndSync(ctx context.Context) error {
	sample, sampleErr := r.sampler.Sample(ctx)
	var decision PressureDecision
	var observeErr error
	if sampleErr == nil {
		decision, observeErr = r.pressure.Observe(sample)
		r.recordRefresh(sample.ObservedAt)
	}
	if decision.ShedLeaseID != "" {
		_ = r.reclaimer.ReclaimLease(
			ctx,
			decision.ShedLeaseID,
			TerminationResourceLimit,
		)
	}
	syncErr := r.readiness.SyncAdmission(ctx, r.scheduler)
	if sampleErr != nil {
		r.scheduler.SetAdmission(false, sampleErr)
	}
	return errors.Join(sampleErr, observeErr, syncErr)
}

func (r *PressureRunner) recordRefresh(observedAt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastRefresh = observedAt
}

func (r *PressureRunner) runWatchdog(ctx context.Context) {
	ticker := time.NewTicker(r.watchdog / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			last := r.lastRefresh
			r.mu.Unlock()
			if last.IsZero() || r.now().Sub(last) > r.watchdog {
				r.scheduler.SetAdmission(false, ErrPressureSamplingGap)
			}
		}
	}
}

// JournalPressureSampler combines host/cgroup probes with the durable lease
// journal to produce content-free pressure samples.
type JournalPressureSampler struct {
	Journal *Journal
	Probe   PressureHostProbe
	Now     func() time.Time
}

// PressureHostProbe reads the host memory and workload cgroup memory.
type PressureHostProbe interface {
	PressureHostSample(context.Context) (workloadMemoryBytes int64, hostMemAvailableBytes int64, err error)
}

func (s JournalPressureSampler) Sample(ctx context.Context) (PressureSample, error) {
	now := s.Now
	if now == nil {
		now = time.Now
	}
	if s.Journal == nil || s.Probe == nil || ctx == nil {
		return PressureSample{}, ErrInvalidPressureRunnerConfig
	}
	workload, host, err := s.Probe.PressureHostSample(ctx)
	if err != nil {
		return PressureSample{}, err
	}
	leases, err := s.Journal.ListLive(ctx, MaxJournalQueryLimit)
	if err != nil {
		return PressureSample{}, err
	}
	sample := PressureSample{
		ObservedAt:            now(),
		WorkloadMemoryBytes:   workload,
		HostMemAvailableBytes: host,
		Leases:                make([]PressureLease, 0, len(leases)),
	}
	for _, lease := range leases {
		switch lease.State {
		case LeaseCreating, LeaseReady, LeaseActive, LeaseOutputPersisting:
		default:
			continue
		}
		pressureLease := PressureLease{
			LeaseID:   lease.LeaseID,
			State:     lease.State,
			StartedAt: lease.CreatedAt,
		}
		if lease.State == LeaseOutputPersisting {
			pressureLease.PersistingSince = lease.UpdatedAt
		}
		sample.Leases = append(sample.Leases, pressureLease)
	}
	return sample, nil
}

// ReclaimLease is used only by sandboxd pressure control; peer-requested
// deletes still go through the authenticated RPC path.
func (s *JournalRPCService) ReclaimLease(
	ctx context.Context,
	leaseID string,
	reason TerminationReason,
) error {
	if s == nil || s.journal == nil || !safeRuntimeToken(leaseID) ||
		!IsValidTerminationReason(reason) {
		return ErrInvalidPressureRunnerConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lease, err := s.journal.GetLease(ctx, leaseID)
	if err != nil {
		return err
	}
	s.finishLease(
		lease,
		derivedRPCRequestID(lease.RequestID, "pressure-shed"),
		reason,
	)
	return nil
}

// ReclaimAllLiveLeases marks all non-terminal leases for durable recovery.
// Product DB/session/run/credit cleanup remains owned by numind-sandbox-reconcile.
func (s *JournalRPCService) ReclaimAllLiveLeases(
	ctx context.Context,
	reason TerminationReason,
) error {
	if s == nil || s.journal == nil || !IsValidTerminationReason(reason) {
		return ErrInvalidPressureRunnerConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	leases, err := s.journal.ListLive(ctx, MaxJournalQueryLimit)
	if err != nil {
		return err
	}
	var joined error
	for _, lease := range leases {
		if lease.State == LeaseTerminated {
			continue
		}
		if err := s.ReclaimLease(ctx, lease.LeaseID, reason); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

// MarkAllLiveRecoveryPending is the shutdown-facing name used by sandboxd.
func (s *JournalRPCService) MarkAllLiveRecoveryPending(
	ctx context.Context,
	reason TerminationReason,
) error {
	return s.ReclaimAllLiveLeases(ctx, reason)
}

// DrainScheduler waits for live and queued work to leave the fixed scheduler.
func DrainScheduler(
	ctx context.Context,
	scheduler *Scheduler,
	tick time.Duration,
) error {
	if ctx == nil || scheduler == nil {
		return ErrInvalidPressureRunnerConfig
	}
	if tick <= 0 {
		tick = 500 * time.Millisecond
	}
	timer := time.NewTicker(tick)
	defer timer.Stop()
	for {
		snapshot := scheduler.Snapshot()
		if snapshot.Containers == 0 && snapshot.Queued == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ErrSandboxDrainDeadline
		case <-timer.C:
		}
	}
}
