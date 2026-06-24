package feishu

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeConfigInitScript stands in for the real `lark-cli config init --new`. It is
// HOME-aware (lark-cli reads $HOME/.lark-cli/config.json): it prints the page URL
// the orchestration scrapes, then — to simulate the user finishing in the browser
// — writes apps[0] into $HOME/.lark-cli/config.json from the appId/appSecret the
// test seeds in $HOME/.fake-creds, then exits 0.
//
// A sleep before exit lets a test observe the "in-progress" window (URL printed,
// process still running, config.json not yet written) if it polls fast enough;
// here we keep it near-instant so the happy path is deterministic and fast.
const fakeConfigInitScript = `#!/bin/sh
set -e
# StartAppCreate launches: lark-cli config init --new
if [ "$1" = "config" ] && [ "$2" = "init" ]; then
  printf '打开以下链接配置应用:\n'
  printf '  https://open.feishu.cn/page/cli?user_code=2AF7-MFWU&lpv=1.0.56&from=cli\n'
  printf '等待配置应用...\n'
  appid=$(cat "$HOME/.fake-creds/appid" 2>/dev/null || true)
  secret=$(cat "$HOME/.fake-creds/secret" 2>/dev/null || true)
  mkdir -p "$HOME/.lark-cli"
  printf '{"apps":[{"appId":"%s","appSecret":"%s","brand":"feishu"}]}\n' "$appid" "$secret" > "$HOME/.lark-cli/config.json"
  exit 0
fi
echo "unhandled args: $@" >&2
exit 1
`

// writeFakeLarkCLI drops the stub script into a temp dir and returns its path.
func writeFakeLarkCLI(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "lark-cli")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { // #nosec G306 -- executable test stub
		t.Fatalf("write fake lark-cli: %v", err)
	}
	return bin
}

// seedFakeCreds writes the appId/secret the fakeConfigInitScript will emit for a
// given user's scratch HOME (homeBase/u{userID}).
func seedFakeCreds(t *testing.T, homeBase string, userID uint, appID, secret string) {
	t.Helper()
	dir := filepath.Join(homeBase, "u"+itoa(userID), ".fake-creds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir fake creds: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "appid"), []byte(appID), 0o600); err != nil {
		t.Fatalf("seed appid: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte(secret), 0o600); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
}

// writeConfigJSON writes a config.json with apps[0] directly under
// homeBase/u{userID}/.lark-cli/ (simulates a previously-provisioned user).
func writeConfigJSON(t *testing.T, homeBase string, userID uint, appID, secret string) {
	t.Helper()
	cfgDir := filepath.Join(homeBase, "u"+itoa(userID), ".lark-cli")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	raw, _ := json.Marshal(larkConfigJSON{Apps: []larkConfigApp{{AppID: appID, AppSecret: secret}}})
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), raw, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
}

func itoa(u uint) string {
	if u == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}

// --- StartAppCreate + PollAppCreated (real os/exec over fake lark-cli) -------

// TestStartAppCreate_ScrapesURLThenPollReadsConfig is the end-to-end happy path:
// the runner launches the fake `config init`, scrapes the page URL, then polls
// until config.json appears and reads appId/appSecret straight out of it.
func TestStartAppCreate_ScrapesURLThenPollReadsConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	seedFakeCreds(t, homeBase, 42, "cli_e2e", "secret-e2e")

	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}

	pageURL, handle, err := r.StartAppCreate(context.Background(), 42)
	if err != nil {
		t.Fatalf("StartAppCreate: %v", err)
	}
	if !strings.HasPrefix(pageURL, "https://open.feishu.cn/page/cli") {
		t.Fatalf("page URL mismatch: %q", pageURL)
	}
	if handle == nil || handle.home == "" {
		t.Fatal("handle must carry a scratch home")
	}

	// Poll until done (the fake exits near-instantly after writing config.json).
	var appID, secret string
	deadline := time.Now().Add(5 * time.Second)
	for {
		var done bool
		appID, secret, done, err = r.PollAppCreated(context.Background(), handle)
		if err != nil {
			t.Fatalf("PollAppCreated: %v", err)
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PollAppCreated never reported done")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if appID != "cli_e2e" || secret != "secret-e2e" {
		t.Fatalf("read wrong creds from config.json: appID=%q secret=%q", appID, secret)
	}

	// On completion the in-flight session tracking is cleaned up.
	r.mu.Lock()
	_, present := r.sessions[handle.home]
	r.mu.Unlock()
	if present {
		t.Fatal("PollAppCreated should drop the in-flight session on completion")
	}
}

// TestStartAppCreate_PersistentHomeIsReusedIdempotently is the G1-home regression:
// the per-user HOME (homeBase/u{userID}) is deterministic and PERSISTENT, so a
// second provision for the same user lands in the SAME directory (reuses the
// existing config.json — no /tmp scratch wipe between connects). It also asserts the
// home is created 0700 (owner-only).
func TestStartAppCreate_PersistentHomeIsReusedIdempotently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	seedFakeCreds(t, homeBase, 42, "cli_persist", "secret-persist")

	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}

	wantHome := filepath.Join(homeBase, "u42")

	// First provision → home created under homeBase, completes.
	_, handle, err := r.StartAppCreate(context.Background(), 42)
	if err != nil {
		t.Fatalf("first StartAppCreate: %v", err)
	}
	if handle.home != wantHome {
		t.Fatalf("home not under persistent base: got %q want %q", handle.home, wantHome)
	}
	// The home must be created 0700 (owner-only) and owned by the running process.
	info, err := os.Stat(wantHome)
	if err != nil {
		t.Fatalf("stat home: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("home perm = %o, want 0700", perm)
	}

	// Drive to completion so the in-flight session is released.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, done, perr := r.PollAppCreated(context.Background(), handle)
		if perr != nil {
			t.Fatalf("PollAppCreated: %v", perr)
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first provision never completed")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A SECOND provision for the same user must reuse the SAME persistent home
	// (idempotent — not a fresh scratch dir). Drive it to completion too.
	_, handle2, err := r.StartAppCreate(context.Background(), 42)
	if err != nil {
		t.Fatalf("second StartAppCreate: %v", err)
	}
	if handle2.home != wantHome {
		t.Fatalf("re-provision used a different home: got %q want %q (must reuse)", handle2.home, wantHome)
	}
}

// TestPollAppCreated_NilHandleIsInProgress confirms a nil/empty handle is treated
// as still-in-progress (not a hard error), so a lost session ref does not surface
// as a failure to the caller.
func TestPollAppCreated_NilHandleIsInProgress(t *testing.T) {
	homeBase := t.TempDir()
	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	if _, _, done, err := r.PollAppCreated(context.Background(), nil); err != nil || done {
		t.Fatalf("nil handle must be (done=false, err=nil), got done=%t err=%v", done, err)
	}
}

// TestResolveHandle_PathOnlyAfterRestart confirms resolveHandle reconstructs a
// usable handle from just the scratch path (post-restart, no in-memory session),
// and PollAppCreated then reads config.json off disk.
func TestResolveHandle_PathOnlyAfterRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	writeConfigJSON(t, homeBase, 5, "cli_restart", "secret-restart")

	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	if len(r.sessions) != 0 {
		t.Fatalf("precondition: sessions empty (post-restart), got %d", len(r.sessions))
	}

	handle := r.resolveHandle(r.homeForUser(5))
	if handle == nil {
		t.Fatal("resolveHandle must reconstruct a path-only handle")
	}
	if handle.session != nil {
		t.Fatal("post-restart handle must have no in-memory session")
	}
	appID, secret, done, err := r.PollAppCreated(context.Background(), handle)
	if err != nil || !done {
		t.Fatalf("PollAppCreated post-restart: done=%t err=%v", done, err)
	}
	if appID != "cli_restart" || secret != "secret-restart" {
		t.Fatalf("read wrong creds: appID=%q secret=%q", appID, secret)
	}
}

func TestResolveHandle_EmptyRefIsNil(t *testing.T) {
	homeBase := t.TempDir()
	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	if h := r.resolveHandle(""); h != nil {
		t.Fatalf("empty ref must resolve to nil, got %v", h)
	}
}

// blockingConfigInitScript prints the page URL like the real `config init`, then
// BLOCKS (waiting on a sentinel file the test creates) before writing config.json
// and exiting. This keeps the process in the "in-flight" window long enough for a
// concurrency test to observe it: URL scraped, process still running, no config
// yet. The test releases it by creating $HOME/.unblock.
const blockingConfigInitScript = `#!/bin/sh
set -e
if [ "$1" = "config" ] && [ "$2" = "init" ]; then
  printf '打开以下链接配置应用:\n'
  printf '  https://open.feishu.cn/page/cli?user_code=2AF7-MFWU&lpv=1.0.56&from=cli\n'
  printf '等待配置应用...\n'
  # Block until the test signals completion.
  i=0
  while [ ! -f "$HOME/.unblock" ]; do
    sleep 0.02
    i=$((i+1))
    [ "$i" -gt 500 ] && break
  done
  appid=$(cat "$HOME/.fake-creds/appid" 2>/dev/null || true)
  secret=$(cat "$HOME/.fake-creds/secret" 2>/dev/null || true)
  mkdir -p "$HOME/.lark-cli"
  printf '{"apps":[{"appId":"%s","appSecret":"%s","brand":"feishu"}]}\n' "$appid" "$secret" > "$HOME/.lark-cli/config.json"
  exit 0
fi
echo "unhandled args: $@" >&2
exit 1
`

// TestStartAppCreate_RejectsConcurrentInFlightForSameUser is the regression for
// the P1: a second StartAppCreate for the same user while the first `config init`
// is still running must be rejected, not silently overwrite the first session
// (which would orphan the first process). A different user is unaffected, and once
// the first finishes a re-provision is allowed again.
func TestStartAppCreate_RejectsConcurrentInFlightForSameUser(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	seedFakeCreds(t, homeBase, 77, "cli_conc", "secret-conc")
	seedFakeCreds(t, homeBase, 88, "cli_other", "secret-other")

	bin := writeFakeLarkCLI(t, blockingConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}

	// First call launches and blocks (script waits on $HOME/.unblock).
	pageURL, handle, err := r.StartAppCreate(context.Background(), 77)
	if err != nil {
		t.Fatalf("first StartAppCreate: %v", err)
	}
	if !strings.HasPrefix(pageURL, "https://open.feishu.cn/page/cli") {
		t.Fatalf("first page URL mismatch: %q", pageURL)
	}
	firstSess := handle.session
	if firstSess == nil {
		t.Fatal("first handle must carry an in-memory session")
	}

	// Second concurrent call for the SAME user must be rejected, and must NOT
	// overwrite the first user's in-flight session.
	if _, _, err := r.StartAppCreate(context.Background(), 77); err == nil {
		t.Fatal("second concurrent StartAppCreate for same user must be rejected")
	} else if !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("rejection error should mention 'already in progress', got: %v", err)
	}
	r.mu.Lock()
	stillFirst := r.sessions[r.homeForUser(77)] == firstSess
	r.mu.Unlock()
	if !stillFirst {
		t.Fatal("rejected concurrent call must not replace the in-flight session")
	}

	// A DIFFERENT user is unaffected (its own scratch home, own session slot).
	otherURL, otherHandle, err := r.StartAppCreate(context.Background(), 88)
	if err != nil {
		t.Fatalf("StartAppCreate for a different user must succeed: %v", err)
	}
	if !strings.HasPrefix(otherURL, "https://open.feishu.cn/page/cli") {
		t.Fatalf("other user page URL mismatch: %q", otherURL)
	}
	// Release both blocking scripts so they exit cleanly.
	if err := os.WriteFile(filepath.Join(r.homeForUser(88), ".unblock"), []byte("1"), 0o600); err != nil {
		t.Fatalf("unblock user 88: %v", err)
	}
	_ = otherHandle

	// Drive user 77 to completion, then confirm a re-provision is allowed once the
	// session is no longer in-flight.
	if err := os.WriteFile(filepath.Join(r.homeForUser(77), ".unblock"), []byte("1"), 0o600); err != nil {
		t.Fatalf("unblock user 77: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, done, perr := r.PollAppCreated(context.Background(), handle)
		if perr != nil {
			t.Fatalf("PollAppCreated(77): %v", perr)
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("user 77 provisioning never completed")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Session was dropped on completion, so a fresh StartAppCreate is allowed.
	// (It will block on the now-removed sentinel; we just assert it is NOT rejected
	// for in-flight, then unblock it to let it exit.)
	if _, h2, err := r.StartAppCreate(context.Background(), 77); err != nil {
		t.Fatalf("re-provision after completion must be allowed, got: %v", err)
	} else if h2 == nil {
		t.Fatal("re-provision must return a handle")
	}
	if err := os.WriteFile(filepath.Join(r.homeForUser(77), ".unblock"), []byte("1"), 0o600); err != nil {
		t.Fatalf("unblock re-provision: %v", err)
	}
}

// TestSessionRefForUser_MatchesScratchHome confirms the deterministic per-user
// ref equals the scratch home StartAppCreate/PollAppCreated use, so a
// poll-by-user resolves the same config.json a poll-by-handle would.
func TestSessionRefForUser_MatchesScratchHome(t *testing.T) {
	homeBase := t.TempDir()
	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	if got := r.sessionRefForUser(13); got != r.homeForUser(13) {
		t.Fatalf("sessionRefForUser mismatch: %q != %q", got, r.homeForUser(13))
	}
	if got := r.sessionRefForUser(0); got != "" {
		t.Fatalf("userID 0 must yield empty ref, got %q", got)
	}
}

// TestPollByUserRef_ReadsConfigAfterRestart drives PollAppCreated through the
// poll-by-user ref (resolveHandle(sessionRefForUser(uid))) — the exact path the
// agent connect tool's Provisioner.PollCredentialsForUser takes on a re-call with
// no in-memory session. It must read config.json off disk.
func TestPollByUserRef_ReadsConfigAfterRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	writeConfigJSON(t, homeBase, 21, "cli_byuser", "secret-byuser")

	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	handle := r.resolveHandle(r.sessionRefForUser(21))
	appID, secret, done, err := r.PollAppCreated(context.Background(), handle)
	if err != nil || !done {
		t.Fatalf("PollAppCreated via user ref: done=%t err=%v", done, err)
	}
	if appID != "cli_byuser" || secret != "secret-byuser" {
		t.Fatalf("read wrong creds: appID=%q secret=%q", appID, secret)
	}
}

// --- AppExists / AppID (home = phase truth source) --------------------------

// TestAppExists_ReadsHomeConfig is the home-truth regression: AppExists reflects
// whether config.json holds a built app, independent of any in-memory session or DB
// row. A user with a written config.json reports true; a user with none reports
// false (clean, no error) so the orchestrator routes to create_app.
func TestAppExists_ReadsHomeConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	writeConfigJSON(t, homeBase, 31, "cli_exists", "secret-exists")

	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}

	ok, err := r.AppExists(context.Background(), 31)
	if err != nil || !ok {
		t.Fatalf("AppExists(31) with config.json must be (true,nil), got ok=%t err=%v", ok, err)
	}
	// A user with no home/config.json is a clean not-built (no error).
	ok, err = r.AppExists(context.Background(), 99)
	if err != nil || ok {
		t.Fatalf("AppExists(99) with no config must be (false,nil), got ok=%t err=%v", ok, err)
	}
}

// TestAppExists_IncompleteConfigIsFalse confirms a config.json that parses but lacks
// a complete apps[0] (config init still in flight) is a clean not-built, not an error.
func TestAppExists_IncompleteConfigIsFalse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	cfgDir := filepath.Join(homeBase, "u32", ".lark-cli")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// apps[] present but empty → "not built yet".
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"apps":[]}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, _ := NewLarkCLIRunner(bin, homeBase)
	ok, err := r.AppExists(context.Background(), 32)
	if err != nil || ok {
		t.Fatalf("incomplete config.json must be (false,nil), got ok=%t err=%v", ok, err)
	}
}

func TestAppID_ReadsHomeConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	writeConfigJSON(t, homeBase, 33, "cli_theid", "secret-theid")
	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, _ := NewLarkCLIRunner(bin, homeBase)

	appID, err := r.AppID(context.Background(), 33)
	if err != nil || appID != "cli_theid" {
		t.Fatalf("AppID(33) must read config.json appId, got %q err=%v", appID, err)
	}
	if _, err := r.AppID(context.Background(), 99); err == nil {
		t.Fatal("AppID with no config must error")
	}
}

// --- lock self-heal (dead/finished session is reclaimable) ------------------

// TestSpawnBlocking_DeadSessionIsReclaimable is the LOCK SELF-HEAL regression: a
// session whose process has already exited (done=true) must NOT block a later spawn
// — only a genuinely alive in-flight process does. This guards against the wedged
// "provisioning already in progress" dead-end. We exercise it directly on the
// session map: seed a DEAD session for a user's home, then StartAppCreate must be
// allowed (reclaim the slot) rather than rejected.
func TestSpawnBlocking_DeadSessionIsReclaimable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	seedFakeCreds(t, homeBase, 55, "cli_heal", "secret-heal")
	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}

	// Seed a DEAD session (process exited) for user 55's home — simulating a prior
	// run that crashed/was reaped and left a stale tracking entry.
	home := r.homeForUser(55)
	dead := &cliSession{home: home, done: true}
	r.mu.Lock()
	r.sessions[home] = dead
	r.mu.Unlock()
	if dead.isAlive() {
		t.Fatal("precondition: seeded session must be dead")
	}

	// StartAppCreate must reclaim the dead slot (NOT reject as in-progress).
	_, handle, err := r.StartAppCreate(context.Background(), 55)
	if err != nil {
		t.Fatalf("StartAppCreate must reclaim a dead session, got: %v", err)
	}
	if handle == nil {
		t.Fatal("reclaimed StartAppCreate must return a handle")
	}
	// Drive to completion so the spawned process exits cleanly.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, _, done, perr := r.PollAppCreated(context.Background(), handle)
		if perr != nil {
			t.Fatalf("PollAppCreated: %v", perr)
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reclaimed provision never completed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestStartAuthorizeLogin_SelfHealsDeadAuthSession confirms the authorize spawn is
// isomorphic and self-healing: a DEAD prior auth-login session (keyed home#auth)
// does not block a fresh StartAuthorizeLogin. It uses the auth fake (prints a URL,
// writes a token, exits).
func TestStartAuthorizeLogin_SelfHealsDeadAuthSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	bin := writeFakeLarkCLI(t, fakeAuthLoginScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	seedFakeAuthURL(t, homeBase, 66, "https://open.feishu.cn/suite/passport/oauth/device?user_code=HEAL")

	// Seed a DEAD auth session under the auth-namespaced key.
	authKey := authSessionKey(r.homeForUser(66))
	r.mu.Lock()
	r.sessions[authKey] = &cliSession{home: authKey, done: true}
	r.mu.Unlock()

	url, err := r.StartAuthorizeLogin(context.Background(), 66)
	if err != nil {
		t.Fatalf("StartAuthorizeLogin must reclaim a dead auth session, got: %v", err)
	}
	if url == "" {
		t.Fatal("StartAuthorizeLogin must return a verification URL")
	}
}

// blockingAuthLoginScript prints the verification URL like the real `auth login`,
// then BLOCKS (waiting on $HOME/.unblock) before writing the token + exiting — so a
// concurrency test can observe the auth-login process while it is genuinely ALIVE
// (URL printed, process still running, not yet authorized). The status branch
// mirrors fakeAuthLoginScript: authorized iff $HOME/.lark-cli/token exists.
const blockingAuthLoginScript = `#!/bin/sh
set -e
if [ "$1" = "auth" ] && [ "$2" = "login" ]; then
  url=$(cat "$HOME/.fake-auth/url" 2>/dev/null || true)
  printf '请在浏览器中打开以下链接完成授权:\n'
  printf '  %s\n' "$url"
  printf '等待授权...\n'
  i=0
  while [ ! -f "$HOME/.unblock" ]; do
    sleep 0.02
    i=$((i+1))
    [ "$i" -gt 500 ] && break
  done
  mkdir -p "$HOME/.lark-cli"
  printf 'tok\n' > "$HOME/.lark-cli/token"
  exit 0
fi
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  if [ -f "$HOME/.lark-cli/token" ]; then
    printf '{"appId":"cli_x","identities":{"user":{"status":"ready","available":true}}}\n'
  else
    printf '{"ok":false,"error":{"type":"config","subtype":"not_configured","message":"not configured"}}\n'
  fi
  exit 0
fi
echo "unhandled args: $@" >&2
exit 1
`

// TestStartAuthorizeLogin_AliveSessionReturnsCachedURL is the P1 regression: a
// second StartAuthorizeLogin while a previous auth-login is STILL ALIVE (the user
// has not finished the browser step) must NOT error "already in progress" — it must
// return the SAME verification URL the first call scraped, so the connect flow can
// re-show the link and keep polling status instead of dead-ending. (App-create keeps
// the rejection; only auth-login reuses the alive session's cached URL.)
func TestStartAuthorizeLogin_AliveSessionReturnsCachedURL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	bin := writeFakeLarkCLI(t, blockingAuthLoginScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	const wantURL = "https://open.feishu.cn/suite/passport/oauth/device?user_code=ALIVE"
	seedFakeAuthURL(t, homeBase, 77, wantURL)

	// First call launches and BLOCKS (script waits on $HOME/.unblock) — the auth-login
	// process stays alive after the URL is scraped.
	firstURL, err := r.StartAuthorizeLogin(context.Background(), 77)
	if err != nil {
		t.Fatalf("first StartAuthorizeLogin: %v", err)
	}
	if firstURL != wantURL {
		t.Fatalf("first verification URL mismatch: got %q want %q", firstURL, wantURL)
	}

	// Second call while the first is still ALIVE must reuse the cached URL, NOT error.
	secondURL, err := r.StartAuthorizeLogin(context.Background(), 77)
	if err != nil {
		t.Fatalf("second StartAuthorizeLogin on an alive session must NOT error, got: %v", err)
	}
	if secondURL != wantURL {
		t.Fatalf("second call must return the cached verification URL: got %q want %q", secondURL, wantURL)
	}

	// Release the blocking process so it writes the token and exits cleanly.
	if werr := os.WriteFile(filepath.Join(r.homeForUser(77), ".unblock"), []byte("1"), 0o600); werr != nil {
		t.Fatalf("unblock auth login: %v", werr)
	}
}
