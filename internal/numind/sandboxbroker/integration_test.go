package sandboxbroker

import (
	"context"
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

func TestIntegrationSecurityContractSchedulerFiveSlotsFIFOAcrossOwners(t *testing.T) {
	scheduler := newScheduler(
		SchedulerTotalContainerMax,
		SchedulerActiveTaskMax,
		SchedulerQueueWaitTimeout,
	)
	for i := 1; i <= 5; i++ {
		owner := "api-blue"
		if i%2 == 0 {
			owner = "api-green"
		}
		req := SchedulerRequest{
			RequestID: "request-" + string(rune('0'+i)),
			LeaseID:   "lease-" + string(rune('0'+i)),
			OwnerID:   owner,
		}
		if err := scheduler.Acquire(context.Background(), req); err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
		if err := scheduler.MarkReady(req.LeaseID); err != nil {
			t.Fatalf("MarkReady %d: %v", i, err)
		}
		if err := scheduler.Activate(req.LeaseID); err != nil {
			t.Fatalf("Activate %d: %v", i, err)
		}
	}
	snapshot := scheduler.Snapshot()
	if snapshot.Containers != 5 || snapshot.Active != 5 || snapshot.Queued != 0 {
		t.Fatalf("initial snapshot = %+v; want 5 containers, 5 active, 0 queued", snapshot)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan error, 1)
	go func() {
		waiting <- scheduler.Acquire(ctx, SchedulerRequest{
			RequestID: "request-6",
			LeaseID:   "lease-6",
			OwnerID:   "api-green",
		})
	}()

	deadline := time.After(500 * time.Millisecond)
	for {
		snapshot = scheduler.Snapshot()
		if reflect.DeepEqual(snapshot.QueueRequestIDs, []string{"request-6"}) {
			break
		}
		select {
		case err := <-waiting:
			t.Fatalf("sixth request returned before a slot was released: %v", err)
		case <-deadline:
			t.Fatalf("sixth request did not enter FIFO: %+v", snapshot)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	if err := scheduler.Release("lease-1"); err != nil {
		t.Fatalf("Release first lease: %v", err)
	}
	select {
	case err := <-waiting:
		if err != nil {
			t.Fatalf("sixth request after release: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sixth FIFO request was not granted after release")
	}
	if err := scheduler.MarkReady("lease-6"); err != nil {
		t.Fatalf("MarkReady sixth: %v", err)
	}
	if err := scheduler.Activate("lease-6"); err != nil {
		t.Fatalf("Activate sixth: %v", err)
	}
	snapshot = scheduler.Snapshot()
	if snapshot.Containers != 5 || snapshot.Active != 5 || snapshot.Queued != 0 {
		t.Fatalf("final snapshot = %+v; want 5 containers, 5 active, 0 queued", snapshot)
	}
}

func TestIntegrationSecurityContractRuntimePolicyFixedContainerAndExecLimits(t *testing.T) {
	seccompPath := filepath.Join(secureTempDir(t), "seccomp.json")
	seccompBody := []byte(`{"defaultAction":"SCMP_ACT_ERRNO"}`)
	if err := os.WriteFile(seccompPath, seccompBody, 0o400); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(seccompBody)
	policy, err := NewRuntimePolicy(RuntimeConfig{
		ImageDigest:        SandboxImageRepository + "@sha256:" + strings.Repeat("a", 64),
		SeccompPath:        seccompPath,
		SeccompSHA256:      "sha256:" + hex.EncodeToString(hash[:]),
		AllowedSkills:      []string{"pptx-author", "docx-author"},
		AllowedToolEnvKeys: []string{"NUMIND_OUTPUT_FORMAT"},
	})
	if err != nil {
		t.Fatalf("NewRuntimePolicy: %v", err)
	}

	spec, err := policy.spawnSpec("lease-1", "broker-primary", runtimeSeccompFDPath)
	if err != nil {
		t.Fatalf("spawnSpec: %v", err)
	}
	if spec.MemoryBytes != 512<<20 ||
		spec.NanoCPUs != 1_000_000_000 ||
		spec.PIDsLimit != 64 ||
		!spec.ReadOnly ||
		spec.Network != "none" ||
		spec.CgroupParent != SandboxWorkloadCgroupParent ||
		spec.User != "1000:1000" {
		t.Fatalf("runtime fixed limits drifted: %+v", spec)
	}
	if !reflect.DeepEqual(spec.CapDrop, []string{"ALL"}) {
		t.Fatalf("CapDrop = %v; want ALL only", spec.CapDrop)
	}
	joinedArgs := strings.Join(spec.dockerArgs(), " ")
	for _, forbidden := range []string{
		"--privileged",
		"--cap-add",
		"/var/run/docker.sock",
		"/run/docker.sock",
		"--network=host",
		"--device",
	} {
		if strings.Contains(joinedArgs, forbidden) {
			t.Fatalf("runtime docker args contain forbidden fragment %q: %s", forbidden, joinedArgs)
		}
	}

	execSpec, err := policy.execSpec(
		[]string{"/bin/sh", "-lc", "printf ok"},
		[]string{"NUMIND_OUTPUT_FORMAT=json", "LANG=C.UTF-8"},
	)
	if err != nil {
		t.Fatalf("execSpec allowed env: %v", err)
	}
	if execSpec.Timeout != RuntimeExecTimeout || execSpec.OutputMaxBytes != MaxExecOutputBytes {
		t.Fatalf("exec fixed limits drifted: %+v", execSpec)
	}
	_, err = policy.execSpec([]string{"/bin/sh"}, []string{"NUMIND_LLM_API_KEY=secret"})
	if !errors.Is(err, ErrRuntimePolicyDenied) {
		t.Fatalf("sensitive env should be denied, got %v", err)
	}
}

func TestIntegrationSecurityContractBrokerTransportLimits(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.SocketDirectoryUID = 0
	cfg.SocketDirectoryGID = 1999
	cfg.SocketUID = 1998
	cfg.SocketGID = 1999
	if err := cfg.validate(); err != nil {
		t.Fatalf("default server config validate: %v", err)
	}
	if cfg.SocketPath != DefaultServerSocketPath ||
		ServerSocketDirectoryMode != 0o2770 ||
		ServerSocketMode != 0o660 ||
		cfg.MetadataMaxBytes != 64<<10 ||
		cfg.MaxConnections != 32 ||
		cfg.MaxCopyStreams != 4 ||
		cfg.MaxLeaseDirectionStreams != 1 ||
		cfg.AggregateCopyBytesPerSecond != 100<<20 {
		t.Fatalf("broker transport limits drifted: %+v", cfg)
	}
	cfg.MetadataMaxBytes = ServerMetadataMaxBytes + 1
	if !errors.Is(cfg.validate(), ErrInvalidServerConfig) {
		t.Fatal("server config should reject metadata limit above 64KiB")
	}
}
