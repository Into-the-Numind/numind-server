package sandboxbroker

import "sync"

// RuntimeTelemetry is the low-cardinality host/runtime snapshot updated only by
// sandboxd's trusted sampler. It intentionally stores no user, request, run, or
// container identifiers.
type RuntimeTelemetry struct {
	mu sync.RWMutex

	workloadMemoryBytes    int64
	hostMemAvailableBytes  int64
	cgroupEvents           map[string]int64
	dataRootBytesUsedRatio float64
}

// AttachRuntimeTelemetry lets the sandboxd composition root enrich /metrics
// without changing the RPC surface used by the main API.
func (s *JournalRPCService) AttachRuntimeTelemetry(
	telemetry *RuntimeTelemetry,
) error {
	if s == nil || telemetry == nil {
		return ErrInvalidServerConfig
	}
	s.telemetry = telemetry
	return nil
}

func (t *RuntimeTelemetry) Publish(
	workloadMemoryBytes int64,
	hostMemAvailableBytes int64,
	cgroupEvents map[string]int64,
	dataRootBytesUsedRatio float64,
) {
	if t == nil {
		return
	}
	copiedEvents := make(map[string]int64, len(cgroupEvents))
	for key, value := range cgroupEvents {
		if key == "" || value < 0 {
			continue
		}
		copiedEvents[key] = value
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.workloadMemoryBytes = workloadMemoryBytes
	t.hostMemAvailableBytes = hostMemAvailableBytes
	t.cgroupEvents = copiedEvents
	t.dataRootBytesUsedRatio = dataRootBytesUsedRatio
}

func (t *RuntimeTelemetry) apply(snapshot *MetricsSnapshot) {
	if t == nil || snapshot == nil {
		return
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	snapshot.WorkloadMemoryBytes = t.workloadMemoryBytes
	snapshot.HostMemAvailableBytes = t.hostMemAvailableBytes
	snapshot.DataRootBytesUsedRatio = t.dataRootBytesUsedRatio
	snapshot.CgroupEvents = make(map[string]int64, len(t.cgroupEvents))
	for key, value := range t.cgroupEvents {
		snapshot.CgroupEvents[key] = value
	}
}
