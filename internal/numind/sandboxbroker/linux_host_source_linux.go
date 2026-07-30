//go:build linux

package sandboxbroker

import "path/filepath"

// NewLinuxHostSource builds the production host source from root-owned local
// paths. It reads no product DB/COS/LLM/Feishu configuration.
func NewLinuxHostSource(
	cfg LinuxHostSourceConfig,
	journal *Journal,
) (*LinuxHostSource, error) {
	cfg = cfg.withDefaults()
	readiness, err := NewLinuxReadinessSource(LinuxReadinessSourceConfig{
		Readiness: ReadinessConfig{
			ParentCgroupPath:   cfg.ParentCgroupPath,
			WorkloadCgroupPath: cfg.WorkloadCgroupPath,
			DataRootPath:       cfg.DataRootPath,
			DataRootUUID:       cfg.DataRootUUID,
			ImageDigest:        cfg.ImageDigest,
		},
		CgroupControllersPath: cfg.CgroupControllersPath,
		DockerBinary:          cfg.DockerBinary,
		FindmntBinary:         cfg.FindmntBinary,
	})
	if err != nil {
		return nil, err
	}
	pressure, err := NewLinuxPressureSource(LinuxPressureSourceConfig{
		Journal:                   journal,
		ProcMeminfoPath:           cfg.ProcMeminfoPath,
		WorkloadMemoryCurrentPath: filepath.Join(cfg.WorkloadCgroupPath, "memory.current"),
	})
	if err != nil {
		return nil, err
	}
	return &LinuxHostSource{
		readiness: readiness,
		pressure:  pressure,
	}, nil
}
