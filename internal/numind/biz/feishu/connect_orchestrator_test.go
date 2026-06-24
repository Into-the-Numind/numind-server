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

// newOrchSigner builds a real StateSigner over the deterministic test key + an
// in-memory nonce store (reuses the state_test fakeNonceStore).
func newOrchSigner(t *testing.T) *StateSigner {
	t.Helper()
	s, err := NewStateSigner(testKey, newFakeNonceStore())
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}
	return s
}

func newTestOrchestrator(t *testing.T, store *svcAccountStore, starter AppStarter, poller AppPoller) *ConnectOrchestrator {
	t.Helper()
	o, err := NewConnectOrchestrator(ConnectOrchestratorDeps{
		Store:        store,
		Signer:       newOrchSigner(t),
		Starter:      starter,
		Poller:       poller,
		AuthorizeURL: "https://open.feishu.cn/open-apis/authen/v1/authorize",
		RedirectURI:  "https://youshu.asia/api/v1/feishu/callback",
		ScopesCSV:    "docx:document im:message",
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
	o := newTestOrchestrator(t, store, starter, &orchAppPoller{})

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

func TestNextConnectStep_AppRowNoToken_Authorizes(t *testing.T) {
	store := newSvcAccountStore()
	// App provisioned (appID present) but no access token yet → authorize step.
	store.put(&model.UserThirdPartyAccount{
		UserID:       6,
		Provider:     ProviderLark,
		AppID:        "cli_app6",
		AppSecretEnc: []byte("enc-secret"),
	})
	o := newTestOrchestrator(t, store, &orchAppStarter{}, &orchAppPoller{})

	step, err := o.NextConnectStep(context.Background(), 6, 200, "授权提示")
	if err != nil {
		t.Fatalf("NextConnectStep: %v", err)
	}
	if step.Phase != ConnectPhaseAuthorize {
		t.Fatalf("phase mismatch: %q", step.Phase)
	}
	if !strings.Contains(step.URL, "app_id=cli_app6") {
		t.Fatalf("authorize URL must carry app_id: %q", step.URL)
	}
	if !strings.Contains(step.URL, "state=") {
		t.Fatalf("authorize URL must carry a signed state: %q", step.URL)
	}
	if !strings.Contains(step.URL, "scope=") {
		t.Fatalf("authorize URL must request scopes: %q", step.URL)
	}
}

func TestNextConnectStep_HasValidToken_Done(t *testing.T) {
	store := newSvcAccountStore()
	future := time.Now().Add(time.Hour)
	store.put(&model.UserThirdPartyAccount{
		UserID:         7,
		Provider:       ProviderLark,
		AppID:          "cli_app7",
		AccessTokenEnc: []byte("enc-access"),
		TokenExpiresAt: &future,
	})
	o := newTestOrchestrator(t, store, &orchAppStarter{}, &orchAppPoller{})

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

func TestNextConnectStep_ExpiredToken_Authorizes(t *testing.T) {
	store := newSvcAccountStore()
	past := time.Now().Add(-time.Hour)
	store.put(&model.UserThirdPartyAccount{
		UserID:         8,
		Provider:       ProviderLark,
		AppID:          "cli_app8",
		AccessTokenEnc: []byte("enc-access"),
		TokenExpiresAt: &past, // expired → re-authorize, not done
	})
	o := newTestOrchestrator(t, store, &orchAppStarter{}, &orchAppPoller{})

	step, err := o.NextConnectStep(context.Background(), 8, 400, "授权提示")
	if err != nil {
		t.Fatalf("NextConnectStep: %v", err)
	}
	if step.Phase != ConnectPhaseAuthorize {
		t.Fatalf("expired token must re-authorize, got %q", step.Phase)
	}
}

// --- PollAndPersistApp ------------------------------------------------------

func TestPollAndPersistApp_PersistsCredsWhenReady(t *testing.T) {
	store := newSvcAccountStore()
	poller := &orchAppPoller{appID: "cli_new", secEnc: []byte("enc-app-secret"), done: true}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, poller)

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
	if string(row.AppSecretEnc) != "enc-app-secret" {
		t.Fatalf("app secret must be stored as the encrypted blob, got %q", row.AppSecretEnc)
	}
	// No token yet at app-create time.
	if len(row.AccessTokenEnc) != 0 {
		t.Fatal("app-create persistence must not write a token")
	}
}

func TestPollAndPersistApp_NotReady_NoWrite(t *testing.T) {
	store := newSvcAccountStore()
	poller := &orchAppPoller{done: false}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, poller)

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

func TestPollAndPersistApp_PreservesExistingScopes(t *testing.T) {
	store := newSvcAccountStore()
	// A prior row exists (e.g. a stale re-provision); persisting new app creds
	// must not blow away an existing token, only refresh app_id + secret.
	existing := []byte("enc-old-access")
	store.put(&model.UserThirdPartyAccount{
		UserID:         11,
		Provider:       ProviderLark,
		AppID:          "cli_old",
		AccessTokenEnc: existing,
		Scopes:         "docx:document",
	})
	poller := &orchAppPoller{appID: "cli_old", secEnc: []byte("enc-secret2"), done: true}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, poller)

	if _, err := o.PollAndPersistApp(context.Background(), 11); err != nil {
		t.Fatalf("PollAndPersistApp: %v", err)
	}
	row, _ := store.Get(context.Background(), 11, ProviderLark)
	if string(row.AccessTokenEnc) != "enc-old-access" {
		t.Fatal("existing access token must be preserved across app re-persist")
	}
	if row.Scopes != "docx:document" {
		t.Fatalf("existing scopes must be preserved, got %q", row.Scopes)
	}
}

func TestPollAndPersistApp_PropagatesPollError(t *testing.T) {
	store := newSvcAccountStore()
	poller := &orchAppPoller{err: errPollBoom}
	o := newTestOrchestrator(t, store, &orchAppStarter{}, poller)

	if _, err := o.PollAndPersistApp(context.Background(), 12); err == nil {
		t.Fatal("poll error must propagate")
	}
}

// --- constructor guards -----------------------------------------------------

func TestNewConnectOrchestrator_NilDeps(t *testing.T) {
	good := ConnectOrchestratorDeps{
		Store:        newSvcAccountStore(),
		Signer:       newOrchSigner(t),
		Starter:      &orchAppStarter{},
		Poller:       &orchAppPoller{},
		AuthorizeURL: "https://x",
		RedirectURI:  "https://y",
	}
	mut := func(f func(d *ConnectOrchestratorDeps)) ConnectOrchestratorDeps {
		d := good
		f(&d)
		return d
	}
	cases := map[string]ConnectOrchestratorDeps{
		"nil store":       mut(func(d *ConnectOrchestratorDeps) { d.Store = nil }),
		"nil signer":      mut(func(d *ConnectOrchestratorDeps) { d.Signer = nil }),
		"nil starter":     mut(func(d *ConnectOrchestratorDeps) { d.Starter = nil }),
		"nil poller":      mut(func(d *ConnectOrchestratorDeps) { d.Poller = nil }),
		"empty authorize": mut(func(d *ConnectOrchestratorDeps) { d.AuthorizeURL = "" }),
		"empty redirect":  mut(func(d *ConnectOrchestratorDeps) { d.RedirectURI = "" }),
	}
	for name, d := range cases {
		if _, err := NewConnectOrchestrator(d); err == nil {
			t.Fatalf("%s must error", name)
		}
	}
}
