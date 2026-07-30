package sandboxbroker

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ServerObservability is implemented by the T10 sandboxd composition root when
// it can prove journal, runtime, pressure, and reconciliation status.
type ServerObservability interface {
	Healthz(context.Context) error
	Readyz(context.Context) error
	SandboxMetrics(context.Context) (MetricsSnapshot, error)
}

// MetricsSnapshot contains bounded, content-free broker telemetry.
type MetricsSnapshot struct {
	Scheduler              SchedulerSnapshot
	LeaseStates            map[LeaseState]int
	Rejections             map[string]int64
	ExecTotal              int64
	CopyInBytes            int64
	CopyOutBytes           int64
	WorkloadMemoryBytes    int64
	HostMemAvailableBytes  int64
	CgroupEvents           map[string]int64
	DataRootBytesUsedRatio float64
	ReconcilePending       int64
}

// RenderPrometheusMetrics emits Prometheus text format without high-cardinality
// labels such as user_id, run_id, request_id, or lease_id.
func RenderPrometheusMetrics(snapshot MetricsSnapshot) string {
	var builder strings.Builder
	writeMetricLine(
		&builder,
		"sandbox_queue_depth",
		nil,
		int64(snapshot.Scheduler.Queued),
	)
	writeMetricLine(
		&builder,
		"sandbox_container_slots",
		map[string]string{"state": "creating"},
		int64(snapshot.Scheduler.Creating),
	)
	writeMetricLine(
		&builder,
		"sandbox_container_slots",
		map[string]string{"state": "ready"},
		int64(snapshot.Scheduler.Ready),
	)
	writeMetricLine(
		&builder,
		"sandbox_container_slots",
		map[string]string{"state": "active"},
		int64(snapshot.Scheduler.Active),
	)
	for _, state := range sortedLeaseStates(snapshot.LeaseStates) {
		writeMetricLine(
			&builder,
			"sandbox_lease_count",
			map[string]string{"state": string(state)},
			int64(snapshot.LeaseStates[state]),
		)
	}
	for _, reason := range sortedStringKeys(snapshot.Rejections) {
		writeMetricLine(
			&builder,
			"sandbox_reject_total",
			map[string]string{"reason": reason},
			snapshot.Rejections[reason],
		)
	}
	writeMetricLine(&builder, "sandbox_exec_total", nil, snapshot.ExecTotal)
	writeMetricLine(
		&builder,
		"sandbox_copy_bytes_total",
		map[string]string{"direction": "in"},
		snapshot.CopyInBytes,
	)
	writeMetricLine(
		&builder,
		"sandbox_copy_bytes_total",
		map[string]string{"direction": "out"},
		snapshot.CopyOutBytes,
	)
	writeMetricLine(
		&builder,
		"sandbox_workload_memory_bytes",
		nil,
		snapshot.WorkloadMemoryBytes,
	)
	writeMetricLine(
		&builder,
		"sandbox_host_mem_available_bytes",
		nil,
		snapshot.HostMemAvailableBytes,
	)
	for _, event := range sortedStringKeys(snapshot.CgroupEvents) {
		writeMetricLine(
			&builder,
			"sandbox_cgroup_events_total",
			map[string]string{"event": event},
			snapshot.CgroupEvents[event],
		)
	}
	if len(snapshot.CgroupEvents) == 0 {
		writeMetricLine(
			&builder,
			"sandbox_cgroup_events_total",
			map[string]string{"event": "none"},
			0,
		)
	}
	builder.WriteString(fmt.Sprintf(
		"sandbox_data_root_bytes_used_ratio %.6f\n",
		snapshot.DataRootBytesUsedRatio,
	))
	writeMetricLine(
		&builder,
		"sandbox_reconcile_pending",
		nil,
		snapshot.ReconcilePending,
	)
	return builder.String()
}

func (s *JournalRPCService) Healthz(ctx context.Context) error {
	if s == nil || s.journal == nil {
		return ErrInvalidServerConfig
	}
	_, err := s.journal.ListLive(ctx, 1)
	return err
}

func (s *JournalRPCService) Readyz(ctx context.Context) error {
	if err := s.Healthz(ctx); err != nil {
		return err
	}
	if s.scheduler == nil || s.runtime == nil {
		return ErrReadinessUnavailable
	}
	if err := s.scheduler.RequireAdmission(); err != nil {
		return err
	}
	return nil
}

func (s *JournalRPCService) SandboxMetrics(
	ctx context.Context,
) (MetricsSnapshot, error) {
	if s == nil || s.journal == nil || s.scheduler == nil {
		return MetricsSnapshot{}, ErrInvalidServerConfig
	}
	leases, err := s.journal.ListLive(ctx, MaxJournalQueryLimit)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	pending, err := s.journal.ListRecoveryPending(ctx, MaxJournalQueryLimit)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	snapshot := MetricsSnapshot{
		Scheduler:        s.scheduler.Snapshot(),
		LeaseStates:      make(map[LeaseState]int),
		ReconcilePending: int64(len(pending)),
	}
	for _, lease := range leases {
		snapshot.LeaseStates[lease.State]++
		snapshot.CopyInBytes += lease.CopyInBytes
		snapshot.CopyOutBytes += lease.CopyOutBytes
	}
	return snapshot, nil
}

func writeMetricLine(
	builder *strings.Builder,
	name string,
	labels map[string]string,
	value int64,
) {
	builder.WriteString(name)
	if len(labels) > 0 {
		builder.WriteByte('{')
		keys := make([]string, 0, len(labels))
		for key := range labels {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for index, key := range keys {
			if index > 0 {
				builder.WriteByte(',')
			}
			builder.WriteString(key)
			builder.WriteString("=\"")
			builder.WriteString(sanitizeMetricLabelValue(labels[key]))
			builder.WriteByte('"')
		}
		builder.WriteByte('}')
	}
	builder.WriteByte(' ')
	builder.WriteString(fmt.Sprintf("%d\n", value))
}

func sortedLeaseStates(values map[LeaseState]int) []LeaseState {
	states := make([]LeaseState, 0, len(values))
	for state := range values {
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i] < states[j]
	})
	return states
}

func sortedStringKeys(values map[string]int64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sanitizeMetricLabelValue(value string) string {
	if value == "" {
		return "unknown"
	}
	if metricLabelHighCardinality(value) {
		return "redacted"
	}
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '_' || char == '-' || char == ':' || char == '.':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
		if builder.Len() >= 64 {
			break
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

func metricLabelHighCardinality(value string) bool {
	normalized := strings.ToLower(value)
	for _, marker := range []string{
		"lease_id",
		"owner_id",
		"request_id",
		"run_id",
		"user_id",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
