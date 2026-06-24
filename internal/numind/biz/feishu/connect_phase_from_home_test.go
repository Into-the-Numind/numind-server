package feishu

// connect_phase_from_home_test.go — regression for the connect phase-state-machine
// root bug (fix/feishu-phase-from-home, 2026-06-24):
//
//	NextConnectStep historically decided the connect PHASE from the DATABASE row
//	(acc.AppID != "" / acc.Connected). The real truth lives in lark-cli's per-user
//	home (config.json for "app built?", `auth status` for "authorized?"). When the
//	two disagreed — home has the app but the DB row is absent / app_id blank — the
//	old code re-ran StartProvision and re-created the app over and over, and the
//	in-memory provisioning lock could wedge ("provisioning already in progress").
//
// These tests pin the FIXED behaviour: the home is the single phase truth source,
// driven through the orchestrator's AppInspector seam (AppExists).
//   - home has app, NOT authorized, DB row absent → authorize (NOT create_app).
//   - home has app, authorized, DB row absent     → done + DB reconciled.
//   - no app in the home                          → create_app.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gorm.io/gorm"
)

// --- regression: home has app, DB row absent → authorize, NOT create_app ----

func TestNextConnectStep_HomeHasAppDBEmpty_GoesToAuthorizeNotRecreate(t *testing.T) {
	store := newSvcAccountStore()
	// DB has NO row for user 42 (the bug trigger). The home, however, already has
	// the app built (AppExists=true) but is not authorized yet.
	starter := &orchAppStarter{pageURL: "https://open.feishu.cn/page/cli?user_code=NEW", ref: "u42", appExists: true}
	auth := &orchAuthorizer{startURL: "https://open.feishu.cn/device?user_code=AUTH"}
	o := newTestOrchestrator(t, store, starter, &orchAppPoller{}, auth)

	step, err := o.NextConnectStep(context.Background(), 42, 0, "")
	if err != nil {
		t.Fatalf("NextConnectStep: %v", err)
	}
	if step.Phase != ConnectPhaseAuthorize {
		t.Fatalf("home-has-app + DB-empty must go to authorize, got %q", step.Phase)
	}
	if starter.calls != 0 {
		t.Fatalf("must NOT re-provision when the home already has an app, got %d StartProvision calls", starter.calls)
	}
	if step.URL == "" {
		t.Fatal("authorize step must carry a verification URL")
	}
}

// --- regression: home has app + authorized, DB empty → done + DB reconciled --

func TestNextConnectStep_HomeAuthorizedDBEmpty_DoneAndReconciles(t *testing.T) {
	store := newSvcAccountStore()
	// DB has NO row; the home has the app AND is already authorized.
	starter := &orchAppStarter{appExists: true}
	auth := &orchAuthorizer{authorized: true}
	o := newTestOrchestrator(t, store, starter, &orchAppPoller{}, auth)

	step, err := o.NextConnectStep(context.Background(), 7, 0, "")
	if err != nil {
		t.Fatalf("NextConnectStep: %v", err)
	}
	if step.Phase != ConnectPhaseDone {
		t.Fatalf("home-authorized must be done, got %q", step.Phase)
	}
	if starter.calls != 0 {
		t.Fatalf("authorized home must not re-provision, got %d calls", starter.calls)
	}
	row, gerr := store.Get(context.Background(), 7, ProviderLark)
	if gerr != nil {
		t.Fatalf("DB row must be reconciled on done, got %v", gerr)
	}
	if !row.Connected {
		t.Fatal("done must reconcile DB connected=true")
	}
}

// --- no app anywhere → create_app -------------------------------------------

func TestNextConnectStep_NoAppInHome_GoesToCreateApp(t *testing.T) {
	store := newSvcAccountStore()
	starter := &orchAppStarter{pageURL: "https://open.feishu.cn/page/cli?user_code=NEW", ref: "u3", appExists: false}
	o := newTestOrchestrator(t, store, starter, &orchAppPoller{}, &orchAuthorizer{})

	step, err := o.NextConnectStep(context.Background(), 3, 0, "")
	if err != nil {
		t.Fatalf("NextConnectStep: %v", err)
	}
	if step.Phase != ConnectPhaseCreateApp {
		t.Fatalf("no app in home must go to create_app, got %q", step.Phase)
	}
	if _, gerr := store.Get(context.Background(), 3, ProviderLark); !errors.Is(gerr, gorm.ErrRecordNotFound) {
		t.Fatalf("create_app must not write a DB row, got %v", gerr)
	}
}

// --- regression (P1): alive in-flight auth-login → authorize, NOT a hard error ---

// TestNextConnectStep_AuthLoginAlreadyInFlight_ReturnsAuthorizeNotError pins the P1
// fix end-to-end through a REAL Provisioner + LarkCLIRunner (over the blocking auth
// fake), so the in-flight session guard is genuinely exercised:
//
//	The agent gives the user the verification link; before the user finishes the
//	browser step, the run re-executes NextConnectStep. AppExists=true (home has the
//	app), IsAuthorized=false (not yet authorized), so it calls StartAuthorize again —
//	but the previous `auth login` process is STILL ALIVE. Pre-fix this surfaced
//	"auth login already in progress for user N" as a hard NextConnectStep failure.
//	The fix returns the cached verification URL instead, so the step is a clean
//	ConnectPhaseAuthorize and the user just re-opens the same link.
func TestNextConnectStep_AuthLoginAlreadyInFlight_ReturnsAuthorizeNotError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	bin := writeFakeLarkCLI(t, blockingAuthLoginScript)
	runner, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	const wantURL = "https://open.feishu.cn/suite/passport/oauth/device?user_code=INFLIGHT"
	// Home already holds the app (AppExists=true) but no token yet (not authorized).
	writeConfigJSON(t, homeBase, 9, "cli_inflight", "secret-inflight")
	seedFakeAuthURL(t, homeBase, 9, wantURL)

	prov := newTestProvisioner(t, runner)
	store := newSvcAccountStore()
	o := newTestOrchestrator(t, store, prov, prov, prov)

	// First call: home has the app, not authorized → launches the (blocking) auth
	// login and returns the authorize step with the scraped URL.
	step1, err := o.NextConnectStep(context.Background(), 9, 0, "")
	if err != nil {
		t.Fatalf("first NextConnectStep: %v", err)
	}
	if step1.Phase != ConnectPhaseAuthorize || step1.URL != wantURL {
		t.Fatalf("first step mismatch: phase=%q url=%q", step1.Phase, step1.URL)
	}

	// Second call while the auth-login is STILL ALIVE must NOT error — it returns the
	// same authorize step (cached URL), letting the user re-open the link.
	step2, err := o.NextConnectStep(context.Background(), 9, 0, "")
	if err != nil {
		t.Fatalf("second NextConnectStep on an alive auth-login must NOT error, got: %v", err)
	}
	if step2.Phase != ConnectPhaseAuthorize || step2.URL != wantURL {
		t.Fatalf("second step must be authorize with the cached URL: phase=%q url=%q", step2.Phase, step2.URL)
	}

	// Release the blocking auth-login process so it writes the token and exits cleanly.
	if werr := os.WriteFile(filepath.Join(runner.homeForUser(9), ".unblock"), []byte("1"), 0o600); werr != nil {
		t.Fatalf("unblock auth login: %v", werr)
	}
}
