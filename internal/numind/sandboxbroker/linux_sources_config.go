package sandboxbroker

import "time"

// LinuxPressureSourceConfig contains root-owned paths for pressure sampling.
type LinuxPressureSourceConfig struct {
	Journal                   *Journal
	ProcMeminfoPath           string
	WorkloadMemoryCurrentPath string
	Now                       func() time.Time
}

// LinuxReadinessSourceConfig contains root-owned paths and identities for
// readiness sampling.
type LinuxReadinessSourceConfig struct {
	Readiness             ReadinessConfig
	DockerHost            string
	DockerConfigDir       string
	ProcMountInfoPath     string
	CgroupControllersPath string
	FindmntBinary         string
	DockerBinary          string
	RuntimeCommandTimeout time.Duration
}
