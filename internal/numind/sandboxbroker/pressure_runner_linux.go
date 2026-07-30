//go:build linux

package sandboxbroker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultProcMeminfoPath          = "/proc/meminfo"
	defaultWorkloadMemoryCurrentRel = "memory.current"
)

// LinuxPressureProbeConfig contains only trusted local probe paths.
type LinuxPressureProbeConfig struct {
	ProcMeminfoPath    string
	WorkloadCgroupPath string
}

// LinuxPressureProbe reads /proc/meminfo and cgroup v2 memory.current.
type LinuxPressureProbe struct {
	procMeminfoPath           string
	workloadMemoryCurrentPath string
}

// LinuxPressureSource combines Linux probes with the journal for sandboxd.
type LinuxPressureSource struct {
	sampler JournalPressureSampler
}

// NewLinuxPressureSource creates the production pressure sampler.
func NewLinuxPressureSource(cfg LinuxPressureSourceConfig) (*LinuxPressureSource, error) {
	if cfg.Journal == nil {
		return nil, ErrInvalidPressureRunnerConfig
	}
	workloadPath := cfg.WorkloadMemoryCurrentPath
	if workloadPath == "" && cfg.WorkloadMemoryCurrentPath == "" {
		return nil, ErrInvalidPressureRunnerConfig
	}
	probe := &LinuxPressureProbe{
		procMeminfoPath:           cfg.ProcMeminfoPath,
		workloadMemoryCurrentPath: workloadPath,
	}
	if probe.procMeminfoPath == "" {
		probe.procMeminfoPath = defaultProcMeminfoPath
	}
	if !safeProbeFile(probe.procMeminfoPath, "/proc/") ||
		!safeProbeFile(probe.workloadMemoryCurrentPath, "/sys/fs/cgroup/") {
		return nil, ErrInvalidPressureRunnerConfig
	}
	return &LinuxPressureSource{
		sampler: JournalPressureSampler{
			Journal: cfg.Journal,
			Probe:   probe,
			Now:     cfg.Now,
		},
	}, nil
}

func (s *LinuxPressureSource) Sample(ctx context.Context) (PressureSample, error) {
	if s == nil {
		return PressureSample{}, ErrInvalidPressureRunnerConfig
	}
	return s.sampler.Sample(ctx)
}

// NewLinuxPressureProbe builds the production pressure probe.
func NewLinuxPressureProbe(cfg LinuxPressureProbeConfig) (*LinuxPressureProbe, error) {
	procMeminfo := cfg.ProcMeminfoPath
	if procMeminfo == "" {
		procMeminfo = defaultProcMeminfoPath
	}
	if !filepath.IsAbs(procMeminfo) ||
		filepath.Clean(procMeminfo) != procMeminfo ||
		!strings.HasPrefix(procMeminfo, "/proc/") {
		return nil, ErrInvalidPressureRunnerConfig
	}
	if !safeReadinessPath(cfg.WorkloadCgroupPath, "/sys/fs/cgroup/") {
		return nil, ErrInvalidPressureRunnerConfig
	}
	return &LinuxPressureProbe{
		procMeminfoPath: procMeminfo,
		workloadMemoryCurrentPath: filepath.Join(
			cfg.WorkloadCgroupPath,
			defaultWorkloadMemoryCurrentRel,
		),
	}, nil
}

func (p *LinuxPressureProbe) PressureHostSample(
	ctx context.Context,
) (int64, int64, error) {
	if p == nil || ctx == nil {
		return 0, 0, ErrInvalidPressureRunnerConfig
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	host, err := readMemAvailableBytes(p.procMeminfoPath)
	if err != nil {
		return 0, 0, err
	}
	workload, err := readCgroupInt64(p.workloadMemoryCurrentPath)
	if err != nil {
		return 0, 0, err
	}
	return workload, host, nil
}

func readMemAvailableBytes(path string) (int64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("%w: read meminfo", ErrInvalidPressureSample)
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimSuffix(fields[0], ":") == "MemAvailable" {
			kib, parseErr := strconv.ParseInt(fields[1], 10, 64)
			if parseErr != nil || kib <= 0 || kib > capacitySampleMaximum/1024 {
				return 0, ErrInvalidPressureSample
			}
			return kib * 1024, nil
		}
	}
	return 0, ErrInvalidPressureSample
}

func readCgroupInt64(path string) (int64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("%w: read cgroup memory", ErrInvalidPressureSample)
	}
	value := strings.TrimSpace(string(content))
	if value == "max" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 || parsed > capacitySampleMaximum {
		return 0, ErrInvalidPressureSample
	}
	return parsed, nil
}
