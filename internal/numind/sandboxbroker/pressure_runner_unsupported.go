//go:build !linux

package sandboxbroker

import "context"

// LinuxPressureProbeConfig contains only trusted local probe paths.
type LinuxPressureProbeConfig struct {
	ProcMeminfoPath    string
	WorkloadCgroupPath string
}

// LinuxPressureProbe is unavailable outside Linux production hosts.
type LinuxPressureProbe struct{}

// NewLinuxPressureProbe compiles on developer machines but fails closed at
// runtime; sandboxd is a Linux service.
func NewLinuxPressureProbe(LinuxPressureProbeConfig) (*LinuxPressureProbe, error) {
	return nil, ErrInvalidPressureRunnerConfig
}

func (*LinuxPressureProbe) PressureHostSample(context.Context) (int64, int64, error) {
	return 0, 0, ErrInvalidPressureRunnerConfig
}
