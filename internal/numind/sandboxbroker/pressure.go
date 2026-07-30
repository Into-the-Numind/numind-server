package sandboxbroker

import (
	"errors"
	"sync"
	"time"
)

const (
	// PressureSampleInterval is the required broker sampling cadence.
	PressureSampleInterval = 2 * time.Second
	// PressureMaximumSampleGap is the largest gap that remains consecutive.
	PressureMaximumSampleGap = 4 * time.Second
	// PressureMinimumSampleGap tolerates early ticker jitter but rejects bursts.
	PressureMinimumSampleGap = 3 * PressureSampleInterval / 4
	// PressurePersistingGrace protects output upload before emergency recovery.
	PressurePersistingGrace = 10 * time.Second
	// PressureShedRetryInterval bounds repeated best-effort requests.
	PressureShedRetryInterval = 10 * time.Second

	// HostAdmissionMemoryBytes stops admission after three low samples.
	HostAdmissionMemoryBytes int64 = 3 * gibibyte / 2
	// HostEmergencyMemoryBytes stops admission and sheds on one sample.
	HostEmergencyMemoryBytes int64 = gibibyte

	pressureConsecutiveSamples = 3
	pressureLeaseHistoryMax    = SchedulerMaxReplayRecords
)

var (
	// ErrInvalidPressureConfig means the capacity plan is not release-ready.
	ErrInvalidPressureConfig = errors.New("invalid sandbox pressure config")
	// ErrInvalidPressureSample means metrics or lease state is malformed.
	ErrInvalidPressureSample = errors.New("invalid sandbox pressure sample")
	// ErrPressureSamplingGap means pressure monitoring is no longer continuous.
	ErrPressureSamplingGap = errors.New("sandbox pressure sampling gap")
)

// PressureReason is a bounded content-free admission transition code.
type PressureReason string

const (
	PressureReasonNone          PressureReason = ""
	PressureReasonWorkloadHigh  PressureReason = "workload_high"
	PressureReasonWorkloadShed  PressureReason = "workload_shed"
	PressureReasonHostLow       PressureReason = "host_low"
	PressureReasonHostEmergency PressureReason = "host_emergency"
	PressureReasonSamplingGap   PressureReason = "sampling_gap"
	PressureReasonInvalidSample PressureReason = "invalid_sample"
	PressureReasonRecovered     PressureReason = "recovered"
)

// PressureLease is the minimum content-free lifecycle view used for shedding.
type PressureLease struct {
	LeaseID         string
	State           LeaseState
	StartedAt       time.Time
	PersistingSince time.Time
}

// PressureSample contains one trusted host/cgroup observation.
type PressureSample struct {
	ObservedAt            time.Time
	WorkloadMemoryBytes   int64
	HostMemAvailableBytes int64
	Leases                []PressureLease
}

// PressureDecision tells the caller whether to admit and which one lease to
// best-effort end. Repeated shed requests are rate-limited per lease.
type PressureDecision struct {
	AdmissionAllowed bool
	Reason           PressureReason
	ShedLeaseID      string
	SamplingGap      bool
}

type pressureLeaseHistory struct {
	StartedAt                 time.Time
	FirstPersistingAt         time.Time
	FirstPersistingReceivedAt time.Time
	LastSeenAt                time.Time
	LastState                 LeaseState
}

// PressureController applies the three-sample hysteresis under one lock.
type PressureController struct {
	mu sync.Mutex

	workloadHigh     int64
	workloadRecovery int64
	workloadShed     int64
	now              func() time.Time

	admissionAllowed bool
	reason           PressureReason
	lastObservedAt   time.Time
	lastReceivedAt   time.Time
	workloadHighRuns int
	workloadShedRuns int
	hostLowRuns      int
	recoveryRuns     int
	shedRequestedAt  map[string]time.Time
	leaseHistory     map[string]pressureLeaseHistory
	generation       uint64
}

// NewPressureController derives thresholds only from a sealed capacity plan.
func NewPressureController(
	plan CapacityPlan,
) (*PressureController, error) {
	return newPressureControllerWithClock(plan, time.Now)
}

func newPressureControllerWithClock(
	plan CapacityPlan,
	now func() time.Time,
) (*PressureController, error) {
	if now == nil {
		return nil, ErrInvalidPressureConfig
	}
	values, err := plan.SystemdValues()
	if err != nil {
		return nil, ErrInvalidPressureConfig
	}
	high := values["NUMIND_SANDBOX_WORKLOAD_MEMORY_HIGH_BYTES"]
	recovery := values["NUMIND_SANDBOX_WORKLOAD_MEMORY_RECOVERY_BYTES"]
	shed := values["NUMIND_SANDBOX_WORKLOAD_MEMORY_SHED_BYTES"]
	if high <= 0 || recovery <= 0 || shed <= 0 ||
		recovery >= high || high >= shed {
		return nil, ErrInvalidPressureConfig
	}
	return &PressureController{
		workloadHigh:     high,
		workloadRecovery: recovery,
		workloadShed:     shed,
		now:              now,
		reason:           PressureReasonSamplingGap,
		shedRequestedAt:  make(map[string]time.Time),
		leaseHistory:     make(map[string]pressureLeaseHistory),
	}, nil
}

// AdmissionAllowed fails closed if the trusted sampler has never reported or
// has stopped reporting, even when no later Observe call arrives.
func (c *PressureController) AdmissionAllowed() bool {
	allowed, _, _ := c.AdmissionStatus()
	return allowed
}

// AdmissionStatus returns the fresh gate and its bounded classification.
func (c *PressureController) AdmissionStatus() (
	bool,
	PressureReason,
	uint64,
) {
	if c == nil {
		return false, PressureReasonInvalidSample, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshFreshnessLocked()
	return c.admissionAllowed, c.reason, c.generation
}

// PublishAdmissionIfCurrent holds the pressure lock while publishing. A sample
// cannot invalidate the checked generation between confirmation and scheduler
// publication.
func (c *PressureController) PublishAdmissionIfCurrent(
	expectedGeneration uint64,
	publish func(bool, PressureReason),
) bool {
	if c == nil || publish == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshFreshnessLocked()
	if c.generation != expectedGeneration {
		return false
	}
	publish(c.admissionAllowed, c.reason)
	return true
}

func (c *PressureController) refreshFreshnessLocked() {
	now := c.now()
	if c.lastReceivedAt.IsZero() ||
		now.Before(c.lastReceivedAt) ||
		now.Sub(c.lastReceivedAt) > PressureMaximumSampleGap {
		changed := c.admissionAllowed ||
			c.reason != PressureReasonSamplingGap
		c.resetRunsLocked()
		c.stopAdmissionLocked(PressureReasonSamplingGap)
		if changed {
			c.bumpGenerationLocked()
		}
	}
}

// Observe applies one consecutive pressure sample.
func (c *PressureController) Observe(
	sample PressureSample,
) (PressureDecision, error) {
	if c == nil {
		return PressureDecision{}, ErrInvalidPressureConfig
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bumpGenerationLocked()

	if err := validatePressureSample(sample); err != nil {
		c.stopAdmissionLocked(PressureReasonInvalidSample)
		return c.decisionLocked(""), err
	}
	receivedAt := c.now()
	if receivedAt.IsZero() {
		c.stopAdmissionLocked(PressureReasonInvalidSample)
		return c.decisionLocked(""), ErrInvalidPressureSample
	}

	sampleReceiptDelta := sample.ObservedAt.Sub(receivedAt)
	gapDetected := sampleReceiptDelta > PressureMaximumSampleGap ||
		sampleReceiptDelta < -PressureMaximumSampleGap
	if !c.lastObservedAt.IsZero() {
		observedGap := sample.ObservedAt.Sub(c.lastObservedAt)
		receivedGap := receivedAt.Sub(c.lastReceivedAt)
		gapDetected = gapDetected ||
			observedGap <= 0 ||
			observedGap < PressureMinimumSampleGap ||
			observedGap > PressureMaximumSampleGap ||
			receivedGap <= 0 ||
			receivedGap < PressureMinimumSampleGap ||
			receivedGap > PressureMaximumSampleGap
	}
	if err := c.validateLeaseContinuityLocked(sample, receivedAt); err != nil {
		c.stopAdmissionLocked(PressureReasonInvalidSample)
		return c.decisionLocked(""), err
	}
	c.lastObservedAt = sample.ObservedAt
	c.lastReceivedAt = receivedAt

	if gapDetected {
		c.resetRunsLocked()
		c.stopAdmissionLocked(PressureReasonSamplingGap)
		if sample.HostMemAvailableBytes < HostEmergencyMemoryBytes {
			c.reason = PressureReasonHostEmergency
			decision := c.decisionLocked(
				c.selectShedLeaseLocked(sample, receivedAt),
			)
			decision.SamplingGap = true
			return decision, nil
		}
		return c.decisionLocked(""), ErrPressureSamplingGap
	}

	c.workloadHighRuns = nextPressureRun(
		c.workloadHighRuns,
		sample.WorkloadMemoryBytes >= c.workloadHigh,
	)
	c.workloadShedRuns = nextPressureRun(
		c.workloadShedRuns,
		sample.WorkloadMemoryBytes >= c.workloadShed,
	)
	c.hostLowRuns = nextPressureRun(
		c.hostLowRuns,
		sample.HostMemAvailableBytes < HostAdmissionMemoryBytes,
	)

	emergency := sample.HostMemAvailableBytes < HostEmergencyMemoryBytes
	shouldShed := emergency ||
		c.workloadShedRuns >= pressureConsecutiveSamples
	switch {
	case emergency:
		c.stopAdmissionLocked(PressureReasonHostEmergency)
	case c.workloadShedRuns >= pressureConsecutiveSamples:
		c.stopAdmissionLocked(PressureReasonWorkloadShed)
	case c.workloadHighRuns >= pressureConsecutiveSamples:
		c.stopAdmissionLocked(PressureReasonWorkloadHigh)
	case c.hostLowRuns >= pressureConsecutiveSamples:
		c.stopAdmissionLocked(PressureReasonHostLow)
	}

	healthy := sample.WorkloadMemoryBytes < c.workloadRecovery &&
		sample.HostMemAvailableBytes >= HostAdmissionMemoryBytes
	if !c.admissionAllowed && healthy {
		c.recoveryRuns = nextPressureRun(c.recoveryRuns, true)
		if c.recoveryRuns >= pressureConsecutiveSamples {
			c.admissionAllowed = true
			c.reason = PressureReasonRecovered
			c.resetRunsLocked()
		}
	} else {
		c.recoveryRuns = 0
	}

	shedLeaseID := ""
	if shouldShed {
		shedLeaseID = c.selectShedLeaseLocked(sample, receivedAt)
	}
	return c.decisionLocked(shedLeaseID), nil
}

func validatePressureSample(sample PressureSample) error {
	if sample.ObservedAt.IsZero() ||
		sample.WorkloadMemoryBytes < 0 ||
		sample.WorkloadMemoryBytes > capacitySampleMaximum ||
		sample.HostMemAvailableBytes < 0 ||
		sample.HostMemAvailableBytes > capacitySampleMaximum ||
		len(sample.Leases) > SchedulerTotalContainerMax {
		return ErrInvalidPressureSample
	}
	seen := make(map[string]struct{}, len(sample.Leases))
	for _, lease := range sample.Leases {
		if !safeRuntimeToken(lease.LeaseID) ||
			lease.StartedAt.IsZero() ||
			lease.StartedAt.After(sample.ObservedAt) {
			return ErrInvalidPressureSample
		}
		if _, duplicate := seen[lease.LeaseID]; duplicate {
			return ErrInvalidPressureSample
		}
		seen[lease.LeaseID] = struct{}{}
		switch lease.State {
		case LeaseCreating, LeaseReady, LeaseActive:
			if !lease.PersistingSince.IsZero() {
				return ErrInvalidPressureSample
			}
		case LeaseOutputPersisting:
			if lease.PersistingSince.IsZero() ||
				lease.PersistingSince.Before(lease.StartedAt) ||
				lease.PersistingSince.After(sample.ObservedAt) {
				return ErrInvalidPressureSample
			}
		case LeaseDestroying, LeaseTerminated, LeaseRecoveryPending:
		default:
			return ErrInvalidPressureSample
		}
	}
	return nil
}

func (c *PressureController) validateLeaseContinuityLocked(
	sample PressureSample,
	receivedAt time.Time,
) error {
	current := make(map[string]struct{}, len(sample.Leases))
	updates := make(map[string]pressureLeaseHistory, len(sample.Leases))
	newHistories := 0
	for _, lease := range sample.Leases {
		current[lease.LeaseID] = struct{}{}
		previous, seen := c.leaseHistory[lease.LeaseID]
		if seen {
			if !lease.StartedAt.Equal(previous.StartedAt) ||
				pressureLeaseStateRank(lease.State) <
					pressureLeaseStateRank(previous.LastState) {
				return ErrInvalidPressureSample
			}
			if !previous.FirstPersistingAt.IsZero() {
				switch lease.State {
				case LeaseOutputPersisting:
					if !lease.PersistingSince.Equal(
						previous.FirstPersistingAt,
					) {
						return ErrInvalidPressureSample
					}
				case LeaseCreating, LeaseReady, LeaseActive:
					return ErrInvalidPressureSample
				}
			}
		} else {
			newHistories++
		}

		history := previous
		history.StartedAt = lease.StartedAt
		history.LastState = lease.State
		history.LastSeenAt = receivedAt
		if lease.State == LeaseOutputPersisting &&
			history.FirstPersistingAt.IsZero() {
			history.FirstPersistingAt = lease.PersistingSince
			history.FirstPersistingReceivedAt = receivedAt.Add(
				lease.PersistingSince.Sub(sample.ObservedAt),
			)
		}
		updates[lease.LeaseID] = history
	}
	for len(c.leaseHistory)+newHistories > pressureLeaseHistoryMax {
		if !c.evictOldestLeaseHistoryLocked(current) {
			return ErrInvalidPressureSample
		}
	}
	for leaseID, history := range updates {
		c.leaseHistory[leaseID] = history
	}
	return nil
}

func (c *PressureController) evictOldestLeaseHistoryLocked(
	current map[string]struct{},
) bool {
	oldestID := ""
	var oldestAt time.Time
	for leaseID, history := range c.leaseHistory {
		if _, live := current[leaseID]; live {
			continue
		}
		if oldestID == "" || history.LastSeenAt.Before(oldestAt) {
			oldestID = leaseID
			oldestAt = history.LastSeenAt
		}
	}
	if oldestID != "" {
		delete(c.leaseHistory, oldestID)
		delete(c.shedRequestedAt, oldestID)
		return true
	}
	return false
}

func pressureLeaseStateRank(state LeaseState) int {
	switch state {
	case LeaseCreating:
		return 1
	case LeaseReady:
		return 2
	case LeaseActive:
		return 3
	case LeaseOutputPersisting:
		return 4
	case LeaseDestroying, LeaseRecoveryPending:
		return 5
	case LeaseTerminated:
		return 6
	default:
		return 0
	}
}

func (c *PressureController) selectShedLeaseLocked(
	sample PressureSample,
	receivedAt time.Time,
) string {
	candidate := PressureLease{}
	for _, lease := range sample.Leases {
		eligible := false
		switch lease.State {
		case LeaseCreating, LeaseReady, LeaseActive:
			eligible = true
		case LeaseOutputPersisting:
			history := c.leaseHistory[lease.LeaseID]
			eligible = !history.FirstPersistingReceivedAt.IsZero() &&
				receivedAt.Sub(history.FirstPersistingReceivedAt) >=
					PressurePersistingGrace
		}
		if !eligible {
			continue
		}
		if last, requested := c.shedRequestedAt[lease.LeaseID]; requested &&
			receivedAt.Sub(last) < PressureShedRetryInterval {
			continue
		}
		if candidate.LeaseID == "" ||
			lease.StartedAt.After(candidate.StartedAt) ||
			(lease.StartedAt.Equal(candidate.StartedAt) &&
				lease.LeaseID > candidate.LeaseID) {
			candidate = lease
		}
	}
	if candidate.LeaseID == "" {
		return ""
	}
	c.shedRequestedAt[candidate.LeaseID] = receivedAt
	return candidate.LeaseID
}

func (c *PressureController) bumpGenerationLocked() {
	c.generation++
	if c.generation == 0 {
		c.generation = 1
	}
}

func (c *PressureController) stopAdmissionLocked(reason PressureReason) {
	c.admissionAllowed = false
	c.reason = reason
	c.recoveryRuns = 0
}

func (c *PressureController) resetRunsLocked() {
	c.workloadHighRuns = 0
	c.workloadShedRuns = 0
	c.hostLowRuns = 0
	c.recoveryRuns = 0
}

func (c *PressureController) decisionLocked(
	shedLeaseID string,
) PressureDecision {
	return PressureDecision{
		AdmissionAllowed: c.admissionAllowed,
		Reason:           c.reason,
		ShedLeaseID:      shedLeaseID,
	}
}

func nextPressureRun(current int, matched bool) int {
	if !matched {
		return 0
	}
	if current >= pressureConsecutiveSamples {
		return pressureConsecutiveSamples
	}
	return current + 1
}
