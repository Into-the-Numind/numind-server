//go:build !linux

package sandboxbroker

// NewLinuxHostSource compiles on developer machines but sandboxd can only run
// with Linux cgroup/procfs probes.
func NewLinuxHostSource(
	cfg LinuxHostSourceConfig,
	_ *Journal,
) (*LinuxHostSource, error) {
	_ = cfg.withDefaults()
	return nil, ErrInvalidReadinessConfig
}
