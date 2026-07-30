package sandboxbroker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRuntimeSpawnSpecIsFullyFixed(t *testing.T) {
	policy := testRuntimePolicy(t)
	spec, err := policy.SpawnSpec("lease-123", "broker-primary")
	if err != nil {
		t.Fatal(err)
	}
	wantImage := SandboxImageRepository + "@sha256:" + strings.Repeat("a", 64)
	if spec.Image != wantImage ||
		spec.User != "1000:1000" ||
		spec.MemoryBytes != 512<<20 ||
		spec.NanoCPUs != 1_000_000_000 ||
		spec.PIDsLimit != 64 ||
		!spec.ReadOnly ||
		spec.Network != "none" ||
		spec.Workdir != "/workdir" ||
		spec.CgroupParent != SandboxWorkloadCgroupParent ||
		spec.Entrypoint != "/bin/sh" {
		t.Fatalf("unexpected fixed spawn spec: %#v", spec)
	}
	if !reflect.DeepEqual(spec.CapDrop, []string{"ALL"}) {
		t.Fatalf("CapDrop = %v", spec.CapDrop)
	}
	if !reflect.DeepEqual(spec.SecurityOpts, []string{
		"no-new-privileges",
		"seccomp=" + policy.seccompPath,
	}) {
		t.Fatalf("SecurityOpts = %v", spec.SecurityOpts)
	}
	wantTmpfs := []string{
		"/workdir:rw,nodev,nosuid,size=536870912,uid=1000,gid=1000,mode=0700",
		"/skills:rw,nodev,nosuid,size=67108864,uid=1000,gid=1000,mode=0700",
		"/tmp:rw,nodev,nosuid,size=67108864,uid=1000,gid=1000,mode=0700",
	}
	if !reflect.DeepEqual(spec.Tmpfs, wantTmpfs) {
		t.Fatalf("Tmpfs = %v", spec.Tmpfs)
	}
	wantLabels := []string{
		"numind.sandbox=1",
		"numind.sandbox.lease_id=lease-123",
		"numind.sandbox.broker_instance=broker-primary",
	}
	if !reflect.DeepEqual(spec.Labels, wantLabels) {
		t.Fatalf("Labels = %v", spec.Labels)
	}
	if !reflect.DeepEqual(spec.EntrypointArgs, []string{"-c", "exec sleep infinity"}) {
		t.Fatalf("EntrypointArgs = %v", spec.EntrypointArgs)
	}

	args, err := policy.DockerSpawnArgs("lease-123", "broker-primary")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{
		"--pull=never",
		"--user=1000:1000",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		"--security-opt=seccomp=",
		"--memory=512m",
		"--cpus=1.0",
		"--pids-limit=64",
		"--read-only",
		"--network=none",
		"--workdir=/workdir",
		"--cgroup-parent=" + SandboxWorkloadCgroupParent,
		wantImage,
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("docker args missing %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{
		"--volume",
		"--mount",
		"--device",
		"--privileged",
		"--cap-add",
		"apparmor=",
		"/var/run/docker.sock",
	} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("docker args contain forbidden option %q: %s", forbidden, joined)
		}
	}
}

func TestRuntimeDockerSpawnArgsCannotBeBuiltFromMutableSpec(t *testing.T) {
	specType := reflect.TypeOf(RuntimeSpawnSpec{})
	if _, exported := specType.MethodByName("DockerArgs"); exported {
		t.Fatal("RuntimeSpawnSpec exposes an executable DockerArgs method")
	}
	policy := testRuntimePolicy(t)
	if _, err := policy.DockerSpawnArgs("bad=lease", "broker-primary"); !errors.Is(err, ErrRuntimePolicyDenied) {
		t.Fatalf("DockerSpawnArgs unsafe label err = %v", err)
	}
}

func TestRuntimePolicyRejectsMutableOrUnverifiedArtifacts(t *testing.T) {
	base := testRuntimeConfig(t)
	tests := []struct {
		name   string
		mutate func(*RuntimeConfig)
	}{
		{name: "tag instead of digest", mutate: func(c *RuntimeConfig) {
			c.ImageDigest = SandboxImageRepository + ":skills-v1.5.3"
		}},
		{name: "wrong repository", mutate: func(c *RuntimeConfig) {
			c.ImageDigest = "docker.io/library/alpine@sha256:" + strings.Repeat("a", 64)
		}},
		{name: "uppercase digest", mutate: func(c *RuntimeConfig) {
			c.ImageDigest = SandboxImageRepository + "@sha256:" + strings.Repeat("A", 64)
		}},
		{name: "relative seccomp", mutate: func(c *RuntimeConfig) {
			c.SeccompPath = "seccomp.json"
		}},
		{name: "wrong seccomp checksum", mutate: func(c *RuntimeConfig) {
			c.SeccompSHA256 = "sha256:" + strings.Repeat("0", 64)
		}},
		{name: "unsafe skill", mutate: func(c *RuntimeConfig) {
			c.AllowedSkills = []string{"../../host"}
		}},
		{name: "sensitive tool env", mutate: func(c *RuntimeConfig) {
			c.AllowedToolEnvKeys = []string{"FEISHU_SECRET"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.mutate(&cfg)
			if _, err := NewRuntimePolicy(cfg); err == nil {
				t.Fatal("unsafe runtime config accepted")
			}
		})
	}
}

func TestRuntimePolicyRejectsUnsafeSeccompFilesystemTargets(t *testing.T) {
	for _, target := range []string{"symlink", "hardlink", "writable"} {
		t.Run(target, func(t *testing.T) {
			parent := t.TempDir()
			victim := filepath.Join(parent, "victim.json")
			body := []byte(`{"defaultAction":"SCMP_ACT_ERRNO"}`)
			if err := os.WriteFile(victim, body, 0o600); err != nil {
				t.Fatal(err)
			}
			seccompPath := filepath.Join(parent, "seccomp.json")
			switch target {
			case "symlink":
				if err := os.Symlink(victim, seccompPath); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(victim, seccompPath); err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.WriteFile(seccompPath, body, 0o666); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(seccompPath, 0o666); err != nil {
					t.Fatal(err)
				}
			}
			sum := sha256.Sum256(body)
			cfg := RuntimeConfig{
				ImageDigest:   SandboxImageRepository + "@sha256:" + strings.Repeat("a", 64),
				SeccompPath:   seccompPath,
				SeccompSHA256: "sha256:" + hex.EncodeToString(sum[:]),
			}
			if _, err := NewRuntimePolicy(cfg); !errors.Is(err, ErrRuntimePolicyDenied) {
				t.Fatalf("NewRuntimePolicy err = %v; want policy denied", err)
			}
		})
	}
}

func TestRuntimeExecSpecFixesIdentityLimitsAndEnvAllowlist(t *testing.T) {
	policy := testRuntimePolicy(t)
	argv := []string{"/bin/sh", "-c", "python /workdir/task.py"}
	env := []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=Asia/Shanghai", "NUMIND_OUTPUT_FORMAT=pdf"}
	spec, err := policy.ExecSpec(argv, env)
	if err != nil {
		t.Fatal(err)
	}
	if spec.User != "1000:1000" ||
		spec.Workdir != "/workdir" ||
		spec.Timeout != 30*time.Second ||
		spec.OutputMaxBytes != 4<<20 {
		t.Fatalf("unexpected fixed exec spec: %#v", spec)
	}
	argv[0] = "mutated"
	env[0] = "HOME=/root"
	if spec.Argv[0] != "/bin/sh" || spec.Env[0] != "LANG=C.UTF-8" {
		t.Fatal("ExecSpec retained caller-owned slices")
	}

	for _, denied := range [][]string{
		{"HOME=/root"},
		{"PATH=/tmp"},
		{"DOCKER_HOST=unix:///var/run/docker.sock"},
		{"HTTPS_PROXY=http://metadata"},
		{"FEISHU_SECRET=value"},
		{"OPENAI_API_KEY=value"},
		{"LANG=C", "LANG=en_US"},
		{"lowercase=value"},
		{"UNKNOWN=value"},
		{"LANG=no\x00pe"},
	} {
		if _, err := policy.ExecSpec([]string{"true"}, denied); !errors.Is(err, ErrRuntimePolicyDenied) {
			t.Fatalf("env %v err = %v; want policy denied", denied, err)
		}
	}
}

func TestRuntimeCopyInPathUsesFixedSkillAllowlist(t *testing.T) {
	policy := testRuntimePolicy(t)
	for _, allowed := range []string{
		"/workdir/task.py",
		"/workdir/input/source.csv",
		"/skills/document-system/SKILL.md",
	} {
		if got, err := policy.CopyInPath(allowed); err != nil || got != allowed {
			t.Fatalf("CopyInPath(%q) = %q, %v", allowed, got, err)
		}
	}
	for _, denied := range []string{
		"/etc/passwd",
		"/workdir/../etc/passwd",
		"/skills/not-allowed/SKILL.md",
		"/skills/document-system",
	} {
		if _, err := policy.CopyInPath(denied); !errors.Is(err, ErrStreamPolicyDenied) {
			t.Fatalf("CopyInPath(%q) err = %v; want stream policy denied", denied, err)
		}
	}
}

func TestRuntimeSpawnSpecRejectsLabelInjection(t *testing.T) {
	policy := testRuntimePolicy(t)
	for _, values := range [][2]string{
		{"lease=other", "broker-primary"},
		{"lease-1", "broker,secondary"},
		{"", "broker-primary"},
	} {
		if _, err := policy.SpawnSpec(values[0], values[1]); !errors.Is(err, ErrRuntimePolicyDenied) {
			t.Fatalf("SpawnSpec(%q,%q) err = %v", values[0], values[1], err)
		}
	}
}

func testRuntimePolicy(t *testing.T) *RuntimePolicy {
	t.Helper()
	policy, err := NewRuntimePolicy(testRuntimeConfig(t))
	if err != nil {
		t.Fatalf("NewRuntimePolicy: %v", err)
	}
	return policy
}

func testRuntimeConfig(t *testing.T) RuntimeConfig {
	t.Helper()
	body := []byte(`{"defaultAction":"SCMP_ACT_ERRNO"}`)
	seccompPath := filepath.Join(t.TempDir(), "seccomp.json")
	if err := os.WriteFile(seccompPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return RuntimeConfig{
		ImageDigest:        SandboxImageRepository + "@sha256:" + strings.Repeat("a", 64),
		SeccompPath:        seccompPath,
		SeccompSHA256:      "sha256:" + hex.EncodeToString(sum[:]),
		AllowedSkills:      []string{"document-system"},
		AllowedToolEnvKeys: []string{"NUMIND_OUTPUT_FORMAT"},
	}
}
