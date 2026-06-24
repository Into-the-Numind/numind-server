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

	// appExists models the home-truth phase source: whether the user's lark-cli home
	// already holds a built app (config.json apps[0]). appExistsErr forces an error.
	appExists      bool
	appExistsErr   error
	appExistsCalls int

	// appID is returned by AppID (home reconcile on the done path); appIDErr forces
	// an error. Defaults to a benign non-empty appId so the done path can reconcile.
	appID    string
	appIDErr error
}

func (f *orchAppStarter) StartProvision(_ context.Context, _ uint) (string, string, error) {
	f.calls++
	if f.err != nil {
		return "", "", f.err
	}
	return f.pageURL, f.ref, nil
}

func (f *orchAppStarter) AppExists(_ context.Context, _ uint) (bool, error) {
	f.appExistsCalls++
	return f.appExists, f.appExistsErr
}

func (f *orchAppStarter) AppID(_ context.Context, _ uint) (string, error) {
	if f.appIDErr != nil {
		return "", f.appIDErr
	}
	if f.appID == "" {
		return "cli_fake_app", nil
	}
	return f.appID, nil
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

// orchAuthorizer fakes the Authorizer seam (blocking auth-login model).
type orchAuthorizer struct {
	startURL      string
	startErr      error
	startCalls    int
	authorized    bool
	authStatusErr error
}

func (f *orchAuthorizer) StartAuthorize(_ context.Context, _ uint) (string, error) {
	f.startCalls++
	return f.startURL, f.startErr
}
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

// --- NextConnectStep (home-truth phase routing) -----------------------------

func TestNextConnectStep_NoAppInHome_StartsAppCreate(t *testing.T) {
	store := newSvcAccountStore()
	// Home has NO app (appExists=false) → create_app, regardless of DB.
	starter := &orchAppStarter{pageURL: "https://open.feishu.cn/page/cli?user_code=ABC", ref: "u5", appExists: false}
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

func TestNextConnectStep_AppInHomeNotAuthorized_StartsAuthorize(t *testing.T) {
	store := newSvcAccountStore()
	// Home has the app but is not authorized → start auth-login, return the URL.
	starter := &orchAppStarter{appExists: true}
	auth := &orchAuthorizer{startURL: "https://open.feishu.cn/device?user_code=XYZ", authorized: false}
	o := newTestOrchestrator(t, store, starter, &orchAppPoller{}, auth)

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
	if starter.calls != 0 {
		t.Fatalf("must not re-provision when the home has an app, got %d", starter.calls)
	}
}

func TestNextConnectStep_AppInHomeAuthorized_DoneReconciles(t *testing.T) {
	store := newSvcAccountStore()
	// Home has the app AND is authorized → done; DB reconciled (connected + app_id).
	starter := &orchAppStarter{appExists: true, appID: "cli_app7"}
	auth := &orchAuthorizer{authorized: true}
	o := newTestOrchestrator(t, store, starter, &orchAppPoller{}, auth)

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
	row, gerr := store.Get(context.Background(), 7, ProviderLark)
	if gerr != nil {
		t.Fatalf("done must reconcile a DB row, got %v", gerr)
	}
	if !row.Connected {
		t.Fatal("done must reconcile connected=true")
	}
	if row.AppID != "cli_app7" {
		t.Fatalf("done must reconcile app_id from the home, got %q", row.AppID)
	}
	if row.ConnectedAt == nil {
		t.Fatal("connected_at must be set on reconcile")
	}
}

func TestNextConnectStep_AppExistsError_Propagates(t *testing.T) {
	store := newSvcAccountStore()
	starter := &orchAppStarter{appExistsErr: errors.New("home read boom")}
	o := newTestOrchestrator(t, store, starter, &orchAppPoller{}, &orchAuthorizer{})

	if _, err := o.NextConnectStep(context.Background(), 1, 0, ""); err == nil {
		t.Fatal("AppExists error must propagate (never silently route to create_app)")
	}
	if starter.calls != 0 {
		t.Fatal("must not provision when the home-read failed")
	}
}

func TestNextConnectStep_AuthStatusError_Propagates(t *testing.T) {
	store := newSvcAccountStore()
	starter := &orchAppStarter{appExists: true}
	auth := &orchAuthorizer{authStatusErr: errors.New("auth status boom")}
	o := newTestOrchestrator(t, store, starter, &orchAppPoller{}, auth)

	if _, err := o.NextConnectStep(context.Background(), 1, 0, ""); err == nil {
		t.Fatal("IsAuthorized error must propagate")
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
