package sandboxbroker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReadinessRequiresExactInfrastructureAndReportsDiskWarning(
	t *testing.T,
) {
	plan := readyCapacityPlanForRuntime(t)
	pressure := readyPressureForReadinessTest(t, plan)
	config := testReadinessConfig()
	source := &fakeReadinessSource{
		snapshot: validReadinessSnapshot(plan, config),
	}
	checker, err := NewReadinessChecker(config, plan, source, pressure)
	if err != nil {
		t.Fatal(err)
	}
	result, err := checker.Check(context.Background())
	if err != nil || !result.Ready || len(result.Failures) != 0 {
		t.Fatalf("ready result=%#v error=%v", result, err)
	}

	source.snapshot.DataRootBytesUsed = 70
	result, err = checker.Check(context.Background())
	if err != nil || !result.Ready ||
		!containsReadinessCode(result.Alerts, ReadinessDiskBytesWarning) {
		t.Fatalf("warning result=%#v error=%v", result, err)
	}
	source.snapshot.DataRootBytesUsed = 84
	source.snapshot.DataRootInodesUsed = 85
	result, err = checker.Check(context.Background())
	if err != nil || result.Ready ||
		!containsReadinessCode(result.Failures, ReadinessDiskInodesBlocked) {
		t.Fatalf("blocked result=%#v error=%v", result, err)
	}
	source.snapshot.DataRootBytesUsed = 85
	source.snapshot.DataRootInodesUsed = 69
	result, err = checker.Check(context.Background())
	if err != nil || result.Ready ||
		!containsReadinessCode(result.Failures, ReadinessDiskBytesBlocked) {
		t.Fatalf("byte-blocked result=%#v error=%v", result, err)
	}
}

func TestReadinessFailsClosedForEveryInfrastructureDependency(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	config := testReadinessConfig()
	tests := []struct {
		name   string
		mutate func(*ReadinessSnapshot)
		code   ReadinessCode
	}{
		{
			name: "runtime",
			mutate: func(snapshot *ReadinessSnapshot) {
				snapshot.RuntimeReady = false
			},
			code: ReadinessRuntimeUnavailable,
		},
		{
			name: "cgroup version",
			mutate: func(snapshot *ReadinessSnapshot) {
				snapshot.CgroupV2 = false
			},
			code: ReadinessCgroupUnavailable,
		},
		{
			name: "controller",
			mutate: func(snapshot *ReadinessSnapshot) {
				delete(snapshot.Controllers, "io")
			},
			code: ReadinessCgroupControllerMissing,
		},
		{
			name: "parent maximum",
			mutate: func(snapshot *ReadinessSnapshot) {
				snapshot.ParentMemoryMaxBytes--
			},
			code: ReadinessCgroupLimitMismatch,
		},
		{
			name: "workload maximum",
			mutate: func(snapshot *ReadinessSnapshot) {
				snapshot.WorkloadMemoryMaxBytes--
			},
			code: ReadinessCgroupLimitMismatch,
		},
		{
			name: "mount",
			mutate: func(snapshot *ReadinessSnapshot) {
				snapshot.DataRootMounted = false
			},
			code: ReadinessDataRootUnmounted,
		},
		{
			name: "mount path",
			mutate: func(snapshot *ReadinessSnapshot) {
				snapshot.DataRootPath = "/"
			},
			code: ReadinessDataRootMismatch,
		},
		{
			name: "filesystem uuid",
			mutate: func(snapshot *ReadinessSnapshot) {
				snapshot.DataRootUUID = "wrong"
			},
			code: ReadinessDataRootMismatch,
		},
		{
			name: "image digest",
			mutate: func(snapshot *ReadinessSnapshot) {
				snapshot.ImageDigest = SandboxImageRepository + "@sha256:" +
					"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			},
			code: ReadinessImageMismatch,
		},
		{
			name: "invalid disk totals",
			mutate: func(snapshot *ReadinessSnapshot) {
				snapshot.DataRootBytesTotal = 0
			},
			code: ReadinessDiskProbeInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pressure := readyPressureForReadinessTest(t, plan)
			snapshot := validReadinessSnapshot(plan, config)
			tt.mutate(&snapshot)
			checker, err := NewReadinessChecker(
				config,
				plan,
				&fakeReadinessSource{snapshot: snapshot},
				pressure,
			)
			if err != nil {
				t.Fatal(err)
			}
			result, err := checker.Check(context.Background())
			if err != nil || result.Ready ||
				!containsReadinessCode(result.Failures, tt.code) {
				t.Fatalf("result=%#v error=%v", result, err)
			}
		})
	}
}

func TestReadinessIncludesPressureAdmissionAndSourceFailure(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	pressure, clock, next := readyPressureControllerForTest(
		t,
		plan,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)
	for index := 0; index < 3; index++ {
		observePressureAt(t, pressure, clock, PressureSample{
			ObservedAt: next.Add(
				time.Duration(index) * PressureSampleInterval,
			),
			WorkloadMemoryBytes:   plan.WorkloadHighBytes,
			HostMemAvailableBytes: 2 * gibibyte,
		})
	}
	config := testReadinessConfig()
	source := &fakeReadinessSource{
		snapshot: validReadinessSnapshot(plan, config),
	}
	checker, err := NewReadinessChecker(config, plan, source, pressure)
	if err != nil {
		t.Fatal(err)
	}
	result, err := checker.Check(context.Background())
	if err != nil || result.Ready ||
		!containsReadinessCode(result.Failures, ReadinessPressureBlocked) {
		t.Fatalf("pressure result=%#v error=%v", result, err)
	}
	if err := checker.RequireAdmission(context.Background()); !errors.Is(
		err,
		ErrSchedulerActiveLimit,
	) {
		t.Fatalf("pressure admission error=%v", err)
	}

	source.err = errors.New("probe failed")
	result, err = checker.Check(context.Background())
	if !errors.Is(err, source.err) || result.Ready ||
		!containsReadinessCode(result.Failures, ReadinessProbeUnavailable) {
		t.Fatalf("probe result=%#v error=%v", result, err)
	}
	if err := checker.RequireAdmission(context.Background()); !errors.Is(
		err,
		source.err,
	) {
		t.Fatalf("admission source error=%v", err)
	}

	source.err = nil
	source.snapshot.RuntimeReady = false
	if err := checker.RequireAdmission(context.Background()); !errors.Is(
		err,
		ErrReadinessUnavailable,
	) {
		t.Fatalf("infrastructure admission error=%v", err)
	}
}

func TestReadinessRejectsUnsafeConfigAndBlockedCapacity(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	pressure := readyPressureForReadinessTest(t, plan)
	config := testReadinessConfig()
	config.DataRootPath = "relative"
	if _, err := NewReadinessChecker(
		config,
		plan,
		&fakeReadinessSource{},
		pressure,
	); !errors.Is(err, ErrInvalidReadinessConfig) {
		t.Fatalf("unsafe path error=%v", err)
	}

	for _, workloadPath := range []string{
		"/sys/fs/cgroup/user.slice/other.slice",
		"/sys/fs/cgroup/user.slice",
		"/sys/fs/cgroup/user.slice/user-1001.slice-other",
		config.ParentCgroupPath,
	} {
		t.Run(workloadPath, func(t *testing.T) {
			invalid := testReadinessConfig()
			invalid.WorkloadCgroupPath = workloadPath
			if _, err := NewReadinessChecker(
				invalid,
				plan,
				&fakeReadinessSource{},
				pressure,
			); !errors.Is(err, ErrInvalidReadinessConfig) {
				t.Fatalf("workload path %q error=%v", workloadPath, err)
			}
		})
	}
}

func TestReadinessSynchronizesActualSchedulerGate(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	pressure, clock, next := readyPressureControllerForTest(
		t,
		plan,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)
	config := testReadinessConfig()
	source := &fakeReadinessSource{
		snapshot: validReadinessSnapshot(plan, config),
	}
	checker, err := NewReadinessChecker(config, plan, source, pressure)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler()
	if err := checker.SyncAdmission(
		context.Background(),
		scheduler,
	); err != nil {
		t.Fatal(err)
	}
	if !scheduler.AdmissionAllowed() {
		t.Fatal("ready checker did not open the actual scheduler gate")
	}

	for index := 0; index < pressureConsecutiveSamples; index++ {
		observePressureAt(t, pressure, clock, PressureSample{
			ObservedAt: next.Add(
				time.Duration(index) * PressureSampleInterval,
			),
			WorkloadMemoryBytes:   plan.WorkloadHighBytes,
			HostMemAvailableBytes: 2 * gibibyte,
		})
	}
	if err := checker.SyncAdmission(
		context.Background(),
		scheduler,
	); !errors.Is(err, ErrSchedulerActiveLimit) {
		t.Fatalf("blocked sync error=%v", err)
	}
	if scheduler.AdmissionAllowed() {
		t.Fatal("blocked checker left the actual scheduler gate open")
	}
}

type fakeReadinessSource struct {
	snapshot ReadinessSnapshot
	err      error
}

func (s *fakeReadinessSource) Snapshot(
	context.Context,
) (ReadinessSnapshot, error) {
	if s.err != nil {
		return ReadinessSnapshot{}, s.err
	}
	snapshot := s.snapshot
	snapshot.Controllers = cloneBoolMap(snapshot.Controllers)
	return snapshot, nil
}

func testReadinessConfig() ReadinessConfig {
	return ReadinessConfig{
		ParentCgroupPath:   "/sys/fs/cgroup/user.slice/user-1001.slice",
		WorkloadCgroupPath: "/sys/fs/cgroup/user.slice/user-1001.slice/numind-sandbox-workload.slice",
		DataRootPath:       "/opt/numind-sandbox/data-root",
		DataRootUUID:       "11111111-2222-3333-4444-555555555555",
		ImageDigest: SandboxImageRepository + "@sha256:" +
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func validReadinessSnapshot(
	plan CapacityPlan,
	config ReadinessConfig,
) ReadinessSnapshot {
	return ReadinessSnapshot{
		RuntimeReady:           true,
		CgroupV2:               true,
		Controllers:            map[string]bool{"memory": true, "cpu": true, "pids": true, "io": true},
		ParentCgroupPath:       config.ParentCgroupPath,
		WorkloadCgroupPath:     config.WorkloadCgroupPath,
		ParentMemoryMaxBytes:   plan.ParentMaxBytes,
		WorkloadMemoryMaxBytes: plan.WorkloadMaxBytes,
		DataRootMounted:        true,
		DataRootPath:           config.DataRootPath,
		DataRootUUID:           config.DataRootUUID,
		DataRootBytesTotal:     100,
		DataRootBytesUsed:      69,
		DataRootInodesTotal:    100,
		DataRootInodesUsed:     69,
		ImageDigest:            config.ImageDigest,
	}
}

func containsReadinessCode(
	codes []ReadinessCode,
	target ReadinessCode,
) bool {
	for _, code := range codes {
		if code == target {
			return true
		}
	}
	return false
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	cloned := make(map[string]bool, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func readyPressureForReadinessTest(
	t *testing.T,
	plan CapacityPlan,
) *PressureController {
	t.Helper()
	controller, _, _ := readyPressureControllerForTest(
		t,
		plan,
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)
	return controller
}
