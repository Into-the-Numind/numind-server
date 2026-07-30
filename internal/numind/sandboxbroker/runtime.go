package sandboxbroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	// SandboxImageRepository is the only production repository sandboxd may run.
	SandboxImageRepository = "ccr.ccs.tencentyun.com/youshunumind/sandbox-skill"
	// SandboxWorkloadCgroupParent is the fixed delegated workload slice.
	SandboxWorkloadCgroupParent = "numind-sandbox-workload.slice"
	// SandboxDockerBinary is the only Docker CLI executable used by sandboxd.
	SandboxDockerBinary = "/usr/bin/docker"
	// RuntimeExecTimeout is the hard wall-clock limit for one exec.
	RuntimeExecTimeout = 30 * time.Second
	// RuntimeSessionTimeout is the hard absolute lifetime of one active lease.
	RuntimeSessionTimeout = MaxActiveLeaseDuration
	// runtimeSeccompFDPath is inherited as child fd 3 for each Docker CLI call.
	runtimeSeccompFDPath   = "/dev/fd/3"
	maxSeccompProfileBytes = 1 << 20
)

var (
	// ErrRuntimePolicyDenied means runtime configuration or caller input is unsafe.
	ErrRuntimePolicyDenied = errors.New("sandbox runtime policy denied")
	// ErrRuntimeIntegrity means a fixed runtime artifact failed integrity checks.
	ErrRuntimeIntegrity = errors.New("sandbox runtime integrity check failed")
)

// RuntimeConfig contains only root-owned inputs that cannot come from broker RPC.
type RuntimeConfig struct {
	ImageDigest        string
	SeccompPath        string
	SeccompSHA256      string
	AllowedSkills      []string
	AllowedToolEnvKeys []string
}

// RuntimePolicy owns the verified immutable container and exec templates.
type RuntimePolicy struct {
	imageDigest    string
	seccompProfile []byte
	allowedSkills  map[string]struct{}
	allowedEnv     map[string]struct{}
}

type runtimeSpawnSpec struct {
	Image          string
	User           string
	SecurityOpts   []string
	CapDrop        []string
	MemoryBytes    int64
	NanoCPUs       int64
	PIDsLimit      int
	ReadOnly       bool
	Network        string
	Workdir        string
	Tmpfs          []string
	CgroupParent   string
	Labels         []string
	Entrypoint     string
	EntrypointArgs []string
}

type runtimeExecSpec struct {
	Argv           []string
	Env            []string
	User           string
	Workdir        string
	Timeout        time.Duration
	OutputMaxBytes int64
}

// NewRuntimePolicy verifies the pinned image and Seccomp artifact once at startup.
func NewRuntimePolicy(cfg RuntimeConfig) (*RuntimePolicy, error) {
	if !validPinnedImage(cfg.ImageDigest) ||
		!filepath.IsAbs(cfg.SeccompPath) ||
		filepath.Clean(cfg.SeccompPath) != cfg.SeccompPath ||
		!validSHA256(cfg.SeccompSHA256) {
		return nil, ErrRuntimePolicyDenied
	}
	seccompIdentity, seccompProfile, err := readVerifiedSeccomp(cfg.SeccompPath)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(seccompIdentity.sha256, cfg.SeccompSHA256) {
		return nil, ErrRuntimeIntegrity
	}

	policy := &RuntimePolicy{
		imageDigest:    cfg.ImageDigest,
		seccompProfile: append([]byte(nil), seccompProfile...),
		allowedSkills:  make(map[string]struct{}, len(cfg.AllowedSkills)),
		allowedEnv:     make(map[string]struct{}, len(cfg.AllowedToolEnvKeys)),
	}
	for _, skill := range cfg.AllowedSkills {
		if !safeRuntimeToken(skill) {
			return nil, ErrRuntimePolicyDenied
		}
		if _, duplicate := policy.allowedSkills[skill]; duplicate {
			return nil, ErrRuntimePolicyDenied
		}
		policy.allowedSkills[skill] = struct{}{}
	}
	for _, key := range cfg.AllowedToolEnvKeys {
		if !validEnvKey(key) || isSensitiveEnvKey(key) ||
			key == "LANG" || key == "TZ" || strings.HasPrefix(key, "LC_") {
			return nil, ErrRuntimePolicyDenied
		}
		if _, duplicate := policy.allowedEnv[key]; duplicate {
			return nil, ErrRuntimePolicyDenied
		}
		policy.allowedEnv[key] = struct{}{}
	}
	return policy, nil
}

func (p *RuntimePolicy) spawnSpec(
	leaseID string,
	brokerInstance string,
	seccompPath string,
) (runtimeSpawnSpec, error) {
	if p == nil ||
		!safeRuntimeToken(leaseID) ||
		!safeRuntimeToken(brokerInstance) ||
		seccompPath != runtimeSeccompFDPath {
		return runtimeSpawnSpec{}, ErrRuntimePolicyDenied
	}
	return runtimeSpawnSpec{
		Image:        p.imageDigest,
		User:         "1000:1000",
		SecurityOpts: []string{"no-new-privileges", "seccomp=" + seccompPath},
		CapDrop:      []string{"ALL"},
		MemoryBytes:  512 << 20,
		NanoCPUs:     1_000_000_000,
		PIDsLimit:    64,
		ReadOnly:     true,
		Network:      "none",
		Workdir:      "/workdir",
		Tmpfs: []string{
			"/workdir:rw,nodev,nosuid,size=536870912,uid=1000,gid=1000,mode=0700",
			"/skills:rw,nodev,nosuid,size=67108864,uid=1000,gid=1000,mode=0700",
			"/tmp:rw,nodev,nosuid,size=67108864,uid=1000,gid=1000,mode=0700",
		},
		CgroupParent: SandboxWorkloadCgroupParent,
		Labels: []string{
			"numind.sandbox=1",
			"numind.sandbox.lease_id=" + leaseID,
			"numind.sandbox.broker_instance=" + brokerInstance,
		},
		Entrypoint:     "/bin/sh",
		EntrypointArgs: []string{"-c", "exec sleep infinity"},
	}, nil
}

func (p *RuntimePolicy) dockerSpawnCommand(
	ctx context.Context,
	binary string,
	leaseID string,
	brokerInstance string,
) (*exec.Cmd, *os.File, error) {
	if p == nil {
		return nil, nil, ErrRuntimePolicyDenied
	}
	seccompFile, err := newAnonymousSeccompFile(p.seccompProfile)
	if err != nil {
		return nil, nil, err
	}
	spec, err := p.spawnSpec(leaseID, brokerInstance, runtimeSeccompFDPath)
	if err != nil {
		_ = seccompFile.Close()
		return nil, nil, err
	}
	command := exec.CommandContext(ctx, binary, spec.dockerArgs()...)
	command.ExtraFiles = []*os.File{seccompFile}
	return command, seccompFile, nil
}

// RunDockerSpawn executes the fixed Docker launch while keeping the verified
// Seccomp inode open and inherited until the Docker CLI has consumed it.
func (p *RuntimePolicy) RunDockerSpawn(
	ctx context.Context,
	leaseID string,
	brokerInstance string,
) ([]byte, error) {
	command, seccompFile, err := p.dockerSpawnCommand(
		ctx,
		SandboxDockerBinary,
		leaseID,
		brokerInstance,
	)
	if err != nil {
		return nil, err
	}
	defer seccompFile.Close()
	return command.Output()
}

func (s runtimeSpawnSpec) dockerArgs() []string {
	args := []string{
		"run",
		"--detach",
		"--pull=never",
		"--user=" + s.User,
		"--cap-drop=ALL",
		"--memory=512m",
		"--cpus=1.0",
		"--pids-limit=64",
		"--read-only",
		"--network=none",
		"--workdir=/workdir",
		"--cgroup-parent=" + SandboxWorkloadCgroupParent,
	}
	for _, option := range s.SecurityOpts {
		args = append(args, "--security-opt="+option)
	}
	for _, tmpfs := range s.Tmpfs {
		args = append(args, "--tmpfs="+tmpfs)
	}
	for _, label := range s.Labels {
		args = append(args, "--label="+label)
	}
	args = append(args, "--entrypoint="+s.Entrypoint, s.Image)
	args = append(args, s.EntrypointArgs...)
	return args
}

func (p *RuntimePolicy) execSpec(argv []string, env []string) (runtimeExecSpec, error) {
	if p == nil || len(argv) == 0 || len(argv) > 128 {
		return runtimeExecSpec{}, ErrRuntimePolicyDenied
	}
	totalBytes := 0
	for _, arg := range argv {
		totalBytes += len(arg)
		if arg == "" || strings.ContainsRune(arg, 0) || totalBytes > 64<<10 {
			return runtimeExecSpec{}, ErrRuntimePolicyDenied
		}
	}
	if len(env) > 64 {
		return runtimeExecSpec{}, ErrRuntimePolicyDenied
	}
	seen := make(map[string]struct{}, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		totalBytes += len(item)
		if !ok || !validEnvKey(key) || strings.ContainsRune(value, 0) ||
			len(value) > 4096 || totalBytes > 64<<10 {
			return runtimeExecSpec{}, ErrRuntimePolicyDenied
		}
		if _, duplicate := seen[key]; duplicate {
			return runtimeExecSpec{}, ErrRuntimePolicyDenied
		}
		seen[key] = struct{}{}
		if !p.envKeyAllowed(key) {
			return runtimeExecSpec{}, ErrRuntimePolicyDenied
		}
	}
	return runtimeExecSpec{
		Argv:           append([]string(nil), argv...),
		Env:            append([]string(nil), env...),
		User:           "1000:1000",
		Workdir:        "/workdir",
		Timeout:        RuntimeExecTimeout,
		OutputMaxBytes: MaxExecOutputBytes,
	}, nil
}

// CopyInPath validates a target against the image skill allowlist.
func (p *RuntimePolicy) CopyInPath(raw string) (string, error) {
	if p == nil {
		return "", ErrRuntimePolicyDenied
	}
	skills := make([]string, 0, len(p.allowedSkills))
	for skill := range p.allowedSkills {
		skills = append(skills, skill)
	}
	return CanonicalCopyInPath(raw, skills)
}

func (p *RuntimePolicy) envKeyAllowed(key string) bool {
	if isSensitiveEnvKey(key) {
		return false
	}
	if key == "LANG" || key == "TZ" ||
		(strings.HasPrefix(key, "LC_") && len(key) > len("LC_")) {
		return true
	}
	_, ok := p.allowedEnv[key]
	return ok
}

type verifiedSeccompIdentity struct {
	device uint64
	inode  uint64
	size   int64
	sha256 string
}

func readVerifiedSeccomp(
	filePath string,
) (verifiedSeccompIdentity, []byte, error) {
	parentFD, err := openExistingDirectoryNoFollow(filepath.Dir(filePath), true)
	if err != nil {
		if errors.Is(err, unix.ELOOP) ||
			errors.Is(err, unix.ENOTDIR) ||
			errors.Is(err, unix.EPERM) ||
			errors.Is(err, unix.EINVAL) {
			return verifiedSeccompIdentity{}, nil, ErrRuntimePolicyDenied
		}
		return verifiedSeccompIdentity{}, nil, fmt.Errorf(
			"%w: open seccomp parent",
			ErrRuntimeIntegrity,
		)
	}
	defer unix.Close(parentFD)

	fd, err := unix.Openat(
		parentFD,
		filepath.Base(filePath),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return verifiedSeccompIdentity{}, nil, ErrRuntimePolicyDenied
		}
		return verifiedSeccompIdentity{}, nil, fmt.Errorf(
			"%w: open seccomp",
			ErrRuntimeIntegrity,
		)
	}
	file := os.NewFile(uintptr(fd), "sandbox-seccomp")

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return verifiedSeccompIdentity{}, nil, fmt.Errorf(
			"%w: inspect seccomp",
			ErrRuntimeIntegrity,
		)
	}
	mode := uint32(stat.Mode)
	ownerAllowed := stat.Uid == 0 || stat.Uid == uint32(os.Geteuid())
	if mode&unix.S_IFMT != unix.S_IFREG ||
		!ownerAllowed ||
		uint64(stat.Nlink) != 1 ||
		os.FileMode(mode).Perm()&0o222 != 0 {
		_ = file.Close()
		return verifiedSeccompIdentity{}, nil, ErrRuntimePolicyDenied
	}
	profile, err := io.ReadAll(io.LimitReader(file, maxSeccompProfileBytes+1))
	if err != nil {
		_ = file.Close()
		return verifiedSeccompIdentity{}, nil, fmt.Errorf(
			"%w: read seccomp",
			ErrRuntimeIntegrity,
		)
	}
	_ = file.Close()
	if len(profile) == 0 || len(profile) > maxSeccompProfileBytes {
		return verifiedSeccompIdentity{}, nil, ErrRuntimePolicyDenied
	}
	hash := sha256.Sum256(profile)
	return verifiedSeccompIdentity{
		device: uint64(stat.Dev),
		inode:  uint64(stat.Ino),
		size:   stat.Size,
		sha256: "sha256:" + hex.EncodeToString(hash[:]),
	}, profile, nil
}

func newAnonymousSeccompFile(profile []byte) (*os.File, error) {
	if len(profile) == 0 || len(profile) > maxSeccompProfileBytes {
		return nil, ErrRuntimePolicyDenied
	}
	file, err := os.CreateTemp("", ".numind-seccomp-*")
	if err != nil {
		return nil, fmt.Errorf("%w: create anonymous seccomp", ErrRuntimeIntegrity)
	}
	name := file.Name()
	if err := os.Remove(name); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: unlink anonymous seccomp", ErrRuntimeIntegrity)
	}
	if _, err := file.Write(profile); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: write anonymous seccomp", ErrRuntimeIntegrity)
	}
	if err := file.Chmod(0o400); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: protect anonymous seccomp", ErrRuntimeIntegrity)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: rewind anonymous seccomp", ErrRuntimeIntegrity)
	}
	return file, nil
}

func validPinnedImage(image string) bool {
	const prefix = SandboxImageRepository + "@sha256:"
	if !strings.HasPrefix(image, prefix) {
		return false
	}
	digest := strings.TrimPrefix(image, prefix)
	return len(digest) == 64 && isLowerHex(digest)
}

func validSHA256(value string) bool {
	const prefix = "sha256:"
	return strings.HasPrefix(value, prefix) &&
		len(value) == len(prefix)+64 &&
		isLowerHex(strings.TrimPrefix(value, prefix))
}

func isLowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func safeRuntimeToken(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') &&
			(char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '.' && char != '_' && char != '-' {
			return false
		}
	}
	return true
}

func validEnvKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for index, char := range key {
		if index == 0 {
			if (char < 'A' || char > 'Z') && char != '_' {
				return false
			}
			continue
		}
		if (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') &&
			char != '_' {
			return false
		}
	}
	return true
}

func isSensitiveEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	switch upper {
	case "HOME", "PATH", "HOSTNAME", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
		"NO_PROXY", "DOCKER_HOST", "DOCKER_CONFIG":
		return true
	}
	for _, fragment := range []string{
		"TOKEN",
		"SECRET",
		"PASSWORD",
		"CREDENTIAL",
		"API_KEY",
		"ACCESS_KEY",
		"PRIVATE_KEY",
		"DATABASE",
		"MYSQL",
		"REDIS",
		"COS_",
		"FEISHU",
		"LARK_",
		"OPENAI",
		"AWS_",
		"TENCENT",
	} {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	return strings.HasPrefix(upper, "DOCKER_")
}
