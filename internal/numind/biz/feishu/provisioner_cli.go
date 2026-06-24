// Package feishu — provisioner_cli.go holds the PRODUCTION implementations of the
// provisioner's seams: the lark-cli device-code runner (os/exec) and the 飞书 v2
// OAuth token exchanger (HTTP). These touch the outside world (process spawning,
// network) and are therefore NOT exercised by the offline unit tests in
// provisioner_test.go — the pure orchestration is. End-to-end coverage requires a
// dev environment with lark-cli installed + a 飞书 test account + a browser to
// complete the device-code step (see the feature S5 validation strategy).
//
// Key behaviours encoded here (from spike-bootstrap.md + lark-cli binary
// inspection on 2026-06-24):
//   - Per-user isolation: lark-cli has no dedicated config-home env var; it reads
//     ~/.lark-cli/ . We override HOME per invocation so each user's app
//     credentials live in their own directory and never collide.
//   - `lark-cli config init --new --name <userid>` BLOCKS until the user finishes
//     the browser step, printing "...open.feishu.cn/page/cli?user_code=..." early
//     then "Waiting for app configuration...". We run it backgrounded, scrape the
//     page URL from its stdout, and let it finish on its own; completion = the
//     credentials are written to that config home.
//   - app_secret is kept encrypted-at-rest by lark-cli and masked in `config
//     show`. To materialize the plaintext (needed as OAuth client_secret) we use
//     `lark-cli apps +init`, which writes a dotenv file with FEISHU_APP_ID /
//     FEISHU_APP_SECRET ("Local environment written to ..."). We read it back via
//     parseEnvCredentials.
//
// ⚠️ Several exact strings/paths below (env file location, `config show` JSON
// shape, completion detection) were derived from binary inspection, NOT a full
// live run. They are isolated in small helpers so a dev run can correct them
// without touching the orchestration. See blockers in the task notes.
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/log"
)

// --- lark-cli device-code runner --------------------------------------------

// defaultLarkCLIBin is the lark-cli executable name; overridable via config so
// dev/prod can pin an absolute path.
const defaultLarkCLIBin = "lark-cli"

// startInitTimeout bounds how long we wait for lark-cli to PRINT the device-code
// page URL after launch (not the whole flow — the user's browser step is
// unbounded and runs in the background).
const startInitTimeout = 30 * time.Second

// larkCLIRunner is the production cliRunner over os/exec.
//
// State across HTTP requests: StartInit launches a long-lived background process
// (it blocks on the user's browser step). We track running sessions in-process so
// a later PollCredentials on the same server instance can observe completion. The
// session ref is the per-user config-home directory path — durable, so even if
// the in-memory entry is lost (restart), PollCredentials can still read whatever
// credentials lark-cli already persisted there.
type larkCLIRunner struct {
	bin      string // lark-cli binary (name on PATH or absolute path)
	homeBase string // base dir under which per-user HOMEs are created

	mu       sync.Mutex
	sessions map[string]*cliSession
}

// cliSession tracks one backgrounded `config init` invocation.
type cliSession struct {
	home string
	cmd  *exec.Cmd

	mu      sync.Mutex
	done    bool
	exitErr error
}

// NewLarkCLIRunner builds a production cliRunner. bin defaults to "lark-cli" (on
// PATH) when empty; homeBase defaults to a temp subdir when empty. It does NOT
// verify lark-cli is installed here (so server startup doesn't hard-depend on it
// when the feature is off) — a missing binary surfaces as a StartInit error.
func NewLarkCLIRunner(bin, homeBase string) (*larkCLIRunner, error) {
	if bin == "" {
		bin = defaultLarkCLIBin
	}
	if homeBase == "" {
		homeBase = filepath.Join(os.TempDir(), "numind-larkcli-homes")
	}
	if err := os.MkdirAll(homeBase, 0o700); err != nil {
		return nil, fmt.Errorf("feishu: create lark-cli home base %q: %w", homeBase, err)
	}
	return &larkCLIRunner{
		bin:      bin,
		homeBase: homeBase,
		sessions: map[string]*cliSession{},
	}, nil
}

// homeForUser returns this user's isolated config-home directory.
func (r *larkCLIRunner) homeForUser(userID uint) string {
	return filepath.Join(r.homeBase, fmt.Sprintf("u%d", userID))
}

// env builds the child-process environment: inherit the parent env but override
// HOME so lark-cli reads/writes THIS user's ~/.lark-cli/ . We also strip the
// agent-context vars (OPENCLAW_HOME / HERMES_HOME) that make `config init`
// refuse to create a new app, and pin the notifier-off vars so no lark-cli call
// triggers an update-check / skills-notifier network probe (which can hang in a
// no-outbound-internet container and stall config show / apps +init). The
// Dockerfile sets the same two as ENV; setting them here too keeps the child env
// clean even when the binary runs outside that image (dev/test/local).
func (r *larkCLIRunner) env(home string) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+3)
	for _, kv := range base {
		if strings.HasPrefix(kv, "HOME=") ||
			strings.HasPrefix(kv, "OPENCLAW_HOME=") ||
			strings.HasPrefix(kv, "HERMES_HOME=") ||
			strings.HasPrefix(kv, "LARKSUITE_CLI_NO_UPDATE_NOTIFIER=") ||
			strings.HasPrefix(kv, "LARKSUITE_CLI_NO_SKILLS_NOTIFIER=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out,
		"HOME="+home,
		"LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
		"LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
	)
	return out
}

// StartInit launches `lark-cli config init --new --name <userID>` in the user's
// isolated HOME, scrapes the device-code page URL from its early stdout, and
// leaves the process running in the background (it blocks until the user finishes
// in the browser). The session ref is the config-home path.
func (r *larkCLIRunner) StartInit(ctx context.Context, userID uint) (pageURL, sessionRef string, err error) {
	home := r.homeForUser(userID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", "", fmt.Errorf("feishu: create user home %q: %w", home, err)
	}

	// Detach from the request ctx: the process must outlive this HTTP request
	// (the user's browser step can take minutes). We bound only the URL-scrape
	// window below, not the process lifetime.
	//
	// bgCtx keeps the request's log fields (request/user ID) but drops its
	// cancellation, so the reaper goroutine below — which may log minutes after
	// the HTTP request returned — does not attach to an already-cancelled ctx
	// (which would mislead observability by hanging the warning off a dead trace).
	bgCtx := context.WithoutCancel(ctx)

	cmd := exec.Command(r.bin, "config", "init", "--new", "--name", fmt.Sprintf("%d", userID)) // #nosec G204 -- bin is config-pinned, userID is a uint
	cmd.Env = r.env(home)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("feishu: stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // fold stderr into the same stream for scraping

	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("feishu: start lark-cli: %w", err)
	}

	sess := &cliSession{home: home, cmd: cmd}
	r.mu.Lock()
	r.sessions[home] = sess
	r.mu.Unlock()

	// Reap the process in the background so its exit status is recorded and it
	// doesn't become a zombie.
	go func() {
		exitErr := cmd.Wait()
		sess.mu.Lock()
		sess.done = true
		sess.exitErr = exitErr
		sess.mu.Unlock()
		if exitErr != nil {
			log.C(bgCtx).Warnw("feishu: lark-cli config init exited with error", "user_id", userID, "error", exitErr.Error())
		}
	}()

	// Scrape the page URL from the early output, bounded by startInitTimeout.
	pageURL, scrapeErr := scrapePageURL(stdout, startInitTimeout)
	if scrapeErr != nil {
		// Kill the orphaned process — we never got a usable URL.
		_ = cmd.Process.Kill()
		r.mu.Lock()
		delete(r.sessions, home)
		r.mu.Unlock()
		return "", "", fmt.Errorf("feishu: scrape device-code URL: %w", scrapeErr)
	}

	return pageURL, home, nil
}

// scrapePageURL reads lines from r until it finds the device-code page URL or the
// timeout elapses. It keeps draining the pipe in a goroutine so the child process
// never blocks on a full stdout buffer after we return.
func scrapePageURL(r io.ReadCloser, timeout time.Duration) (string, error) {
	type result struct {
		url string
		err error
	}
	ch := make(chan result, 1)

	go func() {
		// Accumulate output until we can parse the URL; bufio via parseDeviceCodeURL.
		var buf bytes.Buffer
		tmp := make([]byte, 4096)
		for {
			n, rerr := r.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
				if u, perr := parseDeviceCodeURL(buf.String()); perr == nil {
					ch <- result{url: u}
					// Keep draining so the child doesn't block on a full pipe.
					go io.Copy(io.Discard, r) //nolint:errcheck
					return
				}
			}
			if rerr != nil {
				if u, perr := parseDeviceCodeURL(buf.String()); perr == nil {
					ch <- result{url: u}
				} else {
					ch <- result{err: fmt.Errorf("lark-cli output ended before page URL: %w", rerr)}
				}
				return
			}
		}
	}()

	select {
	case res := <-ch:
		return res.url, res.err
	case <-time.After(timeout):
		return "", fmt.Errorf("timed out after %s waiting for device-code URL", timeout)
	}
}

// ReadCredentials reports whether provisioning finished for sessionRef (the
// config-home path) and, if so, returns the appID + plaintext app_secret.
//
// Completion is detected two ways (either suffices): the tracked background
// process has exited, OR credentials are already readable from the config home
// (covers a server restart that lost the in-memory session). appSecret is
// materialized via materializeSecret.
func (r *larkCLIRunner) ReadCredentials(ctx context.Context, sessionRef string) (appID, appSecret string, done bool, err error) {
	home := sessionRef

	r.mu.Lock()
	sess := r.sessions[home]
	r.mu.Unlock()

	processDone := false
	if sess != nil {
		sess.mu.Lock()
		processDone = sess.done
		exitErr := sess.exitErr
		sess.mu.Unlock()
		if processDone && exitErr != nil {
			return "", "", false, fmt.Errorf("feishu: provisioning process failed: %w", exitErr)
		}
	}

	// Try to read the appId from the config home regardless — if it's there, the
	// user finished even if our in-memory session was lost.
	appID, readErr := r.readAppID(ctx, home)
	if readErr != nil {
		if processDone {
			// Process exited but no appId persisted → genuine failure.
			return "", "", false, fmt.Errorf("feishu: provisioning ended without credentials: %w", readErr)
		}
		// Still in progress.
		return "", "", false, nil
	}

	appSecret, err = r.materializeSecret(ctx, home, appID)
	if err != nil {
		return "", "", false, fmt.Errorf("feishu: materialize app secret: %w", err)
	}
	return appID, appSecret, true, nil
}

// ReadAppSecret resolves the plaintext app_secret for an already-provisioned
// appID (OAuth exchange path). appID is paired with the user's config home; since
// the home is keyed by userID and the runner does not store a reverse appID→home
// map, we resolve the home by scanning for a matching appID.
//
// We scan two sources, in order:
//  1. In-memory sessions (fast path for the same server instance).
//  2. On-disk config homes under r.homeBase (durable fallback). The OAuth
//     callback can land on any server instance and after a restart the
//     in-memory map is empty, but the per-user credentials persist on disk
//     under r.homeBase/u{userID}/.lark-cli/ — so a disk scan still finds them.
//     This mirrors ReadCredentials, which reads the config home regardless of
//     whether the in-memory session is present (see line ~260).
func (r *larkCLIRunner) ReadAppSecret(ctx context.Context, appID string) (string, error) {
	r.mu.Lock()
	homes := make([]string, 0, len(r.sessions))
	for h := range r.sessions {
		homes = append(homes, h)
	}
	r.mu.Unlock()

	for _, home := range homes {
		if got, err := r.readAppID(ctx, home); err == nil && got == appID {
			return r.materializeSecret(ctx, home, appID)
		}
	}

	// Disk-scan fallback: the in-memory map missed (different instance or
	// post-restart). Iterate the persisted per-user config homes.
	seen := make(map[string]struct{}, len(homes))
	for _, h := range homes {
		seen[h] = struct{}{}
	}
	entries, derr := os.ReadDir(r.homeBase)
	if derr != nil {
		return "", fmt.Errorf("%w: scan config homes for app %s: %v", errno.ErrLarkCallFailed, appID, derr)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		home := filepath.Join(r.homeBase, entry.Name())
		if _, dup := seen[home]; dup {
			continue // already tried via the in-memory scan
		}
		if got, err := r.readAppID(ctx, home); err == nil && got == appID {
			return r.materializeSecret(ctx, home, appID)
		}
	}

	return "", fmt.Errorf("%w: no config home found for app %s", errno.ErrLarkCallFailed, appID)
}

// configShowJSON is the subset of `lark-cli config show` JSON we read.
type configShowJSON struct {
	AppID string `json:"appId"`
}

// readAppID runs `lark-cli config show` in the user's home and extracts appId.
// (config show masks appSecret as "****", so we only trust it for the appId.)
func (r *larkCLIRunner) readAppID(ctx context.Context, home string) (string, error) {
	out, err := r.run(ctx, home, "config", "show")
	if err != nil {
		return "", err
	}
	var cfg configShowJSON
	if jerr := json.Unmarshal([]byte(extractFirstJSONObject(out)), &cfg); jerr != nil {
		return "", fmt.Errorf("feishu: parse config show: %w", jerr)
	}
	if cfg.AppID == "" {
		return "", errors.New("feishu: config show has no appId yet")
	}
	return cfg.AppID, nil
}

// materializeSecret writes the app's local env (FEISHU_APP_ID / FEISHU_APP_SECRET)
// via `lark-cli apps +init` and parses the plaintext app_secret back out. The env
// file is written under a temp dir inside the user's home and removed after read
// so the plaintext secret does not linger on disk.
//
// ⚠️ The exact `apps +init` semantics + env-file path are derived from binary
// inspection ("Local environment written to %s"); confirm against a live run.
func (r *larkCLIRunner) materializeSecret(ctx context.Context, home, appID string) (string, error) {
	dir, err := os.MkdirTemp(home, "appinit-")
	if err != nil {
		return "", fmt.Errorf("feishu: temp dir for app init: %w", err)
	}
	defer os.RemoveAll(dir) // best-effort: never leave the secret on disk

	if _, err := r.run(ctx, home, "apps", "+init", "--app-id", appID, "--dir", dir); err != nil {
		return "", fmt.Errorf("feishu: apps +init: %w", err)
	}

	envContent, err := readFirstEnvFile(dir)
	if err != nil {
		return "", err
	}
	_, secret, err := parseEnvCredentials(envContent)
	if err != nil {
		return "", err
	}
	return secret, nil
}

// run executes a lark-cli subcommand in the user's isolated HOME and returns its
// combined stdout/stderr. It is for SHORT, non-blocking subcommands (config show,
// apps +init) — NOT the blocking `config init`.
func (r *larkCLIRunner) run(ctx context.Context, home string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin, args...) // #nosec G204 -- bin config-pinned, args internal
	cmd.Env = r.env(home)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("lark-cli %s: %w (output: %s)", strings.Join(args, " "), err, truncate(buf.String(), 500))
	}
	return buf.String(), nil
}

// readFirstEnvFile finds a dotenv-style file (.env.local preferred, then .env)
// under dir and returns its contents.
func readFirstEnvFile(dir string) (string, error) {
	for _, name := range []string{".env.local", ".env"} {
		p := filepath.Join(dir, name)
		if b, err := os.ReadFile(p); err == nil { // #nosec G304 -- path built from our temp dir
			return string(b), nil
		}
	}
	// Fall back: scan for any *.env / .env* file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("feishu: read app init dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.Contains(e.Name(), ".env") {
			if b, rerr := os.ReadFile(filepath.Join(dir, e.Name())); rerr == nil { // #nosec G304
				return string(b), nil
			}
		}
	}
	return "", errors.New("feishu: no env file with FEISHU_APP_SECRET produced by apps +init")
}

// extractFirstJSONObject returns the substring from the first '{' to the last '}'
// (inclusive), tolerating any human-readable preamble/epilogue lark-cli prints
// around the JSON (e.g. the trailing "Config file path: ..." line).
func extractFirstJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < 0 || end < start {
		return s
	}
	return s[start : end+1]
}

// truncate caps a string for safe error logging (avoid dumping huge CLI output).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// compile-time guard: larkCLIRunner satisfies cliRunner.
var _ cliRunner = (*larkCLIRunner)(nil)

// --- 飞书 v2 OAuth token exchanger (HTTP) ------------------------------------

// oauthTokenURL is the 飞书 v2 token endpoint (confirmed in lark-cli binary +
// spike-bootstrap.md).
const oauthTokenURL = "https://open.feishu.cn/open-apis/authen/v2/oauth/token"

// oauthExchangeTimeout bounds the token POST.
const oauthExchangeTimeout = 15 * time.Second

// httpTokenExchanger performs the authorization-code → token exchange over HTTP.
// It is deliberately a plain *http.Client (NOT aiservice): 飞书 is an external
// business API, not an LLM gateway. redirectURI must match what was used to build
// the authorize URL (T7) or 飞书 rejects the exchange.
type httpTokenExchanger struct {
	client      *http.Client
	redirectURI string
}

// NewHTTPTokenExchanger builds the production tokenExchanger. redirectURI is the
// OAuth callback URL registered in the 飞书 console (config-injected, per env).
func NewHTTPTokenExchanger(redirectURI string) (*httpTokenExchanger, error) {
	if redirectURI == "" {
		return nil, errors.New("feishu: empty OAuth redirect URI")
	}
	return &httpTokenExchanger{
		client:      &http.Client{Timeout: oauthExchangeTimeout},
		redirectURI: redirectURI,
	}, nil
}

// oauthTokenEnvelope is the 飞书 v2 token response envelope: a top-level
// code/msg plus the token fields (which 飞书 returns at the top level for v2).
type oauthTokenEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	oauthTokenResp
}

// Exchange POSTs the authorization-code grant and returns the token fields.
// On a non-zero 飞书 business code or HTTP error it returns ErrLarkCallFailed
// (wrapped) so callers can classify it; it never logs the secret or code.
func (h *httpTokenExchanger) Exchange(ctx context.Context, appID, appSecret, code string) (*oauthTokenResp, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     appID,
		"client_secret": appSecret,
		"code":          code,
		"redirect_uri":  h.redirectURI,
	})
	if err != nil {
		return nil, fmt.Errorf("feishu: marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthTokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("feishu: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: token request: %v", errno.ErrLarkCallFailed, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read token response: %v", errno.ErrLarkCallFailed, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: token endpoint HTTP %d", errno.ErrLarkCallFailed, resp.StatusCode)
	}

	var env oauthTokenEnvelope
	if jerr := json.Unmarshal(raw, &env); jerr != nil {
		return nil, fmt.Errorf("%w: parse token response: %v", errno.ErrLarkCallFailed, jerr)
	}
	if env.Code != 0 {
		// Do NOT echo msg verbatim if it could contain sensitive data; 飞书 msg is
		// a generic error string, safe to include for diagnosis.
		return nil, fmt.Errorf("%w: 飞书 code %d (%s)", errno.ErrLarkCallFailed, env.Code, env.Msg)
	}

	return &env.oauthTokenResp, nil
}

// compile-time guard: httpTokenExchanger satisfies tokenExchanger.
var _ tokenExchanger = (*httpTokenExchanger)(nil)
