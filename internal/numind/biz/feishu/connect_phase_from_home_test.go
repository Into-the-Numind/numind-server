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
