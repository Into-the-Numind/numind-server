package sandbox

import (
	"time"
)

// Backend selects the sandbox implementation.
// "disabled" is the default; "docker" requires Docker daemon access (dev only).
type Backend string

const (
	// BackendDisabled returns a no-op Pool whose Borrow always errors
	// (ErrSandboxDisabled). bash_exec / image_gen tools fall back to friendly
	// errors. This is the prod-safe default (no SANDBOX_BACKEND env / config
	// section = backend=disabled).
	BackendDisabled Backend = "disabled"

	// BackendDocker uses the host Docker daemon via dockerCLIClient (DooD).
	// Requires bind-mounted /var/run/docker.sock and the docker CLI inside
	// the numind-server container (dev Dockerfile WITH_DOCKER_CLI=true).
	BackendDocker Backend = "docker"

	// BackendBroker uses the constrained sandboxd Unix-socket API. The API
	// process never receives either the host Docker socket or the dedicated
	// Rootless Docker socket.
	BackendBroker Backend = "broker"
)

// NetworkPolicy controls outbound network access for a sandbox container.
type NetworkPolicy string

const (
	// NetworkPolicyNone fully isolates the container (--network=none).
	// Default for v1; the only mode actually implemented.
	NetworkPolicyNone NetworkPolicy = "none"

	// NetworkPolicyAllowlist would limit outbound to whitelisted domains
	// (iptables + DNS allowlist). v1 is a stub returning
	// ErrAllowlistNotImplemented; full impl deferred to #14 e2e-rollout.
	NetworkPolicyAllowlist NetworkPolicy = "allowlist"
)

// SandboxConfig captures the full Docker pool sandbox configuration.
// Sourced from viper (sandbox.* keys) or DefaultSandboxConfig when absent.
// Blueprint §4.6.2 + V5 ADR Q2 hardening list.
type SandboxConfig struct {
	Backend         Backend
	PoolMin         int
	PoolMaxWaitMs   int
	ImageTag        string
	MemoryLimitMB   int
	CPUQuota        float64
	PIDsLimit       int
	Timeout         time.Duration
	SessionTimeout  time.Duration
	NetworkPolicy   NetworkPolicy
	AllowedDomains  []string
	WorkdirSizeMB   int
	ReadOnlyRootfs  bool
	Capabilities    []string
	SeccompProfile  string // file name relative to package dir, or absolute path
	AppArmorProfile string
	UserSpec        string

	// Track 4: Skill / invoke_skill extensions.

	// SkillsRoot is the directory (inside the numind-server container) holding
	// per-skill subdirectories. Default `/app/skills` is the in-image path baked
	// by the runtime Dockerfile. AcquireForSkill reads files from here and
	// CopyToContainer them into sandbox children at /skills/<skillName>/.
	// Override via sandbox.skills_root only if you bind-mount an alternate tree.
	SkillsRoot string

	// OutputMaxSizeMB is the per-file size ceiling for sandbox output files.
	// ScanOutput rejects files larger than this. The hard ceiling is 50 MB;
	// this config value can only lower it, never raise it above 50.
	// Defaults to 50.
	OutputMaxSizeMB int

	// OutputZipBombThresholdMB is the zip decompressed-size threshold above
	// which ScanOutput reports ErrZipBomb. Hard-coded to 500 MB in ScanOutput;
	// this field is kept for documentation / future tunability.
	// Defaults to 500.
	OutputZipBombThresholdMB int

	// COSUploadConcurrency controls how many CollectOutputs COS upload
	// goroutines run in parallel. Defaults to 3.
	COSUploadConcurrency int

	// Broker transport limits. sandboxd independently enforces the same or
	// stricter server-side ceilings; the client limits responses too so a
	// malfunctioning peer cannot make the API allocate unbounded memory/disk.
	BrokerSocket             string
	BrokerOwnerID            string
	BrokerMetadataMaxBytes   int
	BrokerExecOutputMaxBytes int
	BrokerCopyInMaxBytes     int
	BrokerCopyOutMaxBytes    int
	BrokerSingleFileMaxBytes int
	BrokerMaxFiles           int
	BrokerMaxConnections     int
}

// DefaultSandboxConfig is the prod-safe baseline: Backend=disabled.
// The remaining fields match blueprint §4.6.2 + V5 ADR Q2 so when callers
// override Backend=docker the hardening clicks in by default.
var DefaultSandboxConfig = SandboxConfig{
	Backend:         BackendDisabled,
	PoolMin:         5,
	PoolMaxWaitMs:   30000,
	ImageTag:        "python:3.11-slim",
	MemoryLimitMB:   512,
	CPUQuota:        1.0,
	PIDsLimit:       64,
	Timeout:         180 * time.Second,
	SessionTimeout:  600 * time.Second,
	NetworkPolicy:   NetworkPolicyNone,
	AllowedDomains:  []string{},
	WorkdirSizeMB:   512,
	ReadOnlyRootfs:  true,
	Capabilities:    []string{"NET_BIND_SERVICE"},
	SeccompProfile:  "seccomp.json",
	AppArmorProfile: "docker-default",
	UserSpec:        "1000:1000",

	// Track 4 defaults.
	// SkillsRoot points at the in-image skill dir baked by the runtime
	// Dockerfile (`COPY skills /app/skills`). Override in config_*.yaml only
	// if you bind-mount an alternate skills tree at runtime.
	SkillsRoot:               "/app/skills",
	OutputMaxSizeMB:          50,
	OutputZipBombThresholdMB: 500,
	COSUploadConcurrency:     3,

	BrokerSocket:             DefaultBrokerSocket,
	BrokerOwnerID:            "",
	BrokerMetadataMaxBytes:   DefaultBrokerMetadataMaxBytes,
	BrokerExecOutputMaxBytes: DefaultBrokerExecOutputMaxBytes,
	BrokerCopyInMaxBytes:     DefaultBrokerCopyInMaxBytes,
	BrokerCopyOutMaxBytes:    DefaultBrokerCopyOutMaxBytes,
	BrokerSingleFileMaxBytes: DefaultBrokerSingleFileMaxBytes,
	BrokerMaxFiles:           DefaultBrokerMaxFiles,
	BrokerMaxConnections:     DefaultBrokerMaxConnections,
}

// viperLike is the subset of *viper.Viper the LoadFromViper helper needs.
// Defining the interface here keeps the sandbox package decoupled from
// the concrete viper dependency for unit tests.
type viperLike interface {
	IsSet(key string) bool
	GetString(key string) string
	GetInt(key string) int
	GetFloat64(key string) float64
	GetBool(key string) bool
	GetStringSlice(key string) []string
	GetDuration(key string) time.Duration
}

// LoadFromViper reads the "sandbox.*" keys from the provided viper-like
// source and returns a SandboxConfig with any missing keys filled from
// DefaultSandboxConfig.
//
// Key mapping (snake_case → field):
//
//	sandbox.backend              → Backend
//	sandbox.pool_min             → PoolMin
//	sandbox.pool_max_wait_ms     → PoolMaxWaitMs
//	sandbox.image_tag            → ImageTag
//	sandbox.memory_limit_mb      → MemoryLimitMB
//	sandbox.cpu_quota            → CPUQuota
//	sandbox.pids_limit           → PIDsLimit
//	sandbox.timeout_seconds      → Timeout
//	sandbox.session_timeout_seconds → SessionTimeout
//	sandbox.network_policy       → NetworkPolicy
//	sandbox.allowed_domains      → AllowedDomains
//	sandbox.workdir_size_mb      → WorkdirSizeMB
//	sandbox.read_only_rootfs     → ReadOnlyRootfs
//	sandbox.capabilities         → Capabilities
//	sandbox.seccomp_profile      → SeccompProfile
//	sandbox.apparmor_profile     → AppArmorProfile
//	sandbox.user_spec            → UserSpec
//	sandbox.broker_owner_id      → BrokerOwnerID
func LoadFromViper(v viperLike) SandboxConfig {
	cfg := DefaultSandboxConfig
	if v == nil {
		return cfg
	}
	if v.IsSet("sandbox.backend") {
		cfg.Backend = Backend(v.GetString("sandbox.backend"))
	}
	if v.IsSet("sandbox.pool_min") {
		cfg.PoolMin = v.GetInt("sandbox.pool_min")
	}
	if v.IsSet("sandbox.pool_max_wait_ms") {
		cfg.PoolMaxWaitMs = v.GetInt("sandbox.pool_max_wait_ms")
	}
	if v.IsSet("sandbox.image_tag") {
		cfg.ImageTag = v.GetString("sandbox.image_tag")
	}
	if v.IsSet("sandbox.memory_limit_mb") {
		cfg.MemoryLimitMB = v.GetInt("sandbox.memory_limit_mb")
	}
	if v.IsSet("sandbox.cpu_quota") {
		cfg.CPUQuota = v.GetFloat64("sandbox.cpu_quota")
	}
	if v.IsSet("sandbox.pids_limit") {
		cfg.PIDsLimit = v.GetInt("sandbox.pids_limit")
	}
	if v.IsSet("sandbox.timeout_seconds") {
		cfg.Timeout = time.Duration(v.GetInt("sandbox.timeout_seconds")) * time.Second
	}
	if v.IsSet("sandbox.session_timeout_seconds") {
		cfg.SessionTimeout = time.Duration(v.GetInt("sandbox.session_timeout_seconds")) * time.Second
	}
	if v.IsSet("sandbox.network_policy") {
		cfg.NetworkPolicy = NetworkPolicy(v.GetString("sandbox.network_policy"))
	}
	if v.IsSet("sandbox.allowed_domains") {
		cfg.AllowedDomains = v.GetStringSlice("sandbox.allowed_domains")
	}
	if v.IsSet("sandbox.workdir_size_mb") {
		cfg.WorkdirSizeMB = v.GetInt("sandbox.workdir_size_mb")
	}
	if v.IsSet("sandbox.read_only_rootfs") {
		cfg.ReadOnlyRootfs = v.GetBool("sandbox.read_only_rootfs")
	}
	if v.IsSet("sandbox.capabilities") {
		cfg.Capabilities = v.GetStringSlice("sandbox.capabilities")
	}
	if v.IsSet("sandbox.seccomp_profile") {
		cfg.SeccompProfile = v.GetString("sandbox.seccomp_profile")
	}
	if v.IsSet("sandbox.apparmor_profile") {
		cfg.AppArmorProfile = v.GetString("sandbox.apparmor_profile")
	}
	if v.IsSet("sandbox.user_spec") {
		cfg.UserSpec = v.GetString("sandbox.user_spec")
	}
	// Track 4 new fields.
	if v.IsSet("sandbox.skills_root") {
		cfg.SkillsRoot = v.GetString("sandbox.skills_root")
	}
	if v.IsSet("sandbox.output_max_size_mb") {
		cfg.OutputMaxSizeMB = v.GetInt("sandbox.output_max_size_mb")
	}
	if v.IsSet("sandbox.output_zip_bomb_threshold_mb") {
		cfg.OutputZipBombThresholdMB = v.GetInt("sandbox.output_zip_bomb_threshold_mb")
	}
	if v.IsSet("sandbox.cos_upload_concurrency") {
		cfg.COSUploadConcurrency = v.GetInt("sandbox.cos_upload_concurrency")
	}
	if v.IsSet("sandbox.broker_socket") {
		cfg.BrokerSocket = v.GetString("sandbox.broker_socket")
	}
	if v.IsSet("sandbox.broker_owner_id") {
		cfg.BrokerOwnerID = v.GetString("sandbox.broker_owner_id")
	}
	if v.IsSet("sandbox.broker_metadata_max_bytes") {
		cfg.BrokerMetadataMaxBytes = v.GetInt("sandbox.broker_metadata_max_bytes")
	}
	if v.IsSet("sandbox.broker_exec_output_max_bytes") {
		cfg.BrokerExecOutputMaxBytes = v.GetInt("sandbox.broker_exec_output_max_bytes")
	}
	if v.IsSet("sandbox.broker_copy_in_max_bytes") {
		cfg.BrokerCopyInMaxBytes = v.GetInt("sandbox.broker_copy_in_max_bytes")
	}
	if v.IsSet("sandbox.broker_copy_out_max_bytes") {
		cfg.BrokerCopyOutMaxBytes = v.GetInt("sandbox.broker_copy_out_max_bytes")
	}
	if v.IsSet("sandbox.broker_single_file_max_bytes") {
		cfg.BrokerSingleFileMaxBytes = v.GetInt("sandbox.broker_single_file_max_bytes")
	}
	if v.IsSet("sandbox.broker_max_files") {
		cfg.BrokerMaxFiles = v.GetInt("sandbox.broker_max_files")
	}
	if v.IsSet("sandbox.broker_max_connections") {
		cfg.BrokerMaxConnections = v.GetInt("sandbox.broker_max_connections")
	}
	return cfg
}
