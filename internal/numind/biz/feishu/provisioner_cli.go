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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

// LarkCLIRunner is the production cliRunner over os/exec.
//
// State across HTTP requests: StartAppCreate launches a long-lived background
// process (it blocks on the user's browser step). We track running sessions
// in-process so a later PollAppCreated on the same server instance can observe
// completion. The session ref is the per-user home path — persistent, so even if
// the in-memory entry is lost (restart), PollAppCreated can still read whatever
// credentials lark-cli already persisted in that home's config.json.
type LarkCLIRunner struct {
	bin      string // lark-cli binary (name on PATH or absolute path)
	homeBase string // PERSISTENT base dir under which per-user HOMEs (u{userID}) live

	mu       sync.Mutex
	sessions map[string]*cliSession
}

// cliSession tracks one backgrounded blocking lark-cli invocation (config init OR
// auth login).
type cliSession struct {
	home string
	cmd  *exec.Cmd

	mu      sync.Mutex
	done    bool
	exitErr error
}

// isAlive reports whether the tracked process is still running (started and not yet
// reaped). A reserved-but-not-yet-started session (cmd nil) counts as alive so a
// concurrent spawn can't slip past the reservation; a reaped (done) session is dead
// and therefore reclaimable. This is the LOCK SELF-HEAL primitive: only a live
// process blocks a re-spawn, a dead/finished one is reclaimed.
func (s *cliSession) isAlive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.done
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
func NewLarkCLIRunner(bin, homeBase string) (*LarkCLIRunner, error) {
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
	return &LarkCLIRunner{
		bin:      bin,
		homeBase: homeBase,
		sessions: map[string]*cliSession{},
	}, nil
}

// homeForUser returns this user's isolated PERSISTENT home directory
// (homeBase/u{userID}). The path is deterministic, so the same user always maps to
// the same home — reconnecting reuses the existing config.json + tokens (idempotent).
func (r *LarkCLIRunner) homeForUser(userID uint) string {
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
func (r *LarkCLIRunner) env(home string) []string {
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
//
// LOCK SELF-HEAL (fix/feishu-phase-from-home): the orchestrator only ever calls this
// when the home does NOT already hold a built app (AppExists==false). The in-flight
// guard below now RECLAIMS a stale/dead session (process exited, e.g. crashed or
// reaped) instead of permanently blocking — only a genuinely ALIVE in-flight process
// is a blocker — so a wedged session can never dead-end the connect flow.
func (r *LarkCLIRunner) StartAppCreate(ctx context.Context, userID uint) (pageURL string, handle *AppCreateHandle, err error) {
	home := r.homeForUser(userID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return "", nil, fmt.Errorf("feishu: create user home %q: %w", home, err)
	}
	sess, scrapedURL, err := r.spawnBlocking(ctx, userID, home,
		[]string{"config", "init", "--new"}, startInitTimeout, appCreateCeiling, "config init")
	if err != nil {
		return "", nil, err
	}
	return scrapedURL, &AppCreateHandle{home: home, session: sess}, nil
}

// spawnBlockingURL is the StartAuthorizeLogin-facing wrapper around spawnBlocking: it
// launches a blocking lark-cli command in the user's home, scrapes the URL it prints,
// and returns only that URL (the auth-login flow tracks no handle — completion is
// observed via AuthStatus). The sessionKey isolates the auth-login session from the
// config-init session for the same home.
func (r *LarkCLIRunner) spawnBlockingURL(ctx context.Context, userID uint, sessionKey string, args []string, scrapeTimeout, ceiling time.Duration) (string, error) {
	_, url, err := r.spawnBlocking(ctx, userID, sessionKey, args, scrapeTimeout, ceiling, args[0]+" "+args[1])
	return url, err
}

// spawnBlocking launches a blocking lark-cli command backgrounded, scrapes the first
// feishu.cn URL it prints (bounded by scrapeTimeout), and leaves the process running
// (it self-bounds; we also enforce a hard ceiling). It is the shared engine behind
// BOTH app-create (config init --new) and authorize (auth login) — the only
// per-command differences are the args, the timeouts, and the session key.
//
// Self-healing in-flight guard: a session is keyed by sessionKey in r.sessions; a
// second call while an ALIVE process holds that key is rejected (avoids orphaning the
// first / corrupting a shared config.json write), but a DEAD prior session (process
// exited) is reclaimed, so a crashed/reaped run can never permanently block a retry.
func (r *LarkCLIRunner) spawnBlocking(ctx context.Context, userID uint, sessionKey string, args []string, scrapeTimeout, ceiling time.Duration, label string) (*cliSession, string, error) {
	// Atomically check-and-reserve the session slot under r.mu so two concurrent
	// callers can't both spawn (TOCTOU). A finished (done) OR process-dead session is
	// reclaimable; only a live in-flight one blocks.
	sess := &cliSession{home: sessionKey}
	r.mu.Lock()
	if existing := r.sessions[sessionKey]; existing != nil && existing.isAlive() {
		r.mu.Unlock()
		return nil, "", fmt.Errorf("feishu: %s already in progress for user %d", label, userID)
	}
	r.sessions[sessionKey] = sess // reserve (replacing any dead/finished prior session)
	r.mu.Unlock()

	// releaseReservation drops our reserved slot on a failure path — but only if it is
	// still OURS (a later successful spawn may have replaced it).
	releaseReservation := func() {
		r.mu.Lock()
		if r.sessions[sessionKey] == sess {
			delete(r.sessions, sessionKey)
		}
		r.mu.Unlock()
	}

	// Detach from the request ctx: the process must outlive this HTTP request (the
	// user's browser step can take minutes). bgCtx keeps the request's log fields but
	// drops cancellation so the reaper (logging minutes later) isn't on a dead ctx.
	bgCtx := context.WithoutCancel(ctx)

	cmd := exec.Command(r.bin, args...) // #nosec G204 -- bin is config-pinned; args are fixed verbs (no user-controlled args)
	cmd.Env = r.env(r.homeForKey(sessionKey))

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		releaseReservation()
		return nil, "", fmt.Errorf("feishu: stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // fold stderr into the same stream for scraping

	if err := cmd.Start(); err != nil {
		releaseReservation()
		return nil, "", fmt.Errorf("feishu: start lark-cli: %w", err)
	}
	sess.cmd = cmd

	// Hard ceiling: kill a process that never completes (user abandoned the browser
	// step). Cancelled by the reaper on normal exit.
	ceilingTimer := time.AfterFunc(ceiling, func() {
		sess.mu.Lock()
		alreadyDone := sess.done
		sess.mu.Unlock()
		if alreadyDone {
			return
		}
		log.C(bgCtx).Warnw("feishu: lark-cli exceeded ceiling, killing", "user_id", userID, "label", label, "ceiling", ceiling.String())
		_ = cmd.Process.Kill()
	})

	// Reap the process so its exit status is recorded and it doesn't become a zombie.
	go func() {
		exitErr := cmd.Wait()
		ceilingTimer.Stop()
		sess.mu.Lock()
		sess.done = true
		sess.exitErr = exitErr
		sess.mu.Unlock()
		if exitErr != nil {
			log.C(bgCtx).Warnw("feishu: lark-cli exited with error", "user_id", userID, "label", label, "error", exitErr.Error())
		}
	}()

	// Scrape the URL from the early output, bounded by scrapeTimeout.
	url, scrapeErr := scrapeURL(stdout, scrapeTimeout, parseFeishuURL)
	if scrapeErr != nil {
		_ = cmd.Process.Kill() // kill the orphaned process — we never got a usable URL
		releaseReservation()
		return nil, "", fmt.Errorf("feishu: scrape %s URL: %w", label, scrapeErr)
	}
	return sess, url, nil
}

// homeForKey maps a session key back to the home dir lark-cli uses as $HOME.
// App-create keys are the bare home; auth-login keys carry the "#auth" suffix — both
// map to the SAME home directory (the suffix only namespaces the in-memory session
// slot so app-create and auth-login don't clobber each other's tracking).
func (r *LarkCLIRunner) homeForKey(key string) string { return strings.TrimSuffix(key, "#auth") }

// scrapeURL reads lines from r until parse extracts a URL or the timeout elapses.
// It keeps draining the pipe in a goroutine so the child process never blocks on a
// full stdout buffer after we return. parse is the per-command URL matcher (config
// init's page URL or auth login's verification URL — both feishu.cn links).
func scrapeURL(r io.ReadCloser, timeout time.Duration, parse func(string) (string, error)) (string, error) {
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
				if u, perr := parse(buf.String()); perr == nil {
					ch <- result{url: u}
					// Keep draining so the child doesn't block on a full pipe.
					go io.Copy(io.Discard, r) //nolint:errcheck
					return
				}
			}
			if rerr != nil {
				if u, perr := parse(buf.String()); perr == nil {
					ch <- result{url: u}
				} else {
					ch <- result{err: fmt.Errorf("lark-cli output ended before URL: %w", rerr)}
				}
				return
			}
		}
	}()

	select {
	case res := <-ch:
		return res.url, res.err
	case <-time.After(timeout):
		return "", fmt.Errorf("timed out after %s waiting for URL", timeout)
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
func (r *LarkCLIRunner) PollAppCreated(ctx context.Context, handle *AppCreateHandle) (appID, appSecret string, done bool, err error) {
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

// AppExists reports whether userID's PERSISTENT home already holds a built app —
// the phase truth source for create_app vs authorize. It reads config.json apps[0]
// directly (no process), so it is correct even when the in-memory session was lost
// (restart) or the DB row is missing/stale. A missing/empty/incomplete config.json
// (app not built yet) is a clean (false, nil); a genuinely unreadable file (e.g. a
// permission error, not os.ErrNotExist) surfaces as an error.
func (r *LarkCLIRunner) AppExists(_ context.Context, userID uint) (bool, error) {
	if userID == 0 {
		return false, nil
	}
	home := r.homeForUser(userID)
	_, _, err := readAppFromConfig(home)
	if err == nil {
		return true, nil
	}
	// "no app yet" — file absent (config init not finished) or apps[]/fields not yet
	// populated — is a clean not-built, NOT an error. Only an unexpected read failure
	// (file present but unreadable) is surfaced.
	if errors.Is(err, os.ErrNotExist) || isConfigIncomplete(err) {
		return false, nil
	}
	if _, statErr := os.Stat(configPath(home)); errors.Is(statErr, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("feishu: app-exists check (user %d): %w", userID, err)
}

// AppID returns the appId from userID's home (config.json apps[0]). Used to
// reconcile the DB row (UI/status) on the done path. Errors when no app is present.
func (r *LarkCLIRunner) AppID(_ context.Context, userID uint) (string, error) {
	if userID == 0 {
		return "", fmt.Errorf("feishu: app-id read: missing user id")
	}
	appID, _, err := readAppFromConfig(r.homeForUser(userID))
	if err != nil {
		return "", fmt.Errorf("feishu: app-id read (user %d): %w", userID, err)
	}
	return appID, nil
}

// resolveHandle reconstructs an AppCreateHandle from a session ref (home path).
// It prefers the live in-memory session (so a poll on the same instance observes
// process exit), falling back to a path-only handle (session nil) so a post-
// restart poll still reads config.json. An empty/unknown ref yields nil.
func (r *LarkCLIRunner) resolveHandle(sessionRef string) *AppCreateHandle {
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
func (r *LarkCLIRunner) sessionRefForUser(userID uint) string {
	if userID == 0 {
		return ""
	}
	return r.homeForUser(userID)
}

// cleanupSession removes the in-flight process tracking for a user's home. It
// does NOT delete config.json (the durable credential store).
func (r *LarkCLIRunner) cleanupSession(home string) {
	r.mu.Lock()
	delete(r.sessions, home)
	r.mu.Unlock()
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

// errConfigNoApp marks a config.json that parses fine but does not yet hold a
// complete apps[0] (no apps / missing appId / missing appSecret) — i.e. the app is
// "not built yet" rather than a corrupt/unreadable file. AppExists treats this as a
// clean (false, nil); PollAppCreated treats it as still-in-progress.
var errConfigNoApp = errors.New("feishu: config.json has no complete app yet")

// isConfigIncomplete reports whether err means "app not built yet" (errConfigNoApp)
// as opposed to a genuine read/parse failure.
func isConfigIncomplete(err error) bool { return errors.Is(err, errConfigNoApp) }

// parseAppFromConfigJSON parses apps[0].appId/appSecret out of lark-cli's
// config.json bytes. Pure (no I/O) so it is unit-tested in isolation. Both fields
// are required; a present-but-incomplete apps[0] is errConfigNoApp ("not built
// yet"), distinct from an unparseable file (a real error).
func parseAppFromConfigJSON(raw []byte) (appID, appSecret string, err error) {
	var cfg larkConfigJSON
	if jerr := json.Unmarshal(raw, &cfg); jerr != nil {
		return "", "", fmt.Errorf("feishu: parse config.json: %w", jerr)
	}
	if len(cfg.Apps) == 0 {
		return "", "", fmt.Errorf("%w (no apps)", errConfigNoApp)
	}
	app := cfg.Apps[0]
	if app.AppID == "" {
		return "", "", fmt.Errorf("%w (apps[0] has no appId)", errConfigNoApp)
	}
	if app.AppSecret == "" {
		return "", "", fmt.Errorf("%w (apps[0] has no appSecret)", errConfigNoApp)
	}
	return app.AppID, app.AppSecret, nil
}

// --- URL parsing (pure helper) ----------------------------------------------

// feishuURLPrefix is the scheme+host prefix every 飞书 link lark-cli prints starts
// with. We match an https://...feishu.cn/... URL on a line so the SAME parser handles
// both config-init's page URL (open.feishu.cn/page/cli?...) and auth-login's
// verification URL (open.feishu.cn/suite/passport/oauth/device?... or similar) —
// only links to the 飞书 domain are accepted so we never grab some unrelated URL.
const feishuURLPrefix = "https://"
const feishuURLHost = "feishu.cn"

// parseFeishuURL extracts the first feishu.cn https URL from lark-cli's stdout. Real
// config-init output:
//
//	打开以下链接配置应用:
//	  https://open.feishu.cn/page/cli?user_code=2AF7-MFWU&lpv=1.0.56&from=cli
//	等待配置应用...
//
// Real auth-login output prints the verification link the same way (a feishu.cn URL
// on its own indented line).
func parseFeishuURL(cliOutput string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(cliOutput))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		idx := strings.Index(line, feishuURLPrefix)
		if idx < 0 {
			continue
		}
		candidate := line[idx:]
		// Accept only a 飞书-domain URL (guard against grabbing an unrelated https link).
		if strings.Contains(candidate, feishuURLHost) {
			return candidate, nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("feishu: scan cli output: %w", err)
	}
	return "", errors.New("feishu: URL not found in lark-cli output")
}

// compile-time guard: LarkCLIRunner satisfies cliRunner.
var _ cliRunner = (*LarkCLIRunner)(nil)
