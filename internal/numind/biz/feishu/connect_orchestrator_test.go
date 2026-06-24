package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"numind-server/internal/pkg/model"

	"gorm.io/gorm"
)

var errPollBoom = errors.New("poll boom")

// --- orchestrator test doubles ----------------------------------------------

// orchAppStarter fakes the AppStarter seam (StartProvision) for the create-app
// branch — independent of the real lark-cli runner.
type orchAppStarter struct {
	pageURL string
	ref     string
	err     error
	calls   int
}

func (f *orchAppStarter) StartProvision(_ context.Context, _ uint) (string, string, error) {
	f.calls++
	if f.err != nil {
		return "", "", f.err
	}
	return f.pageURL, f.ref, nil
}

// orchAppPoller fakes the per-user poll-and-persist seam (PollCredentialsForUser).
type orchAppPoller struct {
	appID  string
	secEnc []byte
	done   bool
	err    error
	calls  int
}

func (f *orchAppPoller) PollCredentialsForUser(_ context.Context, _ uint) (string, []byte, bool, error) {
	f.calls++
	return f.appID, f.secEnc, f.done, f.err
}

// orchAuthorizer fakes the device-code Authorizer seam.
type orchAuthorizer struct {
	startURL      string
	startErr      error
	startCalls    int
	completeErr   error
	completeCalls int
	pending       bool
	authorized    bool
	authStatusErr error
}

func (f *orchAuthorizer) StartAuthorize(_ context.Context, _ uint) (string, error) {
	f.startCalls++
	return f.startURL, f.startErr
}
func (f *orchAuthorizer) CompleteAuthorize(_ context.Context, _ uint) error {
	f.completeCalls++
	return f.completeErr
}
func (f *orchAuthorizer) HasPendingAuthorize(_ uint) bool { return f.pending }
func (f *orchAuthorizer) IsAuthorized(_ context.Context, _ uint) (bool, error) {
	return f.authorized, f.authStatusErr
}

func newTestOrchestrator(t *testing.T, store *svcAccountStore, starter AppStarter, poller AppPoller, auth Authorizer) *ConnectOrchestrator {
	t.Helper()
	o, err := NewConnectOrchestrator(ConnectOrchestratorDeps{
		Store:      store,
		Starter:    starter,
		Poller:     poller,
		Authorizer: auth,
	})
	if err != nil {
		t.Fatalf("NewConnectOrchestrator: %v", err)
	}
	return o
}

// --- NextConnectStep --------------------------------------------------------

func TestNextConnectStep_NoRow_StartsAppCreate(t *testing.T) {
	store := newSvcAccountStore()
	starter := &orchAppStarter{pageURL: "https://open.feishu.cn/page/cli?user_code=ABC", ref: "u5"}
	o := newTestOrchestrator(t, store, starter, &orchAppPoller{}, &orchAuthorizer{})

	step, err := o.NextConnectStep(context.Background(), 5, 100, "授权提示")
	if err != nil {
		t.Fatalf("NextConnectStep: %v", err)
	}
	if step.Phase != ConnectPhaseCreateApp {
		t.Fatalf("phase mismatch: %q", step.Phase)
	}
	if !strings.HasPrefix(step.URL, "https://open.feishu.cn/page/cli") {
		t.Fatalf("create-app URL mismatch: %q", step.URL)
	}
	if starter.calls != 1 {
		t.Fatalf("StartProvision should be called once, got %d", starter.calls)
	}
}

func TestNextConnectStep_AppRowNotConnected_StartsAuthorize(t *testing.T) {
	store := newSvcAccountStore()
	// App provisioned (appID present) but not connected, nothing pending → start
	// the device flow and return the verification URL.
	store.put(&model.UserThirdPartyAccount{
		UserID:   6,
		Provider: ProviderLark,
		AppID:    "cli_app6",
	})
	auth := &orchAuthorizer{startURL: "https://open.feishu.cn/device?user_code=XYZ"}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, &orchAppPoller{}, auth)

	step, err := o.NextConnectStep(context.Background(), 6, 200, "授权提示")
	if err != nil {
		t.Fatalf("NextConnectStep: %v", err)
	}
	if step.Phase != ConnectPhaseAuthorize {
		t.Fatalf("phase mismatch: %q", step.Phase)
	}
	if !strings.HasPrefix(step.URL, "https://open.feishu.cn/") {
		t.Fatalf("authorize URL mismatch: %q", step.URL)
	}
	if auth.startCalls != 1 {
		t.Fatalf("StartAuthorize should be called once, got %d", auth.startCalls)
	}
}

func TestNextConnectStep_Connected_Done(t *testing.T) {
	store := newSvcAccountStore()
	at := time.Now()
	store.put(&model.UserThirdPartyAccount{
		UserID:      7,
		Provider:    ProviderLark,
		AppID:       "cli_app7",
		Connected:   true,
		ConnectedAt: &at,
	})
	o := newTestOrchestrator(t, store, &orchAppStarter{}, &orchAppPoller{}, &orchAuthorizer{})

	step, err := o.NextConnectStep(context.Background(), 7, 300, "授权提示")
	if err != nil {
		t.Fatalf("NextConnectStep: %v", err)
	}
	if step.Phase != ConnectPhaseDone {
		t.Fatalf("expected done, got %q", step.Phase)
	}
	if step.URL != "" {
		t.Fatalf("done must carry no URL, got %q", step.URL)
	}
}

func TestNextConnectStep_PendingDeviceCode_CompletesAndMarksConnected(t *testing.T) {
	store := newSvcAccountStore()
	store.put(&model.UserThirdPartyAccount{
		UserID:   8,
		Provider: ProviderLark,
		AppID:    "cli_app8",
	})
	auth := &orchAuthorizer{pending: true}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, &orchAppPoller{}, auth)

	step, err := o.NextConnectStep(context.Background(), 8, 400, "授权提示")
	if err != nil {
		t.Fatalf("NextConnectStep: %v", err)
	}
	if step.Phase != ConnectPhaseDone {
		t.Fatalf("pending device code completion should yield done, got %q", step.Phase)
	}
	if auth.completeCalls != 1 {
		t.Fatalf("CompleteAuthorize should be called once, got %d", auth.completeCalls)
	}
	// DB must now reflect connected.
	row, _ := store.Get(context.Background(), 8, ProviderLark)
	if !row.Connected {
		t.Fatal("DB row must be marked connected after device-code completion")
	}
	if row.ConnectedAt == nil {
		t.Fatal("connected_at must be set")
	}
}

func TestNextConnectStep_PendingButCompletionFails_RestartsAuthorize(t *testing.T) {
	store := newSvcAccountStore()
	store.put(&model.UserThirdPartyAccount{
		UserID:   9,
		Provider: ProviderLark,
		AppID:    "cli_app9",
	})
	// Completion fails (e.g. device code expired) → restart a fresh authorize.
	auth := &orchAuthorizer{pending: true, completeErr: errors.New("device code expired"), startURL: "https://open.feishu.cn/device?fresh"}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, &orchAppPoller{}, auth)

	step, err := o.NextConnectStep(context.Background(), 9, 500, "授权提示")
	if err != nil {
		t.Fatalf("NextConnectStep: %v", err)
	}
	if step.Phase != ConnectPhaseAuthorize {
		t.Fatalf("failed completion should restart authorize, got %q", step.Phase)
	}
	if auth.startCalls != 1 {
		t.Fatalf("StartAuthorize should be called once on restart, got %d", auth.startCalls)
	}
	// Not connected (completion failed).
	row, _ := store.Get(context.Background(), 9, ProviderLark)
	if row.Connected {
		t.Fatal("row must NOT be connected when completion failed")
	}
}

func TestNextConnectStep_OutOfBandAuthorized_MarksConnectedDone(t *testing.T) {
	store := newSvcAccountStore()
	store.put(&model.UserThirdPartyAccount{
		UserID:   10,
		Provider: ProviderLark,
		AppID:    "cli_app10",
	})
	// DB says not connected, nothing pending, but lark-cli reports an authorization.
	auth := &orchAuthorizer{pending: false, authorized: true}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, &orchAppPoller{}, auth)

	step, err := o.NextConnectStep(context.Background(), 10, 600, "授权提示")
	if err != nil {
		t.Fatalf("NextConnectStep: %v", err)
	}
	if step.Phase != ConnectPhaseDone {
		t.Fatalf("out-of-band authorized should be done, got %q", step.Phase)
	}
	row, _ := store.Get(context.Background(), 10, ProviderLark)
	if !row.Connected {
		t.Fatal("out-of-band authorized must reconcile connected=true")
	}
}

// --- PollAndPersistApp ------------------------------------------------------

func TestPollAndPersistApp_PersistsAppIDWhenReady(t *testing.T) {
	store := newSvcAccountStore()
	poller := &orchAppPoller{appID: "cli_new", secEnc: []byte("enc-app-secret"), done: true}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, poller, &orchAuthorizer{})

	persisted, err := o.PollAndPersistApp(context.Background(), 9)
	if err != nil {
		t.Fatalf("PollAndPersistApp: %v", err)
	}
	if !persisted {
		t.Fatal("expected persisted=true when creds ready")
	}
	row, err := store.Get(context.Background(), 9, ProviderLark)
	if err != nil {
		t.Fatalf("row should be persisted: %v", err)
	}
	if row.AppID != "cli_new" {
		t.Fatalf("appID mismatch: %q", row.AppID)
	}
	// device-code: no token written, and not yet connected (authorize is separate).
	if len(row.AccessTokenEnc) != 0 {
		t.Fatal("app-create persistence must not write a token (device-code)")
	}
	if row.Connected {
		t.Fatal("app-create persistence must not mark connected (authorize is separate)")
	}
}

func TestPollAndPersistApp_NotReady_NoWrite(t *testing.T) {
	store := newSvcAccountStore()
	poller := &orchAppPoller{done: false}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, poller, &orchAuthorizer{})

	persisted, err := o.PollAndPersistApp(context.Background(), 10)
	if err != nil {
		t.Fatalf("PollAndPersistApp: %v", err)
	}
	if persisted {
		t.Fatal("expected persisted=false when not ready")
	}
	if _, err := store.Get(context.Background(), 10, ProviderLark); err != gorm.ErrRecordNotFound {
		t.Fatalf("nothing should be persisted when not ready, got %v", err)
	}
	if store.upserts != 0 {
		t.Fatalf("no upsert when not ready, got %d", store.upserts)
	}
}

func TestPollAndPersistApp_PreservesExistingConnected(t *testing.T) {
	store := newSvcAccountStore()
	// A prior row exists already connected; persisting new app creds must not blow
	// away the connected flag, only refresh app_id.
	at := time.Now()
	store.put(&model.UserThirdPartyAccount{
		UserID:      11,
		Provider:    ProviderLark,
		AppID:       "cli_old",
		Connected:   true,
		ConnectedAt: &at,
	})
	poller := &orchAppPoller{appID: "cli_old", secEnc: []byte("enc-secret2"), done: true}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, poller, &orchAuthorizer{})

	if _, err := o.PollAndPersistApp(context.Background(), 11); err != nil {
		t.Fatalf("PollAndPersistApp: %v", err)
	}
	row, _ := store.Get(context.Background(), 11, ProviderLark)
	if !row.Connected {
		t.Fatal("existing connected flag must be preserved across app re-persist")
	}
}

func TestPollAndPersistApp_PropagatesPollError(t *testing.T) {
	store := newSvcAccountStore()
	poller := &orchAppPoller{err: errPollBoom}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, poller, &orchAuthorizer{})

	if _, err := o.PollAndPersistApp(context.Background(), 12); err == nil {
		t.Fatal("poll error must propagate")
	}
}

// --- constructor guards -----------------------------------------------------

func TestNewConnectOrchestrator_NilDeps(t *testing.T) {
	good := ConnectOrchestratorDeps{
		Store:      newSvcAccountStore(),
		Starter:    &orchAppStarter{},
		Poller:     &orchAppPoller{},
		Authorizer: &orchAuthorizer{},
	}
	mut := func(f func(d *ConnectOrchestratorDeps)) ConnectOrchestratorDeps {
		d := good
		f(&d)
		return d
	}
	cases := map[string]ConnectOrchestratorDeps{
		"nil store":      mut(func(d *ConnectOrchestratorDeps) { d.Store = nil }),
		"nil starter":    mut(func(d *ConnectOrchestratorDeps) { d.Starter = nil }),
		"nil poller":     mut(func(d *ConnectOrchestratorDeps) { d.Poller = nil }),
		"nil authorizer": mut(func(d *ConnectOrchestratorDeps) { d.Authorizer = nil }),
	}
	for name, d := range cases {
		if _, err := NewConnectOrchestrator(d); err == nil {
			t.Fatalf("%s must error", name)
		}
	}
}
