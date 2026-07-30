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
	"strconv"
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
	cfg.BrokerOwnerID = "api-owner"
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
	var createOwner string
	var createOwnerBoot string
	var createAgentRunID uint64
	var createSandboxSessionID uint64
	var copiedIn []byte
	var lifecycleCalls []string

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
			createOwner, _ = raw["owner_id"].(string)
			createOwnerBoot, _ = raw["owner_boot_id"].(string)
			createAgentRunID = uint64(raw["agent_run_id"].(float64))
			createSandboxSessionID = uint64(raw["sandbox_session_id"].(float64))
			mu.Unlock()
			writeBrokerJSONForTest(t, w, BrokerCreateLeaseResponse{
				LeaseID:   "lease-1",
				State:     "ready",
				ExpiresAt: time.Now().Add(time.Minute),
			})
		case http.MethodGet:
			if got := r.URL.Query().Get("owner_id"); got != "api-owner" {
				t.Errorf("list owner_id = %q; want stable owner without boot id", got)
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
			w.Header().Set("Trailer", BrokerStreamStatusTrailer)
			tw := tar.NewWriter(w)
			body := []byte("artifact")
			_ = tw.WriteHeader(&tar.Header{Name: "result.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
			_, _ = tw.Write(body)
			_ = tw.Close()
			w.Header().Set(BrokerStreamStatusTrailer, BrokerStreamStatusComplete)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})
	for _, action := range []string{"activate", "heartbeat", "persisting"} {
		action := action
		mux.HandleFunc("/v1/leases/lease-1/"+action, func(w http.ResponseWriter, r *http.Request) {
			assertMutationRequestIDForTest(t, r)
			if action == "activate" {
				var req BrokerActivateRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode activate request: %v", err)
				}
				if req.AgentRunID != 11 || req.SandboxSessionID != 22 {
					t.Errorf("activate ids = %d/%d", req.AgentRunID, req.SandboxSessionID)
				}
			}
			mu.Lock()
			lifecycleCalls = append(lifecycleCalls, action)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		})
	}
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
			assertMutationRequestIDForTest(t, r)
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
	client := dc.(*brokerDockerClient)
	client.ownerID = "api-owner"
	client.ownerBootID = "boot-1"

	spawnCfg := SpawnConfig{Labels: []string{
		SandboxContainerLabel,
		SandboxContainerOwnerLabelKey + "=caller-must-not-override-owner",
		SandboxContainerOwnerBootLabelKey + "=caller-must-not-override-boot",
	}, ImageTag: "must-not-cross-wire", Volumes: []string{"/host:/container"}}
	id, err := dc.Spawn(context.Background(), spawnCfg)
	if err != nil || id != "lease-1" {
		t.Fatalf("Spawn = id:%q err:%v", id, err)
	}

	mu.Lock()
	sort.Strings(createKeys)
	gotKeys := append([]string(nil), createKeys...)
	mu.Unlock()
	wantKeys := []string{"agent_run_id", "owner_boot_id", "owner_id", "request_id", "sandbox_session_id"}
	if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("create request keys = %v; want only %v", gotKeys, wantKeys)
	}
	if createOwner != "api-owner" || createOwnerBoot != "boot-1" ||
		createAgentRunID != 0 || createSandboxSessionID != 0 {
		t.Fatalf("create binding = owner:%q boot:%q run:%d session:%d",
			createOwner, createOwnerBoot, createAgentRunID, createSandboxSessionID)
	}
	lifecycle, ok := dc.(BrokerLeaseLifecycle)
	if !ok {
		t.Fatal("broker client does not expose lease lifecycle")
	}
	if err := lifecycle.Activate(context.Background(), id, 11, 22); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := lifecycle.Heartbeat(context.Background(), id); err != nil {
		t.Fatalf("Heartbeat: %v", err)
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
	mu.Lock()
	gotLifecycleCalls := append([]string(nil), lifecycleCalls...)
	mu.Unlock()
	if strings.Join(gotLifecycleCalls, ",") != "activate,heartbeat,persisting" {
		t.Fatalf("lifecycle calls = %v", gotLifecycleCalls)
	}
}

func TestBrokerDockerClientMapsErrors(t *testing.T) {
	tests := []struct {
		name string
		code BrokerErrorCode
		want error
		also error
	}{
		{name: "capacity", code: BrokerErrorCapacity, want: ErrPoolExhausted},
		{name: "unavailable", code: BrokerErrorUnavailable, want: ErrBrokerUnavailable},
		{name: "policy", code: BrokerErrorPolicyDenied, want: ErrSandboxPolicyDenied},
		{name: "oom", code: BrokerErrorOOM, want: ErrSandboxOOM},
		{name: "timeout", code: BrokerErrorTimeout, want: ErrSandboxTimeout, also: context.DeadlineExceeded},
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
			if tt.also != nil && !errors.Is(err, tt.also) {
				t.Fatalf("Spawn error = %v; want errors.Is(%v)", err, tt.also)
			}
			if strings.Contains(err.Error(), "internal detail") {
				t.Fatalf("client error leaked broker detail: %v", err)
			}
		})
	}
}

func TestBrokerDockerClientRejectsUnsafeOutputTar(t *testing.T) {
	tests := []tar.Header{
		{Name: "../../escape.txt", Size: 1, Typeflag: tar.TypeReg},
		{Name: "/absolute.txt", Size: 1, Typeflag: tar.TypeReg},
		{Name: "link", Linkname: "../../escape", Typeflag: tar.TypeSymlink},
		{Name: "hardlink", Linkname: "../../escape", Typeflag: tar.TypeLink},
		{Name: "char-device", Typeflag: tar.TypeChar},
		{Name: "block-device", Typeflag: tar.TypeBlock},
		{Name: "fifo", Typeflag: tar.TypeFifo},
	}
	for _, hdr := range tests {
		hdr := hdr
		t.Run(strconv.Itoa(int(hdr.Typeflag))+"-"+strings.ReplaceAll(hdr.Name, "/", "_"), func(t *testing.T) {
			socket := startBrokerTestServer(t, brokerCopyOutHandler(t, func(tw *tar.Writer) {
				if err := tw.WriteHeader(&hdr); err != nil {
					t.Errorf("write tar header: %v", err)
					return
				}
				if hdr.Size > 0 {
					_, _ = tw.Write(bytes.Repeat([]byte("x"), int(hdr.Size)))
				}
			}))
			dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
			if err != nil {
				t.Fatal(err)
			}
			err = dc.CopyFromContainer(context.Background(), "lease-1", "/workdir/output/.", t.TempDir())
			if !errors.Is(err, ErrSandboxPolicyDenied) {
				t.Fatalf("CopyFromContainer err = %v; want policy denied", err)
			}
		})
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
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("broker request did not start")
	}
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

func TestNewBrokerDockerClientRejectsUnsafeLimits(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name   string
		mutate func(*SandboxConfig)
	}{
		{name: "relative socket", mutate: func(cfg *SandboxConfig) { cfg.BrokerSocket = "sandboxd.sock" }},
		{name: "empty owner", mutate: func(cfg *SandboxConfig) { cfg.BrokerOwnerID = "" }},
		{name: "unsafe owner", mutate: func(cfg *SandboxConfig) { cfg.BrokerOwnerID = "api/primary" }},
		{name: "metadata zero", mutate: func(cfg *SandboxConfig) { cfg.BrokerMetadataMaxBytes = 0 }},
		{name: "metadata negative", mutate: func(cfg *SandboxConfig) { cfg.BrokerMetadataMaxBytes = -1 }},
		{name: "metadata raised", mutate: func(cfg *SandboxConfig) { cfg.BrokerMetadataMaxBytes = DefaultBrokerMetadataMaxBytes + 1 }},
		{name: "exec raised", mutate: func(cfg *SandboxConfig) { cfg.BrokerExecOutputMaxBytes = DefaultBrokerExecOutputMaxBytes + 1 }},
		{name: "copy in raised", mutate: func(cfg *SandboxConfig) { cfg.BrokerCopyInMaxBytes = DefaultBrokerCopyInMaxBytes + 1 }},
		{name: "copy out raised", mutate: func(cfg *SandboxConfig) { cfg.BrokerCopyOutMaxBytes = DefaultBrokerCopyOutMaxBytes + 1 }},
		{name: "single raised", mutate: func(cfg *SandboxConfig) { cfg.BrokerSingleFileMaxBytes = DefaultBrokerSingleFileMaxBytes + 1 }},
		{name: "files raised", mutate: func(cfg *SandboxConfig) { cfg.BrokerMaxFiles = DefaultBrokerMaxFiles + 1 }},
		{name: "connections raised", mutate: func(cfg *SandboxConfig) { cfg.BrokerMaxConnections = DefaultBrokerMaxConnections + 1 }},
		{name: "max int", mutate: func(cfg *SandboxConfig) { cfg.BrokerCopyOutMaxBytes = maxInt }},
		{name: "single exceeds copy", mutate: func(cfg *SandboxConfig) {
			cfg.BrokerSingleFileMaxBytes = 2
			cfg.BrokerCopyInMaxBytes = 1
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := brokerTestConfig("/tmp/sandboxd-test.sock")
			tt.mutate(&cfg)
			_, err := NewBrokerDockerClient(cfg, nil)
			if !errors.Is(err, ErrBrokerProtocolViolation) {
				t.Fatalf("NewBrokerDockerClient err = %v; want protocol violation", err)
			}
		})
	}

	cfg := brokerTestConfig("/tmp/sandboxd-test.sock")
	cfg.BrokerMetadataMaxBytes--
	if _, err := NewBrokerDockerClient(cfg, nil); err != nil {
		t.Fatalf("lowered limit rejected: %v", err)
	}
}

func TestBrokerDockerClientRejectsMalformedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"lease_id":"lease-1","state":"ready","extra":true}`},
		{name: "trailing json", body: `{"lease_id":"lease-1","state":"ready"} {"lease_id":"lease-2"}`},
		{name: "null", body: `null`},
		{name: "empty object", body: `{}`},
		{name: "wrong state", body: `{"lease_id":"lease-1","state":"active"}`},
		{name: "missing expires", body: `{"lease_id":"lease-1","state":"ready"}`},
		{name: "null expires", body: `{"lease_id":"lease-1","state":"ready","expires_at":null}`},
		{name: "zero expires", body: `{"lease_id":"lease-1","state":"ready","expires_at":"0001-01-01T00:00:00Z"}`},
		{name: "past expires", body: `{"lease_id":"lease-1","state":"ready","expires_at":"2020-01-01T00:00:00Z"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			}))
			dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = dc.Spawn(context.Background(), SpawnConfig{})
			if !errors.Is(err, ErrBrokerProtocolViolation) {
				t.Fatalf("Spawn err = %v; want protocol violation", err)
			}
		})
	}
}

func TestBrokerDockerClientRejectsSemanticallyIncompleteResponses(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   string
		invoke func(DockerClient) error
	}{
		{
			name: "exec null", path: "/v1/leases/lease-1/exec", body: `null`,
			invoke: func(dc DockerClient) error {
				_, err := dc.Exec(context.Background(), "lease-1", []string{"true"}, ExecOpts{})
				return err
			},
		},
		{
			name: "exec empty", path: "/v1/leases/lease-1/exec", body: `{}`,
			invoke: func(dc DockerClient) error {
				_, err := dc.Exec(context.Background(), "lease-1", []string{"true"}, ExecOpts{})
				return err
			},
		},
		{
			name: "inspect empty", path: "/v1/leases/lease-1", body: `{}`,
			invoke: func(dc DockerClient) error {
				_, err := dc.Inspect(context.Background(), "lease-1")
				return err
			},
		},
		{
			name: "inspect invalid state", path: "/v1/leases/lease-1",
			body: `{"status":"created","exit_code":0,"oom_killed":false,"owner_id":"api","owner_boot_id":"boot"}`,
			invoke: func(dc DockerClient) error {
				_, err := dc.Inspect(context.Background(), "lease-1")
				return err
			},
		},
		{
			name: "inspect missing owner boot", path: "/v1/leases/lease-1",
			body: `{"status":"running","exit_code":0,"oom_killed":false,"owner_id":"api"}`,
			invoke: func(dc DockerClient) error {
				_, err := dc.Inspect(context.Background(), "lease-1")
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path {
					http.Error(w, "unexpected path", http.StatusNotFound)
					return
				}
				_, _ = io.WriteString(w, tt.body)
			}))
			dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
			if err != nil {
				t.Fatal(err)
			}
			err = tt.invoke(dc)
			if !errors.Is(err, ErrBrokerProtocolViolation) {
				t.Fatalf("invoke err = %v; want protocol violation", err)
			}
		})
	}
}

func TestBrokerDockerClientDoesNotLeakUnknownErrorCode(t *testing.T) {
	const secretCode = BrokerErrorCode("secret_internal_code")
	socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		writeBrokerJSONForTest(t, w, BrokerErrorResponse{Error: BrokerErrorBody{Code: secretCode}})
	}))
	dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dc.Spawn(context.Background(), SpawnConfig{})
	if !errors.Is(err, ErrBrokerProtocolViolation) {
		t.Fatalf("Spawn err = %v; want protocol violation", err)
	}
	if strings.Contains(err.Error(), string(secretCode)) {
		t.Fatalf("unknown broker code leaked to caller: %v", err)
	}
}

func TestBrokerDockerClientRejectsMalformedErrorResponses(t *testing.T) {
	tests := []string{
		`{"error":{"code":"capacity","extra":true}}`,
		`{"error":{"code":"capacity"}} {"error":{"code":"oom"}}`,
	}
	for i, body := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, body)
			}))
			dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = dc.Spawn(context.Background(), SpawnConfig{})
			if !errors.Is(err, ErrBrokerProtocolViolation) {
				t.Fatalf("Spawn err = %v; want protocol violation", err)
			}
		})
	}
}

func TestBrokerDockerClientRejectsInvalidActivationBinding(t *testing.T) {
	dc, err := NewBrokerDockerClient(brokerTestConfig("/tmp/sandboxd-test.sock"), nil)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := dc.(BrokerLeaseLifecycle)
	for _, ids := range [][2]uint64{{0, 1}, {1, 0}, {0, 0}} {
		err := lifecycle.Activate(context.Background(), "lease-1", ids[0], ids[1])
		if !errors.Is(err, ErrSandboxPolicyDenied) {
			t.Fatalf("Activate(%d,%d) err = %v; want policy denied", ids[0], ids[1], err)
		}
	}
}

func TestBrokerDockerClientRejectsOversizedCopyIn(t *testing.T) {
	socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	cfg := brokerTestConfig(socket)
	cfg.BrokerCopyInMaxBytes = 4
	cfg.BrokerSingleFileMaxBytes = 4
	dc, err := NewBrokerDockerClient(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = dc.CopyToContainer(context.Background(), "lease-1", "/workdir/input.txt", strings.NewReader("12345"))
	if !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("CopyToContainer err = %v; want input too large", err)
	}
}

func TestBrokerDockerClientClosesCopyInReaderOnCancel(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	requestStarted := make(chan struct{})
	socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- dc.CopyToContainer(ctx, "lease-1", "/workdir/input.txt", reader)
	}()
	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("copy-in request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CopyToContainer err = %v; want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CopyToContainer did not return after cancellation")
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte("must fail after reader close"))
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("pipe writer err = %v; want closed pipe", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("copy-in pipe reader was not closed after cancellation")
	}
}

func TestBrokerDockerClientRejectsBlockingNonCloser(t *testing.T) {
	reader := &blockingReader{started: make(chan struct{}), unblock: make(chan struct{})}
	dc, err := NewBrokerDockerClient(brokerTestConfig("/tmp/sandboxd-test.sock"), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- dc.CopyToContainer(context.Background(), "lease-1", "/workdir/input.txt", reader)
	}()
	select {
	case err = <-done:
		if !errors.Is(err, ErrSandboxPolicyDenied) {
			t.Fatalf("CopyToContainer err = %v; want policy denied", err)
		}
	case <-time.After(2 * time.Second):
		close(reader.unblock)
		t.Fatal("CopyToContainer read an unsafe non-closer")
	}
	select {
	case <-reader.started:
		t.Fatal("unsafe non-closer reader was read")
	default:
	}
}

func TestBrokerDockerClientRejectsNopCloserAroundBlockingReader(t *testing.T) {
	reader := &blockingReader{started: make(chan struct{}), unblock: make(chan struct{})}
	wrapped := io.NopCloser(reader)
	dc, err := NewBrokerDockerClient(brokerTestConfig("/tmp/sandboxd-test.sock"), nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- dc.CopyToContainer(context.Background(), "lease-1", "/workdir/input.txt", wrapped)
	}()
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		close(reader.unblock)
		t.Fatal("CopyToContainer read io.NopCloser-wrapped blocking input")
	}
	if !errors.Is(err, ErrSandboxPolicyDenied) {
		close(reader.unblock)
		t.Fatalf("CopyToContainer err = %v; want policy denied", err)
	}
	select {
	case <-reader.started:
		close(reader.unblock)
		t.Fatal("io.NopCloser blocking reader was read")
	default:
	}
}

func TestBrokerDockerClientRejectsBlockingOSFile(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	dc, err := NewBrokerDockerClient(brokerTestConfig("/tmp/sandboxd-test.sock"), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = dc.CopyToContainer(context.Background(), "lease-1", "/workdir/input.txt", reader)
	if !errors.Is(err, ErrSandboxPolicyDenied) {
		t.Fatalf("CopyToContainer err = %v; want policy denied", err)
	}
}

func TestBrokerDockerClientEnforcesCopyOutLimits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SandboxConfig)
		write  func(*tar.Writer)
	}{
		{
			name: "single file",
			mutate: func(cfg *SandboxConfig) {
				cfg.BrokerSingleFileMaxBytes = 3
				cfg.BrokerCopyInMaxBytes = 3
				cfg.BrokerCopyOutMaxBytes = 10
			},
			write: func(tw *tar.Writer) {
				writeTarFileForTest(t, tw, "a.txt", "1234")
			},
		},
		{
			name: "total bytes",
			mutate: func(cfg *SandboxConfig) {
				cfg.BrokerSingleFileMaxBytes = 5
				cfg.BrokerCopyInMaxBytes = 5
				cfg.BrokerCopyOutMaxBytes = 5
			},
			write: func(tw *tar.Writer) {
				writeTarFileForTest(t, tw, "a.txt", "123")
				writeTarFileForTest(t, tw, "b.txt", "456")
			},
		},
		{
			name: "file count",
			mutate: func(cfg *SandboxConfig) {
				cfg.BrokerMaxFiles = 1
			},
			write: func(tw *tar.Writer) {
				writeTarFileForTest(t, tw, "a.txt", "1")
				writeTarFileForTest(t, tw, "b.txt", "2")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socket := startBrokerTestServer(t, brokerCopyOutHandler(t, tt.write))
			cfg := brokerTestConfig(socket)
			tt.mutate(&cfg)
			dc, err := NewBrokerDockerClient(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = dc.CopyFromContainer(context.Background(), "lease-1", "/workdir/output/.", t.TempDir())
			if !errors.Is(err, ErrOutputTooLarge) {
				t.Fatalf("CopyFromContainer err = %v; want output too large", err)
			}
		})
	}
}

func TestBrokerDockerClientCopyOutHonorsCancelAfterHeaders(t *testing.T) {
	bodyStarted := make(chan struct{})
	socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/persisting") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		tw := tar.NewWriter(w)
		_ = tw.WriteHeader(&tar.Header{Name: "result.txt", Size: 1024, Typeflag: tar.TypeReg})
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(bodyStarted)
		<-r.Context().Done()
	}))
	dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- dc.CopyFromContainer(ctx, "lease-1", "/workdir/output/.", t.TempDir())
	}()
	select {
	case <-bodyStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("copy-out body did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CopyFromContainer err = %v; want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CopyFromContainer did not stop after cancellation")
	}
}

func TestBrokerDockerClientFileErrorsDoNotLeakHostPath(t *testing.T) {
	socket := startBrokerTestServer(t, brokerCopyOutHandler(t, func(tw *tar.Writer) {
		writeTarFileForTest(t, tw, "result.txt", "artifact")
	}))
	dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "result.txt"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = dc.CopyFromContainer(context.Background(), "lease-1", "/workdir/output/.", dst)
	if !errors.Is(err, ErrSandboxPolicyDenied) {
		t.Fatalf("CopyFromContainer err = %v; want policy denied", err)
	}
	if strings.Contains(err.Error(), dst) {
		t.Fatalf("host destination leaked in error: %v", err)
	}
}

func TestDisabledPoolCloseClosesBrokerClient(t *testing.T) {
	dc := &closableMockDockerClient{MockDockerClient: NewMockDockerClient()}
	pool := &disabledPool{dc: dc}
	if err := pool.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !dc.closed {
		t.Fatal("pool did not close its DockerClient")
	}
}

func TestBrokerPoolCloseCancelsActiveUnixRequestAndClosesConnection(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "sbd-close-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "sandboxd.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	createStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	connectionClosed := make(chan struct{})
	var startOnce sync.Once
	var cancelOnce sync.Once
	var closedOnce sync.Once
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				writeBrokerJSONForTest(t, w, BrokerListLeasesResponse{LeaseIDs: []string{}})
				return
			}
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				return
			}
			startOnce.Do(func() { close(createStarted) })
			<-r.Context().Done()
			cancelOnce.Do(func() { close(requestCanceled) })
		}),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				closedOnce.Do(func() { close(connectionClosed) })
			}
		},
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})

	cfg := brokerTestConfig(socket)
	cfg.PoolMin = 1
	dc, err := NewBrokerDockerClient(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPool(cfg, dc, nil)
	select {
	case <-createStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("broker create request did not start")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- pool.Close() }()
	select {
	case <-requestCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe active request cancellation")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pool Close did not join canceled broker request")
	}
	select {
	case <-connectionClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("broker Unix connection remained open")
	}
}

func TestBrokerDockerClientListsPreviousBootByStableOwner(t *testing.T) {
	socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("owner_id"); got != "stable-api-owner" {
			t.Errorf("owner_id = %q; want stable owner without current boot", got)
		}
		writeBrokerJSONForTest(t, w, BrokerListLeasesResponse{LeaseIDs: []string{"previous-boot-lease"}})
	}))
	cfg := brokerTestConfig(socket)
	cfg.BrokerOwnerID = "stable-api-owner"
	dc, err := NewBrokerDockerClient(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := dc.ListByLabel(context.Background(), SandboxContainerLabel)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "previous-boot-lease" {
		t.Fatalf("ListByLabel = %v; want previous boot lease", ids)
	}
}

func TestNewPoolUsesConfiguredBrokerOwnerIdentity(t *testing.T) {
	cfg := brokerTestConfig("/tmp/nonexistent-sandboxd-test.sock")
	cfg.BrokerOwnerID = "stable-api-primary"
	cfg.PoolMin = 0
	dc, err := NewBrokerDockerClient(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool := NewPool(cfg, dc, nil)
	agentPool := pool.(*agentSandboxPool)
	if agentPool.ownerID != "stable-api-primary" {
		t.Fatalf("pool owner = %q; want configured stable owner", agentPool.ownerID)
	}
	if agentPool.ownerBootID == "" {
		t.Fatal("pool owner boot id is empty")
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBrokerDockerClientRejectsMalformedLeaseLists(t *testing.T) {
	tests := []string{
		`null`,
		`{}`,
		`{"lease_ids":null}`,
		`{"lease_ids":[""]}`,
		`{"lease_ids":["duplicate","duplicate"]}`,
	}
	for i, body := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			socket := startBrokerTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			dc, err := NewBrokerDockerClient(brokerTestConfig(socket), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = dc.ListByLabel(context.Background(), SandboxContainerLabel)
			if !errors.Is(err, ErrBrokerProtocolViolation) {
				t.Fatalf("ListByLabel err = %v; want protocol violation", err)
			}
		})
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

func assertMutationRequestIDForTest(t *testing.T, r *http.Request) {
	t.Helper()
	headerID := r.Header.Get("X-Numind-Request-ID")
	if headerID == "" {
		t.Error("mutation request missing X-Numind-Request-ID")
	}
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		t.Errorf("read mutation request: %v", err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(payload))
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Errorf("decode mutation request: %v", err)
		return
	}
	if bodyID, _ := raw["request_id"].(string); bodyID == "" || bodyID != headerID {
		t.Errorf("request id mismatch: header=%q body=%q", headerID, bodyID)
	}
}

func brokerCopyOutHandler(t *testing.T, write func(*tar.Writer)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/persisting") {
			assertMutationRequestIDForTest(t, r)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/files") {
			http.Error(w, "unexpected route", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Trailer", BrokerStreamStatusTrailer)
		tw := tar.NewWriter(w)
		write(tw)
		if err := tw.Close(); err != nil {
			t.Errorf("close tar: %v", err)
		}
		w.Header().Set(BrokerStreamStatusTrailer, BrokerStreamStatusComplete)
	})
}

func writeTarFileForTest(t *testing.T, tw *tar.Writer, name string, body string) {
	t.Helper()
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o600,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Errorf("write tar header: %v", err)
		return
	}
	if _, err := io.WriteString(tw, body); err != nil {
		t.Errorf("write tar body: %v", err)
	}
}

type blockingReader struct {
	started chan struct{}
	unblock chan struct{}
}

func (r *blockingReader) Read(_ []byte) (int, error) {
	close(r.started)
	<-r.unblock
	return 0, io.ErrClosedPipe
}

type closableMockDockerClient struct {
	*MockDockerClient
	closed bool
}

func (c *closableMockDockerClient) Close() error {
	c.closed = true
	return nil
}
