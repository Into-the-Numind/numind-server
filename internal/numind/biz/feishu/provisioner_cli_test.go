package feishu

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeLarkCLIScript is a tiny shell stub standing in for the real lark-cli binary.
// It is HOME-aware (lark-cli reads ~/.lark-cli/), so a runner pointed at a
// per-user homeBase/u{id} dir resolves that user's appId/secret from disk —
// exactly the post-restart code path ReadAppSecret's disk-scan fallback must
// handle (the in-memory sessions map is empty after a restart).
//
//   - `config show`           → emits {"appId":"<contents of $HOME/.lark-cli/appid>"}
//   - `apps +init ... --dir D` → writes D/.env.local with the matching secret
const fakeLarkCLIScript = `#!/bin/sh
set -e
cmd="$1"
sub="$2"
case "$cmd" in
  config)
    if [ "$sub" = "show" ]; then
      appid=$(cat "$HOME/.lark-cli/appid" 2>/dev/null || true)
      printf '{"appId":"%s"}\n' "$appid"
      exit 0
    fi
    ;;
  apps)
    # apps +init --app-id <id> --dir <dir>
    appid=""
    dir=""
    shift 2
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --app-id) appid="$2"; shift 2 ;;
        --dir) dir="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    secret=$(cat "$HOME/.lark-cli/secret" 2>/dev/null || true)
    printf 'FEISHU_APP_ID=%s\nFEISHU_APP_SECRET=%s\n' "$appid" "$secret" > "$dir/.env.local"
    echo "Local environment written to $dir/.env.local"
    exit 0
    ;;
esac
echo "unhandled args: $@" >&2
exit 1
`

// writeFakeLarkCLI drops the stub script into a temp dir and returns its path.
func writeFakeLarkCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "lark-cli")
	if err := os.WriteFile(bin, []byte(fakeLarkCLIScript), 0o700); err != nil { // #nosec G306 -- executable test stub
		t.Fatalf("write fake lark-cli: %v", err)
	}
	return bin
}

// seedUserHome creates homeBase/u{userID}/.lark-cli/{appid,secret} so the fake
// CLI can report this provisioned user's credentials.
func seedUserHome(t *testing.T, homeBase string, userID uint, appID, secret string) {
	t.Helper()
	cfg := filepath.Join(homeBase, "u"+itoa(userID), ".lark-cli")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatalf("mkdir user config home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "appid"), []byte(appID), 0o600); err != nil {
		t.Fatalf("seed appid: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "secret"), []byte(secret), 0o600); err != nil {
		t.Fatalf("seed secret: %v", err)
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

// TestReadAppSecret_DiskScanFallbackAfterRestart is the regression for the P1:
// after a server restart the in-memory sessions map is empty, but the OAuth
// exchange callback still needs to resolve the app_secret from the per-user
// config home persisted on disk under homeBase/u{userID}/.lark-cli/. Before the
// fix, ReadAppSecret only scanned r.sessions (empty) and always returned
// ErrLarkCallFailed for every provisioned user post-restart.
func TestReadAppSecret_DiskScanFallbackAfterRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	// Two provisioned users persisted on disk; NO in-memory sessions (fresh
	// runner == the post-restart state).
	seedUserHome(t, homeBase, 7, "cli_app7", "secret-seven")
	seedUserHome(t, homeBase, 9, "cli_app9", "secret-nine")

	bin := writeFakeLarkCLI(t)
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

// TestReadAppSecret_UnknownAppStillErrors confirms the disk-scan fallback does
// not paper over a genuinely-missing app: an unknown appID still yields an error.
func TestReadAppSecret_UnknownAppStillErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake lark-cli stub is a /bin/sh script")
	}
	homeBase := t.TempDir()
	seedUserHome(t, homeBase, 1, "cli_app1", "s1")

	bin := writeFakeLarkCLI(t)
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
