// Package feishu — auth_cli.go is the PRODUCTION device-code authorization runner
// (G2-authorize, 2026-06-24). It replaces the old redirect-OAuth token exchange:
// authorization now goes entirely through lark-cli's device flow, so there is no
// redirect_uri / authorize URL / OAuth callback anymore.
//
// Two-call device flow (lark-cli auth login), per the lark-shared skill + verified
// lark-cli reality on 2026-06-24:
//
//   - StartAuthLogin(ctx, userID): run
//     `lark-cli auth login --no-wait --json --domain docs,im,base` (HOME=user home).
//     It returns IMMEDIATELY with a verification_url + device_code in JSON. We hand
//     the verification_url to the user (the agent shows the link; the run pauses)
//     and persist the device_code to a transient file in the user's home so the
//     resume step can complete the SAME device flow. The token is NEVER returned —
//     lark-cli stores it in the home on completion.
//   - CompleteAuthLogin(ctx, userID): after the user finishes in the browser and the
//     run resumes, read the persisted device_code and run
//     `lark-cli auth login --device-code <code>` (HOME=user home). lark-cli polls,
//     completes the grant, and persists the user_access_token into the home (it
//     auto-refreshes thereafter). We then delete the transient device_code file.
//
// Status: AuthStatus(ctx, userID) runs `lark-cli auth status --json` (HOME=home)
// and reports whether the home holds a usable authorization.
//
// Security (CLAUDE.md / .claude/rules): the device_code is a short-lived
// authorization-flow artifact, NOT a token; it is persisted only inside the user's
// own home (0600) and never returned to the caller / LLM / logs. The token itself
// lives only in lark-cli's home store — never in our DB, never in our process
// memory, never logged. NOT routed through aiservice (飞书 is an external business
// API). The lark-cli invocation args are config-pinned (no user-controlled args).
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"numind-server/internal/pkg/errno"
)

// authLoginDomains are the business domains requested in one device-code grant so
// every 飞书 ops tool (docs / im / base) works after a single authorization
// (缺则后续 403). Requested via --domain (additive scope per lark-cli).
const authLoginDomains = "docs,im,base"

// authStartTimeout bounds the `auth login --no-wait` call — it returns
// immediately (it does NOT block on the browser), so a short timeout suffices.
const authStartTimeout = 30 * time.Second

// authCompleteTimeout bounds the `auth login --device-code` poll. lark-cli polls
// the device grant; by the time we call this the user has already authorized, so
// completion is fast, but we allow headroom for the poll round-trip.
const authCompleteTimeout = 60 * time.Second

// authStatusTimeout bounds the (network-free, unless --verify) `auth status` call.
const authStatusTimeout = 15 * time.Second

// pendingDeviceCodeFile is the transient per-user file holding the in-flight
// device_code between StartAuthLogin (yield) and CompleteAuthLogin (resume). It
// lives INSIDE the user's home (alongside .lark-cli) so it is naturally isolated
// per user and never leaks across users. 0600. It is deleted on completion.
const pendingDeviceCodeFile = ".numind-pending-devicecode"

// authRunner abstracts the lark-cli device-code authorization interaction so the
// orchestration is unit-tested with a fake lark-cli (no live 飞书). The production
// implementation is *LarkCLIRunner (methods below).
type authRunner interface {
	// StartAuthLogin kicks off the device flow for userID and returns the
	// verification URL the user opens in a browser. The device_code is persisted
	// internally (home file) — NOT returned — so CompleteAuthLogin can finish the
	// same flow on resume.
	StartAuthLogin(ctx context.Context, userID uint) (verificationURL string, err error)
	// CompleteAuthLogin completes the device flow for userID using the persisted
	// device_code (after the user authorized in the browser). On success lark-cli
	// has stored the token in the home; the transient device_code file is removed.
	CompleteAuthLogin(ctx context.Context, userID uint) error
	// HasPendingDeviceCode reports whether a StartAuthLogin device_code is awaiting
	// completion for userID (distinguishes "start authorize" from "resume/complete").
	HasPendingDeviceCode(userID uint) bool
	// AuthStatus reports whether userID's home holds a usable authorization
	// (lark-cli auth status ok). A transport/parse failure returns (false, err).
	AuthStatus(ctx context.Context, userID uint) (connected bool, err error)
}

// authLoginJSON is the relevant subset of `lark-cli auth login --no-wait --json`
// output. lark-cli wraps results in an {ok, error, _notice} envelope; the
// device-flow fields (verification_url + device_code) may sit at the top level or
// nested under "data" depending on the lark-cli version, so we accept BOTH shapes
// (deviceFlowFields embedded twice) and pick whichever is populated.
type authLoginJSON struct {
	OK    bool          `json:"ok"`
	Error *larkCLIError `json:"error"`
	deviceFlowFields
	Data *deviceFlowFields `json:"data"`
}

// deviceFlowFields holds the device-flow URL + code. We tolerate the common
// field-name variants (verification_url / verification_uri / verification_uri_complete)
// so a minor lark-cli output rename does not break the flow.
type deviceFlowFields struct {
	VerificationURL         string `json:"verification_url"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	DeviceCode              string `json:"device_code"`
}

// url returns the best available verification URL (prefer the *complete one that
// embeds the user_code so the user does not have to type it).
func (d deviceFlowFields) url() string {
	switch {
	case d.VerificationURIComplete != "":
		return d.VerificationURIComplete
	case d.VerificationURL != "":
		return d.VerificationURL
	default:
		return d.VerificationURI
	}
}

// larkCLIError is the lark-cli JSON error envelope (auth/validation/config etc.).
type larkCLIError struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message string `json:"message"`
}

// authStatusJSON is the relevant subset of `lark-cli auth status --json`. ok=true
// means the home holds a usable authorization.
type authStatusJSON struct {
	OK    bool          `json:"ok"`
	Error *larkCLIError `json:"error"`
}

// pendingDeviceCodePath returns the transient device_code file path inside a
// user's home.
func (r *LarkCLIRunner) pendingDeviceCodePath(userID uint) string {
	return filepath.Join(r.homeForUser(userID), pendingDeviceCodeFile)
}

// StartAuthLogin runs `lark-cli auth login --no-wait --json --domain docs,im,base`
// in the user's home, parses out the verification URL + device_code, persists the
// device_code (0600, home-local) for the later complete step, and returns the URL.
func (r *LarkCLIRunner) StartAuthLogin(ctx context.Context, userID uint) (string, error) {
	home := r.homeForUser(userID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", fmt.Errorf("feishu: create user home %q: %w", home, err)
	}

	raw, err := r.runCLI(ctx, home, authStartTimeout,
		"auth", "login", "--no-wait", "--json", "--domain", authLoginDomains)
	if err != nil {
		return "", err
	}

	var out authLoginJSON
	if jerr := json.Unmarshal(raw, &out); jerr != nil {
		return "", fmt.Errorf("%w: parse auth login output: %v", errno.ErrLarkCallFailed, jerr)
	}
	if !out.OK {
		return "", fmt.Errorf("%w: auth login failed: %s", errno.ErrLarkCallFailed, errMsg(out.Error))
	}

	fields := out.deviceFlowFields
	if out.Data != nil && (out.Data.url() != "" || out.Data.DeviceCode != "") {
		fields = *out.Data
	}
	verificationURL := fields.url()
	deviceCode := fields.DeviceCode
	if verificationURL == "" || deviceCode == "" {
		return "", fmt.Errorf("%w: auth login returned no verification_url/device_code", errno.ErrLarkCallFailed)
	}

	// Persist the device_code so the resume step can complete the SAME device flow.
	// 0600 inside the user's own home — never returned, never logged.
	if werr := os.WriteFile(r.pendingDeviceCodePath(userID), []byte(deviceCode), 0o600); werr != nil { // #nosec G306 -- 0600 secret-flow artifact
		return "", fmt.Errorf("feishu: persist device code (user %d): %w", userID, werr)
	}
	return verificationURL, nil
}

// CompleteAuthLogin reads the persisted device_code and runs
// `lark-cli auth login --device-code <code>` to finish the grant. On success the
// transient device_code file is deleted (best-effort). A missing device_code file
// means there is nothing in flight to complete.
func (r *LarkCLIRunner) CompleteAuthLogin(ctx context.Context, userID uint) error {
	path := r.pendingDeviceCodePath(userID)
	codeRaw, err := os.ReadFile(path) // #nosec G304 -- path built from our homeBase
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: no pending device authorization to complete (user %d)", errno.ErrLarkCallFailed, userID)
		}
		return fmt.Errorf("feishu: read pending device code (user %d): %w", userID, err)
	}
	deviceCode := strings.TrimSpace(string(codeRaw))
	if deviceCode == "" {
		_ = os.Remove(path)
		return fmt.Errorf("%w: pending device code is empty (user %d)", errno.ErrLarkCallFailed, userID)
	}

	home := r.homeForUser(userID)
	raw, runErr := r.runCLI(ctx, home, authCompleteTimeout,
		"auth", "login", "--device-code", deviceCode, "--json")
	if runErr != nil {
		return runErr
	}
	// Parse the envelope to surface a clean error (e.g. authorization_pending /
	// expired) rather than treating any output as success.
	var out authStatusJSON // {ok, error} envelope is shared with status
	if jerr := json.Unmarshal(raw, &out); jerr != nil {
		return fmt.Errorf("%w: parse device-code completion output: %v", errno.ErrLarkCallFailed, jerr)
	}
	if !out.OK {
		return fmt.Errorf("%w: device-code authorization failed: %s", errno.ErrLarkCallFailed, errMsg(out.Error))
	}

	// Success: the token is now in the home. Drop the transient device_code.
	_ = os.Remove(path)
	return nil
}

// HasPendingDeviceCode reports whether a device_code awaits completion for userID.
func (r *LarkCLIRunner) HasPendingDeviceCode(userID uint) bool {
	_, err := os.Stat(r.pendingDeviceCodePath(userID))
	return err == nil
}

// AuthStatus runs `lark-cli auth status --json` and reports whether the home holds
// a usable authorization (ok=true). A "not configured / not logged in" status is a
// clean connected=false (no error); only a transport/parse failure returns an error.
func (r *LarkCLIRunner) AuthStatus(ctx context.Context, userID uint) (bool, error) {
	home := r.homeForUser(userID)
	raw, err := r.runCLI(ctx, home, authStatusTimeout, "auth", "status", "--json")
	if err != nil {
		return false, err
	}
	var out authStatusJSON
	if jerr := json.Unmarshal(raw, &out); jerr != nil {
		return false, fmt.Errorf("%w: parse auth status output: %v", errno.ErrLarkCallFailed, jerr)
	}
	return out.OK, nil
}

// runCLI runs a lark-cli subcommand in the given home with a timeout and returns
// its combined stdout/stderr. lark-cli emits its JSON envelope to stdout even on a
// business error (exit 0), so we do NOT treat a non-zero exit as fatal when there
// is parseable output; we DO return an error when there is no output at all. The
// args are config-pinned (no user-controlled args except the opaque device_code,
// which is passed as a single argv element — never shell-interpolated).
func (r *LarkCLIRunner) runCLI(ctx context.Context, home string, timeout time.Duration, args ...string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, r.bin, args...) // #nosec G204 -- bin config-pinned; args are fixed verbs + opaque device_code
	cmd.Env = r.env(home)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	runErr := cmd.Run()
	out := bytes.TrimSpace(buf.Bytes())
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
// diagnosis (the message is a generic 飞书/CLI error string).
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
