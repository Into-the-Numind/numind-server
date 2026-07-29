package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func startBrokerTestServer(t *testing.T, handler http.Handler) string {
	t.Helper()
	socketDir, err := os.MkdirTemp("/tmp", "sbd-test-")
	if err != nil {
		t.Fatalf("short socket temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "sandboxd.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		// Tests intentionally include handlers that wait for client
		// cancellation. Close active connections immediately; a graceful
		// Shutdown would wait for such handlers before cancelling them.
		_ = srv.Close()
		_ = ln.Close()
	})
	return socket
}

func brokerTestConfig(socket string) SandboxConfig {
	cfg := DefaultSandboxConfig
	cfg.Backend = BackendBroker
	cfg.BrokerSocket = socket
	cfg.BrokerMetadataMaxBytes = 64 << 10
	cfg.BrokerExecOutputMaxBytes = 4 << 20
	cfg.BrokerCopyInMaxBytes = 100 << 20
	cfg.BrokerCopyOutMaxBytes = 200 << 20
	cfg.BrokerSingleFileMaxBytes = 50 << 20
	cfg.BrokerMaxFiles = 10
	cfg.BrokerMaxConnections = 32
	return cfg
}

func TestBrokerDockerClientImplementsContract(t *testing.T) {
	var mu sync.Mutex
	var createKeys []string
	var copiedIn []byte

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/leases", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var raw map[string]any
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Errorf("decode create request: %v", err)
				http.Error(w, "bad", http.StatusBadRequest)
				return
			}
			mu.Lock()
			for k := range raw {
				createKeys = append(createKeys, k)
			}
			mu.Unlock()
			writeBrokerJSONForTest(t, w, BrokerCreateLeaseResponse{
				LeaseID: "lease-1",
				State:   "ready",
			})
		case http.MethodGet:
			if got := r.URL.Query().Get("owner_id"); got == "" {
				t.Error("list request missing owner_id")
			}
			writeBrokerJSONForTest(t, w, BrokerListLeasesResponse{LeaseIDs: []string{"lease-old"}})
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/leases/lease-1/exec", func(w http.ResponseWriter, r *http.Request) {
		var req BrokerExecRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode exec request: %v", err)
		}
		if strings.Join(req.Argv, " ") != "/bin/sh -c echo ok" {
			t.Errorf("exec argv = %v", req.Argv)
		}
		writeBrokerJSONForTest(t, w, BrokerExecResponse{
			Stdout:   "ok\n",
			ExitCode: 0,
			Duration: 12 * time.Millisecond,
		})
	})
	mux.HandleFunc("/v1/leases/lease-1/files", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			if got := r.URL.Query().Get("path"); got != "/workdir/input.txt" {
				t.Errorf("copy-in path = %q", got)
			}
			copiedIn, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/x-tar")
			tw := tar.NewWriter(w)
			body := []byte("artifact")
			_ = tw.WriteHeader(&tar.Header{Name: "result.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
			_, _ = tw.Write(body)
			_ = tw.Close()
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/leases/lease-1/mkdir", func(w http.ResponseWriter, r *http.Request) {
		var req BrokerMkdirRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if len(req.Dirs) != 2 {
			t.Errorf("mkdir dirs = %v", req.Dirs)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/leases/lease-1", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeBrokerJSONForTest(t, w, BrokerInspectResponse{
				Status:      "running",
				OwnerID:     "api-owner",
				OwnerBootID: "boot-1",
			})
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})

	socket := startBrokerTestServer(t, mux)
	dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
	if err != nil {
		t.Fatalf("NewBrokerDockerClient: %v", err)
	}

	spawnCfg := SpawnConfig{Labels: []string{
		SandboxContainerLabel,
		SandboxContainerOwnerLabelKey + "=api-owner",
		SandboxContainerOwnerBootLabelKey + "=boot-1",
	}, ImageTag: "must-not-cross-wire", Volumes: []string{"/host:/container"}}
	id, err := dc.Spawn(context.Background(), spawnCfg)
	if err != nil || id != "lease-1" {
		t.Fatalf("Spawn = id:%q err:%v", id, err)
	}

	mu.Lock()
	sort.Strings(createKeys)
	gotKeys := append([]string(nil), createKeys...)
	mu.Unlock()
	wantKeys := []string{"owner_boot_id", "owner_id", "request_id"}
	if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("create request keys = %v; want only %v", gotKeys, wantKeys)
	}

	execRes, err := dc.Exec(context.Background(), id, []string{"/bin/sh", "-c", "echo ok"}, ExecOpts{
		Timeout: 30 * time.Second,
		Workdir: "/must/not/cross",
		User:    "0:0",
		Env:     []string{"LANG=zh_CN.UTF-8"},
	})
	if err != nil || execRes.Stdout != "ok\n" || execRes.ExitCode != 0 {
		t.Fatalf("Exec = %#v err:%v", execRes, err)
	}

	if err := dc.CopyToContainer(context.Background(), id, "/workdir/input.txt", strings.NewReader("input")); err != nil {
		t.Fatalf("CopyToContainer: %v", err)
	}
	if string(copiedIn) != "input" {
		t.Fatalf("copied input = %q", copiedIn)
	}

	dst := t.TempDir()
	if err := dc.CopyFromContainer(context.Background(), id, "/workdir/output/.", dst); err != nil {
		t.Fatalf("CopyFromContainer: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dst, "result.txt"))
	if err != nil || string(out) != "artifact" {
		t.Fatalf("extracted output = %q err:%v", out, err)
	}

	if err := dc.ExecMkdir(context.Background(), id, "/workdir/input", "/workdir/output"); err != nil {
		t.Fatalf("ExecMkdir: %v", err)
	}
	insp, err := dc.Inspect(context.Background(), id)
	if err != nil || insp.Status != "running" {
		t.Fatalf("Inspect = %#v err:%v", insp, err)
	}
	if insp.Labels[SandboxContainerOwnerLabelKey] != "api-owner" ||
		insp.Labels[SandboxContainerOwnerBootLabelKey] != "boot-1" {
		t.Fatalf("Inspect labels = %#v", insp.Labels)
	}
	ids, err := dc.ListByLabel(context.Background(), SandboxContainerLabel)
	if err != nil || len(ids) != 1 || ids[0] != "lease-old" {
		t.Fatalf("ListByLabel = %v err:%v", ids, err)
	}
	if err := dc.Destroy(context.Background(), id); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
}

func TestBrokerDockerClientMapsErrors(t *testing.T) {
	tests := []struct {
		name string
		code BrokerErrorCode
		want error
	}{
		{name: "capacity", code: BrokerErrorCapacity, want: ErrPoolExhausted},
		{name: "unavailable", code: BrokerErrorUnavailable, want: ErrBrokerUnavailable},
		{name: "policy", code: BrokerErrorPolicyDenied, want: ErrSandboxPolicyDenied},
		{name: "oom", code: BrokerErrorOOM, want: ErrSandboxOOM},
		{name: "timeout", code: BrokerErrorTimeout, want: ErrSandboxTimeout},
		{name: "input-too-large", code: BrokerErrorInputTooLarge, want: ErrInputTooLarge},
		{name: "output-too-large", code: BrokerErrorOutputTooLarge, want: ErrOutputTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				writeBrokerJSONForTest(t, w, BrokerErrorResponse{Error: BrokerErrorBody{Code: tt.code, Message: "internal detail"}})
			}))
			dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = dc.Spawn(context.Background(), SpawnConfig{})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Spawn error = %v; want errors.Is(%v)", err, tt.want)
			}
			if strings.Contains(err.Error(), "internal detail") {
				t.Fatalf("client error leaked broker detail: %v", err)
			}
		})
	}
}

func TestBrokerDockerClientRejectsUnsafeOutputTar(t *testing.T) {
	socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tw := tar.NewWriter(w)
		_ = tw.WriteHeader(&tar.Header{Name: "../../escape.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("x"))
		_ = tw.Close()
	}))
	dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = dc.CopyFromContainer(context.Background(), "lease-1", "/workdir/output/.", t.TempDir())
	if !errors.Is(err, ErrSandboxPolicyDenied) {
		t.Fatalf("CopyFromContainer err = %v; want policy denied", err)
	}
}

func TestBrokerDockerClientHonorsCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	socket := startBrokerTestServer(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-r.Context().Done()
	}))
	dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := dc.Spawn(ctx, SpawnConfig{})
		done <- err
	}()
	<-requestStarted
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Spawn err = %v; want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broker client did not stop after context cancellation")
	}
}

func TestBrokerDockerClientRejectsOversizedMetadata(t *testing.T) {
	socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.CopyN(w, bytes.NewReader(bytes.Repeat([]byte("x"), 2048)), 2048)
	}))
	cfg := brokerTestConfig(socket)
	cfg.BrokerMetadataMaxBytes = 1024
	dc, err := NewBrokerDockerClient(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dc.Spawn(context.Background(), SpawnConfig{})
	if !errors.Is(err, ErrBrokerResponseTooLarge) {
		t.Fatalf("Spawn err = %v; want response too large", err)
	}
}

func writeBrokerJSONForTest(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
