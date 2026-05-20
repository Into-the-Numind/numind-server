package sandbox

import (
	"testing"
	"time"
)

func TestDefaultSandboxConfig_Defaults(t *testing.T) {
	cfg := DefaultSandboxConfig
	if cfg.Backend != BackendDisabled {
		t.Errorf("default Backend = %q; want %q (prod-safe)", cfg.Backend, BackendDisabled)
	}
	if cfg.PoolMin != 5 {
		t.Errorf("default PoolMin = %d; want 5", cfg.PoolMin)
	}
	if cfg.ImageTag != "python:3.11-slim" {
		t.Errorf("default ImageTag = %q; want python:3.11-slim", cfg.ImageTag)
	}
	if cfg.MemoryLimitMB != 512 {
		t.Errorf("default MemoryLimitMB = %d; want 512", cfg.MemoryLimitMB)
	}
	if cfg.CPUQuota != 1.0 {
		t.Errorf("default CPUQuota = %v; want 1.0", cfg.CPUQuota)
	}
	if cfg.PIDsLimit != 64 {
		t.Errorf("default PIDsLimit = %d; want 64", cfg.PIDsLimit)
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("default Timeout = %v; want 30s", cfg.Timeout)
	}
	if cfg.NetworkPolicy != NetworkPolicyNone {
		t.Errorf("default NetworkPolicy = %q; want %q", cfg.NetworkPolicy, NetworkPolicyNone)
	}
	if !cfg.ReadOnlyRootfs {
		t.Errorf("default ReadOnlyRootfs = false; want true")
	}
	if cfg.UserSpec != "1000:1000" {
		t.Errorf("default UserSpec = %q; want 1000:1000", cfg.UserSpec)
	}
}

func TestLoadFromViper_NilReturnsDefault(t *testing.T) {
	cfg := LoadFromViper(nil)
	if cfg.Backend != BackendDisabled {
		t.Errorf("nil viper → Backend = %q; want disabled", cfg.Backend)
	}
}

// fakeViper implements viperLike for tests.
type fakeViper struct {
	keys map[string]any
}

func newFakeViper(kv map[string]any) *fakeViper {
	return &fakeViper{keys: kv}
}

func (f *fakeViper) IsSet(key string) bool {
	_, ok := f.keys[key]
	return ok
}
func (f *fakeViper) GetString(key string) string {
	if v, ok := f.keys[key].(string); ok {
		return v
	}
	return ""
}
func (f *fakeViper) GetInt(key string) int {
	if v, ok := f.keys[key].(int); ok {
		return v
	}
	return 0
}
func (f *fakeViper) GetFloat64(key string) float64 {
	if v, ok := f.keys[key].(float64); ok {
		return v
	}
	return 0
}
func (f *fakeViper) GetBool(key string) bool {
	if v, ok := f.keys[key].(bool); ok {
		return v
	}
	return false
}
func (f *fakeViper) GetStringSlice(key string) []string {
	if v, ok := f.keys[key].([]string); ok {
		return v
	}
	return nil
}
func (f *fakeViper) GetDuration(key string) time.Duration {
	if v, ok := f.keys[key].(time.Duration); ok {
		return v
	}
	return 0
}

func TestLoadFromViper_OverridesDefaults(t *testing.T) {
	v := newFakeViper(map[string]any{
		"sandbox.backend":                 "docker",
		"sandbox.pool_min":                10,
		"sandbox.pool_max_wait_ms":        60000,
		"sandbox.image_tag":               "python:3.12-slim",
		"sandbox.memory_limit_mb":         1024,
		"sandbox.cpu_quota":               2.0,
		"sandbox.pids_limit":              128,
		"sandbox.timeout_seconds":         60,
		"sandbox.session_timeout_seconds": 600,
		"sandbox.network_policy":          "none",
		"sandbox.allowed_domains":         []string{"api.youshu.asia"},
		"sandbox.workdir_size_mb":         1024,
		"sandbox.read_only_rootfs":        false,
		"sandbox.capabilities":            []string{"NET_ADMIN", "NET_BIND_SERVICE"},
		"sandbox.seccomp_profile":         "/etc/seccomp.json",
		"sandbox.apparmor_profile":        "myprofile",
		"sandbox.user_spec":               "1234:1234",
	})
	cfg := LoadFromViper(v)
	if cfg.Backend != BackendDocker {
		t.Errorf("Backend override = %q; want docker", cfg.Backend)
	}
	if cfg.PoolMin != 10 {
		t.Errorf("PoolMin = %d; want 10", cfg.PoolMin)
	}
	if cfg.PoolMaxWaitMs != 60000 {
		t.Errorf("PoolMaxWaitMs = %d; want 60000", cfg.PoolMaxWaitMs)
	}
	if cfg.ImageTag != "python:3.12-slim" {
		t.Errorf("ImageTag = %q; want python:3.12-slim", cfg.ImageTag)
	}
	if cfg.MemoryLimitMB != 1024 {
		t.Errorf("MemoryLimitMB = %d; want 1024", cfg.MemoryLimitMB)
	}
	if cfg.CPUQuota != 2.0 {
		t.Errorf("CPUQuota = %v; want 2.0", cfg.CPUQuota)
	}
	if cfg.PIDsLimit != 128 {
		t.Errorf("PIDsLimit = %d; want 128", cfg.PIDsLimit)
	}
	if cfg.Timeout != 60*time.Second {
		t.Errorf("Timeout = %v; want 60s", cfg.Timeout)
	}
	if cfg.SessionTimeout != 600*time.Second {
		t.Errorf("SessionTimeout = %v; want 600s", cfg.SessionTimeout)
	}
	if len(cfg.AllowedDomains) != 1 || cfg.AllowedDomains[0] != "api.youshu.asia" {
		t.Errorf("AllowedDomains = %v; want [api.youshu.asia]", cfg.AllowedDomains)
	}
	if cfg.ReadOnlyRootfs != false {
		t.Errorf("ReadOnlyRootfs = true; want false")
	}
	if len(cfg.Capabilities) != 2 || cfg.Capabilities[0] != "NET_ADMIN" {
		t.Errorf("Capabilities = %v; want [NET_ADMIN NET_BIND_SERVICE]", cfg.Capabilities)
	}
	if cfg.UserSpec != "1234:1234" {
		t.Errorf("UserSpec = %q; want 1234:1234", cfg.UserSpec)
	}
}

func TestLoadFromViper_PartialOverride(t *testing.T) {
	v := newFakeViper(map[string]any{
		"sandbox.backend":  "docker",
		"sandbox.pool_min": 3,
	})
	cfg := LoadFromViper(v)
	if cfg.Backend != BackendDocker {
		t.Errorf("Backend = %q; want docker", cfg.Backend)
	}
	if cfg.PoolMin != 3 {
		t.Errorf("PoolMin = %d; want 3", cfg.PoolMin)
	}
	// Unset → defaults preserved
	if cfg.MemoryLimitMB != 512 {
		t.Errorf("MemoryLimitMB = %d; want default 512", cfg.MemoryLimitMB)
	}
}
