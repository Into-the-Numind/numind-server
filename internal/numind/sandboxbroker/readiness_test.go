package sandboxbroker

import (
	"context"
	"errors"
	"net/http"
	"sync"
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
	err = scheduler.Acquire(
		context.Background(),
		testSchedulerRequest(1, "api-blue"),
	)
	status, code := rpcErrorContract(err)
	if !errors.Is(err, ErrSchedulerActiveLimit) ||
		status != http.StatusTooManyRequests ||
		code != "capacity" {
		t.Fatalf("capacity error=%v contract=%d/%s", err, status, code)
	}
}

func TestReadinessSamplingFailurePublishesUnavailableEndToEnd(
	t *testing.T,
) {
	plan := readyCapacityPlanForRuntime(t)
	at := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	pressure, _ := newTestPressureController(t, plan, at)
	config := testReadinessConfig()
	checker, err := NewReadinessChecker(
		config,
		plan,
		&fakeReadinessSource{
			snapshot: validReadinessSnapshot(plan, config),
		},
		pressure,
	)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler()
	if err := checker.SyncAdmission(
		context.Background(),
		scheduler,
	); !errors.Is(err, ErrReadinessUnavailable) {
		t.Fatalf("startup sync error=%v", err)
	}
	err = scheduler.Acquire(
		context.Background(),
		testSchedulerRequest(0, "api-blue"),
	)
	if !errors.Is(err, ErrReadinessUnavailable) {
		t.Fatalf("scheduler error=%v", err)
	}
	status, code := rpcErrorContract(err)
	if status != http.StatusServiceUnavailable || code != "unavailable" {
		t.Fatalf("error contract=%d/%s", status, code)
	}
}

func TestReadinessSyncSerializesSnapshotPublication(t *testing.T) {
	plan := readyCapacityPlanForRuntime(t)
	pressure := readyPressureForReadinessTest(t, plan)
	config := testReadinessConfig()
	source := &orderedReadinessSource{
		snapshot:      validReadinessSnapshot(plan, config),
		firstStarted:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	checker, err := NewReadinessChecker(config, plan, source, pressure)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler()
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() {
		firstDone <- checker.SyncAdmission(context.Background(), scheduler)
	}()
	<-source.firstStarted
	go func() {
		secondDone <- checker.SyncAdmission(context.Background(), scheduler)
	}()
	select {
	case <-source.secondStarted:
		t.Fatal("second readiness snapshot overtook the first publication")
	case <-time.After(20 * time.Millisecond):
	}
	close(source.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; !errors.Is(err, source.secondErr) {
		t.Fatalf("second sync error=%v", err)
	}
	if scheduler.AdmissionAllowed() {
		t.Fatal("older healthy result reopened scheduler after newer failure")
	}
}

func TestReadinessDoesNotPublishOpenAcrossPressureGenerationChange(
	t *testing.T,
) {
	plan := readyCapacityPlanForRuntime(t)
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	pressure, clock, next := readyPressureControllerForTest(
		t,
		plan,
		start,
	)
	for index := 0; index < pressureConsecutiveSamples-1; index++ {
		observePressureAt(t, pressure, clock, PressureSample{
			ObservedAt: next.Add(
				time.Duration(index) * PressureSampleInterval,
			),
			WorkloadMemoryBytes:   plan.WorkloadHighBytes,
			HostMemAvailableBytes: 2 * gibibyte,
		})
	}
	raceGate := &pressurePublishRaceGate{
		controller: pressure,
		beforePublish: func() {
			observePressureAt(t, pressure, clock, PressureSample{
				ObservedAt: next.Add(
					2 * PressureSampleInterval,
				),
				WorkloadMemoryBytes:   plan.WorkloadHighBytes,
				HostMemAvailableBytes: 2 * gibibyte,
			})
		},
	}
	config := testReadinessConfig()
	checker, err := NewReadinessChecker(
		config,
		plan,
		&fakeReadinessSource{
			snapshot: validReadinessSnapshot(plan, config),
		},
		raceGate,
	)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler()
	if err := checker.SyncAdmission(
		context.Background(),
		scheduler,
	); !errors.Is(err, ErrReadinessUnavailable) {
		t.Fatalf("generation race sync error=%v", err)
	}
	if scheduler.AdmissionAllowed() {
		t.Fatal("stale healthy generation opened scheduler")
	}
}

type fakeReadinessSource struct {
	snapshot ReadinessSnapshot
	err      error
}

type pressurePublishRaceGate struct {
	controller    *PressureController
	once          sync.Once
	beforePublish func()
}

func (g *pressurePublishRaceGate) AdmissionStatus() (
	bool,
	PressureReason,
	uint64,
) {
	return g.controller.AdmissionStatus()
}

func (g *pressurePublishRaceGate) PublishAdmissionIfCurrent(
	generation uint64,
	publish func(bool, PressureReason),
) bool {
	g.once.Do(g.beforePublish)
	return g.controller.PublishAdmissionIfCurrent(generation, publish)
}

type orderedReadinessSource struct {
	mu            sync.Mutex
	calls         int
	snapshot      ReadinessSnapshot
	firstStarted  chan struct{}
	releaseFirst  chan struct{}
	secondStarted chan struct{}
	secondErr     error
}

func (s *orderedReadinessSource) Snapshot(
	context.Context,
) (ReadinessSnapshot, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	if s.secondErr == nil {
		s.secondErr = errors.New("newer readiness failure")
	}
	s.mu.Unlock()
	switch call {
	case 1:
		close(s.firstStarted)
		<-s.releaseFirst
		return s.snapshot, nil
	case 2:
		close(s.secondStarted)
		return ReadinessSnapshot{}, s.secondErr
	default:
		return ReadinessSnapshot{}, errors.New("unexpected readiness call")
	}
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
