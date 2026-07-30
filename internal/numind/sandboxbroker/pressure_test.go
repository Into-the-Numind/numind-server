package sandboxbroker

import (
	"errors"
	"testing"
	"time"
)

type pressureTestClock struct {
	now time.Time
}

func (c *pressureTestClock) Now() time.Time {
	return c.now
}

func TestPressureStartsClosedRequiresThreeSamplesAndHasWatchdog(
	t *testing.T,
) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	controller, clock := newTestPressureController(t, plan, start)
	if controller.AdmissionAllowed() {
		t.Fatal("controller admitted before the first trusted sample")
	}

	var decision PressureDecision
	for index := 0; index < pressureConsecutiveSamples; index++ {
		sample := healthyPressureSample(
			plan,
			start.Add(time.Duration(index)*PressureSampleInterval),
		)
		decision = observePressureAt(t, controller, clock, sample)
		if index < pressureConsecutiveSamples-1 &&
			decision.AdmissionAllowed {
			t.Fatalf("controller admitted after only %d samples", index+1)
		}
	}
	if !decision.AdmissionAllowed {
		t.Fatalf("controller did not open after trusted recovery: %#v", decision)
	}

	clock.now = clock.now.Add(PressureMaximumSampleGap + time.Nanosecond)
	if controller.AdmissionAllowed() {
		t.Fatal("controller stayed open after the sampler stopped")
	}
}

func TestPressureRejectsSampleTimestampOutsideTrustedReceiptWindow(
	t *testing.T,
) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	controller, clock := newTestPressureController(t, plan, start)
	clock.now = start
	decision, err := controller.Observe(healthyPressureSample(
		plan,
		start.Add(PressureMaximumSampleGap+time.Second),
	))
	if !errors.Is(err, ErrPressureSamplingGap) ||
		decision.AdmissionAllowed {
		t.Fatalf("future sample decision=%#v error=%v", decision, err)
	}
}

func TestPressureStopsAfterThreeHighSamplesAndRecoversWithHysteresis(
	t *testing.T,
) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	controller, clock, next := readyPressureControllerForTest(
		t,
		plan,
		start,
	)

	var decision PressureDecision
	for index := 0; index < pressureConsecutiveSamples; index++ {
		sample := PressureSample{
			ObservedAt: next.Add(
				time.Duration(index) * PressureSampleInterval,
			),
			WorkloadMemoryBytes:   plan.WorkloadHighBytes,
			HostMemAvailableBytes: 2 * gibibyte,
		}
		decision = observePressureAt(t, controller, clock, sample)
		if index < pressureConsecutiveSamples-1 &&
			!decision.AdmissionAllowed {
			t.Fatalf("high sample %d stopped early: %#v", index, decision)
		}
	}
	if decision.AdmissionAllowed ||
		decision.Reason != PressureReasonWorkloadHigh {
		t.Fatalf("third high decision=%#v", decision)
	}

	recoveryStart := next.Add(
		pressureConsecutiveSamples * PressureSampleInterval,
	)
	for index := 0; index < pressureConsecutiveSamples; index++ {
		sample := PressureSample{
			ObservedAt: recoveryStart.Add(
				time.Duration(index) * PressureSampleInterval,
			),
			WorkloadMemoryBytes:   plan.WorkloadRecoveryBytes - 1,
			HostMemAvailableBytes: 2 * gibibyte,
		}
		decision = observePressureAt(t, controller, clock, sample)
		if index < pressureConsecutiveSamples-1 &&
			decision.AdmissionAllowed {
			t.Fatalf("recovery sample %d opened early: %#v", index, decision)
		}
	}
	if !decision.AdmissionAllowed ||
		decision.Reason != PressureReasonRecovered {
		t.Fatalf("third recovery decision=%#v", decision)
	}
}

func TestPressureEmergencyAfterGapStillShedsAndAllowsZeroMemory(
	t *testing.T,
) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	controller, clock, next := readyPressureControllerForTest(
		t,
		plan,
		start,
	)
	emergencyAt := next.Add(PressureMaximumSampleGap + time.Second)
	clock.now = emergencyAt
	decision, err := controller.Observe(PressureSample{
		ObservedAt:            emergencyAt,
		WorkloadMemoryBytes:   plan.WorkloadRecoveryBytes,
		HostMemAvailableBytes: 0,
		Leases: []PressureLease{
			{
				LeaseID:   "lease-old",
				State:     LeaseActive,
				StartedAt: start.Add(-time.Minute),
			},
			{
				LeaseID:   "lease-new",
				State:     LeaseActive,
				StartedAt: start.Add(-time.Second),
			},
		},
	})
	if err != nil ||
		decision.AdmissionAllowed ||
		decision.Reason != PressureReasonHostEmergency ||
		!decision.SamplingGap ||
		decision.ShedLeaseID != "lease-new" {
		t.Fatalf("emergency decision=%#v error=%v", decision, err)
	}
}

func TestPressureStopsAfterThreeHostLowSamples(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	controller, clock, next := readyPressureControllerForTest(
		t,
		plan,
		start,
	)
	var decision PressureDecision
	for index := 0; index < pressureConsecutiveSamples; index++ {
		decision = observePressureAt(t, controller, clock, PressureSample{
			ObservedAt: next.Add(
				time.Duration(index) * PressureSampleInterval,
			),
			WorkloadMemoryBytes:   0,
			HostMemAvailableBytes: HostAdmissionMemoryBytes - 1,
		})
	}
	if decision.AdmissionAllowed ||
		decision.Reason != PressureReasonHostLow {
		t.Fatalf("host-low decision=%#v", decision)
	}
}

func TestPressureWorkloadShedProtectsStablePersistingWindow(
	t *testing.T,
) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	controller, clock, next := readyPressureControllerForTest(
		t,
		plan,
		start,
	)
	persistingSince := next.Add(-6 * time.Second)
	lease := PressureLease{
		LeaseID:         "lease-persisting",
		State:           LeaseOutputPersisting,
		StartedAt:       start.Add(-time.Minute),
		PersistingSince: persistingSince,
	}

	var decision PressureDecision
	for index := 0; index < pressureConsecutiveSamples; index++ {
		decision = observePressureAt(t, controller, clock, PressureSample{
			ObservedAt: next.Add(
				time.Duration(index) * PressureSampleInterval,
			),
			WorkloadMemoryBytes:   plan.WorkloadShedBytes,
			HostMemAvailableBytes: 2 * gibibyte,
			Leases:                []PressureLease{lease},
		})
	}
	if decision.ShedLeaseID != "lease-persisting" {
		t.Fatalf("expired receipt grace decision=%#v", decision)
	}

	shiftedAt := next.Add(6 * PressureSampleInterval)
	lease.PersistingSince = shiftedAt
	clock.now = shiftedAt
	decision, err := controller.Observe(PressureSample{
		ObservedAt:            shiftedAt,
		WorkloadMemoryBytes:   plan.WorkloadShedBytes,
		HostMemAvailableBytes: 2 * gibibyte,
		Leases:                []PressureLease{lease},
	})
	if !errors.Is(err, ErrInvalidPressureSample) ||
		decision.AdmissionAllowed {
		t.Fatalf("shifted lifecycle decision=%#v error=%v", decision, err)
	}
}

func TestPressureShedRetrySurvivesLeaseOmission(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	controller, clock, next := readyPressureControllerForTest(
		t,
		plan,
		start,
	)
	lease := PressureLease{
		LeaseID:   "lease-active",
		State:     LeaseActive,
		StartedAt: start.Add(-time.Minute),
	}
	first := observePressureAt(t, controller, clock, PressureSample{
		ObservedAt:            next,
		WorkloadMemoryBytes:   0,
		HostMemAvailableBytes: 0,
		Leases:                []PressureLease{lease},
	})
	if first.ShedLeaseID != lease.LeaseID {
		t.Fatalf("first emergency decision=%#v", first)
	}
	observePressureAt(t, controller, clock, PressureSample{
		ObservedAt:            next.Add(PressureSampleInterval),
		WorkloadMemoryBytes:   0,
		HostMemAvailableBytes: 0,
	})
	reappeared := observePressureAt(t, controller, clock, PressureSample{
		ObservedAt:            next.Add(2 * PressureSampleInterval),
		WorkloadMemoryBytes:   0,
		HostMemAvailableBytes: 0,
		Leases:                []PressureLease{lease},
	})
	if reappeared.ShedLeaseID != "" {
		t.Fatalf("omission reset shed retry: %#v", reappeared)
	}
}

func TestPressureFirstObservationHonorsElapsedPersistence(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	controller, clock, next := readyPressureControllerForTest(
		t,
		plan,
		start,
	)
	lease := PressureLease{
		LeaseID:         "lease-old-persisting",
		State:           LeaseOutputPersisting,
		StartedAt:       start.Add(-time.Minute),
		PersistingSince: next.Add(-PressurePersistingGrace),
	}
	decision := observePressureAt(t, controller, clock, PressureSample{
		ObservedAt:            next,
		WorkloadMemoryBytes:   0,
		HostMemAvailableBytes: 0,
		Leases:                []PressureLease{lease},
	})
	if decision.ShedLeaseID != lease.LeaseID {
		t.Fatalf("elapsed persistence received extra grace: %#v", decision)
	}
}

func TestPressurePersistenceHistorySurvivesStateRegressionAndOmission(
	t *testing.T,
) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	controller, clock, next := readyPressureControllerForTest(
		t,
		plan,
		start,
	)
	lease := PressureLease{
		LeaseID:         "lease-persisting",
		State:           LeaseOutputPersisting,
		StartedAt:       start.Add(-time.Minute),
		PersistingSince: next.Add(-time.Second),
	}
	observePressureAt(t, controller, clock, PressureSample{
		ObservedAt:            next,
		WorkloadMemoryBytes:   0,
		HostMemAvailableBytes: 2 * gibibyte,
		Leases:                []PressureLease{lease},
	})

	regressedAt := next.Add(PressureSampleInterval)
	regressed := lease
	regressed.State = LeaseActive
	regressed.PersistingSince = time.Time{}
	clock.now = regressedAt
	if _, err := controller.Observe(PressureSample{
		ObservedAt:            regressedAt,
		WorkloadMemoryBytes:   0,
		HostMemAvailableBytes: 2 * gibibyte,
		Leases:                []PressureLease{regressed},
	}); !errors.Is(err, ErrInvalidPressureSample) {
		t.Fatalf("persisting-to-active regression error=%v", err)
	}

	controller, clock, next = readyPressureControllerForTest(
		t,
		plan,
		start.Add(time.Hour),
	)
	lease.StartedAt = start.Add(time.Hour - time.Minute)
	lease.PersistingSince = next.Add(-time.Second)
	observePressureAt(t, controller, clock, PressureSample{
		ObservedAt:            next,
		WorkloadMemoryBytes:   0,
		HostMemAvailableBytes: 2 * gibibyte,
		Leases:                []PressureLease{lease},
	})
	observePressureAt(
		t,
		controller,
		clock,
		healthyPressureSample(plan, next.Add(PressureSampleInterval)),
	)
	reappearedAt := next.Add(2 * PressureSampleInterval)
	lease.PersistingSince = reappearedAt
	clock.now = reappearedAt
	if _, err := controller.Observe(PressureSample{
		ObservedAt:            reappearedAt,
		WorkloadMemoryBytes:   0,
		HostMemAvailableBytes: 2 * gibibyte,
		Leases:                []PressureLease{lease},
	}); !errors.Is(err, ErrInvalidPressureSample) {
		t.Fatalf("reappeared persistence reset error=%v", err)
	}
}

func TestPressureSamplingGapFailsClosedThenRecoversWithEarlyJitter(
	t *testing.T,
) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	controller, clock, next := readyPressureControllerForTest(
		t,
		plan,
		start,
	)
	gapAt := next.Add(PressureMaximumSampleGap + time.Second)
	clock.now = gapAt
	decision, err := controller.Observe(healthyPressureSample(plan, gapAt))
	if !errors.Is(err, ErrPressureSamplingGap) ||
		decision.AdmissionAllowed {
		t.Fatalf("gap decision=%#v error=%v", decision, err)
	}

	for index := 1; index <= pressureConsecutiveSamples; index++ {
		at := gapAt.Add(time.Duration(index) * (PressureSampleInterval - 200*time.Millisecond))
		decision = observePressureAt(
			t,
			controller,
			clock,
			healthyPressureSample(plan, at),
		)
	}
	if !decision.AdmissionAllowed {
		t.Fatalf("controller did not recover after harmless jitter: %#v", decision)
	}
}

func TestPressureBurstSamplesCannotOpenAdmission(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	controller, clock := newTestPressureController(t, plan, start)
	observePressureAt(
		t,
		controller,
		clock,
		healthyPressureSample(plan, start),
	)
	for index := 1; index < pressureConsecutiveSamples; index++ {
		at := start.Add(time.Duration(index) * time.Millisecond)
		clock.now = at
		decision, err := controller.Observe(healthyPressureSample(plan, at))
		if !errors.Is(err, ErrPressureSamplingGap) ||
			decision.AdmissionAllowed {
			t.Fatalf("burst %d decision=%#v error=%v", index, decision, err)
		}
	}
	if controller.AdmissionAllowed() {
		t.Fatal("burst samples opened admission")
	}
}

func TestPressureRejectsBlockedCapacityPlanAndInvalidSamples(t *testing.T) {
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	blocked, err := calculateSandboxCapacityAt(CapacityEvidence{
		Mode:    CapacityEvidenceHistorical,
		Samples: capacitySamples(start, end, 169, 3*gibibyte),
	}, end)
	if !errors.Is(err, ErrCapacityInsufficient) {
		t.Fatal(err)
	}
	if _, err := NewPressureController(blocked); !errors.Is(
		err,
		ErrInvalidPressureConfig,
	) {
		t.Fatalf("blocked plan error=%v", err)
	}

	plan := readyCapacityPlanForRuntime(t)
	controller, clock := newTestPressureController(t, plan, end)
	clock.now = end
	decision, err := controller.Observe(PressureSample{
		ObservedAt:            end,
		WorkloadMemoryBytes:   -1,
		HostMemAvailableBytes: 2 * gibibyte,
	})
	if !errors.Is(err, ErrInvalidPressureSample) ||
		decision.AdmissionAllowed {
		t.Fatalf("invalid sample decision=%#v error=%v", decision, err)
	}
}

func newTestPressureController(
	t *testing.T,
	plan CapacityPlan,
	at time.Time,
) (*PressureController, *pressureTestClock) {
	t.Helper()
	clock := &pressureTestClock{now: at}
	controller, err := newPressureControllerWithClock(plan, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	return controller, clock
}

func readyPressureControllerForTest(
	t *testing.T,
	plan CapacityPlan,
	start time.Time,
) (*PressureController, *pressureTestClock, time.Time) {
	t.Helper()
	controller, clock := newTestPressureController(t, plan, start)
	for index := 0; index < pressureConsecutiveSamples; index++ {
		observePressureAt(
			t,
			controller,
			clock,
			healthyPressureSample(
				plan,
				start.Add(time.Duration(index)*PressureSampleInterval),
			),
		)
	}
	next := start.Add(pressureConsecutiveSamples * PressureSampleInterval)
	return controller, clock, next
}

func healthyPressureSample(plan CapacityPlan, at time.Time) PressureSample {
	return PressureSample{
		ObservedAt:            at,
		WorkloadMemoryBytes:   plan.WorkloadRecoveryBytes - 1,
		HostMemAvailableBytes: 2 * gibibyte,
	}
}

func observePressureAt(
	t *testing.T,
	controller *PressureController,
	clock *pressureTestClock,
	sample PressureSample,
) PressureDecision {
	t.Helper()
	clock.now = sample.ObservedAt
	decision, err := controller.Observe(sample)
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func readyCapacityPlanForRuntime(t *testing.T) CapacityPlan {
	t.Helper()
	end := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	start := end.Add(-7 * 24 * time.Hour)
	plan, err := calculateSandboxCapacityAt(CapacityEvidence{
		Mode:    CapacityEvidenceHistorical,
		Samples: capacitySamples(start, end, 169, 8*gibibyte),
	}, end)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
