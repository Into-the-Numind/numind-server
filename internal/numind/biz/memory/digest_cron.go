package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/log"
	"numind-server/internal/pkg/metrics"
)

// ─── Tuning constants ────────────────────────────────────────────────────────

// DefaultDigestWorkerConcurrency is the per-cron-run goroutine pool size.
// 20 workers × ~5s avg LLM RTT → ~5500 users / hour processed.
// (Spec §R3 estimates 17 min for 10K active users.)
const DefaultDigestWorkerConcurrency = 20

// DefaultDigestLockTTL is the Redis SETNX lock TTL — must exceed worst-case
// cron run duration. Spec defaults to 1h; large user bases may need to bump.
const DefaultDigestLockTTL = time.Hour

// DefaultDigestPerUserTimeout is the wall-time budget for one user's digest
// generate + upsert. Caps run-away LLM calls + DB hiccups so the worker pool
// doesn't stall the whole cron.
const DefaultDigestPerUserTimeout = 2 * time.Minute

// digestLockKeyPrefix is the Redis key prefix for cron locks.
const digestLockKeyPrefix = "digest_cron:"

// ─── DigestCron interface + config ───────────────────────────────────────────

// DigestCron is the 4-granularity cron-driven digest pipeline.
//
// Each Run*Digest method:
//  1. Acquires a Redis SETNX lock keyed (granularity, period) — early-exits
//     with nil when another instance holds it (multi-instance deploy safety).
//  2. Calls digestStore.GetUsersActive* to enumerate users with activity in
//     the target window — empty active list = no LLM calls (saves cost).
//  3. Distributes per-user generate+upsert across a worker pool
//     (DefaultDigestWorkerConcurrency goroutines).
//  4. Per-user failures (LLM err / DB err / panic) log+counter+continue; the
//     whole-cron return value reflects the worst-case error class only when
//     ALL users fail (otherwise nil).
//
// Layer A only: every digest is per-user, never aggregated across parent/child.
// D7 isolation is preserved by digestStore — see memory_digest_store.go.
type DigestCron interface {
	// RunDailyDigest aggregates the day immediately preceding runDate (in
	// Asia/Shanghai). runDate is typically time.Now() from the cron scheduler.
	RunDailyDigest(ctx context.Context, runDate time.Time) error
	// RunWeeklyDigest aggregates the ISO week immediately preceding the one
	// containing runDate (Monday-Sunday in Asia/Shanghai).
	RunWeeklyDigest(ctx context.Context, runDate time.Time) error
	// RunMonthlyDigest aggregates the calendar month immediately preceding the
	// one containing runDate (Asia/Shanghai).
	RunMonthlyDigest(ctx context.Context, runDate time.Time) error
	// RunQuarterlyDigest aggregates the calendar quarter immediately preceding
	// the one containing runDate (Q1=Jan-Mar, etc., Asia/Shanghai).
	RunQuarterlyDigest(ctx context.Context, runDate time.Time) error
}

// DigestCronConfig holds the cron tuning knobs.
type DigestCronConfig struct {
	WorkerConcurrency int
	LockTTL           time.Duration
	PerUserTimeout    time.Duration
}

// DefaultDigestCronConfig returns the spec-defined defaults.
func DefaultDigestCronConfig() DigestCronConfig {
	return DigestCronConfig{
		WorkerConcurrency: DefaultDigestWorkerConcurrency,
		LockTTL:           DefaultDigestLockTTL,
		PerUserTimeout:    DefaultDigestPerUserTimeout,
	}
}

// digestCronViperGetter is the narrow viper surface we need.
type digestCronViperGetter interface {
	GetInt(key string) int
}

// LoadDigestCronConfigFromViper reads the digest cron config from viper.
//
// Config layout (config_*.yaml):
//
//	agent:
//	  memory:
//	    digest:
//	      worker_concurrency: 20
//	      redis_lock_ttl_seconds: 3600
//	      per_user_timeout_seconds: 120
func LoadDigestCronConfigFromViper(v digestCronViperGetter) DigestCronConfig {
	cfg := DefaultDigestCronConfig()
	if n := v.GetInt("agent.memory.digest.worker_concurrency"); n > 0 {
		cfg.WorkerConcurrency = n
	}
	if s := v.GetInt("agent.memory.digest.redis_lock_ttl_seconds"); s > 0 {
		cfg.LockTTL = time.Duration(s) * time.Second
	}
	if s := v.GetInt("agent.memory.digest.per_user_timeout_seconds"); s > 0 {
		cfg.PerUserTimeout = time.Duration(s) * time.Second
	}
	return cfg
}

// ─── digestCron implementation ───────────────────────────────────────────────

// redisClient is the minimal Redis surface we use (SetNX + Del + Eval).
// Decoupled from *redis.Client so tests can use miniredis or a stub.
//
// Eval is required for the compare-and-delete (CAS) lock release path —
// see releaseLockCASScript and acquireLock for why a plain Del is unsafe
// when the lock TTL has expired and another instance has acquired the lock.
type redisClient interface {
	SetNX(ctx context.Context, key string, value any, ttl time.Duration) *redis.BoolCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd
}

// releaseLockCASScript is a Lua compare-and-delete: only delete the key when
// its current value matches the caller's expected instance ID. Returns 1 on
// successful delete, 0 when the key has a different owner (or has expired).
//
// Without this CAS check, an unlucky timing window can occur:
//  1. Instance A acquires lock, but its run runs longer than LockTTL.
//  2. Lock expires → instance B acquires it (same key, new value).
//  3. Instance A finishes and calls Del → would delete B's lock.
//
// The CAS check eliminates that race. The TTL still serves as the safety net
// for crash / panic paths that bypass the release defer.
const releaseLockCASScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
`

// digestCron is the production DigestCron implementation.
type digestCron struct {
	digestStore store.IMemoryDigestStore
	generator   DigestGenerator
	rdb         redisClient // may be nil — lock falls back to "no lock" mode
	cfg         DigestCronConfig

	// instanceID is a unique-per-process identifier embedded into the Redis
	// lock value so multi-instance deploys can audit which instance held the
	// lock. Generated at construction (e.g. hostname + pid).
	instanceID string
}

// DigestCronOption configures a digestCron at construction.
type DigestCronOption func(*digestCron)

// WithDigestCronRedisClient injects a Redis client for SETNX-based locking.
// nil = "no lock" mode (single-instance deploys, tests).
func WithDigestCronRedisClient(c redisClient) DigestCronOption {
	return func(d *digestCron) { d.rdb = c }
}

// WithDigestCronInstanceID sets the lock-owner identifier (default: pid-based).
func WithDigestCronInstanceID(id string) DigestCronOption {
	return func(d *digestCron) { d.instanceID = id }
}

// NewDigestCron constructs a DigestCron. digestStore + generator are required.
// rdb may be nil (single-instance deploys); WithDigestCronRedisClient injects it.
func NewDigestCron(
	digestStore store.IMemoryDigestStore,
	generator DigestGenerator,
	cfg DigestCronConfig,
	opts ...DigestCronOption,
) DigestCron {
	if digestStore == nil {
		panic("memory.NewDigestCron: digestStore must be non-nil")
	}
	if generator == nil {
		panic("memory.NewDigestCron: generator must be non-nil")
	}
	if cfg.WorkerConcurrency <= 0 {
		cfg.WorkerConcurrency = DefaultDigestWorkerConcurrency
	}
	if cfg.LockTTL <= 0 {
		cfg.LockTTL = DefaultDigestLockTTL
	}
	if cfg.PerUserTimeout <= 0 {
		cfg.PerUserTimeout = DefaultDigestPerUserTimeout
	}
	c := &digestCron{
		digestStore: digestStore,
		generator:   generator,
		cfg:         cfg,
		instanceID:  defaultInstanceID(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// defaultInstanceID returns a process-unique identifier. Uses pid + a single
// nanosecond timestamp (avoids dependency on os.Hostname which can fail in
// containers). The pid+timestamp pair is unique across container restarts and
// across replicas on the same host.
func defaultInstanceID() string {
	return fmt.Sprintf("pid-%d-%d", os.Getpid(), time.Now().UnixNano())
}

// ─── RunDailyDigest ──────────────────────────────────────────────────────────

func (c *digestCron) RunDailyDigest(ctx context.Context, runDate time.Time) error {
	// Target date = yesterday in Asia/Shanghai.
	now := runDate.In(shanghaiLoc)
	yesterday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, shanghaiLoc).
		AddDate(0, 0, -1)
	dateKey := yesterday.Format("2006-01-02")
	lockKey := digestLockKeyPrefix + "daily:" + dateKey

	return c.runOne(
		ctx,
		"daily",
		lockKey,
		func(ctx context.Context) ([]uint, error) {
			return c.digestStore.GetUsersActiveOn(ctx, yesterday)
		},
		func(ctx context.Context, userID uint) error {
			digest, err := c.generator.GenerateDaily(ctx, userID, yesterday)
			if err != nil {
				return err
			}
			return c.digestStore.UpsertDaily(ctx, digest)
		},
	)
}

// ─── RunWeeklyDigest ─────────────────────────────────────────────────────────

func (c *digestCron) RunWeeklyDigest(ctx context.Context, runDate time.Time) error {
	// Target ISO week = the one ending the day before runDate's week.
	// Operationally: cron runs Monday → aggregate Mon-Sun of the previous week.
	now := runDate.In(shanghaiLoc)
	thisMonday := mondayOfWeek(now)
	lastMonday := thisMonday.AddDate(0, 0, -7)
	isoYear, isoWeek := lastMonday.ISOWeek()
	periodKey := fmt.Sprintf("%d-W%02d", isoYear, isoWeek)
	lockKey := digestLockKeyPrefix + "weekly:" + periodKey

	// Active-user enumeration window: last Monday 00:00 to this Monday 00:00.
	from := lastMonday
	to := thisMonday

	return c.runOne(
		ctx,
		"weekly",
		lockKey,
		func(ctx context.Context) ([]uint, error) {
			return c.digestStore.GetUsersActiveInRange(ctx, from, to)
		},
		func(ctx context.Context, userID uint) error {
			digest, err := c.generator.GenerateWeekly(ctx, userID, isoYear, isoWeek)
			if err != nil {
				return err
			}
			return c.digestStore.UpsertWeekly(ctx, digest)
		},
	)
}

// ─── RunMonthlyDigest ────────────────────────────────────────────────────────

func (c *digestCron) RunMonthlyDigest(ctx context.Context, runDate time.Time) error {
	// Target month = the one before runDate's month.
	now := runDate.In(shanghaiLoc)
	thisMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, shanghaiLoc)
	lastMonthStart := thisMonthStart.AddDate(0, -1, 0)
	year, month := lastMonthStart.Year(), int(lastMonthStart.Month())
	periodKey := fmt.Sprintf("%d-%02d", year, month)
	lockKey := digestLockKeyPrefix + "monthly:" + periodKey

	from := lastMonthStart
	to := thisMonthStart

	return c.runOne(
		ctx,
		"monthly",
		lockKey,
		func(ctx context.Context) ([]uint, error) {
			return c.digestStore.GetUsersActiveInRange(ctx, from, to)
		},
		func(ctx context.Context, userID uint) error {
			digest, err := c.generator.GenerateMonthly(ctx, userID, year, month)
			if err != nil {
				return err
			}
			return c.digestStore.UpsertMonthly(ctx, digest)
		},
	)
}

// ─── RunQuarterlyDigest ──────────────────────────────────────────────────────

func (c *digestCron) RunQuarterlyDigest(ctx context.Context, runDate time.Time) error {
	now := runDate.In(shanghaiLoc)
	curYear, curQ := quarterOf(now)
	targetY, targetQ := curYear, curQ-1
	if targetQ < 1 {
		targetQ = 4
		targetY--
	}
	firstMonth := (targetQ-1)*3 + 1
	quarterStart := time.Date(targetY, time.Month(firstMonth), 1, 0, 0, 0, 0, shanghaiLoc)
	nextQuarterStart := quarterStart.AddDate(0, 3, 0)
	periodKey := fmt.Sprintf("%dQ%d", targetY, targetQ)
	lockKey := digestLockKeyPrefix + "quarterly:" + periodKey

	from := quarterStart
	to := nextQuarterStart

	return c.runOne(
		ctx,
		"quarterly",
		lockKey,
		func(ctx context.Context) ([]uint, error) {
			return c.digestStore.GetUsersActiveInRange(ctx, from, to)
		},
		func(ctx context.Context, userID uint) error {
			digest, err := c.generator.GenerateQuarterly(ctx, userID, targetY, targetQ)
			if err != nil {
				return err
			}
			return c.digestStore.UpsertQuarterly(ctx, digest)
		},
	)
}

// ─── runOne (lock + enumerate + worker pool) ─────────────────────────────────

// runOne is the shared cron skeleton. Takes:
//
//	gran     - granularity label for metrics + logging
//	lockKey  - Redis lock key (period-specific)
//	listFn   - returns active user IDs
//	processFn - per-user generate + upsert
//
// Acquires Redis SETNX lock → enumerates → spawns worker pool → drains →
// releases lock. Failures inside processFn are per-user; the overall return
// reflects whether the cron *itself* failed (lock acquisition err / list err).
func (c *digestCron) runOne(
	ctx context.Context,
	gran string,
	lockKey string,
	listFn func(ctx context.Context) ([]uint, error),
	processFn func(ctx context.Context, userID uint) error,
) error {
	metrics.MemoryDigestJobRunInc(gran)

	// Lock acquisition (best-effort — nil client = single-instance mode).
	acquired, releaseLock := c.acquireLock(ctx, lockKey)
	if !acquired {
		log.Infow("memory.digest cron lock held; skipping",
			"granularity", gran, "lock_key", lockKey)
		return nil
	}
	defer releaseLock()

	// Enumerate active users.
	userIDs, err := listFn(ctx)
	if err != nil {
		metrics.MemoryDigestJobFailedInc(gran)
		return fmt.Errorf("memory.digest cron list users: %w", err)
	}
	if len(userIDs) == 0 {
		log.Infow("memory.digest cron: no active users; skipping LLM calls",
			"granularity", gran)
		return nil
	}

	log.Infow("memory.digest cron starting",
		"granularity", gran, "user_count", len(userIDs),
		"workers", c.cfg.WorkerConcurrency)

	// Worker pool.
	jobs := make(chan uint, len(userIDs))
	for _, uid := range userIDs {
		jobs <- uid
	}
	close(jobs)

	var wg sync.WaitGroup
	var successCount, failCount, panicCount int64
	var countsMu sync.Mutex

	workers := c.cfg.WorkerConcurrency
	if workers > len(userIDs) {
		workers = len(userIDs)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for uid := range jobs {
				// Per-user timeout: independent ctx so one slow LLM call
				// doesn't stall the worker for the next user.
				perUserCtx, cancel := context.WithTimeout(ctx, c.cfg.PerUserTimeout)
				func() {
					defer cancel()
					defer func() {
						if r := recover(); r != nil {
							log.Errorw("memory.digest cron per-user panic recovered",
								"granularity", gran, "user_id", uid,
								"panic", fmt.Sprintf("%v", r))
							countsMu.Lock()
							panicCount++
							countsMu.Unlock()
						}
					}()
					if err := processFn(perUserCtx, uid); err != nil {
						log.Warnw("memory.digest cron per-user failed",
							"granularity", gran, "user_id", uid, "error", err)
						countsMu.Lock()
						failCount++
						countsMu.Unlock()
						return
					}
					countsMu.Lock()
					successCount++
					countsMu.Unlock()
				}()
			}
		}()
	}
	wg.Wait()

	totalFail := failCount + panicCount
	if totalFail > 0 {
		metrics.MemoryDigestJobFailedInc(gran)
	}
	log.Infow("memory.digest cron complete",
		"granularity", gran,
		"users_total", len(userIDs),
		"users_success", successCount,
		"users_failed", failCount,
		"users_panic", panicCount,
	)
	return nil
}

// acquireLock attempts a Redis SETNX with cfg.LockTTL. Returns (acquired,
// releaseFn). When rdb is nil (single-instance mode), always returns
// (true, no-op release).
//
// Lock value = c.instanceID; on release we use a Lua compare-and-delete
// (see releaseLockCASScript) so we never accidentally delete a lock that
// has expired and been re-acquired by another instance. TTL still backstops
// crash / panic paths that bypass the release defer entirely.
func (c *digestCron) acquireLock(ctx context.Context, key string) (bool, func()) {
	if c.rdb == nil {
		return true, func() {}
	}
	ok, err := c.rdb.SetNX(ctx, key, c.instanceID, c.cfg.LockTTL).Result()
	if err != nil {
		// Redis hiccup — don't run (avoid double-run risk), don't log as error
		// at info level (cron can retry next cycle).
		log.Warnw("memory.digest cron lock SETNX failed; skipping run",
			"lock_key", key, "error", err)
		return false, func() {}
	}
	if !ok {
		return false, func() {}
	}
	release := func() {
		// CAS delete: only delete when our instance still owns the lock.
		// Eliminates the "lock expired → another instance acquired → we deleted
		// their lock" race. TTL is the safety net for crash / panic paths.
		res, derr := c.rdb.Eval(ctx, releaseLockCASScript, []string{key}, c.instanceID).Result()
		if derr != nil {
			log.Warnw("memory.digest cron lock CAS release failed; relying on TTL",
				"lock_key", key, "error", derr)
			return
		}
		// Eval returns int64(0) when the key was not deleted (owner mismatch /
		// already expired). Not an error — just an audit signal.
		if n, _ := res.(int64); n != 1 {
			log.Warnw("memory.digest cron lock not released by CAS — already expired or owned by another instance",
				"lock_key", key, "result", res)
		}
	}
	return true, release
}

// ─── Errors ──────────────────────────────────────────────────────────────────

// ErrCronLockHeld is returned by tests to indicate the lock is held — not used
// in production paths (production logs + returns nil).
var ErrCronLockHeld = errors.New("memory.digest: cron lock held by another instance")
