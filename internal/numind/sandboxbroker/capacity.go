package sandboxbroker

import (
	"errors"
	"sort"
	"time"
)

const (
	mebibyte int64 = 1024 * 1024
	gibibyte int64 = 1024 * mebibyte

	capacityHistoricalMinimum = 7 * 24 * time.Hour
	capacityFreshMinimum      = 72 * time.Hour
	capacityMaximumSampleGap  = time.Hour

	capacityParentPOCMax       = 11 * gibibyte / 4
	capacityCoreReserve        = 5 * gibibyte / 4
	capacityControlHigh        = 256 * mebibyte
	capacityControlReserve     = 384 * mebibyte
	capacityParentHeadroom     = 128 * mebibyte
	capacityParentMinimum      = 2 * gibibyte
	capacityWorkloadMinimum    = 3 * gibibyte / 2
	capacityWorkloadHighMax    = 2 * gibibyte
	capacityParentFloorQuantum = 64 * mebibyte
	capacitySampleMaximum      = 1 << 60
)

var (
	// ErrCapacityEvidenceInvalid means a sample or evidence declaration is malformed.
	ErrCapacityEvidenceInvalid = errors.New("invalid sandbox capacity evidence")
	// ErrCapacityEvidenceInsufficient means the evidence window is too short.
	ErrCapacityEvidenceInsufficient = errors.New("insufficient sandbox capacity evidence")
	// ErrCapacityInsufficient means this host cannot safely enable customer traffic.
	ErrCapacityInsufficient = errors.New("insufficient sandbox host capacity")
)

// CapacityEvidenceMode records whether the input is established history or a
// deliberate new sampling window.
type CapacityEvidenceMode string

const (
	// CapacityEvidenceHistorical requires at least seven days of evidence.
	CapacityEvidenceHistorical CapacityEvidenceMode = "historical"
	// CapacityEvidenceFreshSampling requires an explicit 72-hour sampling window.
	CapacityEvidenceFreshSampling CapacityEvidenceMode = "fresh"
)

// CapacitySample is one host MemAvailable observation. BusinessWindow must be
// true only for samples from the same product traffic period used for the P1.
type CapacitySample struct {
	ObservedAt        time.Time
	MemAvailableBytes int64
	BusinessWindow    bool
}

// CapacityEvidence is the complete bounded input to the production formula.
type CapacityEvidence struct {
	Mode    CapacityEvidenceMode
	Samples []CapacitySample
}

// CapacityPlan is the exact set of values later written to systemd units.
type CapacityPlan struct {
	EvidenceMode          CapacityEvidenceMode
	EvidenceStart         time.Time
	EvidenceEnd           time.Time
	EvidenceDuration      time.Duration
	TotalSamples          int
	BusinessSamples       int
	BaselineBytes         int64
	ParentMaxBytes        int64
	WorkloadMaxBytes      int64
	WorkloadHighBytes     int64
	WorkloadRecoveryBytes int64
	WorkloadShedBytes     int64
	ControlHighBytes      int64
	ControlMaxBytes       int64
	ParentHeadroomBytes   int64
}

// CalculateSandboxCapacity validates evidence and derives all production
// ceilings. It returns the calculated values with ErrCapacityInsufficient so a
// release report can explain a low-capacity block without enabling traffic.
func CalculateSandboxCapacity(evidence CapacityEvidence) (CapacityPlan, error) {
	minimumDuration, ok := capacityMinimumDuration(evidence.Mode)
	if !ok || len(evidence.Samples) < 2 {
		return CapacityPlan{}, ErrCapacityEvidenceInvalid
	}
	samples := append([]CapacitySample(nil), evidence.Samples...)
	sort.Slice(samples, func(left int, right int) bool {
		return samples[left].ObservedAt.Before(samples[right].ObservedAt)
	})
	businessValues := make([]int64, 0, len(samples))
	for index, sample := range samples {
		if sample.ObservedAt.IsZero() ||
			sample.MemAvailableBytes <= 0 ||
			sample.MemAvailableBytes > capacitySampleMaximum {
			return CapacityPlan{}, ErrCapacityEvidenceInvalid
		}
		if index > 0 &&
			!sample.ObservedAt.After(samples[index-1].ObservedAt) {
			return CapacityPlan{}, ErrCapacityEvidenceInvalid
		}
		if index > 0 &&
			sample.ObservedAt.Sub(samples[index-1].ObservedAt) >
				capacityMaximumSampleGap {
			return CapacityPlan{}, ErrCapacityEvidenceInsufficient
		}
		if sample.BusinessWindow {
			businessValues = append(
				businessValues,
				sample.MemAvailableBytes,
			)
		}
	}
	duration := samples[len(samples)-1].ObservedAt.Sub(samples[0].ObservedAt)
	if duration < minimumDuration || len(businessValues) < 2 {
		return CapacityPlan{}, ErrCapacityEvidenceInsufficient
	}

	baseline := memAvailableP1(businessValues)
	parentCandidate := baseline - capacityCoreReserve
	if parentCandidate < 0 {
		parentCandidate = 0
	}
	if parentCandidate > capacityParentPOCMax {
		parentCandidate = capacityParentPOCMax
	}
	parentMax := floorBytes(parentCandidate, capacityParentFloorQuantum)
	workloadMax := parentMax - capacityControlReserve - capacityParentHeadroom
	if workloadMax < 0 {
		workloadMax = 0
	}
	workloadHigh := workloadMax * 90 / 100
	if workloadHigh > capacityWorkloadHighMax {
		workloadHigh = capacityWorkloadHighMax
	}
	plan := CapacityPlan{
		EvidenceMode:          evidence.Mode,
		EvidenceStart:         samples[0].ObservedAt,
		EvidenceEnd:           samples[len(samples)-1].ObservedAt,
		EvidenceDuration:      duration,
		TotalSamples:          len(samples),
		BusinessSamples:       len(businessValues),
		BaselineBytes:         baseline,
		ParentMaxBytes:        parentMax,
		WorkloadMaxBytes:      workloadMax,
		WorkloadHighBytes:     workloadHigh,
		WorkloadRecoveryBytes: workloadMax * 80 / 100,
		WorkloadShedBytes:     workloadMax * 96 / 100,
		ControlHighBytes:      capacityControlHigh,
		ControlMaxBytes:       capacityControlReserve,
		ParentHeadroomBytes:   capacityParentHeadroom,
	}
	if parentMax < capacityParentMinimum ||
		workloadMax < capacityWorkloadMinimum {
		return plan, ErrCapacityInsufficient
	}
	return plan, nil
}

// SystemdValues returns only fixed low-cardinality byte ceilings.
func (p CapacityPlan) SystemdValues() map[string]int64 {
	return map[string]int64{
		"NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES":        p.ParentMaxBytes,
		"NUMIND_SANDBOX_WORKLOAD_MEMORY_MAX_BYTES":      p.WorkloadMaxBytes,
		"NUMIND_SANDBOX_WORKLOAD_MEMORY_HIGH_BYTES":     p.WorkloadHighBytes,
		"NUMIND_SANDBOX_WORKLOAD_MEMORY_RECOVERY_BYTES": p.WorkloadRecoveryBytes,
		"NUMIND_SANDBOX_WORKLOAD_MEMORY_SHED_BYTES":     p.WorkloadShedBytes,
		"NUMIND_SANDBOX_CONTROL_MEMORY_HIGH_BYTES":      p.ControlHighBytes,
		"NUMIND_SANDBOX_CONTROL_MEMORY_MAX_BYTES":       p.ControlMaxBytes,
		"NUMIND_SANDBOX_PARENT_HEADROOM_BYTES":          p.ParentHeadroomBytes,
	}
}

func capacityMinimumDuration(
	mode CapacityEvidenceMode,
) (time.Duration, bool) {
	switch mode {
	case CapacityEvidenceHistorical:
		return capacityHistoricalMinimum, true
	case CapacityEvidenceFreshSampling:
		return capacityFreshMinimum, true
	default:
		return 0, false
	}
}

func memAvailableP1(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(left int, right int) bool {
		return sorted[left] < sorted[right]
	})
	rank := (len(sorted) + 99) / 100
	return sorted[rank-1]
}

func floorBytes(value int64, quantum int64) int64 {
	if value <= 0 || quantum <= 0 {
		return 0
	}
	return value / quantum * quantum
}
