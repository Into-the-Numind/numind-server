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
	if cfg.BrokerSocket != "/run/numind-sandbox/sandboxd.sock" {
		t.Errorf("default BrokerSocket = %q", cfg.BrokerSocket)
	}
	if cfg.BrokerOwnerID != "" {
		t.Errorf("default BrokerOwnerID = %q; want explicit empty fail-closed value", cfg.BrokerOwnerID)
	}
	if cfg.BrokerMetadataMaxBytes != 64<<10 {
		t.Errorf("default BrokerMetadataMaxBytes = %d; want %d", cfg.BrokerMetadataMaxBytes, 64<<10)
	}
	if cfg.BrokerExecOutputMaxBytes != 4<<20 {
		t.Errorf("default BrokerExecOutputMaxBytes = %d; want %d", cfg.BrokerExecOutputMaxBytes, 4<<20)
	}
	if cfg.BrokerCopyInMaxBytes != 100<<20 || cfg.BrokerCopyOutMaxBytes != 200<<20 {
		t.Errorf("default broker copy limits = in:%d out:%d", cfg.BrokerCopyInMaxBytes, cfg.BrokerCopyOutMaxBytes)
	}
	if cfg.BrokerSingleFileMaxBytes != 50<<20 {
		t.Errorf("default BrokerSingleFileMaxBytes = %d; want %d", cfg.BrokerSingleFileMaxBytes, 50<<20)
	}
	if cfg.BrokerMaxFiles != 10 || cfg.BrokerMaxConnections != 32 {
		t.Errorf("default broker count limits = files:%d connections:%d", cfg.BrokerMaxFiles, cfg.BrokerMaxConnections)
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
		"sandbox.backend":                      "docker",
		"sandbox.pool_min":                     10,
		"sandbox.pool_max_wait_ms":             60000,
		"sandbox.image_tag":                    "python:3.12-slim",
		"sandbox.memory_limit_mb":              1024,
		"sandbox.cpu_quota":                    2.0,
		"sandbox.pids_limit":                   128,
		"sandbox.timeout_seconds":              60,
		"sandbox.session_timeout_seconds":      600,
		"sandbox.network_policy":               "none",
		"sandbox.allowed_domains":              []string{"api.youshu.asia"},
		"sandbox.workdir_size_mb":              1024,
		"sandbox.read_only_rootfs":             false,
		"sandbox.capabilities":                 []string{"NET_ADMIN", "NET_BIND_SERVICE"},
		"sandbox.seccomp_profile":              "/etc/seccomp.json",
		"sandbox.apparmor_profile":             "myprofile",
		"sandbox.user_spec":                    "1234:1234",
		"sandbox.broker_socket":                "/tmp/test-sandboxd.sock",
		"sandbox.broker_owner_id":              "api-primary",
		"sandbox.broker_metadata_max_bytes":    32768,
		"sandbox.broker_exec_output_max_bytes": 1048576,
		"sandbox.broker_copy_in_max_bytes":     2097152,
		"sandbox.broker_copy_out_max_bytes":    4194304,
		"sandbox.broker_single_file_max_bytes": 524288,
		"sandbox.broker_max_files":             5,
		"sandbox.broker_max_connections":       12,
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
	if cfg.BrokerSocket != "/tmp/test-sandboxd.sock" {
		t.Errorf("BrokerSocket = %q", cfg.BrokerSocket)
	}
	if cfg.BrokerOwnerID != "api-primary" {
		t.Errorf("BrokerOwnerID = %q", cfg.BrokerOwnerID)
	}
	if cfg.BrokerMetadataMaxBytes != 32768 || cfg.BrokerExecOutputMaxBytes != 1048576 {
		t.Errorf("broker response limits = metadata:%d exec:%d", cfg.BrokerMetadataMaxBytes, cfg.BrokerExecOutputMaxBytes)
	}
	if cfg.BrokerCopyInMaxBytes != 2097152 || cfg.BrokerCopyOutMaxBytes != 4194304 {
		t.Errorf("broker copy limits = in:%d out:%d", cfg.BrokerCopyInMaxBytes, cfg.BrokerCopyOutMaxBytes)
	}
	if cfg.BrokerSingleFileMaxBytes != 524288 || cfg.BrokerMaxFiles != 5 || cfg.BrokerMaxConnections != 12 {
		t.Errorf("broker count limits = single:%d files:%d connections:%d",
			cfg.BrokerSingleFileMaxBytes, cfg.BrokerMaxFiles, cfg.BrokerMaxConnections)
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
