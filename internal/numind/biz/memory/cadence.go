package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
)

// Cadence defaults (spec §关键参数). Centralised so trivial_test + cadence_test
// can compare against the same source-of-truth values, and so config loaders
// can fall back to safe defaults when keys are unset.
const (
	// DefaultDialecticCooldown is the minimum gap between dialectic runs for
	// the same user. Spec §设计要点 "5min cooldown".
	DefaultDialecticCooldown = 5 * time.Minute

	// DefaultDialecticMaxCooldown is the upper bound — even with no new
	// facts, re-run after this window to prevent insight staleness.
	// Spec §设计要点 "30min 兜底重跑".
	DefaultDialecticMaxCooldown = 30 * time.Minute

	// DefaultDialecticMinNewFacts is the new-fact-delta threshold that
	// triggers dialectic re-run inside the cooldown→max-cooldown window.
	// Spec §设计要点 "3 new facts 触发".
	DefaultDialecticMinNewFacts = 3
)

// CadenceConfig holds the cadence tuning knobs. Loaded from viper via
// LoadCadenceConfigFromViper; tests can construct it directly to exercise
// boundaries without touching the global viper state.
type CadenceConfig struct {
	DialecticCooldown    time.Duration
	DialecticMaxCooldown time.Duration
	DialecticMinNewFacts int
}

// DefaultCadenceConfig returns the spec-defined defaults.
func DefaultCadenceConfig() CadenceConfig {
	return CadenceConfig{
		DialecticCooldown:    DefaultDialecticCooldown,
		DialecticMaxCooldown: DefaultDialecticMaxCooldown,
		DialecticMinNewFacts: DefaultDialecticMinNewFacts,
	}
}

// LoadCadenceConfigFromViper reads the cadence config from viper, falling
// back to DefaultCadenceConfig values for any unset key.
//
// Config layout (config_*.yaml):
//
//	agent:
//	  memory:
//	    dialectic_cooldown_seconds: 300
//	    dialectic_max_cooldown_seconds: 1800
//	    dialectic_min_new_facts: 3
func LoadCadenceConfigFromViper(v viperGetter) CadenceConfig {
	cfg := DefaultCadenceConfig()
	if s := v.GetInt("agent.memory.dialectic_cooldown_seconds"); s > 0 {
		cfg.DialecticCooldown = time.Duration(s) * time.Second
	}
	if s := v.GetInt("agent.memory.dialectic_max_cooldown_seconds"); s > 0 {
		cfg.DialecticMaxCooldown = time.Duration(s) * time.Second
	}
	if s := v.GetInt("agent.memory.dialectic_min_new_facts"); s > 0 {
		cfg.DialecticMinNewFacts = s
	}
	return cfg
}

// viperGetter is the narrow surface of *viper.Viper / viper package globals
// we actually need. Decouples LoadCadenceConfigFromViper from the concrete
// viper import for easier testing.
type viperGetter interface {
	GetInt(key string) int
}

// CadenceService gates expensive Layer A dialectic runs to a configurable
// cadence. The decision is read-only over user_memory_profile (no writes —
// only the dialectic service writes cached_insight* fields).
//
// Layer A only: ShouldRunDialectic operates per user_id. B2B2C parent/child
// isolation (D7) is preserved because every call accepts a single userID
// and the underlying store keys on user_id only.
type CadenceService struct {
	profileStore store.IUserMemoryProfileStore
	cfg          CadenceConfig
	now          func() time.Time // injectable for tests
}

// NewCadenceService constructs a CadenceService. profileStore is required.
// Pass DefaultCadenceConfig() in production code.
func NewCadenceService(profileStore store.IUserMemoryProfileStore, cfg CadenceConfig) *CadenceService {
	return &CadenceService{
		profileStore: profileStore,
		cfg:          cfg,
		now:          time.Now,
	}
}

// withClock returns a copy of s with the clock swapped — INTERNAL TEST USE
// ONLY. Production code should use NewCadenceService which wires time.Now.
//
//nolint:unused // exercised by cadence_test.go via the unexported alias below
func (s *CadenceService) withClock(now func() time.Time) *CadenceService {
	clone := *s
	clone.now = now
	return &clone
}

// ShouldRunDialectic reports whether the caller should invoke the dialectic
// LLM pipeline now. Decision tree (spec §设计要点):
//
//  1. profile == nil OR CachedInsightAt == nil → true (first-time user)
//  2. now - CachedInsightAt < DialecticCooldown → false (cool-down active)
//  3. (TotalFacts - CachedInsightFactCount) >= DialecticMinNewFacts → true
//  4. now - CachedInsightAt >= DialecticMaxCooldown → true (anti-staleness)
//  5. else → false
//
// Errors are non-fatal — store lookup failures default to "skip" (return
// false) and log a warning. The dialectic gate is opportunistic; missing a
// re-run is preferable to crashing the agent runner on transient DB issues.
//
// Layer A invariant: never queries via parent_user_id. D7 (B2B2C isolation)
// is enforced upstream by IUserMemoryProfileStore.Get(userID).
func (s *CadenceService) ShouldRunDialectic(ctx context.Context, userID uint) (bool, error) {
	if s == nil || s.profileStore == nil {
		// Fail-open is wrong here: a nil service shouldn't be running
		// dialectic by default. Return false + a sentinel error so the
		// caller can decide (most callers will log + skip).
		return false, fmt.Errorf("memory.CadenceService.ShouldRunDialectic: nil receiver or nil store")
	}

	profile, err := s.profileStore.Get(ctx, userID)
	if err != nil {
		// Not-found = first-time user → run dialectic.
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, nil
		}
		// Other errors: log + skip. Dialectic is best-effort; we don't
		// want to surface a DB hiccup to the user as a chat failure.
		log.Warnw("memory.CadenceService.ShouldRunDialectic: profile lookup failed",
			"user_id", userID, "error", err)
		return false, fmt.Errorf("memory.CadenceService.ShouldRunDialectic: profile lookup: %w", err)
	}

	// Profile exists but no cached insight yet → run.
	if profile == nil || profile.CachedInsightAt == nil {
		return true, nil
	}

	sinceLast := s.now().Sub(*profile.CachedInsightAt)

	// Within hard cool-down → skip regardless of new-fact delta.
	// Boundary: spec table case "ExactlyAtCooldown" expects false; using `<`
	// (strict) here means sinceLast == cooldown ⇒ falls through to step 3+,
	// but step 3 requires newFacts >= min and step 4 requires sinceLast >=
	// max — at exactly cooldown with newFacts=0 the function returns false.
	if sinceLast < s.cfg.DialecticCooldown {
		return false, nil
	}

	newFacts := profile.TotalFacts - profile.CachedInsightFactCount
	if newFacts >= s.cfg.DialecticMinNewFacts {
		return true, nil
	}

	if sinceLast >= s.cfg.DialecticMaxCooldown {
		return true, nil
	}

	return false, nil
}
