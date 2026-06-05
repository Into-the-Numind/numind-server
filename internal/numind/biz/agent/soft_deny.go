package agent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"numind-server/internal/pkg/log"
)

// SoftDenyConfig holds the soft-interception tunables (agent-security-hardening).
//
// Soft interception turns a permission-gate Deny into a single blocked tool call
// (a tool-result message fed back to the LLM) instead of terminating the whole run.
// The three thresholds form the anti-loop guard:
//   - MaxSame:     consecutive denials of the SAME (tool+input) fingerprint → trip.
//   - MaxTotal:    consecutive denials of ANY fingerprint (reset on success) → trip.
//   - MaxLifetime: per-fingerprint denials over the WHOLE run (never reset by
//     success) → trip. Closes the "interpose one benign success between each blocked
//     attempt" bypass (S2 review R2-B).
//
// Enabled=false reverts to the legacy hard-terminate behavior (safety valve).
type SoftDenyConfig struct {
	Enabled     bool
	MaxSame     int
	MaxTotal    int
	MaxLifetime int
}

// defaultSoftDenyConfig is the safe default: soft interception ON with sane thresholds.
// This makes a missing biz.go wire still soft-intercept (R2-E zero-config safe default).
var (
	softDenyCfgMu sync.RWMutex
	softDenyCfg   = SoftDenyConfig{Enabled: true, MaxSame: 3, MaxTotal: 5, MaxLifetime: 10}
)

// SetSoftDenyConfig stores the process-wide soft-deny config (wired once from viper in
// biz.go). When the config disables soft interception, it loud-warns once here (rather
// than per-run) so the unsafe legacy-terminate mode is observable in logs.
func SetSoftDenyConfig(cfg SoftDenyConfig) {
	if cfg.MaxSame <= 0 {
		cfg.MaxSame = 3
	}
	if cfg.MaxTotal <= 0 {
		cfg.MaxTotal = 5
	}
	if cfg.MaxLifetime <= 0 {
		cfg.MaxLifetime = 10
	}
	if !cfg.Enabled {
		log.Warnw("SoftDenyController: soft interception DISABLED (agent.permission.soft_deny.enabled=false) — permission denials will HARD-terminate the run (legacy behavior). Intended only for controlled debugging.")
	}
	softDenyCfgMu.Lock()
	defer softDenyCfgMu.Unlock()
	softDenyCfg = cfg
}

// CurrentSoftDenyConfig returns the process-wide soft-deny config (runner reads this
// per-run to construct a fresh per-run controller).
func CurrentSoftDenyConfig() SoftDenyConfig {
	softDenyCfgMu.RLock()
	defer softDenyCfgMu.RUnlock()
	return softDenyCfg
}

// SoftDenyController is the per-run state for soft interception + the anti-loop guard.
// One instance per run, injected into ctx via WithSoftDenyController. All methods are
// safe for the single-runner-goroutine model; the mutex guards against any future
// concurrent tool execution.
type SoftDenyController struct {
	mu          sync.Mutex
	enabled     bool
	maxSame     int
	maxTotal    int
	maxLifetime int

	pending      *PermissionDenialDetail // reason for the in-flight denied call (set by the deny hook)
	consecutive  int                     // consecutive denials (any fp); reset on success
	lastFP       string                  // fingerprint of the previous denial
	sameStreak   int                     // consecutive denials of lastFP; reset on success
	lifetimeByFP map[string]int          // per-fp denials over the whole run; NEVER reset
}

// NewSoftDenyController builds a per-run controller from cfg.
//
// Note: the "enabled=false" loud-warn lives in SetSoftDenyConfig (fired once at
// process wire-up) rather than here, to avoid one warn per run. Callers that build a
// disabled controller directly (e.g. tests) deliberately get no warn.
func NewSoftDenyController(cfg SoftDenyConfig) *SoftDenyController {
	maxSame, maxTotal, maxLifetime := cfg.MaxSame, cfg.MaxTotal, cfg.MaxLifetime
	if maxSame <= 0 {
		maxSame = 3
	}
	if maxTotal <= 0 {
		maxTotal = 5
	}
	if maxLifetime <= 0 {
		maxLifetime = 10
	}
	return &SoftDenyController{
		enabled:      cfg.Enabled,
		maxSame:      maxSame,
		maxTotal:     maxTotal,
		maxLifetime:  maxLifetime,
		lifetimeByFP: make(map[string]int),
	}
}

// Enabled reports whether soft interception is active. When false, the adapter falls
// back to the legacy hard-terminate path.
func (c *SoftDenyController) Enabled() bool {
	if c == nil {
		return false
	}
	return c.enabled
}

// SetPending records the denial reason for the in-flight tool call. Called by the
// permission/compliance deny hook (which holds the PermissionDenialDetail) so the
// adapter's Resolve can surface a meaningful reason to the LLM. Safe with nil detail.
func (c *SoftDenyController) SetPending(detail *PermissionDenialDetail) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = detail
}

// Resolve accounts for one denial of (toolName,input) and decides whether the anti-loop
// guard trips. Returns:
//   - tripped=false: soft path — msg is the LLM-facing tool-result to feed back so the
//     ReAct loop continues.
//   - tripped=true:  the run should hard-terminate (TerminalPermissionDenied); the
//     caller falls through to the legacy error return.
//
// It consumes the pending reason (SetPending) so a stale reason never leaks to a later call.
func (c *SoftDenyController) Resolve(toolName, input string) (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	fp := softDenyFingerprint(toolName, input)
	c.consecutive++
	if fp == c.lastFP {
		c.sameStreak++
	} else {
		c.sameStreak = 1
		c.lastFP = fp
	}
	if c.lifetimeByFP == nil {
		c.lifetimeByFP = make(map[string]int)
	}
	c.lifetimeByFP[fp]++

	reason := ""
	if c.pending != nil {
		reason = c.pending.Message
	}
	c.pending = nil // consume

	tripped := c.sameStreak >= c.maxSame ||
		c.consecutive >= c.maxTotal ||
		c.lifetimeByFP[fp] >= c.maxLifetime

	return tripped, softDenyToolResult(reason, c.sameStreak)
}

// OnSuccess marks a successful (non-denied) tool execution: the agent made progress, so
// the consecutive and same-streak counters reset. The per-fingerprint LIFETIME counter
// is intentionally NOT reset (R2-B: prevents success-interposition from evading the cap).
func (c *SoftDenyController) OnSuccess() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consecutive = 0
	c.sameStreak = 0
	// lastFP is intentionally NOT cleared: the next Resolve for the same fp increments
	// sameStreak from 0→1 (fp==lastFP branch), correctly restarting the streak at 1.
}

// softDenyFingerprint identifies a (tool, input) pair for anti-loop counting.
//
// SHA-1 is used for deduplication only, never as a security boundary. Inputs originate
// from the platform's own LLM (not an external adversary), so theoretical collisions are
// not exploitable here. //nolint:gosec // G401: non-cryptographic dedup hash.
func softDenyFingerprint(toolName, input string) string {
	h := sha1.Sum([]byte(input))
	return toolName + ":" + hex.EncodeToString(h[:])
}

// softDenyToolResult formats the model-facing tool-result for a soft-denied call. It
// carries the concrete reason and escalates the wording on repeated same-fingerprint
// attempts so the LLM is told, in increasingly firm terms, to stop retrying.
func softDenyToolResult(reason string, sameStreak int) string {
	var b strings.Builder
	b.WriteString("⚠️ 该工具调用被平台安全策略拦截，未执行。")
	if strings.TrimSpace(reason) != "" {
		b.WriteString("\n原因：")
		b.WriteString(reason)
	}
	if sameStreak >= 2 {
		b.WriteString(fmt.Sprintf("\n你已多次尝试此被禁操作（已拦截 %d 次），请立即停止重试。", sameStreak))
	}
	b.WriteString("\n请不要以相同或同类方式重试此操作。你可以：换一种不触发安全策略的方式完成任务，或向用户说明该操作受限。")
	return b.String()
}

// --- ctx helpers (mirror permission_sink.go) ---

type softDenyControllerKey struct{}

// WithSoftDenyController stores the per-run controller in ctx; runner.Run / RunStream
// inject it alongside WithPermissionSink.
func WithSoftDenyController(ctx context.Context, c *SoftDenyController) context.Context {
	return context.WithValue(ctx, softDenyControllerKey{}, c)
}

// SoftDenyFromCtx retrieves the controller; nil means soft interception is not wired for
// this run (adapter falls back to legacy hard-terminate).
func SoftDenyFromCtx(ctx context.Context) *SoftDenyController {
	c, _ := ctx.Value(softDenyControllerKey{}).(*SoftDenyController)
	return c
}
