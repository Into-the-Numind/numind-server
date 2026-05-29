package sandbox

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed seccomp.json
var defaultSeccompProfile []byte

var (
	seccompPathOnce sync.Once
	seccompPath     string
	seccompPathErr  error
)

// ResolveSeccompPath materialises the embedded seccomp profile to a temp file
// on disk and returns the absolute path. Subsequent calls return the same
// path (sync.Once). Used by BuildSpawnConfig to feed Docker's
// --security-opt seccomp=<file> flag.
//
// The temp file lives in os.TempDir()/numind-sandbox-seccomp.json. It is not
// cleaned up at process exit; the OS will clean it on reboot. This is
// intentional: container Spawns reuse the same file path across the server's
// lifetime.
func ResolveSeccompPath() (string, error) {
	seccompPathOnce.Do(func() {
		dir := os.TempDir()
		path := filepath.Join(dir, "numind-sandbox-seccomp.json")
		if err := os.WriteFile(path, defaultSeccompProfile, 0o644); err != nil {
			seccompPathErr = fmt.Errorf("ResolveSeccompPath: write %s: %w", path, err)
			return
		}
		seccompPath = path
	})
	return seccompPath, seccompPathErr
}

// resetSeccompPathForTesting clears the sync.Once state for tests.
func resetSeccompPathForTesting() {
	seccompPathOnce = sync.Once{}
	seccompPath = ""
	seccompPathErr = nil
}

// BuildSpawnConfig assembles SpawnConfig for a sandbox container based on
// SandboxConfig + V5 ADR Q2 hardening list. The caller must pass an absolute
// path to the seccomp profile (from ResolveSeccompPath).
func BuildSpawnConfig(cfg SandboxConfig, absSeccompPath string) SpawnConfig {
	network, _ := NetworkPolicyForBackend(cfg.NetworkPolicy)

	securityOpts := []string{
		"no-new-privileges",
	}
	if absSeccompPath != "" {
		securityOpts = append(securityOpts, "seccomp="+absSeccompPath)
	}
	if cfg.AppArmorProfile != "" {
		securityOpts = append(securityOpts, "apparmor="+cfg.AppArmorProfile)
	}

	return SpawnConfig{
		ImageTag:     cfg.ImageTag,
		SecurityOpts: securityOpts,
		User:         cfg.UserSpec,
		CapDrop:      []string{"ALL"},
		CapAdd:       append([]string(nil), cfg.Capabilities...),
		Memory:       fmt.Sprintf("%dm", cfg.MemoryLimitMB),
		CPUs:         fmt.Sprintf("%.1f", cfg.CPUQuota),
		PIDsLimit:    cfg.PIDsLimit,
		ReadOnly:     cfg.ReadOnlyRootfs,
		Tmpfs: []string{
			fmt.Sprintf("/workdir:size=%dm,uid=1000,gid=1000", cfg.WorkdirSizeMB),
		},
		Network:  network,
		Detached: true,
		Labels:   []string{SandboxContainerLabel},
	}
}

// ValidateSecurityChecklist asserts that the SpawnConfig matches the V5 ADR Q2
// minimum hardening list. Returns the missing checks (empty slice = OK).
func ValidateSecurityChecklist(s SpawnConfig) []string {
	var missing []string

	hasOpt := func(prefix string) bool {
		for _, o := range s.SecurityOpts {
			if strings.HasPrefix(o, prefix) {
				return true
			}
		}
		return false
	}

	if !hasOpt("seccomp=") {
		missing = append(missing, "seccomp profile")
	}
	if !hasOpt("apparmor=") {
		missing = append(missing, "apparmor profile")
	}
	if !hasOpt("no-new-privileges") {
		missing = append(missing, "no-new-privileges")
	}
	if s.User == "" || s.User == "root" || s.User == "0:0" {
		missing = append(missing, "non-root user")
	}
	if !sliceContains(s.CapDrop, "ALL") {
		missing = append(missing, "cap-drop ALL")
	}
	if !s.ReadOnly {
		missing = append(missing, "read-only rootfs")
	}
	if !hasTmpfsMount(s.Tmpfs, "/workdir") {
		missing = append(missing, "tmpfs /workdir")
	}
	if s.Memory == "" {
		missing = append(missing, "memory limit")
	}
	if s.CPUs == "" {
		missing = append(missing, "cpu limit")
	}
	if s.PIDsLimit == 0 {
		missing = append(missing, "pids limit")
	}
	return missing
}

func sliceContains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func hasTmpfsMount(mounts []string, prefix string) bool {
	for _, m := range mounts {
		if strings.HasPrefix(m, prefix) {
			return true
		}
	}
	return false
}
