package feishu

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeAuthCLIScript stands in for the real `lark-cli auth ...`. It is HOME-aware
// (lark-cli reads/writes $HOME/.lark-cli/). It handles the three device-code verbs:
//
//   - auth login --no-wait --json --domain ... → prints an {ok:true, verification_url,
//     device_code} envelope, scraping the values from $HOME/.fake-auth/{url,code}.
//   - auth login --device-code <code> --json → verifies the code matches the seeded
//     one, writes a fake token marker into $HOME/.lark-cli/token, prints {ok:true}.
//   - auth status --json → {ok:true} iff $HOME/.lark-cli/token exists, else {ok:false}.
const fakeAuthCLIScript = `#!/bin/sh
set -e
if [ "$1" = "auth" ] && [ "$2" = "login" ] && [ "$3" = "--no-wait" ]; then
  url=$(cat "$HOME/.fake-auth/url" 2>/dev/null || true)
  code=$(cat "$HOME/.fake-auth/code" 2>/dev/null || true)
  printf '{"ok":true,"verification_url":"%s","device_code":"%s"}\n' "$url" "$code"
  exit 0
fi
if [ "$1" = "auth" ] && [ "$2" = "login" ] && [ "$3" = "--device-code" ]; then
  got="$4"
  want=$(cat "$HOME/.fake-auth/code" 2>/dev/null || true)
  if [ "$got" != "$want" ]; then
    printf '{"ok":false,"error":{"type":"auth","subtype":"invalid_grant","message":"device code mismatch"}}\n'
    exit 0
  fi
  mkdir -p "$HOME/.lark-cli"
  printf 'tok\n' > "$HOME/.lark-cli/token"
  printf '{"ok":true}\n'
  exit 0
fi
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  if [ -f "$HOME/.lark-cli/token" ]; then
    printf '{"ok":true}\n'
  else
    printf '{"ok":false,"error":{"type":"config","subtype":"not_configured","message":"not configured"}}\n'
  fi
  exit 0
fi
echo "unhandled args: $@" >&2
exit 1
`

// seedFakeAuth writes the verification url + device code the fakeAuthCLIScript emits
// for a given user's home (homeBase/u{userID}).
func seedFakeAuth(t *testing.T, homeBase string, userID uint, url, code string) {
	t.Helper()
	dir := filepath.Join(homeBase, "u"+itoa(userID), ".fake-auth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir fake auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "url"), []byte(url), 0o600); err != nil {
		t.Fatalf("seed url: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "code"), []byte(code), 0o600); err != nil {
		t.Fatalf("seed code: %v", err)
	}
}

func newAuthTestRunner(t *testing.T) (*LarkCLIRunner, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	bin := writeFakeLarkCLI(t, fakeAuthCLIScript)
	r, err := NewLarkCLIRunner(bin, homeBase)
	if err != nil {
		t.Fatalf("NewLarkCLIRunner: %v", err)
	}
	return r, homeBase
}

// --- StartAuthLogin ---------------------------------------------------------

func TestStartAuthLogin_ReturnsURLAndPersistsDeviceCode(t *testing.T) {
	r, homeBase := newAuthTestRunner(t)
	seedFakeAuth(t, homeBase, 42, "https://open.feishu.cn/suite/passport/oauth/device?user_code=ABCD", "dev_code_42")

	url, err := r.StartAuthLogin(context.Background(), 42)
	if err != nil {
		t.Fatalf("StartAuthLogin: %v", err)
	}
	if !strings.HasPrefix(url, "https://open.feishu.cn/") {
		t.Fatalf("verification URL mismatch: %q", url)
	}
	// device_code must be persisted (so resume can complete) but NEVER returned.
	if strings.Contains(url, "dev_code_42") {
		t.Fatal("device_code must not leak into the returned URL")
	}
	if !r.HasPendingDeviceCode(42) {
		t.Fatal("device_code should be persisted after StartAuthLogin")
	}
	codeRaw, err := os.ReadFile(r.pendingDeviceCodePath(42))
	if err != nil {
		t.Fatalf("read persisted device code: %v", err)
	}
	if strings.TrimSpace(string(codeRaw)) != "dev_code_42" {
		t.Fatalf("persisted device code mismatch: %q", codeRaw)
	}
}

func TestStartAuthLogin_NoDeviceCodeInOutput_Errors(t *testing.T) {
	r, homeBase := newAuthTestRunner(t)
	// Seed empty code → fake prints empty device_code → StartAuthLogin must error.
	seedFakeAuth(t, homeBase, 7, "https://x", "")

	if _, err := r.StartAuthLogin(context.Background(), 7); err == nil {
		t.Fatal("StartAuthLogin must error when device_code is absent")
	}
	if r.HasPendingDeviceCode(7) {
		t.Fatal("no device_code should be persisted on failure")
	}
}

// --- CompleteAuthLogin ------------------------------------------------------

func TestCompleteAuthLogin_CompletesAndClearsPending(t *testing.T) {
	r, homeBase := newAuthTestRunner(t)
	seedFakeAuth(t, homeBase, 42, "https://open.feishu.cn/device", "dev_code_42")

	if _, err := r.StartAuthLogin(context.Background(), 42); err != nil {
		t.Fatalf("StartAuthLogin: %v", err)
	}
	if err := r.CompleteAuthLogin(context.Background(), 42); err != nil {
		t.Fatalf("CompleteAuthLogin: %v", err)
	}
	// Pending device code file is consumed on success.
	if r.HasPendingDeviceCode(42) {
		t.Fatal("pending device code must be cleared after completion")
	}
	// And the home now reports connected.
	ok, err := r.AuthStatus(context.Background(), 42)
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if !ok {
		t.Fatal("AuthStatus should be connected after completion")
	}
}

func TestCompleteAuthLogin_NoPending_Errors(t *testing.T) {
	r, _ := newAuthTestRunner(t)
	if err := r.CompleteAuthLogin(context.Background(), 99); err == nil {
		t.Fatal("CompleteAuthLogin without a pending device code must error")
	}
}

// --- AuthStatus -------------------------------------------------------------

func TestAuthStatus_FalseWhenNotLoggedIn(t *testing.T) {
	r, _ := newAuthTestRunner(t)
	ok, err := r.AuthStatus(context.Background(), 5)
	if err != nil {
		t.Fatalf("AuthStatus: %v", err)
	}
	if ok {
		t.Fatal("AuthStatus should be false before any login")
	}
}
