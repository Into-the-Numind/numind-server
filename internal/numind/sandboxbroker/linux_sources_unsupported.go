//go:build !linux

package sandboxbroker

import "context"

// LinuxPressureSource is unavailable on non-Linux developer machines.
type LinuxPressureSource struct{}

func NewLinuxPressureSource(
	LinuxPressureSourceConfig,
) (*LinuxPressureSource, error) {
	return nil, ErrInvalidPressureConfig
}

func (*LinuxPressureSource) Sample(context.Context) (PressureSample, error) {
	return PressureSample{}, ErrInvalidPressureConfig
}

// LinuxReadinessSource is unavailable on non-Linux developer machines.
type LinuxReadinessSource struct{}

func NewLinuxReadinessSource(
	LinuxReadinessSourceConfig,
) (*LinuxReadinessSource, error) {
	return nil, ErrInvalidReadinessConfig
}

func (*LinuxReadinessSource) Snapshot(context.Context) (ReadinessSnapshot, error) {
	return ReadinessSnapshot{}, ErrInvalidReadinessConfig
}
