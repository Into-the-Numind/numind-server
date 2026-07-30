//go:build linux

package sandboxbroker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultCgroupControllersPath = "/sys/fs/cgroup/cgroup.controllers"
	defaultMountInfoPath         = "/proc/self/mountinfo"
	defaultFindmntBinary         = "/usr/bin/findmnt"
)

// LinuxReadinessSource probes only local Linux runtime state.
type LinuxReadinessSource struct {
	dockerBinary          string
	dockerEnv             []string
	runtimeCommandTimeout time.Duration
	config                ReadinessConfig
	cgroupControllersPath string
	mountInfoPath         string
	findmntBinary         string
}

// NewLinuxReadinessSource creates the production readiness probe.
func NewLinuxReadinessSource(
	cfg LinuxReadinessSourceConfig,
) (*LinuxReadinessSource, error) {
	controllers := cfg.CgroupControllersPath
	if controllers == "" {
		controllers = defaultCgroupControllersPath
	}
	mountInfo := cfg.ProcMountInfoPath
	if mountInfo == "" {
		mountInfo = defaultMountInfoPath
	}
	findmnt := cfg.FindmntBinary
	if findmnt == "" {
		findmnt = defaultFindmntBinary
	}
	dockerBinary := cfg.DockerBinary
	if dockerBinary == "" {
		dockerBinary = SandboxDockerBinary
	}
	timeout := cfg.RuntimeCommandTimeout
	if timeout == 0 {
		timeout = RuntimeExecTimeout
	}
	if !validReadinessConfig(cfg.Readiness) ||
		!safeProbeFile(controllers, "/sys/fs/cgroup/") ||
		!safeProbeFile(mountInfo, "/proc/") ||
		!filepath.IsAbs(findmnt) ||
		filepath.Clean(findmnt) != findmnt ||
		!filepath.IsAbs(dockerBinary) ||
		filepath.Clean(dockerBinary) != dockerBinary ||
		!safeDockerHost(cfg.DockerHost) ||
		!safeDockerConfigDir(cfg.DockerConfigDir) ||
		timeout <= 0 ||
		timeout > RuntimeExecTimeout {
		return nil, ErrInvalidReadinessConfig
	}
	return &LinuxReadinessSource{
		dockerBinary:          dockerBinary,
		dockerEnv:             dockerCommandEnv(cfg.DockerHost, cfg.DockerConfigDir),
		runtimeCommandTimeout: timeout,
		config:                cfg.Readiness,
		cgroupControllersPath: controllers,
		mountInfoPath:         mountInfo,
		findmntBinary:         findmnt,
	}, nil
}

func (s *LinuxReadinessSource) Snapshot(
	ctx context.Context,
) (ReadinessSnapshot, error) {
	if s == nil || ctx == nil {
		return ReadinessSnapshot{}, ErrInvalidReadinessConfig
	}
	if err := ctx.Err(); err != nil {
		return ReadinessSnapshot{}, err
	}
	snapshot := ReadinessSnapshot{
		ParentCgroupPath:   s.config.ParentCgroupPath,
		WorkloadCgroupPath: s.config.WorkloadCgroupPath,
		Controllers:        map[string]bool{},
	}
	if _, err := s.runDocker(ctx, "version", "--format", "{{.Server.Version}}"); err == nil {
		snapshot.RuntimeReady = true
	}
	if snapshot.RuntimeReady {
		if _, err := s.runDocker(ctx, "image", "inspect", s.config.ImageDigest); err == nil {
			snapshot.ImageDigest = s.config.ImageDigest
		} else {
			snapshot.RuntimeReady = false
		}
	}
	controllers, err := readCgroupControllers(s.cgroupControllersPath)
	if err == nil {
		snapshot.CgroupV2 = true
		snapshot.Controllers = controllers
	}
	snapshot.ParentMemoryMaxBytes, _ = readCgroupInt64(
		filepath.Join(s.config.ParentCgroupPath, "memory.max"),
	)
	snapshot.WorkloadMemoryMaxBytes, _ = readCgroupInt64(
		filepath.Join(s.config.WorkloadCgroupPath, "memory.max"),
	)
	mountPoint, mounted := findMountPointForPath(
		s.mountInfoPath,
		s.config.DataRootPath,
	)
	snapshot.DataRootMounted = mounted
	if mounted {
		snapshot.DataRootPath = mountPoint
	}
	snapshot.DataRootUUID = s.filesystemUUID(ctx)
	totalBytes, usedBytes, totalInodes, usedInodes, statErr := statfsUsage(
		s.config.DataRootPath,
	)
	if statErr == nil {
		snapshot.DataRootBytesTotal = totalBytes
		snapshot.DataRootBytesUsed = usedBytes
		snapshot.DataRootInodesTotal = totalInodes
		snapshot.DataRootInodesUsed = usedInodes
	}
	return snapshot, nil
}

func (s *LinuxReadinessSource) runDocker(
	ctx context.Context,
	args ...string,
) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, s.runtimeCommandTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, s.dockerBinary, args...)
	command.Env = append([]string(nil), s.dockerEnv...)
	output, err := command.Output()
	if commandCtx.Err() != nil {
		return nil, commandCtx.Err()
	}
	return output, err
}

func (s *LinuxReadinessSource) filesystemUUID(ctx context.Context) string {
	if s.findmntBinary == "" {
		return ""
	}
	command := exec.CommandContext(
		ctx,
		s.findmntBinary,
		"--noheadings",
		"--output",
		"UUID",
		"--target",
		s.config.DataRootPath,
	)
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	output, err := command.Output()
	if err != nil {
		return ""
	}
	uuid := strings.TrimSpace(string(output))
	if !validFilesystemUUID(uuid) {
		return ""
	}
	return uuid
}

func safeProbeFile(value string, prefix string) bool {
	return filepath.IsAbs(value) &&
		filepath.Clean(value) == value &&
		strings.HasPrefix(value, prefix) &&
		!strings.ContainsRune(value, 0)
}

func readCgroupControllers(path string) (map[string]bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	controllers := map[string]bool{}
	for _, item := range strings.Fields(string(content)) {
		controllers[item] = true
	}
	return controllers, nil
}

func findMountPointForPath(mountInfoPath string, target string) (string, bool) {
	content, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return "", false
	}
	best := ""
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mountPoint := unescapeMountInfoPath(fields[4])
		if mountPoint == "" {
			continue
		}
		if mountPoint == target ||
			strings.HasPrefix(target, strings.TrimRight(mountPoint, "/")+"/") {
			if len(mountPoint) > len(best) {
				best = mountPoint
			}
		}
	}
	return best, best != ""
}

func unescapeMountInfoPath(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' || index+3 >= len(value) {
			builder.WriteByte(value[index])
			continue
		}
		octal := value[index+1 : index+4]
		parsed, err := strconv.ParseInt(octal, 8, 32)
		if err != nil {
			builder.WriteByte(value[index])
			continue
		}
		builder.WriteRune(rune(parsed))
		index += 3
	}
	return builder.String()
}

func statfsUsage(path string) (int64, int64, int64, int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("%w: statfs", ErrReadinessUnavailable)
	}
	blockSize := int64(stat.Bsize)
	totalBytes := int64(stat.Blocks) * blockSize
	freeBytes := int64(stat.Bavail) * blockSize
	totalInodes := int64(stat.Files)
	freeInodes := int64(stat.Ffree)
	return totalBytes,
		totalBytes - freeBytes,
		totalInodes,
		totalInodes - freeInodes,
		nil
}
