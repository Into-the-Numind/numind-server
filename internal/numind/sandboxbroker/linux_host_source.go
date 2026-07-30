package sandboxbroker

import "context"

// LinuxHostSource combines readiness and pressure probes for sandboxd.
type LinuxHostSource struct {
	readiness ReadinessSource
	pressure  PressureSampler
}

// Snapshot implements ReadinessSource.
func (s *LinuxHostSource) Snapshot(ctx context.Context) (ReadinessSnapshot, error) {
	if s == nil || s.readiness == nil {
		return ReadinessSnapshot{}, ErrInvalidReadinessConfig
	}
	return s.readiness.Snapshot(ctx)
}

// Sample implements PressureSampler.
func (s *LinuxHostSource) Sample(ctx context.Context) (PressureSample, error) {
	if s == nil || s.pressure == nil {
		return PressureSample{}, ErrInvalidPressureRunnerConfig
	}
	return s.pressure.Sample(ctx)
}
