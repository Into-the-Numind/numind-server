// Package feishu — auth_cli.go is the PRODUCTION authorization runner (G2-authorize,
// rewritten 2026-06-24 for fix/feishu-phase-from-home). It replaces the earlier
// device-code split-flow (--no-wait / --device-code + a persisted device_code file),
// which was error-prone. Authorization is now ISOMORPHIC to app-create: a BLOCKING
// `lark-cli auth login` runs in the background, prints a verification URL, and exits
// once the user finishes in the browser (lark-cli then holds + auto-refreshes the
// user_access_token in the home).
//
// Blocking auth-login flow (lark-cli auth login, verified reality 2026-06-24):
//
//   - StartAuthorizeLogin(ctx, userID): launch `lark-cli auth login --domain
//     docs,im,base` (HOME=user home) backgrounded; it BLOCKS, prints a verification
//     URL, and the user opens it to grant scopes. We scrape the URL from its early
//     stdout (bounded by authStartTimeout), reuse the SAME background+reaper+ceiling
//     machinery as config-init, and leave the process running until the user finishes.
//     Self-healing: a second call while a live auth-login is in flight does NOT spawn
//     a duplicate and NEVER errors "already in progress" — the connect flow simply
//     re-checks AuthStatus; if the prior process already died it is reclaimed and a
//     fresh login is started.
//   - AuthStatus(ctx, userID): run `lark-cli auth status --json` (HOME=home) and
//     report whether identities.user.available is true (authorized — a needs_refresh
//     status with available=true still counts; lark-cli auto-refreshes).
//
// There is no separate "complete" step: once the user authorizes, the background
// process exits with the token persisted, and the next AuthStatus read sees it.
//
// Security (CLAUDE.md / .claude/rules): the token lives only in lark-cli's home —
// never in our DB, never in our process memory, never logged. The lark-cli args are
// config-pinned (no user-controlled args). NOT routed through aiservice (飞书 is an
// external business API).
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"numind-server/internal/pkg/errno"
)

// authLoginDomains are the business domains requested in one authorization grant so
// every 飞书 ops tool (docs / im / base) works after a single login (缺则后续 403).
const authLoginDomains = "docs,im,base"

// authStartTimeout bounds how long we wait for `auth login` to PRINT the
// verification URL after launch (NOT the whole flow — the user's browser step runs
// in the background, bounded by authLoginCeiling).
const authStartTimeout = 30 * time.Second

// authLoginCeiling is the hard upper bound on the backgrounded `auth login` process
// (mirrors appCreateCeiling). lark-cli self-bounds its device-flow wait; we kill
// anything that overruns so a never-finished browser step does not leak a process.
const authLoginCeiling = 12 * time.Minute

// authStatusTimeout bounds the (network-free, unless --verify) `auth status` call.
const authStatusTimeout = 15 * time.Second

// authRunner abstracts the lark-cli authorization interaction so the orchestration
// is unit-tested with a fake lark-cli (no live 飞书). The production implementation
// is *LarkCLIRunner (methods below).
type authRunner interface {
	// StartAuthorizeLogin launches the blocking `auth login` for userID, scrapes the
	// verification URL from its early stdout, and returns it (leaving the process
	// running until the user authorizes). Self-healing: never errors on a concurrent
	// in-flight login; a dead prior login is reclaimed.
	StartAuthorizeLogin(ctx context.Context, userID uint) (verificationURL string, err error)
	// AuthStatus reports whether userID's home holds a usable authorization
	// (identities.user.available == true). A transport/parse failure returns
	// (false, err); a clean "not authorized" is (false, nil).
	AuthStatus(ctx context.Context, userID uint) (connected bool, err error)
}

// larkCLIError is the lark-cli JSON error envelope (auth/validation/config etc.).
type larkCLIError struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message string `json:"message"`
}

// authStatusJSON is the relevant subset of `lark-cli auth status --json`. When an
// app is configured, lark-cli reports per-identity availability under
// identities.user.available (true ⇒ authorized, even if status == needs_refresh —
// lark-cli auto-refreshes). When NOT configured (no app) it returns an {ok,error}
// envelope with ok=false instead, so we tolerate both shapes.
type authStatusJSON struct {
	OK         *bool         `json:"ok"`
	Error      *larkCLIError `json:"error"`
	Identities *struct {
		User *struct {
			Status    string `json:"status"`
			Available bool   `json:"available"`
		} `json:"user"`
	} `json:"identities"`
}

// authorized reports whether the parsed status means the user is authorized.
func (s authStatusJSON) authorized() bool {
	if s.Identities != nil && s.Identities.User != nil {
		return s.Identities.User.Available
	}
	return false
}

// authSessionKey namespaces an auth-login session in the shared sessions map so it
// never collides with a config-init session for the same home (different lifecycle).
func authSessionKey(home string) string { return home + "#auth" }

// StartAuthorizeLogin launches the BLOCKING `lark-cli auth login --domain
// docs,im,base` in the user's home, scrapes the verification URL from its early
// stdout, and leaves the process running in the background (it blocks until the user
// finishes in the browser). It reuses the same background+scrape+reaper+ceiling
// machinery as StartAppCreate (via spawnBlockingURL) and is self-healing: a second
// call while a previous login is still alive does NOT spawn a duplicate and does NOT
// error — it returns the SAME verification URL the live session already scraped, so
// the connect flow re-shows the link and keeps polling status (a DEAD prior login is
// reclaimed and a fresh login is started).
func (r *LarkCLIRunner) StartAuthorizeLogin(ctx context.Context, userID uint) (string, error) {
	home := r.homeForUser(userID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("feishu: create user home %q: %w", home, err)
	}
	return r.spawnBlockingURL(ctx, userID, authSessionKey(home),
		[]string{"auth", "login", "--domain", authLoginDomains}, authStartTimeout, authLoginCeiling)
}

// AuthStatus runs `lark-cli auth status --json` and reports whether the home holds a
// usable authorization (identities.user.available == true). A "not configured / not
// logged in" status is a clean (false, nil); only a transport/parse failure errors.
func (r *LarkCLIRunner) AuthStatus(ctx context.Context, userID uint) (bool, error) {
	home := r.homeForUser(userID)
	raw, err := r.runCLI(ctx, home, authStatusTimeout, "auth", "status", "--json")
	if err != nil {
		return false, err
	}
	out, perr := parseAuthStatus(raw)
	if perr != nil {
		return false, perr
	}
	return out.authorized(), nil
}

// parseAuthStatus parses `auth status --json` bytes. Pure (no I/O) so it is
// unit-tested in isolation. It tolerates the trailing non-JSON lines some lark-cli
// commands print by decoding only the first JSON value.
func parseAuthStatus(raw []byte) (authStatusJSON, error) {
	out, err := decodeFirstJSON[authStatusJSON](raw)
	if err != nil {
		return authStatusJSON{}, fmt.Errorf("%w: parse auth status output: %v", errno.ErrLarkCallFailed, err)
	}
	return out, nil
}

// decodeFirstJSON decodes the FIRST JSON value out of raw, ignoring any trailing
// non-JSON output (e.g. lark-cli's "Config file path: ..." footer on some commands).
func decodeFirstJSON[T any](raw []byte) (T, error) {
	var v T
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&v); err != nil {
		return v, err
	}
	return v, nil
}

// runCLI runs a short-lived lark-cli subcommand in the given home with a timeout and
// returns its combined stdout/stderr. lark-cli emits its JSON envelope to stdout even
// on a business error (exit 0), so we do NOT treat a non-zero exit as fatal when
// there is parseable output; we DO return an error when there is no output at all.
// The args are config-pinned (no user-controlled args).
func (r *LarkCLIRunner) runCLI(ctx context.Context, home string, timeout time.Duration, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, r.bin, args...) // #nosec G204 -- bin config-pinned; args are fixed verbs
	cmd.Env = r.env(home)
	out, runErr := cmd.CombinedOutput()
	out = bytes.TrimSpace(out)
	if len(out) > 0 {
		// lark-cli returned its JSON envelope — let the caller parse ok/error.
		return out, nil
	}
	if cctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%w: lark-cli %s timed out after %s", errno.ErrLarkCallFailed, args[0], timeout)
	}
	if runErr != nil {
		return nil, fmt.Errorf("%w: lark-cli %s produced no output: %v", errno.ErrLarkCallFailed, args[0], runErr)
	}
	return nil, fmt.Errorf("%w: lark-cli %s produced no output", errno.ErrLarkCallFailed, args[0])
}

// errMsg renders a lark-cli error envelope into a short, secret-free string for
// diagnosis (the message is a generic 飞书/CLI error string). Used by ops_cli.go to
// surface shortcut-call failures.
func errMsg(e *larkCLIError) string {
	if e == nil {
		return "unknown error"
	}
	if e.Subtype != "" {
		return fmt.Sprintf("%s/%s: %s", e.Type, e.Subtype, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// compile-time guard: LarkCLIRunner satisfies authRunner.
var _ authRunner = (*LarkCLIRunner)(nil)
