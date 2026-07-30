//go:build linux

package sandboxbroker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxPressureProbeParsesMeminfoAndCgroupValues(t *testing.T) {
	dir := t.TempDir()
	meminfoPath := filepath.Join(dir, "meminfo")
	if err := os.WriteFile(
		meminfoPath,
		[]byte("MemTotal: 8192000 kB\nMemAvailable: 3145728 kB\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	host, err := readMemAvailableBytes(meminfoPath)
	if err != nil {
		t.Fatal(err)
	}
	if host != 3*gibibyte {
		t.Fatalf("MemAvailable bytes = %d", host)
	}

	memoryCurrentPath := filepath.Join(dir, "memory.current")
	if err := os.WriteFile(memoryCurrentPath, []byte("536870912\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workload, err := readCgroupInt64(memoryCurrentPath)
	if err != nil {
		t.Fatal(err)
	}
	if workload != 512<<20 {
		t.Fatalf("memory.current = %d", workload)
	}
	if err := os.WriteFile(memoryCurrentPath, []byte("max\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workload, err = readCgroupInt64(memoryCurrentPath)
	if err != nil || workload != 0 {
		t.Fatalf("memory.current max = %d, %v", workload, err)
	}
}

func TestLinuxPressureProbeRejectsUnsafeConfigPaths(t *testing.T) {
	if _, err := NewLinuxPressureProbe(LinuxPressureProbeConfig{
		ProcMeminfoPath:    "/tmp/meminfo",
		WorkloadCgroupPath: "/sys/fs/cgroup/numind-sandbox-control.slice",
	}); !errors.Is(err, ErrInvalidPressureRunnerConfig) {
		t.Fatalf("unsafe proc path err = %v", err)
	}
	if _, err := NewLinuxPressureProbe(LinuxPressureProbeConfig{
		ProcMeminfoPath:    "/proc/meminfo",
		WorkloadCgroupPath: "/tmp/cgroup",
	}); !errors.Is(err, ErrInvalidPressureRunnerConfig) {
		t.Fatalf("unsafe cgroup path err = %v", err)
	}
}
