package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseOptionsRequiresAbsoluteConfigAndNoExtraArgs(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseOptions([]string{"--config", "sandboxd.yaml"}, &stderr); err == nil {
		t.Fatal("relative config accepted")
	}
	if _, err := parseOptions([]string{"--config", "/tmp/sandboxd.yaml", "extra"}, &stderr); err == nil {
		t.Fatal("extra argument accepted")
	}
	opts, err := parseOptions([]string{"--config", "/tmp/sandboxd.yaml"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if opts.configPath != "/tmp/sandboxd.yaml" {
		t.Fatalf("configPath = %q", opts.configPath)
	}
}

func TestLoadSandboxdConfigRejectsBusinessSectionsAndConfigProd(t *testing.T) {
	dir := t.TempDir()
	business := filepath.Join(dir, "sandboxd.yaml")
	if err := os.WriteFile(business, []byte("sandboxd: {}\ndb:\n  host: prod\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSandboxdConfig(business); err == nil ||
		!strings.Contains(err.Error(), "business section") {
		t.Fatalf("business config err = %v", err)
	}
	configProd := filepath.Join(dir, "config_prod.yaml")
	if err := os.WriteFile(configProd, []byte("sandboxd: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSandboxdConfig(configProd); err == nil {
		t.Fatal("config_prod.yaml accepted")
	}
}

func TestLoadSandboxdConfigReadsOnlySandboxdSection(t *testing.T) {
	path := writeSandboxdConfig(t, "sandboxd.yaml", "")
	cfg, err := loadSandboxdConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BrokerInstance != "prod-slot-a" ||
		cfg.JournalPath != "/opt/numind-sandbox/journal/leases.db" ||
		cfg.DockerHost != "unix:///run/user/1000/docker.sock" ||
		len(cfg.AllowedAPIUIDs) != 1 ||
		cfg.AllowedAPIUIDs[0] != 1001 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if cfg.Runtime.ImageDigest == "" ||
		cfg.Readiness.ImageDigest != cfg.Runtime.ImageDigest {
		t.Fatalf("runtime/readiness image digest not wired: %#v", cfg.Readiness)
	}
}

func TestRunWithFactoryCleansUpAndTreatsContextCanceledAsSuccess(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "sandboxd.yaml")
	calledCleanup := false
	factory := func(context.Context, options, io.Writer) (daemonRunner, func(), error) {
		return fakeDaemonRunner{err: context.Canceled}, func() {
			calledCleanup = true
		}, nil
	}
	var stdout, stderr bytes.Buffer
	code := runWithFactory(
		[]string{"--config", configPath},
		&stdout,
		&stderr,
		factory,
	)
	if code != 0 || !calledCleanup {
		t.Fatalf("code=%d cleanup=%v stderr=%q", code, calledCleanup, stderr.String())
	}
}

type fakeDaemonRunner struct {
	err error
}

func (r fakeDaemonRunner) Run(context.Context) error {
	return r.err
}

func writeSandboxdConfig(t *testing.T, name string, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	body := `sandboxd:
  journal_path: /opt/numind-sandbox/journal/leases.db
  broker_instance: prod-slot-a
  docker_host: unix:///run/user/1000/docker.sock
  docker_config_dir: /opt/numind-sandbox/docker-config
  allowed_api_uids: [1001]
  socket:
    path: /run/numind-sandbox/sandboxd.sock
    uid: 0
    gid: 1001
    dir_uid: 0
    dir_gid: 1001
  runtime:
    image_digest: ccr.ccs.tencentyun.com/youshunumind/sandbox-skill@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    seccomp_path: /opt/numind-sandbox/seccomp/sandbox.json
    seccomp_sha256: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    allowed_skills: [document-system]
    allowed_tool_env_keys: [NUMIND_OUTPUT_FORMAT]
  capacity:
    evidence_mode: historical
    baseline_bytes: 8589934592
    parent_max_bytes: 2952790016
    workload_max_bytes: 2415919104
    workload_high_bytes: 2147483648
    workload_recovery_bytes: 1932735283
    workload_shed_bytes: 2319282339
    control_high_bytes: 268435456
    control_max_bytes: 402653184
    parent_headroom_bytes: 134217728
  readiness:
    parent_cgroup_path: /sys/fs/cgroup/numind-sandbox-control.slice
    workload_cgroup_path: /sys/fs/cgroup/numind-sandbox-control.slice/numind-sandbox-workload.slice
    data_root_path: /opt/numind-sandbox/data-root
    data_root_uuid: 11111111-1111-1111-1111-111111111111
`
	if err := os.WriteFile(path, []byte(body+extra), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunWithFactoryReturnsSetupFailure(t *testing.T) {
	factoryErr := errors.New("boom")
	factory := func(context.Context, options, io.Writer) (daemonRunner, func(), error) {
		return nil, nil, factoryErr
	}
	var stdout, stderr bytes.Buffer
	code := runWithFactory(
		[]string{"--config", "/tmp/sandboxd.yaml"},
		&stdout,
		&stderr,
		factory,
	)
	if code != 1 || !strings.Contains(stderr.String(), "setup failed") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
