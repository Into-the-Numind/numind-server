package sandboxbroker

import (
	"errors"
	"sort"
	"time"
)

const (
	mebibyte int64 = 1024 * 1024
	gibibyte int64 = 1024 * mebibyte

	capacityHistoricalMinimum  = 7 * 24 * time.Hour
	capacityFreshMinimum       = 72 * time.Hour
	capacityMaximumSampleGap   = time.Hour
	capacityMaximumBusinessGap = 24 * time.Hour
	capacityMaximumEvidenceAge = time.Hour
	capacityMaximumFutureSkew  = 5 * time.Minute
	capacityBusinessDivisor    = 6
	capacityMaximumSamples     = 1_000_000
	capacityBusinessStartHour  = 8
	capacityBusinessEndHour    = 23

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
	ErrCapacityInsufficient  = errors.New("insufficient sandbox host capacity")
	capacityBusinessTimezone = time.FixedZone("Asia/Shanghai", 8*60*60)
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

// CapacitySample is one host MemAvailable observation. BusinessWindow must
// match the fixed 08:00-23:00 Asia/Shanghai window derived from ObservedAt.
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
	ready                 bool
	seal                  capacityPlanSeal
}

type capacityPlanSeal struct {
	baselineBytes         int64
	parentMaxBytes        int64
	workloadMaxBytes      int64
	workloadHighBytes     int64
	workloadRecoveryBytes int64
	workloadShedBytes     int64
	controlHighBytes      int64
	controlMaxBytes       int64
	parentHeadroomBytes   int64
}

// StaticCapacityPlanConfig is the already-reviewed production capacity plan
// read by sandboxd from its own non-business config at startup.
type StaticCapacityPlanConfig struct {
	EvidenceMode          CapacityEvidenceMode
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
	return calculateSandboxCapacityAt(evidence, time.Now().UTC())
}

// NewStaticCapacityPlan seals a production capacity plan without re-reading
// business traffic or product secrets inside sandboxd.
func NewStaticCapacityPlan(config StaticCapacityPlanConfig) (CapacityPlan, error) {
	if _, ok := capacityMinimumDuration(config.EvidenceMode); !ok {
		return CapacityPlan{}, ErrCapacityEvidenceInvalid
	}
	plan := CapacityPlan{
		EvidenceMode:          config.EvidenceMode,
		BaselineBytes:         config.BaselineBytes,
		ParentMaxBytes:        config.ParentMaxBytes,
		WorkloadMaxBytes:      config.WorkloadMaxBytes,
		WorkloadHighBytes:     config.WorkloadHighBytes,
		WorkloadRecoveryBytes: config.WorkloadRecoveryBytes,
		WorkloadShedBytes:     config.WorkloadShedBytes,
		ControlHighBytes:      config.ControlHighBytes,
		ControlMaxBytes:       config.ControlMaxBytes,
		ParentHeadroomBytes:   config.ParentHeadroomBytes,
		ready:                 true,
		seal: capacityPlanSeal{
			baselineBytes:         config.BaselineBytes,
			parentMaxBytes:        config.ParentMaxBytes,
			workloadMaxBytes:      config.WorkloadMaxBytes,
			workloadHighBytes:     config.WorkloadHighBytes,
			workloadRecoveryBytes: config.WorkloadRecoveryBytes,
			workloadShedBytes:     config.WorkloadShedBytes,
			controlHighBytes:      config.ControlHighBytes,
			controlMaxBytes:       config.ControlMaxBytes,
			parentHeadroomBytes:   config.ParentHeadroomBytes,
		},
	}
	if !validReadyCapacityPlan(plan) {
		return CapacityPlan{}, ErrCapacityInsufficient
	}
	return plan, nil
}

func calculateSandboxCapacityAt(
	evidence CapacityEvidence,
	evaluatedAt time.Time,
) (CapacityPlan, error) {
	minimumDuration, ok := capacityMinimumDuration(evidence.Mode)
	if !ok ||
		evaluatedAt.IsZero() ||
		len(evidence.Samples) < 2 ||
		len(evidence.Samples) > capacityMaximumSamples {
		return CapacityPlan{}, ErrCapacityEvidenceInvalid
	}
	samples := append([]CapacitySample(nil), evidence.Samples...)
	sort.Slice(samples, func(left int, right int) bool {
		return samples[left].ObservedAt.Before(samples[right].ObservedAt)
	})
	businessValues := make([]int64, 0, len(samples))
	businessTimes := make([]time.Time, 0, len(samples))
	for index, sample := range samples {
		if sample.ObservedAt.IsZero() ||
			sample.MemAvailableBytes <= 0 ||
			sample.MemAvailableBytes > capacitySampleMaximum ||
			sample.ObservedAt.After(
				evaluatedAt.Add(capacityMaximumFutureSkew),
			) ||
			sample.BusinessWindow !=
				isCapacityBusinessWindow(sample.ObservedAt) {
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
			businessTimes = append(businessTimes, sample.ObservedAt)
		}
	}
	duration := samples[len(samples)-1].ObservedAt.Sub(samples[0].ObservedAt)
	if evaluatedAt.Sub(samples[len(samples)-1].ObservedAt) >
		capacityMaximumEvidenceAge {
		return CapacityPlan{}, ErrCapacityEvidenceInsufficient
	}
	if duration < minimumDuration ||
		len(businessValues)*capacityBusinessDivisor < len(samples) ||
		!businessEvidenceCoversWindow(
			samples[0].ObservedAt,
			samples[len(samples)-1].ObservedAt,
			businessTimes,
		) {
		return CapacityPlan{}, ErrCapacityEvidenceInsufficient
	}

	baseline := memAvailableP1(businessValues)
	parentMax := parentMaxFromBaseline(baseline)
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
	plan.seal = capacityPlanSeal{
		baselineBytes:         plan.BaselineBytes,
		parentMaxBytes:        plan.ParentMaxBytes,
		workloadMaxBytes:      plan.WorkloadMaxBytes,
		workloadHighBytes:     plan.WorkloadHighBytes,
		workloadRecoveryBytes: plan.WorkloadRecoveryBytes,
		workloadShedBytes:     plan.WorkloadShedBytes,
		controlHighBytes:      plan.ControlHighBytes,
		controlMaxBytes:       plan.ControlMaxBytes,
		parentHeadroomBytes:   plan.ParentHeadroomBytes,
	}
	plan.ready = true
	return plan, nil
}

// SystemdValues returns only fixed low-cardinality byte ceilings.
func (p CapacityPlan) SystemdValues() (map[string]int64, error) {
	if !validReadyCapacityPlan(p) {
		return nil, ErrCapacityInsufficient
	}
	return map[string]int64{
		"NUMIND_SANDBOX_PARENT_MEMORY_MAX_BYTES":        p.seal.parentMaxBytes,
		"NUMIND_SANDBOX_WORKLOAD_MEMORY_MAX_BYTES":      p.seal.workloadMaxBytes,
		"NUMIND_SANDBOX_WORKLOAD_MEMORY_HIGH_BYTES":     p.seal.workloadHighBytes,
		"NUMIND_SANDBOX_WORKLOAD_MEMORY_RECOVERY_BYTES": p.seal.workloadRecoveryBytes,
		"NUMIND_SANDBOX_WORKLOAD_MEMORY_SHED_BYTES":     p.seal.workloadShedBytes,
		"NUMIND_SANDBOX_CONTROL_MEMORY_HIGH_BYTES":      p.seal.controlHighBytes,
		"NUMIND_SANDBOX_CONTROL_MEMORY_MAX_BYTES":       p.seal.controlMaxBytes,
		"NUMIND_SANDBOX_PARENT_HEADROOM_BYTES":          p.seal.parentHeadroomBytes,
	}, nil
}

func validReadyCapacityPlan(plan CapacityPlan) bool {
	expectedParent := parentMaxFromBaseline(plan.seal.baselineBytes)
	expectedWorkload := expectedParent -
		capacityControlReserve -
		capacityParentHeadroom
	expectedHigh := expectedWorkload * 90 / 100
	if expectedHigh > capacityWorkloadHighMax {
		expectedHigh = capacityWorkloadHighMax
	}
	return plan.ready &&
		expectedParent >= capacityParentMinimum &&
		expectedParent <= capacityParentPOCMax &&
		expectedParent%capacityParentFloorQuantum == 0 &&
		expectedWorkload >= capacityWorkloadMinimum &&
		plan.seal.parentMaxBytes == expectedParent &&
		plan.seal.workloadMaxBytes == expectedWorkload &&
		plan.seal.workloadHighBytes == expectedHigh &&
		plan.seal.workloadRecoveryBytes == expectedWorkload*80/100 &&
		plan.seal.workloadShedBytes == expectedWorkload*96/100 &&
		plan.seal.controlHighBytes == capacityControlHigh &&
		plan.seal.controlMaxBytes == capacityControlReserve &&
		plan.seal.parentHeadroomBytes == capacityParentHeadroom &&
		plan.BaselineBytes == plan.seal.baselineBytes &&
		plan.ParentMaxBytes == plan.seal.parentMaxBytes &&
		plan.WorkloadMaxBytes == plan.seal.workloadMaxBytes &&
		plan.WorkloadHighBytes == plan.seal.workloadHighBytes &&
		plan.WorkloadRecoveryBytes == plan.seal.workloadRecoveryBytes &&
		plan.WorkloadShedBytes == plan.seal.workloadShedBytes &&
		plan.ControlHighBytes == plan.seal.controlHighBytes &&
		plan.ControlMaxBytes == plan.seal.controlMaxBytes &&
		plan.ParentHeadroomBytes == plan.seal.parentHeadroomBytes
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

func businessEvidenceCoversWindow(
	start time.Time,
	end time.Time,
	businessTimes []time.Time,
) bool {
	if len(businessTimes) < 2 ||
		businessTimes[0].Sub(start) > capacityMaximumBusinessGap ||
		end.Sub(businessTimes[len(businessTimes)-1]) >
			capacityMaximumBusinessGap {
		return false
	}
	for index := 1; index < len(businessTimes); index++ {
		if businessTimes[index].Sub(businessTimes[index-1]) >
			capacityMaximumBusinessGap {
			return false
		}
	}
	return true
}

func isCapacityBusinessWindow(observedAt time.Time) bool {
	hour := observedAt.In(capacityBusinessTimezone).Hour()
	return hour >= capacityBusinessStartHour &&
		hour < capacityBusinessEndHour
}

func floorBytes(value int64, quantum int64) int64 {
	if value <= 0 || quantum <= 0 {
		return 0
	}
	return value / quantum * quantum
}

func parentMaxFromBaseline(baseline int64) int64 {
	parentCandidate := baseline - capacityCoreReserve
	if parentCandidate < 0 {
		parentCandidate = 0
	}
	if parentCandidate > capacityParentPOCMax {
		parentCandidate = capacityParentPOCMax
	}
	return floorBytes(parentCandidate, capacityParentFloorQuantum)
}
