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
	// PressurePersistingGrace protects output upload before emergency recovery.
	PressurePersistingGrace = 10 * time.Second
	// PressureShedRetryInterval bounds repeated best-effort requests.
	PressureShedRetryInterval = 10 * time.Second

	// HostAdmissionMemoryBytes stops admission after three low samples.
	HostAdmissionMemoryBytes int64 = 3 * gibibyte / 2
	// HostEmergencyMemoryBytes stops admission and sheds on one sample.
	HostEmergencyMemoryBytes int64 = gibibyte

	pressureConsecutiveSamples = 3
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
}

// PressureController applies the three-sample hysteresis under one lock.
type PressureController struct {
	mu sync.Mutex

	workloadHigh     int64
	workloadRecovery int64
	workloadShed     int64

	admissionAllowed bool
	reason           PressureReason
	lastObservedAt   time.Time
	workloadHighRuns int
	workloadShedRuns int
	hostLowRuns      int
	recoveryRuns     int
	shedRequestedAt  map[string]time.Time
}

// NewPressureController derives thresholds only from a sealed capacity plan.
func NewPressureController(
	plan CapacityPlan,
) (*PressureController, error) {
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
		admissionAllowed: true,
		shedRequestedAt:  make(map[string]time.Time),
	}, nil
}

// AdmissionAllowed reports the current gate without exposing counters.
func (c *PressureController) AdmissionAllowed() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.admissionAllowed
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

	if err := validatePressureSample(sample); err != nil {
		c.stopAdmissionLocked(PressureReasonInvalidSample)
		return c.decisionLocked(""), err
	}
	if !c.lastObservedAt.IsZero() {
		gap := sample.ObservedAt.Sub(c.lastObservedAt)
		if gap <= 0 || gap < PressureSampleInterval ||
			gap > PressureMaximumSampleGap {
			if gap > 0 {
				c.lastObservedAt = sample.ObservedAt
			}
			c.resetRunsLocked()
			c.stopAdmissionLocked(PressureReasonSamplingGap)
			return c.decisionLocked(""), ErrPressureSamplingGap
		}
	}
	c.lastObservedAt = sample.ObservedAt

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
		shedLeaseID = c.selectShedLeaseLocked(sample)
	}
	return c.decisionLocked(shedLeaseID), nil
}

func validatePressureSample(sample PressureSample) error {
	if sample.ObservedAt.IsZero() ||
		sample.WorkloadMemoryBytes < 0 ||
		sample.WorkloadMemoryBytes > capacitySampleMaximum ||
		sample.HostMemAvailableBytes <= 0 ||
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

func (c *PressureController) selectShedLeaseLocked(
	sample PressureSample,
) string {
	live := make(map[string]struct{}, len(sample.Leases))
	for _, lease := range sample.Leases {
		live[lease.LeaseID] = struct{}{}
	}
	for leaseID := range c.shedRequestedAt {
		if _, ok := live[leaseID]; !ok {
			delete(c.shedRequestedAt, leaseID)
		}
	}

	candidate := PressureLease{}
	for _, lease := range sample.Leases {
		eligible := false
		switch lease.State {
		case LeaseCreating, LeaseReady, LeaseActive:
			eligible = true
		case LeaseOutputPersisting:
			eligible = sample.ObservedAt.Sub(lease.PersistingSince) >=
				PressurePersistingGrace
		}
		if !eligible {
			continue
		}
		if last, requested := c.shedRequestedAt[lease.LeaseID]; requested &&
			sample.ObservedAt.Sub(last) < PressureShedRetryInterval {
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
	c.shedRequestedAt[candidate.LeaseID] = sample.ObservedAt
	return candidate.LeaseID
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
