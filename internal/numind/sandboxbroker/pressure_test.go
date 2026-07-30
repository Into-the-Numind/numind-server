package sandboxbroker

import (
	"errors"
	"testing"
	"time"
)

func TestPressureStopsAfterThreeHighSamplesAndRecoversWithHysteresis(
	t *testing.T,
) {
	plan := readyCapacityPlanForRuntime(t)
	controller, err := NewPressureController(plan)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 2; index++ {
		decision, err := controller.Observe(PressureSample{
			ObservedAt:            start.Add(time.Duration(index) * PressureSampleInterval),
			WorkloadMemoryBytes:   plan.WorkloadHighBytes,
			HostMemAvailableBytes: 2 * gibibyte,
		})
		if err != nil || !decision.AdmissionAllowed {
			t.Fatalf("high sample %d decision=%#v error=%v", index, decision, err)
		}
	}
	decision, err := controller.Observe(PressureSample{
		ObservedAt:            start.Add(2 * PressureSampleInterval),
		WorkloadMemoryBytes:   plan.WorkloadHighBytes,
		HostMemAvailableBytes: 2 * gibibyte,
	})
	if err != nil || decision.AdmissionAllowed ||
		decision.Reason != PressureReasonWorkloadHigh {
		t.Fatalf("third high decision=%#v error=%v", decision, err)
	}

	for index := 3; index < 5; index++ {
		decision, err = controller.Observe(PressureSample{
			ObservedAt:            start.Add(time.Duration(index) * PressureSampleInterval),
			WorkloadMemoryBytes:   plan.WorkloadRecoveryBytes - 1,
			HostMemAvailableBytes: 2 * gibibyte,
		})
		if err != nil || decision.AdmissionAllowed {
			t.Fatalf("recovery sample %d decision=%#v error=%v", index, decision, err)
		}
	}
	decision, err = controller.Observe(PressureSample{
		ObservedAt:            start.Add(5 * PressureSampleInterval),
		WorkloadMemoryBytes:   plan.WorkloadRecoveryBytes - 1,
		HostMemAvailableBytes: 2 * gibibyte,
	})
	if err != nil || !decision.AdmissionAllowed ||
		decision.Reason != PressureReasonRecovered {
		t.Fatalf("third recovery decision=%#v error=%v", decision, err)
	}
}

func TestPressureHostThresholdAndEmergencyShedNewestEligible(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	controller, err := NewPressureController(plan)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	leases := []PressureLease{
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
		{
			LeaseID:         "lease-persisting",
			State:           LeaseOutputPersisting,
			StartedAt:       start.Add(-time.Minute),
			PersistingSince: start.Add(-9 * time.Second),
		},
	}
	decision, err := controller.Observe(PressureSample{
		ObservedAt:            start,
		WorkloadMemoryBytes:   plan.WorkloadRecoveryBytes,
		HostMemAvailableBytes: HostEmergencyMemoryBytes - 1,
		Leases:                leases,
	})
	if err != nil || decision.AdmissionAllowed ||
		decision.Reason != PressureReasonHostEmergency ||
		decision.ShedLeaseID != "lease-new" {
		t.Fatalf("emergency decision=%#v error=%v", decision, err)
	}
}

func TestPressureStopsAfterThreeHostLowSamples(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	controller, err := NewPressureController(plan)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	var decision PressureDecision
	for index := 0; index < 3; index++ {
		decision, err = controller.Observe(PressureSample{
			ObservedAt: start.Add(
				time.Duration(index) * PressureSampleInterval,
			),
			WorkloadMemoryBytes:   0,
			HostMemAvailableBytes: HostAdmissionMemoryBytes - 1,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if decision.AdmissionAllowed ||
		decision.Reason != PressureReasonHostLow {
		t.Fatalf("host-low decision=%#v", decision)
	}
}

func TestPressureWorkloadShedProtectsPersistingForTenSeconds(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	controller, err := NewPressureController(plan)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 3; index++ {
		decision, observeErr := controller.Observe(PressureSample{
			ObservedAt:            start.Add(time.Duration(index) * PressureSampleInterval),
			WorkloadMemoryBytes:   plan.WorkloadShedBytes,
			HostMemAvailableBytes: 2 * gibibyte,
			Leases: []PressureLease{{
				LeaseID:         "lease-persisting",
				State:           LeaseOutputPersisting,
				StartedAt:       start.Add(-time.Minute),
				PersistingSince: start.Add(-4 * time.Second),
			}},
		})
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		if index == 2 && decision.ShedLeaseID != "" {
			t.Fatalf("persisting lease shed before grace: %#v", decision)
		}
	}
	decision, err := controller.Observe(PressureSample{
		ObservedAt:            start.Add(3 * PressureSampleInterval),
		WorkloadMemoryBytes:   plan.WorkloadShedBytes,
		HostMemAvailableBytes: 2 * gibibyte,
		Leases: []PressureLease{{
			LeaseID:         "lease-persisting",
			State:           LeaseOutputPersisting,
			StartedAt:       start.Add(-time.Minute),
			PersistingSince: start.Add(-4 * time.Second),
		}},
	})
	if err != nil || decision.ShedLeaseID != "lease-persisting" {
		t.Fatalf("expired persistence grace decision=%#v error=%v", decision, err)
	}
}

func TestPressureSamplingGapFailsClosedThenRecovers(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	controller, err := NewPressureController(plan)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	if _, err := controller.Observe(PressureSample{
		ObservedAt:            start,
		WorkloadMemoryBytes:   0,
		HostMemAvailableBytes: 2 * gibibyte,
	}); err != nil {
		t.Fatal(err)
	}
	decision, err := controller.Observe(PressureSample{
		ObservedAt:            start.Add(PressureMaximumSampleGap + time.Second),
		WorkloadMemoryBytes:   0,
		HostMemAvailableBytes: 2 * gibibyte,
	})
	if !errors.Is(err, ErrPressureSamplingGap) ||
		decision.AdmissionAllowed {
		t.Fatalf("gap decision=%#v error=%v", decision, err)
	}
	for index := 1; index <= 3; index++ {
		decision, err = controller.Observe(PressureSample{
			ObservedAt: start.Add(
				PressureMaximumSampleGap +
					time.Second +
					time.Duration(index)*PressureSampleInterval,
			),
			WorkloadMemoryBytes:   0,
			HostMemAvailableBytes: 2 * gibibyte,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !decision.AdmissionAllowed {
		t.Fatalf("controller did not recover: %#v", decision)
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

	controller, err := NewPressureController(readyCapacityPlanForRuntime(t))
	if err != nil {
		t.Fatal(err)
	}
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
