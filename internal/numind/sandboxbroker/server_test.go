package sandboxbroker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServerConfigRejectsNetworkLikeAndRaisedLimits(t *testing.T) {
	base := testServerConfig(t)
	tests := []struct {
		name   string
		mutate func(*ServerConfig)
	}{
		{name: "relative socket", mutate: func(c *ServerConfig) {
			c.SocketPath = "127.0.0.1:9099"
		}},
		{name: "non socket suffix", mutate: func(c *ServerConfig) {
			c.SocketPath = filepath.Join(filepath.Dir(c.SocketPath), "listener")
		}},
		{name: "metadata raised", mutate: func(c *ServerConfig) {
			c.MetadataMaxBytes = ServerMetadataMaxBytes + 1
		}},
		{name: "connections raised", mutate: func(c *ServerConfig) {
			c.MaxConnections = ServerMaxConnections + 1
		}},
		{name: "copy streams raised", mutate: func(c *ServerConfig) {
			c.MaxCopyStreams = ServerMaxCopyStreams + 1
		}},
		{name: "per lease changed", mutate: func(c *ServerConfig) {
			c.MaxLeaseDirectionStreams = 2
		}},
		{name: "rate raised", mutate: func(c *ServerConfig) {
			c.AggregateCopyBytesPerSecond = ServerCopyBytesPerSecond + 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.mutate(&config)
			if _, err := NewServer(
				config,
				testRPCService{},
				testPeerAuthorizer{},
			); !errors.Is(err, ErrInvalidServerConfig) {
				t.Fatalf("NewServer error = %v", err)
			}
		})
	}
}

func TestServerStrictCreateListInspectAndMutationContracts(t *testing.T) {
	server := newTestServer(t, testRPCService{})
	requestID := "11111111-1111-4111-8111-111111111111"

	create := newPeerRequest(
		t,
		http.MethodPost,
		"/v1/leases",
		`{"request_id":"`+requestID+`","owner_id":"api-blue","owner_boot_id":"boot-1","agent_run_id":0,"sandbox_session_id":0}`,
		requestID,
	)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, create)
	if response.Code != http.StatusCreated ||
		strings.Contains(response.Body.String(), "container") {
		t.Fatalf("create response = %d %s", response.Code, response.Body.String())
	}

	list := newPeerRequest(t, http.MethodGet, "/v1/leases?owner_id=api-blue", "", "")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, list)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"lease_ids":["lease-1"]`) {
		t.Fatalf("list response = %d %s", response.Code, response.Body.String())
	}

	inspect := newPeerRequest(t, http.MethodGet, "/v1/leases/lease-1", "", "")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, inspect)
	if response.Code != http.StatusOK ||
		strings.Contains(response.Body.String(), "container") {
		t.Fatalf("inspect response = %d %s", response.Code, response.Body.String())
	}

	activate := newPeerRequest(
		t,
		http.MethodPost,
		"/v1/leases/lease-1/activate",
		`{"request_id":"`+requestID+`","agent_run_id":7,"sandbox_session_id":8}`,
		requestID,
	)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, activate)
	if response.Code != http.StatusNoContent {
		t.Fatalf("activate response = %d %s", response.Code, response.Body.String())
	}
}

func TestServerRecoveryPendingRoutesAreInternalAndBounded(t *testing.T) {
	service := testRecoveryRPCService{
		leases: []RecoveryPendingRPCLease{{
			LeaseID:           "lease-1",
			AgentRunID:        11,
			SandboxSessionID:  22,
			State:             string(LeaseRecoveryPending),
			TerminationReason: string(TerminationContainerMissing),
		}},
	}
	server := newTestServer(t, &service)

	list := newPeerRequest(
		t,
		http.MethodGet,
		"/v1/recovery-pending?limit=5",
		"",
		"",
	)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, list)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"agent_run_id":11`) ||
		strings.Contains(response.Body.String(), "container_id") {
		t.Fatalf("list response = %d %s", response.Code, response.Body.String())
	}

	requestID := "22222222-2222-4222-8222-222222222222"
	mark := newPeerRequest(
		t,
		http.MethodPost,
		"/v1/recovery-pending/lease-1/reconciled",
		`{"request_id":"`+requestID+`"}`,
		requestID,
	)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, mark)
	if response.Code != http.StatusNoContent || service.marked != "lease-1" {
		t.Fatalf("mark response = %d %s marked=%s", response.Code, response.Body.String(), service.marked)
	}

	badLimit := newPeerRequest(t, http.MethodGet, "/v1/recovery-pending?limit=0", "", "")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, badLimit)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("bad limit response = %d %s", response.Code, response.Body.String())
	}
}

func TestServerRejectsUnknownFieldsMismatchedIDsAndUnauthenticatedHTTP(t *testing.T) {
	server := newTestServer(t, testRPCService{})
	requestID := "11111111-1111-4111-8111-111111111111"
	tests := []struct {
		name    string
		request *http.Request
		status  int
	}{
		{
			name: "unknown field",
			request: newPeerRequest(
				t,
				http.MethodPost,
				"/v1/leases",
				`{"request_id":"`+requestID+`","owner_id":"api","owner_boot_id":"boot","agent_run_id":0,"sandbox_session_id":0,"image":"host"}`,
				requestID,
			),
			status: http.StatusBadRequest,
		},
		{
			name: "mismatched header",
			request: newPeerRequest(
				t,
				http.MethodPost,
				"/v1/leases",
				`{"request_id":"`+requestID+`","owner_id":"api","owner_boot_id":"boot","agent_run_id":0,"sandbox_session_id":0}`,
				"22222222-2222-4222-8222-222222222222",
			),
			status: http.StatusBadRequest,
		},
		{
			name: "extra query",
			request: newPeerRequest(
				t,
				http.MethodGet,
				"/v1/leases?owner_id=api&image=host",
				"",
				"",
			),
			status: http.StatusBadRequest,
		},
		{
			name:    "missing peer",
			request: httptest.NewRequest(http.MethodGet, "/v1/leases?owner_id=api", nil),
			status:  http.StatusForbidden,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			server.ServeHTTP(response, test.request)
			if response.Code != test.status {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestServerCopyLimitsAreReleasedAndPerDirectionExclusive(t *testing.T) {
	entered := make(chan struct{}, 1)
	releaseCopy := make(chan struct{})
	service := testRPCService{
		copyIn: func(_ io.Reader) error {
			entered <- struct{}{}
			<-releaseCopy
			return nil
		},
	}
	server := newTestServer(t, service)
	requestID := "11111111-1111-4111-8111-111111111111"
	firstDone := make(chan int, 1)
	go func() {
		request := newPeerRequest(
			t,
			http.MethodPut,
			"/v1/leases/lease-1/files?path=%2Fworkdir%2Finput%2Fa",
			"x",
			requestID,
		)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		firstDone <- response.Code
	}()
	<-entered

	second := newPeerRequest(
		t,
		http.MethodPut,
		"/v1/leases/lease-1/files?path=%2Fworkdir%2Finput%2Fb",
		"x",
		"22222222-2222-4222-8222-222222222222",
	)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, second)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("second copy response = %d %s", response.Code, response.Body.String())
	}
	close(releaseCopy)
	if status := <-firstDone; status != http.StatusNoContent {
		t.Fatalf("first copy status = %d", status)
	}
}

func TestServerSocketRejectsSymlinkAndUnsafeMode(t *testing.T) {
	config := testServerConfig(t)
	target := filepath.Join(filepath.Dir(config.SocketPath), "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, config.SocketPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listenServerUnix(config); !errors.Is(err, ErrUnsafeServerSocket) {
		t.Fatalf("symlink listen error = %v", err)
	}
	if err := os.Remove(config.SocketPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(config.SocketPath), 0o777); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listenServerUnix(config); !errors.Is(err, ErrUnsafeServerSocket) {
		t.Fatalf("unsafe directory listen error = %v", err)
	}
}

func TestServerSocketCreatedWithExactIdentityAndRemovedByInode(t *testing.T) {
	config := testServerConfig(t)
	listener, identity, err := listenServerUnix(config)
	if err != nil {
		t.Fatal(err)
	}
	if identity == (socketIdentity{}) {
		t.Fatal("empty socket identity")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := removeServerSocket(config, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(config.SocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still exists: %v", err)
	}
}

func TestServerListenAndServeUsesUnixAndAuthenticatedConnectionContext(t *testing.T) {
	server := newTestServer(t, testRPCService{})
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.ListenAndServe()
	}()
	waitForSocketPath(t, server.config.SocketPath)

	transport := &http.Transport{
		DialContext: func(
			ctx context.Context,
			_ string,
			_ string,
		) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", server.config.SocketPath)
		},
	}
	client := &http.Client{Transport: transport}
	response, err := client.Get("http://sandboxd/v1/leases?owner_id=api-blue")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK ||
		!strings.Contains(string(payload), `"lease-1"`) {
		t.Fatalf("response = %d %s", response.StatusCode, payload)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestJournalRPCServiceLifecycleAndMutationReplay(t *testing.T) {
	journal := openTestJournal(t, testJournalPath(t))
	scheduler := NewScheduler()
	scheduler.SetAdmission(true, nil)
	runtime := &testContainerRuntime{}
	service, err := NewJournalRPCService(journal, scheduler, runtime)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	peer := PeerCredentials{PID: 1, UID: 1000, GID: 1000}
	createID := "11111111-1111-4111-8111-111111111111"
	create := CreateLeaseRPCRequest{
		RequestID:   createID,
		OwnerID:     "api-blue",
		OwnerBootID: "boot-1",
	}
	created, err := service.CreateLease(ctx, peer, create)
	if err != nil {
		t.Fatal(err)
	}
	if created.State != string(LeaseReady) || created.LeaseID == "" {
		t.Fatalf("created = %#v", created)
	}
	scheduler.SetAdmission(false, ErrReadinessUnavailable)
	replayed, err := service.CreateLease(ctx, peer, create)
	if err != nil || replayed.LeaseID != created.LeaseID {
		t.Fatalf("create replay = %#v err=%v", replayed, err)
	}
	scheduler.SetAdmission(true, nil)
	runtime.assertCalls(t, "spawn", 1)

	activate := ActivateRPCRequest{
		RequestID:        "22222222-2222-4222-8222-222222222222",
		AgentRunID:       7,
		SandboxSessionID: 8,
	}
	scheduler.SetAdmission(false, ErrReadinessUnavailable)
	if err := service.Activate(
		ctx,
		peer,
		created.LeaseID,
		activate,
	); !errors.Is(err, ErrReadinessUnavailable) {
		t.Fatalf("closed activation error=%v", err)
	}
	readyLease, err := journal.GetLease(ctx, created.LeaseID)
	if err != nil || readyLease.State != LeaseReady {
		t.Fatalf("blocked activation published journal state=%#v error=%v", readyLease, err)
	}
	scheduler.SetAdmission(true, nil)
	if err := service.Activate(ctx, peer, created.LeaseID, activate); err != nil {
		t.Fatal(err)
	}
	scheduler.SetAdmission(false, ErrReadinessUnavailable)
	if err := service.Activate(ctx, peer, created.LeaseID, activate); err != nil {
		t.Fatalf("active replay was blocked by admission: %v", err)
	}
	scheduler.SetAdmission(true, nil)
	exec := ExecRPCRequest{
		RequestID: "33333333-3333-4333-8333-333333333333",
		Argv:      []string{"/bin/true"},
	}
	if _, err := service.Exec(ctx, peer, created.LeaseID, exec); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Exec(ctx, peer, created.LeaseID, exec); !errors.Is(
		err,
		ErrRPCReplayResultUnavailable,
	) {
		t.Fatalf("exec replay error = %v", err)
	}
	runtime.assertCalls(t, "exec", 1)

	mkdir := MkdirRPCRequest{
		RequestID: "44444444-4444-4444-8444-444444444444",
		Dirs:      []string{"/workdir/input"},
	}
	if err := service.Mkdir(ctx, peer, created.LeaseID, mkdir); err != nil {
		t.Fatal(err)
	}
	if err := service.Mkdir(ctx, peer, created.LeaseID, mkdir); err != nil {
		t.Fatal(err)
	}
	runtime.assertCalls(t, "mkdir", 1)

	copyInID := "55555555-5555-4555-8555-555555555555"
	if err := service.CopyIn(
		ctx,
		peer,
		created.LeaseID,
		"/workdir/input/a",
		copyInID,
		strings.NewReader("abc"),
	); err != nil {
		t.Fatal(err)
	}
	if err := service.CopyIn(
		ctx,
		peer,
		created.LeaseID,
		"/workdir/input/a",
		copyInID,
		strings.NewReader("abc"),
	); err != nil {
		t.Fatal(err)
	}
	runtime.assertCalls(t, "copy_in", 1)

	if err := service.MarkPersisting(ctx, peer, created.LeaseID, MutationRPCRequest{
		RequestID: "66666666-6666-4666-8666-666666666666",
	}); err != nil {
		t.Fatal(err)
	}
	copyOutID := "77777777-7777-4777-8777-777777777777"
	for index := 0; index < 2; index++ {
		reader, err := service.CopyOut(
			ctx,
			peer,
			created.LeaseID,
			"/workdir/output/a",
			copyOutID,
		)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, reader)
		_ = reader.Close()
	}
	runtime.assertCalls(t, "copy_out", 2)

	lease, err := journal.GetLease(ctx, created.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	if lease.CopyInFiles != 1 ||
		lease.CopyInBytes != 3 ||
		lease.CopyOutFiles != 1 ||
		lease.CopyOutBytes != 3 {
		t.Fatalf("copy counters = %#v", lease)
	}
	if _, err := service.Inspect(
		ctx,
		PeerCredentials{UID: 1001},
		created.LeaseID,
	); !errors.Is(err, ErrLeaseNotFound) {
		t.Fatalf("cross-peer inspect error = %v", err)
	}
	deleteRequest := MutationRPCRequest{
		RequestID: "88888888-8888-4888-8888-888888888888",
	}
	if err := service.Delete(ctx, peer, created.LeaseID, deleteRequest); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, peer, created.LeaseID, deleteRequest); err != nil {
		t.Fatal(err)
	}
	runtime.assertCalls(t, "delete", 1)
	assertSchedulerCounts(t, scheduler, 0, 0, 0)
}

func TestJournalRPCServiceDoesNotSpawnWhileAdmissionIsClosed(
	t *testing.T,
) {
	journal := openTestJournal(t, testJournalPath(t))
	scheduler := NewScheduler()
	runtime := &testContainerRuntime{}
	service, err := NewJournalRPCService(journal, scheduler, runtime)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateLease(
		context.Background(),
		PeerCredentials{PID: 1, UID: 1000, GID: 1000},
		CreateLeaseRPCRequest{
			RequestID:   "11111111-1111-4111-8111-111111111111",
			OwnerID:     "api-blue",
			OwnerBootID: "boot-1",
		},
	)
	if !errors.Is(err, ErrSchedulerAdmissionBlocked) {
		t.Fatalf("closed admission create error=%v", err)
	}
	runtime.assertCalls(t, "spawn", 0)
	var count int
	if err := journal.db.QueryRow(`SELECT COUNT(*) FROM lease`).Scan(
		&count,
	); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("closed admission grew journal by %d leases", count)
	}
}

func TestActivationLockIsPerLeaseAndContextCancellable(t *testing.T) {
	var locks activationLockSet
	unlockFirst, err := locks.Lock(context.Background(), "lease-first")
	if err != nil {
		t.Fatal(err)
	}
	defer unlockFirst()

	unlockOther, err := locks.Lock(context.Background(), "lease-other")
	if err != nil {
		t.Fatalf("different lease was globally blocked: %v", err)
	}
	unlockOther()

	waitContext, cancelWait := context.WithTimeout(
		context.Background(),
		20*time.Millisecond,
	)
	defer cancelWait()
	if _, err := locks.Lock(
		waitContext,
		"lease-first",
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-lease wait error=%v", err)
	}
}

func waitForSocketPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("socket %s was not created", path)
}

func newTestServer(t *testing.T, service RPCService) *Server {
	t.Helper()
	server, err := NewServer(
		testServerConfig(t),
		service,
		testPeerAuthorizer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func testServerConfig(t *testing.T) ServerConfig {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(home, ".numind-socket-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(directory)
	})
	if err := os.Chmod(directory, os.ModeSetgid|0o770); err != nil {
		t.Fatal(err)
	}
	config := DefaultServerConfig()
	config.SocketPath = filepath.Join(directory, "sandboxd.sock")
	config.SocketDirectoryUID = uint32(os.Geteuid())
	config.SocketDirectoryGID = uint32(os.Getegid())
	config.SocketUID = uint32(os.Geteuid())
	config.SocketGID = uint32(os.Getegid())
	return config
}

func newPeerRequest(
	t *testing.T,
	method string,
	path string,
	body string,
	requestID string,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		if method == http.MethodPut {
			request.Header.Set("Content-Type", "application/octet-stream")
		} else {
			request.Header.Set("Content-Type", "application/json")
		}
	}
	if requestID != "" {
		request.Header.Set("X-Numind-Request-ID", requestID)
	}
	return request.WithContext(context.WithValue(
		request.Context(),
		peerContextKey{},
		PeerCredentials{PID: 1, UID: 1000, GID: 1000},
	))
}

type testPeerAuthorizer struct{}

func (testPeerAuthorizer) Authorize(net.Conn) (PeerCredentials, error) {
	return PeerCredentials{PID: 1, UID: 1000, GID: 1000}, nil
}

type testRPCService struct {
	copyIn func(io.Reader) error
}

type testRecoveryRPCService struct {
	testRPCService
	leases []RecoveryPendingRPCLease
	marked string
}

func (s *testRecoveryRPCService) ListRecoveryPending(
	context.Context,
	PeerCredentials,
	int,
) ([]RecoveryPendingRPCLease, error) {
	return append([]RecoveryPendingRPCLease(nil), s.leases...), nil
}

func (s *testRecoveryRPCService) MarkReconciled(
	_ context.Context,
	_ PeerCredentials,
	leaseID string,
	_ MutationRPCRequest,
) error {
	s.marked = leaseID
	return nil
}

type testContainerRuntime struct {
	mu    sync.Mutex
	calls map[string]int
}

func (r *testContainerRuntime) record(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = make(map[string]int)
	}
	r.calls[name]++
}

func (r *testContainerRuntime) assertCalls(
	t *testing.T,
	name string,
	want int,
) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if got := r.calls[name]; got != want {
		t.Fatalf("%s calls = %d; want %d", name, got, want)
	}
}

func (r *testContainerRuntime) Spawn(context.Context, string) (string, error) {
	r.record("spawn")
	return "container-1", nil
}

func (r *testContainerRuntime) Exec(
	context.Context,
	string,
	[]string,
	[]string,
) (ExecRPCResponse, error) {
	r.record("exec")
	return ExecRPCResponse{ExitCode: 0, Duration: time.Millisecond}, nil
}

func (r *testContainerRuntime) CopyIn(
	_ context.Context,
	_ string,
	_ string,
	reader io.Reader,
) (int64, error) {
	r.record("copy_in")
	return io.Copy(io.Discard, reader)
}

func (r *testContainerRuntime) CopyOut(
	context.Context,
	string,
	CopyOutSource,
) (RuntimeCopyOut, error) {
	r.record("copy_out")
	return RuntimeCopyOut{
		Reader: io.NopCloser(strings.NewReader("abc")),
		Files:  1,
		Bytes:  3,
	}, nil
}

func (r *testContainerRuntime) Mkdir(context.Context, string, []string) error {
	r.record("mkdir")
	return nil
}

func (r *testContainerRuntime) Inspect(
	context.Context,
	string,
) (RuntimeInspect, error) {
	r.record("inspect")
	return RuntimeInspect{Status: "running"}, nil
}

func (r *testContainerRuntime) Delete(context.Context, string) error {
	r.record("delete")
	return nil
}

func (testRPCService) CreateLease(
	context.Context,
	PeerCredentials,
	CreateLeaseRPCRequest,
) (CreateLeaseRPCResponse, error) {
	return CreateLeaseRPCResponse{
		LeaseID:   "lease-1",
		State:     string(LeaseReady),
		ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}

func (testRPCService) Activate(
	context.Context,
	PeerCredentials,
	string,
	ActivateRPCRequest,
) error {
	return nil
}

func (testRPCService) Heartbeat(
	context.Context,
	PeerCredentials,
	string,
	MutationRPCRequest,
) error {
	return nil
}

func (testRPCService) MarkPersisting(
	context.Context,
	PeerCredentials,
	string,
	MutationRPCRequest,
) error {
	return nil
}

func (testRPCService) Exec(
	context.Context,
	PeerCredentials,
	string,
	ExecRPCRequest,
) (ExecRPCResponse, error) {
	return ExecRPCResponse{}, nil
}

func (s testRPCService) CopyIn(
	_ context.Context,
	_ PeerCredentials,
	_ string,
	_ string,
	_ string,
	reader io.Reader,
) error {
	if s.copyIn != nil {
		return s.copyIn(reader)
	}
	_, err := io.Copy(io.Discard, reader)
	return err
}

func (testRPCService) CopyOut(
	context.Context,
	PeerCredentials,
	string,
	string,
	string,
) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("tar")), nil
}

func (testRPCService) Mkdir(
	context.Context,
	PeerCredentials,
	string,
	MkdirRPCRequest,
) error {
	return nil
}

func (testRPCService) Inspect(
	context.Context,
	PeerCredentials,
	string,
) (InspectRPCResponse, error) {
	return InspectRPCResponse{
		Status:      "running",
		OwnerID:     "api-blue",
		OwnerBootID: "boot-1",
	}, nil
}

func (testRPCService) ListLeases(
	context.Context,
	PeerCredentials,
	string,
) ([]string, error) {
	return []string{"lease-1"}, nil
}

func (testRPCService) Delete(
	context.Context,
	PeerCredentials,
	string,
	MutationRPCRequest,
) error {
	return nil
}
