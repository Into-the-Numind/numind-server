package sandboxbroker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthReadyAndMetricsRoutesUseObservabilityProvider(t *testing.T) {
	service := observabilityTestService{
		snapshot: MetricsSnapshot{
			Scheduler: SchedulerSnapshot{
				Ready:           1,
				Active:          2,
				Queued:          3,
				QueueRequestIDs: []string{"request-secret"},
			},
			LeaseStates: map[LeaseState]int{
				LeaseReady:  1,
				LeaseActive: 2,
			},
			Rejections: map[string]int64{
				"queue_full": 4,
			},
			ExecTotal:              5,
			CopyInBytes:            6,
			CopyOutBytes:           7,
			WorkloadMemoryBytes:    8,
			HostMemAvailableBytes:  9,
			CgroupEvents:           map[string]int64{"oom": 10},
			DataRootBytesUsedRatio: 0.5,
			ReconcilePending:       11,
		},
	}
	server := newTestServer(t, service)

	for _, path := range []string{"/healthz", "/readyz"} {
		response := httptest.NewRecorder()
		server.ServeHTTP(
			response,
			newPeerRequest(t, http.MethodGet, path, "", ""),
		)
		if response.Code != http.StatusOK {
			t.Fatalf("%s response = %d %s", path, response.Code, response.Body.String())
		}
	}

	response := httptest.NewRecorder()
	server.ServeHTTP(
		response,
		newPeerRequest(t, http.MethodGet, "/metrics", "", ""),
	)
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(body, "sandbox_queue_depth 3") ||
		!strings.Contains(body, "sandbox_reconcile_pending 11") {
		t.Fatalf("metrics response = %d %s", response.Code, body)
	}
	if strings.Contains(body, "request-secret") {
		t.Fatalf("metrics leaked queue request id: %s", body)
	}
}

func TestJournalRPCServiceImplementsHealthReadyMetrics(t *testing.T) {
	var _ ServerObservability = (*JournalRPCService)(nil)

	ctx := context.Background()
	journal := openTestJournal(t, testJournalPath(t))
	scheduler := NewScheduler()
	service, err := NewJournalRPCService(
		journal,
		scheduler,
		&testContainerRuntime{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Healthz(ctx); err != nil {
		t.Fatalf("healthz err = %v", err)
	}
	if err := service.Readyz(ctx); !errors.Is(err, ErrSchedulerAdmissionBlocked) {
		t.Fatalf("closed readyz err = %v", err)
	}
	scheduler.SetAdmission(true, nil)
	if err := service.Readyz(ctx); err != nil {
		t.Fatalf("readyz err = %v", err)
	}
	snapshot, err := service.SandboxMetrics(ctx)
	if err != nil {
		t.Fatalf("metrics err = %v", err)
	}
	if snapshot.ReconcilePending != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestHealthReadyUnavailableMatrix(t *testing.T) {
	server := newTestServer(t, observabilityTestService{
		healthErr: ErrJournalClosed,
		readyErr:  ErrReadinessUnavailable,
	})
	response := httptest.NewRecorder()
	server.ServeHTTP(
		response,
		newPeerRequest(t, http.MethodGet, "/healthz", "", ""),
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("health response = %d %s", response.Code, response.Body.String())
	}

	server = newTestServer(t, observabilityTestService{
		readyErr: ErrReadinessUnavailable,
	})
	response = httptest.NewRecorder()
	server.ServeHTTP(
		response,
		newPeerRequest(t, http.MethodGet, "/readyz", "", ""),
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready response = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	server.ServeHTTP(
		response,
		newPeerRequest(t, http.MethodPost, "/healthz", "", ""),
	)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method response = %d %s", response.Code, response.Body.String())
	}
}

func TestMetricsRenderAvoidsHighCardinalityLabels(t *testing.T) {
	rendered := RenderPrometheusMetrics(MetricsSnapshot{
		Scheduler: SchedulerSnapshot{
			Queued:          1,
			QueueRequestIDs: []string{"request-1"},
		},
		LeaseStates: map[LeaseState]int{LeaseActive: 1},
		Rejections:  map[string]int64{"owner_id=customer-1": 1},
	})
	for _, forbidden := range []string{
		"user_id",
		"run_id",
		"lease_id",
		"owner_id",
		"request_id",
		"request-1",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("metrics leaked high-cardinality token %q: %s", forbidden, rendered)
		}
	}
	for _, required := range []string{
		"sandbox_lease_count",
		"sandbox_queue_depth",
		"sandbox_reject_total",
		"sandbox_exec_total",
		"sandbox_copy_bytes_total",
		"sandbox_workload_memory_bytes",
		"sandbox_host_mem_available_bytes",
		"sandbox_cgroup_events_total",
		"sandbox_data_root_bytes_used_ratio",
		"sandbox_reconcile_pending",
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("metrics missing %s: %s", required, rendered)
		}
	}
}

type observabilityTestService struct {
	testRPCService
	healthErr  error
	readyErr   error
	metricsErr error
	snapshot   MetricsSnapshot
}

func (s observabilityTestService) Healthz(context.Context) error {
	return s.healthErr
}

func (s observabilityTestService) Readyz(context.Context) error {
	return s.readyErr
}

func (s observabilityTestService) SandboxMetrics(
	context.Context,
) (MetricsSnapshot, error) {
	if s.metricsErr != nil {
		return MetricsSnapshot{}, s.metricsErr
	}
	if s.snapshot.LeaseStates == nil && s.snapshot.Rejections == nil &&
		s.snapshot.CgroupEvents == nil && s.snapshot.ExecTotal == 0 {
		return MetricsSnapshot{}, errors.New("missing metrics")
	}
	return s.snapshot, nil
}
