package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"numind-server/internal/pkg/log"
)

// ─── CronRunner constants ────────────────────────────────────────────────────

// Default cron expressions (Asia/Shanghai timezone). 6-field syntax (with
// seconds) per robfig/cron WithSeconds option.
const (
	DefaultDailyCron     = "0 0 4 * * *"         // 04:00 every day
	DefaultWeeklyCron    = "0 30 4 * * 1"        // 04:30 every Monday (ISO week Mon=1)
	DefaultMonthlyCron   = "0 30 4 1 * *"        // 04:30 on the 1st of every month
	DefaultQuarterlyCron = "0 30 4 1 1,4,7,10 *" // 04:30 on Jan/Apr/Jul/Oct 1st
)

// ─── CronRunner ──────────────────────────────────────────────────────────────

// CronRunner schedules and supervises the 4 DigestCron jobs (daily / weekly /
// monthly / quarterly) using robfig/cron/v3.
//
// Lifecycle:
//
//	runner := NewCronRunner(digestCron, CronRunnerConfig{Enabled: true, ...})
//	runner.Start()       // attaches jobs to cron + starts ticker
//	defer runner.Stop()  // graceful drain (waits for in-flight jobs)
//
// When Enabled=false, NewCronRunner returns a runner whose Start/Stop are
// no-ops — useful for local dev that doesn't want digest jobs firing.
type CronRunner struct {
	c         *cron.Cron
	digest    DigestCron
	cfg       CronRunnerConfig
	startOnce sync.Once
	stopOnce  sync.Once
}

// CronRunnerConfig holds the cron-schedule tuning knobs.
type CronRunnerConfig struct {
	Enabled       bool           // master switch (false = no jobs registered, no scheduler)
	Timezone      *time.Location // typically Asia/Shanghai
	DailyCron     string         // robfig/cron 6-field "s m h d M w"
	WeeklyCron    string
	MonthlyCron   string
	QuarterlyCron string
}

// DefaultCronRunnerConfig returns the spec-defined defaults (enabled by default,
// Asia/Shanghai timezone, spec cron expressions).
func DefaultCronRunnerConfig() CronRunnerConfig {
	return CronRunnerConfig{
		Enabled:       true,
		Timezone:      shanghaiLoc,
		DailyCron:     DefaultDailyCron,
		WeeklyCron:    DefaultWeeklyCron,
		MonthlyCron:   DefaultMonthlyCron,
		QuarterlyCron: DefaultQuarterlyCron,
	}
}

// cronRunnerViperGetter is the minimal viper surface for config loading.
type cronRunnerViperGetter interface {
	GetBool(key string) bool
	GetString(key string) string
}

// LoadCronRunnerConfigFromViper reads the runner config from viper.
//
// Config layout (config_*.yaml):
//
//	agent:
//	  memory:
//	    digest:
//	      enabled: true
//	      timezone: "Asia/Shanghai"
//	      daily_cron: "0 0 4 * * *"
//	      weekly_cron: "0 30 4 * * 1"
//	      monthly_cron: "0 30 4 1 * *"
//	      quarterly_cron: "0 30 4 1 1,4,7,10 *"
//
// Defaults: enabled=true, timezone=Asia/Shanghai, spec cron exprs.
func LoadCronRunnerConfigFromViper(v cronRunnerViperGetter) CronRunnerConfig {
	cfg := DefaultCronRunnerConfig()
	// Viper's GetBool returns false for unset keys; only treat explicit "false"
	// as opt-out — caller wires SetDefault("...digest.enabled", true) before
	// LoadCronRunnerConfigFromViper to make defaults discoverable.
	cfg.Enabled = v.GetBool("agent.memory.digest.enabled")
	if tz := v.GetString("agent.memory.digest.timezone"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			cfg.Timezone = loc
		} else {
			log.Warnw("memory.digest cron: invalid timezone; falling back to Asia/Shanghai",
				"timezone", tz, "error", err)
		}
	}
	if s := v.GetString("agent.memory.digest.daily_cron"); s != "" {
		cfg.DailyCron = s
	}
	if s := v.GetString("agent.memory.digest.weekly_cron"); s != "" {
		cfg.WeeklyCron = s
	}
	if s := v.GetString("agent.memory.digest.monthly_cron"); s != "" {
		cfg.MonthlyCron = s
	}
	if s := v.GetString("agent.memory.digest.quarterly_cron"); s != "" {
		cfg.QuarterlyCron = s
	}
	return cfg
}

// NewCronRunner constructs a CronRunner. digest is required; nil → panic.
func NewCronRunner(digest DigestCron, cfg CronRunnerConfig) *CronRunner {
	if digest == nil {
		panic("memory.NewCronRunner: digest must be non-nil")
	}
	if cfg.Timezone == nil {
		cfg.Timezone = shanghaiLoc
	}
	if cfg.DailyCron == "" {
		cfg.DailyCron = DefaultDailyCron
	}
	if cfg.WeeklyCron == "" {
		cfg.WeeklyCron = DefaultWeeklyCron
	}
	if cfg.MonthlyCron == "" {
		cfg.MonthlyCron = DefaultMonthlyCron
	}
	if cfg.QuarterlyCron == "" {
		cfg.QuarterlyCron = DefaultQuarterlyCron
	}
	// robfig/cron 6-field schedule parser (with seconds).
	c := cron.New(
		cron.WithLocation(cfg.Timezone),
		cron.WithSeconds(),
	)
	return &CronRunner{
		c:      c,
		digest: digest,
		cfg:    cfg,
	}
}

// Start registers the 4 jobs and starts the ticker. Idempotent — multiple
// calls only attach jobs once.
//
// When cfg.Enabled is false, Start returns immediately without scheduling
// anything (still safe to call Stop later).
func (r *CronRunner) Start() error {
	var registerErr error
	r.startOnce.Do(func() {
		if !r.cfg.Enabled {
			log.Infow("memory.digest cron disabled via config — no jobs registered")
			return
		}
		// daily
		if _, err := r.c.AddFunc(r.cfg.DailyCron, func() {
			r.runJob("daily", r.digest.RunDailyDigest)
		}); err != nil {
			registerErr = fmt.Errorf("memory.digest cron register daily: %w", err)
			return
		}
		// weekly
		if _, err := r.c.AddFunc(r.cfg.WeeklyCron, func() {
			r.runJob("weekly", r.digest.RunWeeklyDigest)
		}); err != nil {
			registerErr = fmt.Errorf("memory.digest cron register weekly: %w", err)
			return
		}
		// monthly
		if _, err := r.c.AddFunc(r.cfg.MonthlyCron, func() {
			r.runJob("monthly", r.digest.RunMonthlyDigest)
		}); err != nil {
			registerErr = fmt.Errorf("memory.digest cron register monthly: %w", err)
			return
		}
		// quarterly
		if _, err := r.c.AddFunc(r.cfg.QuarterlyCron, func() {
			r.runJob("quarterly", r.digest.RunQuarterlyDigest)
		}); err != nil {
			registerErr = fmt.Errorf("memory.digest cron register quarterly: %w", err)
			return
		}
		r.c.Start()
		log.Infow("memory.digest cron started",
			"timezone", r.cfg.Timezone.String(),
			"daily", r.cfg.DailyCron,
			"weekly", r.cfg.WeeklyCron,
			"monthly", r.cfg.MonthlyCron,
			"quarterly", r.cfg.QuarterlyCron,
		)
	})
	return registerErr
}

// Stop gracefully halts the scheduler, waiting for any in-flight job to finish.
// Idempotent — additional calls are no-ops.
func (r *CronRunner) Stop() {
	r.stopOnce.Do(func() {
		if !r.cfg.Enabled {
			return
		}
		ctx := r.c.Stop()
		<-ctx.Done()
		log.Infow("memory.digest cron stopped")
	})
}

// runJob wraps a DigestCron.Run* invocation with:
//   - background context (cron has no caller deadline)
//   - panic-recover (one bad cron tick must not kill the scheduler)
//   - error log (per-job errors are already counted via metrics inside the cron)
func (r *CronRunner) runJob(name string, runFn func(ctx context.Context, runDate time.Time) error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Errorw("memory.digest cron job panic recovered",
				"job", name, "panic", fmt.Sprintf("%v", rec))
		}
	}()
	ctx := context.Background()
	now := time.Now()
	log.Infow("memory.digest cron tick", "job", name, "started_at", now.Format(time.RFC3339))
	if err := runFn(ctx, now); err != nil {
		log.Errorw("memory.digest cron job error", "job", name, "error", err)
	}
}
