// Package feishu — provisioner_cli.go holds the PRODUCTION implementations of the
// provisioner's seams: the lark-cli config-init runner (os/exec) and the 飞书 v2
// OAuth token exchanger (HTTP). These touch the outside world (process spawning,
// network) and are therefore NOT exercised by the offline unit tests in
// provisioner_test.go — the pure orchestration is. The config.json reader IS
// exercised offline via a fake lark-cli shell script (provisioner_cli_test.go).
//
// Key behaviours encoded here (from spike-bootstrap.md + verified lark-cli
// reality on 2026-06-24):
//
//   - Per-user isolation + persistence (G1-home): lark-cli has no dedicated
//     config-home env var; it reads $HOME/.lark-cli/config.json . We override HOME
//     per user (homeBase/u{userID}) so each user's app credentials + tokens live in
//     their OWN PERSISTENT directory and never collide. homeBase comes from config
//     (feishu.home_base) and MUST point at a durable volume (e.g. dev
//     /opt/numind/dev/feishu-homes) so a redeploy does not wipe the homes and a
//     user reconnecting reuses the same home (idempotent — MkdirAll is a no-op when
//     it already exists, lark-cli reuses the existing config.json + tokens).
//   - `lark-cli config init --new` (HOME=user's persistent home) prints
//     "打开以下链接配置应用:\n  https://open.feishu.cn/page/cli?user_code=...\n等待配置应用..."
//     and BLOCKS until the user finishes the browser step (creates the app), then
//     exits 0 — having written the new app into that home's config.json:
//     {"apps":[{"appId":"cli_xxx","appSecret":"...","brand":"feishu",...}]} .
//     We run it backgrounded, scrape the page URL from its early stdout, and let
//     it finish on its own. config init self-bounds the wait (~10min); we add a
//     hard ceiling so a never-completing process is reaped.
//   - Completion = the process exited AND apps[0] is readable from config.json.
//     We read appId + plaintext appSecret directly from config.json — no `apps
//     +init` / `config show` materialization dance (lark-cli stores the secret in
//     plaintext in config.json).
//
// The per-user home (homeBase/u{userID}) is the durable store: config.json holds
// the persisted app creds and lark-cli's token store, and is the source
// ReadAppSecret reads from during the later OAuth exchange ("一次绑永久用"). On
// completion we clean up only the in-flight process tracking, not config.json.
package feishu

import (
	"bufio"
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

// --- lark-cli config-init runner --------------------------------------------

// defaultLarkCLIBin is the lark-cli executable name; overridable via config so
// dev/prod can pin an absolute path.
const defaultLarkCLIBin = "lark-cli"

// startInitTimeout bounds how long we wait for lark-cli to PRINT the page URL
// after launch (not the whole flow — the user's browser step runs in the
// background, bounded by appCreateCeiling).
const startInitTimeout = 30 * time.Second

// appCreateCeiling is the hard upper bound on the backgrounded `config init`
// process. lark-cli self-bounds its wait around ~10min; we kill anything that
// overruns this so a never-finished browser step does not leak a process forever.
const appCreateCeiling = 12 * time.Minute

// larkCLIRunner is the production cliRunner over os/exec.
//
// State across HTTP requests: StartAppCreate launches a long-lived background
// process (it blocks on the user's browser step). We track running sessions
// in-process so a later PollAppCreated on the same server instance can observe
// completion. The session ref is the per-user home path — persistent, so even if
// the in-memory entry is lost (restart), PollAppCreated can still read whatever
// credentials lark-cli already persisted in that home's config.json.
type larkCLIRunner struct {
	bin      string // lark-cli binary (name on PATH or absolute path)
	homeBase string // PERSISTENT base dir under which per-user HOMEs (u{userID}) live

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
// PATH) when empty. homeBase is the PERSISTENT per-user home base (feishu.home_base):
// when empty it falls back to a temp subdir, but that fallback is NON-persistent
// (lost on reboot / container replace) — production MUST set feishu.home_base to a
// durable volume so credentials + tokens survive a redeploy. It does NOT verify
// lark-cli is installed here (so server startup doesn't hard-depend on it when the
// feature is off) — a missing binary surfaces as a StartAppCreate error.
//
// The home base is created 0700 and is owned by the running process user (we rely on
// the process umask / default ownership — MkdirAll creates dirs as the current uid).
func NewLarkCLIRunner(bin, homeBase string) (*larkCLIRunner, error) {
	if bin == "" {
		bin = defaultLarkCLIBin
	}
	if homeBase == "" {
		// Non-persistent fallback only (feishu.home_base unset). Production sets it.
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

// homeForUser returns this user's isolated PERSISTENT home directory
// (homeBase/u{userID}). The path is deterministic, so the same user always maps to
// the same home — reconnecting reuses the existing config.json + tokens (idempotent).
func (r *larkCLIRunner) homeForUser(userID uint) string {
	return filepath.Join(r.homeBase, fmt.Sprintf("u%d", userID))
}

// configPath returns the lark-cli config.json path inside a user's home.
func configPath(home string) string {
	return filepath.Join(home, ".lark-cli", "config.json")
}

// env builds the child-process environment: inherit the parent env but override
// HOME so lark-cli reads/writes THIS user's $HOME/.lark-cli/ . We also strip the
// agent-context vars (OPENCLAW_HOME / HERMES_HOME) that make `config init` refuse
// to create a new app, and pin the notifier-off vars so no lark-cli call triggers
// an update-check / skills-notifier network probe (which can hang in a
// no-outbound-internet container and stall the run). The Dockerfile sets the same
// two as ENV; setting them here too keeps the child env clean even when the binary
// runs outside that image (dev/test/local).
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

// StartAppCreate launches `lark-cli config init --new` in the user's isolated
// PERSISTENT home (homeBase/u{userID}), scrapes the page URL from its early stdout,
// and leaves the process running in the background (it blocks until the user
// finishes in the browser). The returned handle carries the home path + the process
// session tracking. The home dir is created 0700 (owned by the running process
// user); MkdirAll is a no-op when it already exists, so a re-provision reuses it.
func (r *larkCLIRunner) StartAppCreate(ctx context.Context, userID uint) (pageURL string, handle *AppCreateHandle, err error) {
	home := r.homeForUser(userID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", nil, fmt.Errorf("feishu: create user home %q: %w", home, err)
	}

	// Concurrency guard: reject a second StartAppCreate for the same user while a
	// previous in-flight `config init` is still running. Without this, the second
	// call would overwrite r.sessions[home] and orphan the first process (it would
	// linger until appCreateCeiling reaps it, meanwhile both processes write the
	// same config.json and can corrupt each other's write / file lock). A finished
	// (done) session is NOT a blocker — the user may legitimately re-provision.
	//
	// The check-and-reserve is done atomically under r.mu so two concurrent callers
	// can't both pass the check and then both spawn a process (TOCTOU): the winner
	// reserves the slot with a placeholder session; losers see it and bail. We
	// attach the actual *exec.Cmd to the reserved session after a successful Start,
	// and release the reservation on any early-return failure path so a failed
	// launch never blocks a later retry.
	sess := &cliSession{home: home}
	r.mu.Lock()
	if existing := r.sessions[home]; existing != nil {
		existing.mu.Lock()
		inFlight := !existing.done
		existing.mu.Unlock()
		if inFlight {
			r.mu.Unlock()
			return "", nil, fmt.Errorf("feishu: provisioning already in progress for user %d", userID)
		}
	}
	r.sessions[home] = sess // reserve the slot before spawning
	r.mu.Unlock()

	// releaseReservation drops our reserved slot on a failure path — but only if it
	// is still OURS (a later successful StartAppCreate may have replaced it).
	releaseReservation := func() {
		r.mu.Lock()
		if r.sessions[home] == sess {
			delete(r.sessions, home)
		}
		r.mu.Unlock()
	}

	// Detach from the request ctx: the process must outlive this HTTP request
	// (the user's browser step can take minutes). We bound the URL-scrape window
	// (startInitTimeout) and the whole process lifetime (appCreateCeiling)
	// separately, not via the request ctx.
	//
	// bgCtx keeps the request's log fields (request/user ID) but drops its
	// cancellation, so the reaper goroutine below — which may log minutes after the
	// HTTP request returned — does not attach to an already-cancelled ctx.
	bgCtx := context.WithoutCancel(ctx)

	cmd := exec.Command(r.bin, "config", "init", "--new") // #nosec G204 -- bin is config-pinned, no user-controlled args
	cmd.Env = r.env(home)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		releaseReservation()
		return "", nil, fmt.Errorf("feishu: stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // fold stderr into the same stream for scraping

	if err := cmd.Start(); err != nil {
		releaseReservation()
		return "", nil, fmt.Errorf("feishu: start lark-cli: %w", err)
	}

	sess.cmd = cmd

	// Hard ceiling: kill a process that never completes (user abandoned the
	// browser step) so it doesn't leak. Cancelled by the reaper on normal exit.
	ceiling := time.AfterFunc(appCreateCeiling, func() {
		sess.mu.Lock()
		alreadyDone := sess.done
		sess.mu.Unlock()
		if alreadyDone {
			return
		}
		log.C(bgCtx).Warnw("feishu: lark-cli config init exceeded ceiling, killing", "user_id", userID, "ceiling", appCreateCeiling.String())
		_ = cmd.Process.Kill()
	})

	// Reap the process in the background so its exit status is recorded and it
	// doesn't become a zombie.
	go func() {
		exitErr := cmd.Wait()
		ceiling.Stop()
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
		releaseReservation()
		return "", nil, fmt.Errorf("feishu: scrape page URL: %w", scrapeErr)
	}

	return pageURL, &AppCreateHandle{home: home, session: sess}, nil
}

// scrapePageURL reads lines from r until it finds the page URL or the timeout
// elapses. It keeps draining the pipe in a goroutine so the child process never
// blocks on a full stdout buffer after we return.
func scrapePageURL(r io.ReadCloser, timeout time.Duration) (string, error) {
	type result struct {
		url string
		err error
	}
	ch := make(chan result, 1)

	go func() {
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
		return "", fmt.Errorf("timed out after %s waiting for page URL", timeout)
	}
}

// PollAppCreated reports whether provisioning finished for the handle and, if so,
// returns the appID + plaintext app_secret read from config.json.
//
// Completion is detected two ways (either suffices): the tracked background
// process has exited, OR apps[0] is already readable from config.json (covers a
// server restart that lost the in-memory session, where handle.session is nil).
// On completion we clean up the in-flight process tracking (drop the session
// entry); config.json itself is kept as the durable per-user credential store.
func (r *larkCLIRunner) PollAppCreated(ctx context.Context, handle *AppCreateHandle) (appID, appSecret string, done bool, err error) {
	if handle == nil || handle.home == "" {
		// Unknown/stale ref → treat as still-in-progress rather than erroring, so a
		// poll after a lost session ref does not surface a hard failure.
		return "", "", false, nil
	}
	home := handle.home

	processDone := false
	if handle.session != nil {
		handle.session.mu.Lock()
		processDone = handle.session.done
		exitErr := handle.session.exitErr
		handle.session.mu.Unlock()
		if processDone && exitErr != nil {
			return "", "", false, fmt.Errorf("feishu: provisioning process failed: %w", exitErr)
		}
	}

	// Read apps[0] from config.json regardless — if it's there, the user finished
	// even if our in-memory session was lost (post-restart).
	appID, appSecret, readErr := readAppFromConfig(home)
	if readErr != nil {
		if processDone {
			// Process exited but no app persisted → genuine failure.
			return "", "", false, fmt.Errorf("feishu: provisioning ended without credentials: %w", readErr)
		}
		// Still in progress.
		return "", "", false, nil
	}

	// Done: drop the in-flight process tracking (config.json persists the creds).
	r.cleanupSession(home)
	return appID, appSecret, true, nil
}

// resolveHandle reconstructs an AppCreateHandle from a session ref (home path).
// It prefers the live in-memory session (so a poll on the same instance observes
// process exit), falling back to a path-only handle (session nil) so a post-
// restart poll still reads config.json. An empty/unknown ref yields nil.
func (r *larkCLIRunner) resolveHandle(sessionRef string) *AppCreateHandle {
	if sessionRef == "" {
		return nil
	}
	r.mu.Lock()
	sess := r.sessions[sessionRef]
	r.mu.Unlock()
	return &AppCreateHandle{home: sessionRef, session: sess}
}

// sessionRefForUser returns the deterministic durable session ref for userID:
// the per-user home path (the same path StartAppCreate uses and that PollAppCreated
// reads config.json from). userID 0 has no home.
func (r *larkCLIRunner) sessionRefForUser(userID uint) string {
	if userID == 0 {
		return ""
	}
	return r.homeForUser(userID)
}

// cleanupSession removes the in-flight process tracking for a user's home. It
// does NOT delete config.json (the durable credential store).
func (r *larkCLIRunner) cleanupSession(home string) {
	r.mu.Lock()
	delete(r.sessions, home)
	r.mu.Unlock()
}

// ReadAppSecret resolves the plaintext app_secret for an already-provisioned
// appID (OAuth exchange path). The OAuth callback can land on any server instance
// and after a restart the in-memory map is empty, so we resolve by scanning the
// persistent per-user homes under r.homeBase for a config.json whose apps[0].appId
// matches.
func (r *larkCLIRunner) ReadAppSecret(ctx context.Context, appID string) (string, error) {
	entries, derr := os.ReadDir(r.homeBase)
	if derr != nil {
		return "", fmt.Errorf("%w: scan config homes for app %s: %v", errno.ErrLarkCallFailed, appID, derr)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		home := filepath.Join(r.homeBase, entry.Name())
		gotID, gotSecret, err := readAppFromConfig(home)
		if err == nil && gotID == appID {
			return gotSecret, nil
		}
	}
	return "", fmt.Errorf("%w: no config home found for app %s", errno.ErrLarkCallFailed, appID)
}

// --- config.json reading (pure-ish helpers) ---------------------------------

// larkConfigJSON is the subset of lark-cli's ~/.lark-cli/config.json we read.
// Real shape: {"apps":[{"appId":"cli_xxx","appSecret":"...","brand":"feishu",
// "users":[{"userOpenId":"ou_...","userName":"..."}]}]}
type larkConfigJSON struct {
	Apps []larkConfigApp `json:"apps"`
}

type larkConfigApp struct {
	AppID     string `json:"appId"`
	AppSecret string `json:"appSecret"`
}

// readAppFromConfig reads apps[0].appId/appSecret from the user's home
// config.json. It returns an error when the file is absent (app not created yet),
// unparseable, or apps[0] is incomplete — so callers can distinguish
// "in-progress" from "done".
func readAppFromConfig(home string) (appID, appSecret string, err error) {
	raw, err := os.ReadFile(configPath(home)) // #nosec G304 -- path built from our homeBase
	if err != nil {
		return "", "", fmt.Errorf("feishu: read config.json: %w", err)
	}
	return parseAppFromConfigJSON(raw)
}

// parseAppFromConfigJSON parses apps[0].appId/appSecret out of lark-cli's
// config.json bytes. Pure (no I/O) so it is unit-tested in isolation. Both fields
// are required; a present-but-incomplete apps[0] is an error.
func parseAppFromConfigJSON(raw []byte) (appID, appSecret string, err error) {
	var cfg larkConfigJSON
	if jerr := json.Unmarshal(raw, &cfg); jerr != nil {
		return "", "", fmt.Errorf("feishu: parse config.json: %w", jerr)
	}
	if len(cfg.Apps) == 0 {
		return "", "", errors.New("feishu: config.json has no apps yet")
	}
	app := cfg.Apps[0]
	if app.AppID == "" {
		return "", "", errors.New("feishu: config.json apps[0] has no appId")
	}
	if app.AppSecret == "" {
		return "", "", errors.New("feishu: config.json apps[0] has no appSecret")
	}
	return app.AppID, app.AppSecret, nil
}

// --- page URL parsing (pure helper) -----------------------------------------

// deviceCodeURLMarker is the public 飞书 page the lark-cli prints. We match on this
// exact host+path so we don't accidentally grab some other URL from the output.
const deviceCodeURLMarker = "https://open.feishu.cn/page/cli"

// parseDeviceCodeURL extracts the page URL from lark-cli's stdout. Real output:
//
//	打开以下链接配置应用:
//	  https://open.feishu.cn/page/cli?user_code=2AF7-MFWU&lpv=1.0.56&from=cli
//	等待配置应用...
func parseDeviceCodeURL(cliOutput string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(cliOutput))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if idx := strings.Index(line, deviceCodeURLMarker); idx >= 0 {
			// Take from the marker to the end of the (already trimmed) line; the
			// query string is part of the URL.
			return line[idx:], nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("feishu: scan cli output: %w", err)
	}
	return "", errors.New("feishu: page URL not found in lark-cli output")
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

// oauthTokenEnvelope is the 飞书 v2 token response envelope: a top-level code/msg
// plus the token fields (which 飞书 returns at the top level for v2).
type oauthTokenEnvelope struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	oauthTokenResp
}

// Exchange POSTs the authorization-code grant and returns the token fields. On a
// non-zero 飞书 business code or HTTP error it returns ErrLarkCallFailed (wrapped)
// so callers can classify it; it never logs the secret or code.
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
		// 飞书 msg is a generic error string, safe to include for diagnosis.
		return nil, fmt.Errorf("%w: 飞书 code %d (%s)", errno.ErrLarkCallFailed, env.Code, env.Msg)
	}

	return &env.oauthTokenResp, nil
}

// compile-time guard: httpTokenExchanger satisfies tokenExchanger.
var _ tokenExchanger = (*httpTokenExchanger)(nil)
