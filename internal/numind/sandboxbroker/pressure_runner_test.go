package sandboxbroker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPressureRunnerSamplingErrorFailsClosedAfterReadinessSync(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	pressure, _, _ := readyPressureControllerForTest(t, plan, start)
	scheduler := NewScheduler()
	config := testReadinessConfig()
	readiness, err := NewReadinessChecker(
		config,
		plan,
		&fakeReadinessSource{snapshot: validReadinessSnapshot(plan, config)},
		pressure,
	)
	if err != nil {
		t.Fatal(err)
	}
	sampleErr := errors.New("sampler unavailable")
	runner, err := NewPressureRunner(PressureRunnerConfig{
		Scheduler: scheduler,
		Pressure:  pressure,
		Readiness: readiness,
		Sampler:   pressureRunnerSampler{err: sampleErr},
		Reclaimer: &recordingPressureReclaimer{},
		Now:       func() time.Time { return start },
		Interval:  PressureSampleInterval,
		Watchdog:  PressureMaximumSampleGap,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = runner.sampleAndSync(context.Background())
	if !errors.Is(err, sampleErr) {
		t.Fatalf("sampleAndSync err = %v", err)
	}
	if scheduler.AdmissionAllowed() {
		t.Fatal("scheduler opened after a sampler error")
	}
	if err := scheduler.RequireAdmission(); !errors.Is(err, sampleErr) {
		t.Fatalf("admission err = %v", err)
	}
}

func TestPressureRunnerReclaimsShedLeaseBeforeReadinessSync(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	pressure, clock, next := readyPressureControllerForTest(t, plan, start)
	clock.now = next
	scheduler := NewScheduler()
	reclaimer := &recordingPressureReclaimer{}
	config := testReadinessConfig()
	readiness, err := NewReadinessChecker(
		config,
		plan,
		&orderCheckingReadinessSource{
			t:         t,
			reclaimer: reclaimer,
			leaseID:   "lease-new",
			snapshot:  validReadinessSnapshot(plan, config),
		},
		pressure,
	)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewPressureRunner(PressureRunnerConfig{
		Scheduler: scheduler,
		Pressure:  pressure,
		Readiness: readiness,
		Sampler: pressureRunnerSampler{sample: PressureSample{
			ObservedAt:            next,
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
		}},
		Reclaimer: reclaimer,
		Now:       func() time.Time { return clock.Now() },
		Interval:  PressureSampleInterval,
		Watchdog:  PressureMaximumSampleGap,
	})
	if err != nil {
		t.Fatal(err)
	}

	_ = runner.sampleAndSync(context.Background())
	if !reclaimer.calledFor("lease-new") {
		t.Fatalf("shed lease was not reclaimed first: %#v", reclaimer.leases)
	}
}

type pressureRunnerSampler struct {
	sample PressureSample
	err    error
}

func (s pressureRunnerSampler) Sample(context.Context) (PressureSample, error) {
	return s.sample, s.err
}

type recordingPressureReclaimer struct {
	leases []string
}

func (r *recordingPressureReclaimer) ReclaimLease(
	_ context.Context,
	leaseID string,
	_ TerminationReason,
) error {
	r.leases = append(r.leases, leaseID)
	return nil
}

func (r *recordingPressureReclaimer) calledFor(leaseID string) bool {
	for _, called := range r.leases {
		if called == leaseID {
			return true
		}
	}
	return false
}

type orderCheckingReadinessSource struct {
	t         *testing.T
	reclaimer *recordingPressureReclaimer
	leaseID   string
	snapshot  ReadinessSnapshot
}

func (s *orderCheckingReadinessSource) Snapshot(
	context.Context,
) (ReadinessSnapshot, error) {
	s.t.Helper()
	if !s.reclaimer.calledFor(s.leaseID) {
		s.t.Fatalf("readiness checked before lease %q was reclaimed", s.leaseID)
	}
	snapshot := s.snapshot
	snapshot.Controllers = cloneBoolMap(snapshot.Controllers)
	return snapshot, nil
}
