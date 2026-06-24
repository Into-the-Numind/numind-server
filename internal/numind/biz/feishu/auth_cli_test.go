package feishu

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeAuthLoginScript stands in for the real blocking `lark-cli auth login` +
// `lark-cli auth status --json`. It is HOME-aware (lark-cli reads/writes
// $HOME/.lark-cli/):
//
//   - auth login --domain ... → prints a verification URL (scraped from
//     $HOME/.fake-auth/url), then — to simulate the user finishing in the browser —
//     writes a token marker into $HOME/.lark-cli/token and exits 0 (BLOCKING model,
//     isomorphic to config init).
//   - auth status --json → emits the identities.user.available shape: available=true
//     iff $HOME/.lark-cli/token exists, else the not_configured {ok:false} envelope.
const fakeAuthLoginScript = `#!/bin/sh
set -e
if [ "$1" = "auth" ] && [ "$2" = "login" ]; then
  url=$(cat "$HOME/.fake-auth/url" 2>/dev/null || true)
  printf '请在浏览器中打开以下链接完成授权:\n'
  printf '  %s\n' "$url"
  printf '等待授权...\n'
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

// seedFakeAuthURL writes the verification URL the fakeAuthLoginScript scrapes for a
// given user's home (homeBase/u{userID}).
func seedFakeAuthURL(t *testing.T, homeBase string, userID uint, url string) {
	t.Helper()
	dir := filepath.Join(homeBase, "u"+itoa(userID), ".fake-auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir fake auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "url"), []byte(url), 0o600); err != nil {
		t.Fatalf("seed url: %v", err)
	}
}

func newAuthTestRunner(t *testing.T, script string) (*LarkCLIRunner, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	bin := writeFakeLarkCLI(t, script)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	return r, homeBase
}

// --- StartAuthorizeLogin (blocking, scrape URL) -----------------------------

func TestStartAuthorizeLogin_ReturnsVerificationURL(t *testing.T) {
	r, homeBase := newAuthTestRunner(t, fakeAuthLoginScript)
	seedFakeAuthURL(t, homeBase, 42, "https://open.feishu.cn/suite/passport/oauth/device?user_code=ABCD")

	url, err := r.StartAuthorizeLogin(context.Background(), 42)
	if err != nil {
		t.Fatalf("StartAuthorizeLogin: %v", err)
	}
	if !strings.HasPrefix(url, "https://open.feishu.cn/") {
		t.Fatalf("verification URL mismatch: %q", url)
	}

	// The process writes the token + exits; AuthStatus then reports authorized. Poll
	// briefly since the background process completes asynchronously.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ok, serr := r.AuthStatus(context.Background(), 42)
		if serr != nil {
			t.Fatalf("AuthStatus: %v", serr)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("AuthStatus should report authorized after the login process finishes")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// --- AuthStatus (identities.user.available parsing) -------------------------

func TestAuthStatus_FalseWhenNotLoggedIn(t *testing.T) {
	r, _ := newAuthTestRunner(t, fakeAuthLoginScript)
	ok, err := r.AuthStatus(context.Background(), 5)
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if ok {
		t.Fatal("AuthStatus should be false before any login")
	}
}

// --- parseAuthStatus (pure parser over real lark-cli shapes) ----------------

func TestParseAuthStatus_AuthorizedShape(t *testing.T) {
	// Real `auth status --json` shape when the user is authorized.
	raw := []byte(`{"appId":"cli_x","brand":"feishu","identities":{"bot":{"status":"ready","available":true},"user":{"status":"ready","available":true}},"identity":"user"}`)
	out, err := parseAuthStatus(raw)
	if err != nil {
		t.Fatalf("parseAuthStatus: %v", err)
	}
	if !out.authorized() {
		t.Fatal("user.available=true must be authorized")
	}
}

func TestParseAuthStatus_NeedsRefreshStillAuthorized(t *testing.T) {
	// needs_refresh with available=true still counts (lark-cli auto-refreshes).
	raw := []byte(`{"identities":{"user":{"status":"needs_refresh","available":true}}}`)
	out, err := parseAuthStatus(raw)
	if err != nil {
		t.Fatalf("parseAuthStatus: %v", err)
	}
	if !out.authorized() {
		t.Fatal("needs_refresh + available=true must still be authorized")
	}
}

func TestParseAuthStatus_UserMissingNotAuthorized(t *testing.T) {
	// App configured, bot ready, but the user identity is missing → not authorized.
	raw := []byte(`{"appId":"cli_x","identities":{"bot":{"status":"ready","available":true},"user":{"status":"missing","available":false}},"identity":"bot"}`)
	out, err := parseAuthStatus(raw)
	if err != nil {
		t.Fatalf("parseAuthStatus: %v", err)
	}
	if out.authorized() {
		t.Fatal("user.available=false must NOT be authorized")
	}
}

func TestParseAuthStatus_NotConfiguredEnvelope(t *testing.T) {
	// No app configured → {ok:false,error:...} envelope (no identities) → not authorized.
	raw := []byte(`{"ok":false,"error":{"type":"config","subtype":"not_configured","message":"not configured"}}`)
	out, err := parseAuthStatus(raw)
	if err != nil {
		t.Fatalf("parseAuthStatus: %v", err)
	}
	if out.authorized() {
		t.Fatal("not_configured envelope must NOT be authorized")
	}
}

func TestParseAuthStatus_TrailingNonJSONTolerated(t *testing.T) {
	// Some lark-cli commands append a trailing non-JSON line; decode only the first value.
	raw := []byte("{\"identities\":{\"user\":{\"available\":true}}}\n\nConfig file path: /home/u1/.lark-cli/config.json\n")
	out, err := parseAuthStatus(raw)
	if err != nil {
		t.Fatalf("parseAuthStatus must tolerate a trailing footer: %v", err)
	}
	if !out.authorized() {
		t.Fatal("trailing footer must not break parsing")
	}
}

func TestParseAuthStatus_Garbage(t *testing.T) {
	if _, err := parseAuthStatus([]byte("not json")); err == nil {
		t.Fatal("unparseable auth status must error")
	}
}
