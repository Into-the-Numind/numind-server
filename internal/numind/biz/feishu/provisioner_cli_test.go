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

// --- ReadAppSecret disk scan (OAuth exchange path) --------------------------

// TestReadAppSecret_DiskScanFallbackAfterRestart is the regression for the P1:
// after a server restart the in-memory sessions map is empty, but the OAuth
// exchange callback still needs to resolve the app_secret from the per-user
// config.json persisted on disk under homeBase/u{userID}/.lark-cli/config.json.
func TestReadAppSecret_DiskScanFallbackAfterRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	// Two provisioned users persisted on disk; NO in-memory sessions (fresh
	// runner == the post-restart state).
	writeConfigJSON(t, homeBase, 7, "cli_app7", "secret-seven")
	writeConfigJSON(t, homeBase, 9, "cli_app9", "secret-nine")

	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	if len(r.sessions) != 0 {
		t.Fatalf("precondition: sessions map must be empty (post-restart), got %d", len(r.sessions))
	}

	secret, err := r.ReadAppSecret(context.Background(), "cli_app9")
	if err != nil {
		t.Fatalf("ReadAppSecret should resolve via disk scan after restart, got: %v", err)
	}
	if secret != "secret-nine" {
		t.Fatalf("resolved wrong secret: %q (want secret-nine)", secret)
	}

	// Sanity: the other user still resolves independently.
	secret7, err := r.ReadAppSecret(context.Background(), "cli_app7")
	if err != nil {
		t.Fatalf("ReadAppSecret(cli_app7): %v", err)
	}
	if secret7 != "secret-seven" {
		t.Fatalf("resolved wrong secret for app7: %q", secret7)
	}
}

// TestReadAppSecret_UnknownAppStillErrors confirms the disk-scan does not paper
// over a genuinely-missing app: an unknown appID still yields an error.
func TestReadAppSecret_UnknownAppStillErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	writeConfigJSON(t, homeBase, 1, "cli_app1", "s1")

	bin := writeFakeLarkCLI(t, fakeConfigInitScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}

	if _, err := r.ReadAppSecret(context.Background(), "cli_does_not_exist"); err == nil {
		t.Fatal("ReadAppSecret must error for an app with no config home")
	} else if !strings.Contains(err.Error(), "cli_does_not_exist") {
		t.Fatalf("error should name the missing app, got: %v", err)
	}
}
