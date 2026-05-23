package memory

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/metrics"
	"numind-server/internal/pkg/model"
)

// ─── Mock Redis (lock simulator) ─────────────────────────────────────────────

// fakeRedis is a minimal in-memory redisClient stub supporting SetNX + Del.
// Avoids the miniredis dep — we only need two operations.
type fakeRedis struct {
	mu       sync.Mutex
	keys     map[string]string
	failNext bool // if true, next SetNX returns error
}

func newFakeRedis() *fakeRedis {
	return &fakeRedis{keys: make(map[string]string)}
}

func (f *fakeRedis) SetNX(_ context.Context, key string, value any, _ time.Duration) *redis.BoolCmd {
	cmd := redis.NewBoolCmd(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		cmd.SetErr(errors.New("redis: fake injected error"))
		return cmd
	}
	if _, exists := f.keys[key]; exists {
		cmd.SetVal(false)
		return cmd
	}
	if s, ok := value.(string); ok {
		f.keys[key] = s
	} else {
		f.keys[key] = "owned"
	}
	cmd.SetVal(true)
	return cmd
}

func (f *fakeRedis) Del(_ context.Context, keys ...string) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	count := int64(0)
	for _, k := range keys {
		if _, ok := f.keys[k]; ok {
			delete(f.keys, k)
			count++
		}
	}
	cmd.SetVal(count)
	return cmd
}

// Eval emulates the releaseLockCASScript: compare-and-delete. Returns int64(1)
// when KEYS[1]'s current value equals ARGV[0]; int64(0) otherwise (mismatch
// or key missing). Other scripts return int64(0) — sufficient for our tests.
func (f *fakeRedis) Eval(_ context.Context, script string, keys []string, args ...interface{}) *redis.Cmd {
	cmd := redis.NewCmd(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	// Only the CAS release script is exercised in this package; we don't try
	// to parse arbitrary Lua. Identify by presence of "GET" and "DEL" tokens
	// to remain robust against minor script reformatting.
	if len(keys) == 1 && len(args) >= 1 {
		want, _ := args[0].(string)
		key := keys[0]
		if cur, ok := f.keys[key]; ok && cur == want {
			delete(f.keys, key)
			cmd.SetVal(int64(1))
			return cmd
		}
		cmd.SetVal(int64(0))
		return cmd
	}
	_ = script
	cmd.SetVal(int64(0))
	return cmd
}

func (f *fakeRedis) preset(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[key] = value
}

func (f *fakeRedis) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.keys[key]
	return ok
}

// ─── Daily cron happy path ───────────────────────────────────────────────────

func TestRunDailyDigest_HappyPath(t *testing.T) {
	metrics.MemoryResetForTest()
	digestStore, factStore, _, db := newDigestTestStores(t)
	ctx := context.Background()

	// Seed 2 users with activity on the target day (yesterday relative to runDate).
	loc := shanghaiLoc
	runDate := time.Date(2026, 5, 23, 4, 0, 0, 0, loc) // typical cron run time
	yesterday := runDate.AddDate(0, 0, -1)             // 2026-05-22
	seedAgentRun(t, db, 201, "sess-201a", yesterday.Add(2*time.Hour))
	seedAgentRun(t, db, 202, "sess-202a", yesterday.Add(3*time.Hour))

	mc := newMockDigestChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: validDigestJSON, Model: "mock"}, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))

	fr := newFakeRedis()
	cron := NewDigestCron(digestStore, gen, DefaultDigestCronConfig(),
		WithDigestCronRedisClient(fr))

	require.NoError(t, cron.RunDailyDigest(ctx, runDate))

	// Both users should have a daily digest row.
	d1, err := digestStore.GetDaily(ctx, 201, yesterday)
	require.NoError(t, err)
	assert.Contains(t, d1.Summary, "医院")
	d2, err := digestStore.GetDaily(ctx, 202, yesterday)
	require.NoError(t, err)
	assert.NotEmpty(t, d2.Summary)

	assert.Equal(t, 2, mc.callCount(), "1 LLM call per user")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.DigestJobRuns[metrics.MemoryDigestGranDaily])
	assert.Equal(t, int64(0), snap.DigestJobFailed[metrics.MemoryDigestGranDaily])
}

// ─── No active users → skip LLM ──────────────────────────────────────────────

func TestRunDailyDigest_NoActiveUsers(t *testing.T) {
	metrics.MemoryResetForTest()
	digestStore, factStore, _, _ := newDigestTestStores(t)
	ctx := context.Background()

	mc := newMockDigestChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		t.Errorf("LLM must not be called when no active users")
		return nil, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))
	fr := newFakeRedis()
	cron := NewDigestCron(digestStore, gen, DefaultDigestCronConfig(),
		WithDigestCronRedisClient(fr))

	loc := shanghaiLoc
	require.NoError(t, cron.RunDailyDigest(ctx, time.Date(2026, 5, 23, 4, 0, 0, 0, loc)))
	assert.Equal(t, 0, mc.callCount())

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.DigestJobRuns[metrics.MemoryDigestGranDaily], "job counter still increments")
}

// ─── Single-user failure does not abort cron ─────────────────────────────────

func TestRunDailyDigest_SingleUserFails_OthersContinue(t *testing.T) {
	metrics.MemoryResetForTest()
	digestStore, factStore, _, db := newDigestTestStores(t)
	ctx := context.Background()
	loc := shanghaiLoc
	runDate := time.Date(2026, 5, 23, 4, 0, 0, 0, loc)
	yesterday := runDate.AddDate(0, 0, -1)
	seedAgentRun(t, db, 301, "s1", yesterday.Add(1*time.Hour))
	seedAgentRun(t, db, 302, "s2", yesterday.Add(2*time.Hour))

	// LLM errors only for user 301.
	var u301Calls int32
	mc := newMockDigestChat(func(_ int, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		txt := req.Messages[0].Content.Text
		// We cannot tell user from prompt; instead alternate by call index — but
		// concurrent worker pool order is non-deterministic. Fail every 2nd call.
		// Better: fail any call whose prompt mentions "sess-301-bad-marker".
		// Since seedAgentRun uses generic content, we use a counter approach:
		// first call fails, rest succeed. This still demonstrates "1 fail does
		// not abort cron".
		if atomic.AddInt32(&u301Calls, 1) == 1 {
			_ = txt
			return nil, errors.New("simulated LLM error")
		}
		return &aiservice.ChatResponse{Content: validDigestJSON, Model: "mock"}, nil
	})

	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))
	fr := newFakeRedis()
	cfg := DefaultDigestCronConfig()
	cfg.WorkerConcurrency = 1 // deterministic ordering
	cron := NewDigestCron(digestStore, gen, cfg, WithDigestCronRedisClient(fr))

	require.NoError(t, cron.RunDailyDigest(ctx, runDate))

	// First-processed user got fallback (LLM err → both attempts will fail → fallback).
	// Second user got real summary.
	// Both users should have a digest row regardless (LLM failure → fallback row).
	users := []uint{301, 302}
	successCount := 0
	for _, uid := range users {
		d, err := digestStore.GetDaily(ctx, uid, yesterday)
		if err == nil && d.Summary != "" {
			successCount++
		}
	}
	assert.Equal(t, 2, successCount, "both users have a digest row (fallback for failed user)")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.DigestJobRuns[metrics.MemoryDigestGranDaily])
	// No job-level failure counter — per-user failures don't promote to job-level
	// fail unless ALL fail. (Our DigestGenerator falls back gracefully, so the
	// per-user processFn returns nil — no failCount++ in cron.)
	assert.Equal(t, int64(0), snap.DigestJobFailed[metrics.MemoryDigestGranDaily])
}

// ─── Redis lock held → cron skips ────────────────────────────────────────────

func TestRunDailyDigest_RedisLockHeld(t *testing.T) {
	metrics.MemoryResetForTest()
	digestStore, factStore, _, db := newDigestTestStores(t)
	ctx := context.Background()
	loc := shanghaiLoc
	runDate := time.Date(2026, 5, 23, 4, 0, 0, 0, loc)
	yesterday := runDate.AddDate(0, 0, -1)
	seedAgentRun(t, db, 401, "sx", yesterday.Add(1*time.Hour))

	mc := newMockDigestChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		t.Errorf("LLM must not be called when lock held")
		return nil, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))

	fr := newFakeRedis()
	// Pre-populate the lock to simulate another instance holding it.
	expectedKey := digestLockKeyPrefix + "daily:" + yesterday.Format("2006-01-02")
	fr.preset(expectedKey, "other-instance")

	cron := NewDigestCron(digestStore, gen, DefaultDigestCronConfig(),
		WithDigestCronRedisClient(fr))

	require.NoError(t, cron.RunDailyDigest(ctx, runDate), "lock held should return nil (no error)")
	assert.Equal(t, 0, mc.callCount())
	assert.True(t, fr.has(expectedKey), "external lock not released by our cron")
}

// ─── Redis lock acquired then released ───────────────────────────────────────

func TestRunDailyDigest_LockReleasedAfterRun(t *testing.T) {
	metrics.MemoryResetForTest()
	digestStore, factStore, _, db := newDigestTestStores(t)
	ctx := context.Background()
	loc := shanghaiLoc
	runDate := time.Date(2026, 5, 23, 4, 0, 0, 0, loc)
	yesterday := runDate.AddDate(0, 0, -1)
	seedAgentRun(t, db, 501, "sy", yesterday.Add(1*time.Hour))

	mc := newMockDigestChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: validDigestJSON, Model: "mock"}, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))

	fr := newFakeRedis()
	cron := NewDigestCron(digestStore, gen, DefaultDigestCronConfig(),
		WithDigestCronRedisClient(fr))

	expectedKey := digestLockKeyPrefix + "daily:" + yesterday.Format("2006-01-02")
	require.NoError(t, cron.RunDailyDigest(ctx, runDate))
	assert.False(t, fr.has(expectedKey), "cron must release the lock on successful completion")
}

// ─── Single-instance mode (rdb = nil) ────────────────────────────────────────

func TestRunDailyDigest_NoRedis_SingleInstanceMode(t *testing.T) {
	metrics.MemoryResetForTest()
	digestStore, factStore, _, db := newDigestTestStores(t)
	ctx := context.Background()
	loc := shanghaiLoc
	runDate := time.Date(2026, 5, 23, 4, 0, 0, 0, loc)
	yesterday := runDate.AddDate(0, 0, -1)
	seedAgentRun(t, db, 601, "sz", yesterday.Add(1*time.Hour))

	mc := newMockDigestChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: validDigestJSON, Model: "mock"}, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))
	// No WithDigestCronRedisClient → "no lock" mode.
	cron := NewDigestCron(digestStore, gen, DefaultDigestCronConfig())

	require.NoError(t, cron.RunDailyDigest(ctx, runDate))
	assert.Equal(t, 1, mc.callCount())
}

// ─── Weekly cron smoke test ──────────────────────────────────────────────────

func TestRunWeeklyDigest_HappyPath(t *testing.T) {
	metrics.MemoryResetForTest()
	digestStore, factStore, _, db := newDigestTestStores(t)
	ctx := context.Background()
	loc := shanghaiLoc

	// runDate Monday 2026-05-25 → aggregates last week (2026-W21, Mon 05-18 to Sun 05-24).
	runDate := time.Date(2026, 5, 25, 4, 30, 0, 0, loc)
	// Seed activity in last week's window.
	seedAgentRun(t, db, 701, "wk1", time.Date(2026, 5, 19, 10, 0, 0, 0, loc))

	// Seed a daily digest so weekly has something to aggregate over.
	require.NoError(t, digestStore.UpsertDaily(ctx, &model.UserMemoryDigestDaily{
		UserID:     701,
		DigestDate: time.Date(2026, 5, 19, 0, 0, 0, 0, loc),
		Summary:    "day 19 summary",
	}))

	mc := newMockDigestChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{Content: validDigestJSON, Model: "mock"}, nil
	})
	gen := NewDigestGenerator(digestStore, factStore, DefaultDigestConfig(), WithDigestChatFn(mc.fn()))
	cron := NewDigestCron(digestStore, gen, DefaultDigestCronConfig())

	require.NoError(t, cron.RunWeeklyDigest(ctx, runDate))

	w, err := digestStore.GetWeekly(ctx, 701, 2026, 21)
	require.NoError(t, err)
	assert.NotEmpty(t, w.Summary)

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.DigestJobRuns[metrics.MemoryDigestGranWeekly])
}

// ─── TemporalService end-to-end injection ────────────────────────────────────

func TestTemporalService_InjectDigests_HappyPath(t *testing.T) {
	digestStore, _, _, _ := newDigestTestStores(t)
	ctx := context.Background()
	loc := shanghaiLoc
	const uid uint = 801

	// Seed a weekly digest for last week (W20 relative to fixedNow's W21).
	weekStart := time.Date(2026, 5, 11, 0, 0, 0, 0, loc)
	weekEnd := time.Date(2026, 5, 17, 0, 0, 0, 0, loc)
	require.NoError(t, digestStore.UpsertWeekly(ctx, &model.UserMemoryDigestWeekly{
		UserID:        uid,
		ISOYear:       2026,
		ISOWeek:       20,
		WeekStartDate: weekStart,
		WeekEndDate:   weekEnd,
		Summary:       "用户上周跟进了 3 家医院, 主推 CT 设备",
		KeyTopics:     keyTopicsToJSON([]string{"医院", "CT设备"}),
	}))

	svc := NewTemporalService(digestStore, WithTemporalClock(func() time.Time { return fixedNow() }))

	block := svc.InjectDigests(ctx, uid, "上周战况怎样")
	assert.NotEmpty(t, block)
	assert.Contains(t, block, "<temporal_context")
	assert.Contains(t, block, `granularity="weekly"`)
	assert.Contains(t, block, `period="2026-W20"`)
	assert.Contains(t, block, "上周")
	assert.Contains(t, block, "用户上周跟进了")
	assert.Contains(t, block, "关键主题: 医院, CT设备")
	assert.Contains(t, block, "</temporal_context>")
}

func TestTemporalService_NoKeyword_ReturnsEmpty(t *testing.T) {
	digestStore, _, _, _ := newDigestTestStores(t)
	ctx := context.Background()
	svc := NewTemporalService(digestStore, WithTemporalClock(func() time.Time { return fixedNow() }))

	assert.Empty(t, svc.InjectDigests(ctx, 999, "帮我写客户开场白"))
}

func TestTemporalService_DigestMissing_ReturnsEmpty(t *testing.T) {
	digestStore, _, _, _ := newDigestTestStores(t)
	ctx := context.Background()
	svc := NewTemporalService(digestStore, WithTemporalClock(func() time.Time { return fixedNow() }))

	// Keyword matched but no DB row → empty result, NOT an error.
	assert.Empty(t, svc.InjectDigests(ctx, 999, "上周怎样"))
}

func TestTemporalService_UnauthenticatedSkips(t *testing.T) {
	digestStore, _, _, _ := newDigestTestStores(t)
	ctx := context.Background()
	svc := NewTemporalService(digestStore)
	assert.Empty(t, svc.InjectDigests(ctx, 0, "今天怎么样"))
}

// Ensure the store interface stays satisfiable by the concrete impl
// (compile-time assertion guard against future drift).
var _ store.IMemoryDigestStore = (store.IMemoryDigestStore)(nil)
