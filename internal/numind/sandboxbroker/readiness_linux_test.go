//go:build linux

package sandboxbroker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLinuxReadinessHelpersParseControllersAndMountInfo(t *testing.T) {
	dir := t.TempDir()
	controllersPath := filepath.Join(dir, "cgroup.controllers")
	if err := os.WriteFile(
		controllersPath,
		[]byte("cpuset cpu io memory pids\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	controllers, err := readCgroupControllers(controllersPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, controller := range []string{"cpu", "io", "memory", "pids"} {
		if !controllers[controller] {
			t.Fatalf("controller %q missing from %#v", controller, controllers)
		}
	}

	mountInfoPath := filepath.Join(dir, "mountinfo")
	if err := os.WriteFile(
		mountInfoPath,
		[]byte("36 25 0:32 / /opt/numind\\040sandbox/data-root rw,relatime - ext4 /dev/sda1 rw\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	mountPoint, mounted := findMountPointForPath(
		mountInfoPath,
		"/opt/numind sandbox/data-root/jobs",
	)
	if !mounted || mountPoint != "/opt/numind sandbox/data-root" {
		t.Fatalf("mountPoint=%q mounted=%v", mountPoint, mounted)
	}
}

func TestLinuxReadinessSourceRejectsUnsafeDockerHost(t *testing.T) {
	_, err := NewLinuxReadinessSource(LinuxReadinessSourceConfig{
		Readiness:             testReadinessConfig(),
		DockerHost:            "unix:///var/run/docker.sock",
		DockerConfigDir:       "/opt/numind-sandbox/docker-config",
		ProcMountInfoPath:     "/proc/self/mountinfo",
		CgroupControllersPath: "/sys/fs/cgroup/cgroup.controllers",
		FindmntBinary:         "/usr/bin/findmnt",
		DockerBinary:          "/usr/bin/docker",
		RuntimeCommandTimeout: time.Second,
	})
	if !errors.Is(err, ErrInvalidReadinessConfig) {
		t.Fatalf("unsafe docker host err = %v", err)
	}
}
