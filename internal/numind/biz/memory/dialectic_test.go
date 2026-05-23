package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/aiservice/profile"
	"numind-server/internal/pkg/metrics"
	"numind-server/internal/pkg/model"
)

// ─── Test helpers ──────────────────────────────────────────────────────────────

// newDialecticTestStores spins up an in-memory SQLite + auto-migrates the
// memory schema and returns wired stores. Mirrors the
// extractor_test / cadence_test / selector_test helpers so reviewers can
// compare patterns side-by-side.
func newDialecticTestStores(t *testing.T) (store.IUserMemoryFactStore, store.IUserMemoryProfileStore, *gorm.DB) {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.UserMemoryProfile{}, &model.UserMemoryFact{}))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return store.NewUserMemoryFactStore(db), store.NewUserMemoryProfileStore(db), db
}

// seedDialecticFacts inserts n facts for the given user with confidence
// decreasing from 0.95 down (capped at 0.70 to satisfy validateExtractedFact
// thresholds in case other paths re-validate). Returns the created facts
// already with assigned IDs.
func seedDialecticFacts(t *testing.T, factStore store.IUserMemoryFactStore, userID uint, n int) []model.UserMemoryFact {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		conf := 0.95 - float64(i)*0.01
		if conf < 0.70 {
			conf = 0.70
		}
		f := &model.UserMemoryFact{
			UUID:              fmt.Sprintf("dial-uuid-%d-%d", userID, i),
			UserID:            userID,
			SubjectID:         nil, // Layer A invariant — never write Layer B in V1.5
			Content:           fmt.Sprintf("使用者背景 fact #%d for user %d (idx=%d)", i, userID, i),
			Category:          model.MemoryFactCategoryContext,
			Confidence:        conf,
			Importance:        0.50,
			SourceSessionID:   "sess-dial-test",
			SourceMessageUUID: fmt.Sprintf("msg-dial-%d", i),
			SourceExtractedAt: time.Now(),
			EmbeddingHash:     fmt.Sprintf("dial-hash-%d-%d", userID, i),
			IsArchived:        false,
		}
		require.NoError(t, factStore.Create(ctx, f))
	}
	rows, err := factStore.List(ctx, userID, store.ListFactOpts{OrderBy: "confidence", Limit: n})
	require.NoError(t, err)
	require.Len(t, rows, n)
	return rows
}

// mockDialecticChat is a deterministic aiservice.Chat replacement that
// records invocations and returns a programmable response.
type mockDialecticChat struct {
	mu       sync.Mutex
	calls    int
	respFn   func(callIdx int, req aiservice.ChatRequest) (*aiservice.ChatResponse, error)
	lastReq  aiservice.ChatRequest
	lastTask string
}

func newMockDialecticChat(fn func(int, aiservice.ChatRequest) (*aiservice.ChatResponse, error)) *mockDialecticChat {
	return &mockDialecticChat{respFn: fn}
}

func (m *mockDialecticChat) fn() dialecticChatFn {
	return func(_ context.Context, taskID string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		m.mu.Lock()
		idx := m.calls
		m.calls++
		m.lastReq = req
		m.lastTask = taskID
		m.mu.Unlock()
		return m.respFn(idx, req)
	}
}

func (m *mockDialecticChat) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// staticDialecticResp returns a mock that always replies with the given text.
func staticDialecticResp(text string) *mockDialecticChat {
	return newMockDialecticChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content: text,
			Model:   "mock-qwen-plus",
			Usage:   aiservice.TokenUsage{PromptTokens: 800, CompletionTokens: 200, TotalTokens: 1000},
		}, nil
	})
}

// errDialecticResp returns a mock that always errors.
func errDialecticResp(err error) *mockDialecticChat {
	return newMockDialecticChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		return nil, err
	})
}

// panicDialecticResp returns a mock that panics on call.
func panicDialecticResp(msg string) *mockDialecticChat {
	return newMockDialecticChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		panic(msg)
	})
}

// validInsightText is a Chinese narrative within [100, 800] runes for the
// happy-path tests. Crafted to read like a real Layer A description so review
// can confirm tone matches the prompt's expectation.
const validInsightText = "该使用者是中级医疗器械销售，主管华东区，主要跟进三甲医院普外科。" +
	"对效率敏感，回话简洁高效，习惯快节奏会议。建议主动给出可执行方案而非选项陈列，" +
	"避免冗长开场白。话术偏向直接说明产品价值 + 数据支撑，避免抒情或类比。" +
	"当前对话节奏快，建议优先输出关键信息（价格 / 库存 / 交期），辅助决策建议放在最后一段。"

// newDialecticSvcForTest is the production-shape constructor used by all the
// happy-path tests: real CadenceService + real stores + sync executor + mock
// chat. Reduces boilerplate per case.
func newDialecticSvcForTest(
	t *testing.T,
	factStore store.IUserMemoryFactStore,
	profileStore store.IUserMemoryProfileStore,
	chat *mockDialecticChat,
) DialecticService {
	t.Helper()
	cadenceSvc := NewCadenceService(profileStore, DefaultCadenceConfig())
	return NewDialecticService(
		factStore,
		profileStore,
		cadenceSvc,
		DefaultDialecticConfig(),
		WithDialecticChatFn(chat.fn()),
		WithDialecticExecutor(SyncDialecticExecutor), // sync for deterministic assertions
	)
}

// ─── Case 1: cadence not ready → skip ────────────────────────────────────────

func TestMaybeRecompute_CadenceNotReady_Skip(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 42

	// Seed facts so the only thing keeping cadence "not ready" is a recent
	// cached_insight (cooldown active + new-fact delta below threshold).
	_ = seedDialecticFacts(t, factStore, uid, 5)

	// Prime a profile row: TotalFacts==5 == CachedInsightFactCount,
	// CachedInsightAt == now → inside cooldown.
	now := time.Now()
	err := profileStore.Upsert(ctx, &model.UserMemoryProfile{
		UserID:                 uid,
		CachedInsight:          "old insight",
		CachedInsightAt:        &now,
		CachedInsightFactCount: 5,
		TotalFacts:             5,
	})
	require.NoError(t, err)

	mc := staticDialecticResp(validInsightText)
	svc := newDialecticSvcForTest(t, factStore, profileStore, mc)

	svc.MaybeRecompute(ctx, uid)

	assert.Equal(t, 0, mc.callCount(), "cadence-not-ready must skip LLM")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.DialecticRuns[metrics.MemoryDialecticSkip], "skip counter +1")
	assert.Equal(t, int64(0), snap.DialecticRuns[metrics.MemoryDialecticRun])
	assert.Equal(t, int64(0), snap.DialecticRuns[metrics.MemoryDialecticFailed])

	// Cache untouched.
	prof, err := profileStore.Get(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "old insight", prof.CachedInsight, "skipped recompute must not clobber cache")
}

// ─── Case 2: 0 facts → no recompute ──────────────────────────────────────────

func TestMaybeRecompute_ZeroFacts_NoRecompute(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 43

	// No facts seeded. No profile row → cadence returns true (first-time user).
	mc := staticDialecticResp(validInsightText)
	svc := newDialecticSvcForTest(t, factStore, profileStore, mc)

	svc.MaybeRecompute(ctx, uid)

	assert.Equal(t, 0, mc.callCount(), "0 facts must not call LLM")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(0), snap.DialecticRuns[metrics.MemoryDialecticRun])
	assert.Equal(t, int64(0), snap.DialecticRuns[metrics.MemoryDialecticFailed])

	// Profile row may exist now (cadence Get auto-creates? No — only on first
	// Insight write. Here factStore.List returns [] so we return early before
	// any write). Should still be NotFound.
	_, err := profileStore.Get(ctx, uid)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "no profile row should be created on empty facts")
}

// ─── Case 3: happy path → writes cache ───────────────────────────────────────

func TestMaybeRecompute_HappyPath_WritesCache(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 44

	seeded := seedDialecticFacts(t, factStore, uid, 10)

	mc := staticDialecticResp(validInsightText)
	svc := newDialecticSvcForTest(t, factStore, profileStore, mc)

	svc.MaybeRecompute(ctx, uid)

	require.Equal(t, 1, mc.callCount(), "happy path must invoke LLM exactly once")

	// Profile row written with insight.
	prof, err := profileStore.Get(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, validInsightText, prof.CachedInsight)
	require.NotNil(t, prof.CachedInsightAt)
	assert.Equal(t, len(seeded), prof.CachedInsightFactCount)
	assert.WithinDuration(t, time.Now(), *prof.CachedInsightAt, 5*time.Second)

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.DialecticRuns[metrics.MemoryDialecticRun])
	assert.Equal(t, int64(0), snap.DialecticRuns[metrics.MemoryDialecticFailed])
	assert.Equal(t, int64(1), snap.DialecticDurationCount, "histogram observation should land")

	// Verify task profile + prompt shape.
	assert.Equal(t, profile.AgentDialectic, mc.lastTask, "must call agent.dialectic profile (D4 — qwen-plus / deepseek-v3-2)")
	require.NotEmpty(t, mc.lastReq.Messages)
	promptText := mc.lastReq.Messages[0].Content.Text
	assert.Contains(t, promptText, "使用者本人", "Layer A prompt must say 使用者本人")
	assert.Contains(t, promptText, "不要描述使用者关注的客户", "prompt must explicitly disallow Layer B")
	// Multi-scenario examples present (not just sales).
	assert.Contains(t, promptText, "数据分析师场景", "prompt must include data-analyst example")
	assert.Contains(t, promptText, "SOP 操作员场景", "prompt must include SOP-operator example")
	assert.Contains(t, promptText, "PPT 文员场景", "prompt must include PPT-clerk example")
}

// ─── Case 4: LLM failure → keeps old cache ───────────────────────────────────

func TestMaybeRecompute_LLMFailure_KeepsOldCache(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 45

	_ = seedDialecticFacts(t, factStore, uid, 5)

	// Prime an old cache.
	oldT := time.Now().Add(-1 * time.Hour) // > 30-min max cooldown → cadence will run
	require.NoError(t, profileStore.Upsert(ctx, &model.UserMemoryProfile{
		UserID:                 uid,
		CachedInsight:          "previously cached insight (do not clobber on failure)",
		CachedInsightAt:        &oldT,
		CachedInsightFactCount: 3, // < 5 total facts → also triggers new-fact-delta path
		TotalFacts:             5,
	}))

	mc := errDialecticResp(errors.New("LLM service unavailable"))
	svc := newDialecticSvcForTest(t, factStore, profileStore, mc)

	svc.MaybeRecompute(ctx, uid)

	assert.Equal(t, 1, mc.callCount(), "cadence ready, LLM should be called once")

	// Old cache preserved.
	prof, err := profileStore.Get(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "previously cached insight (do not clobber on failure)", prof.CachedInsight)
	assert.Equal(t, 3, prof.CachedInsightFactCount, "fact_count must not advance on failure")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(0), snap.DialecticRuns[metrics.MemoryDialecticRun])
	assert.Equal(t, int64(1), snap.DialecticRuns[metrics.MemoryDialecticFailed])
	assert.Equal(t, int64(1), snap.DialecticDurationCount, "histogram should observe even failed runs")
}

// ─── Case 5: LLM returns too short → reject ─────────────────────────────────

func TestMaybeRecompute_LLMReturnsTooShort_Reject(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 46

	_ = seedDialecticFacts(t, factStore, uid, 5)

	// Prime old cache so we can verify it survives.
	oldT := time.Now().Add(-1 * time.Hour)
	require.NoError(t, profileStore.Upsert(ctx, &model.UserMemoryProfile{
		UserID:                 uid,
		CachedInsight:          "older valid insight",
		CachedInsightAt:        &oldT,
		CachedInsightFactCount: 2,
		TotalFacts:             5,
	}))

	// Single character → 1 rune, fails validInsight (min 100).
	mc := staticDialecticResp("好")
	svc := newDialecticSvcForTest(t, factStore, profileStore, mc)

	svc.MaybeRecompute(ctx, uid)

	prof, err := profileStore.Get(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "older valid insight", prof.CachedInsight, "too-short response must preserve old cache")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(0), snap.DialecticRuns[metrics.MemoryDialecticRun])
	assert.Equal(t, int64(1), snap.DialecticRuns[metrics.MemoryDialecticFailed])
}

// ─── Case 6: LLM returns too long → reject ───────────────────────────────────

func TestMaybeRecompute_LLMReturnsTooLong_Reject(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 47

	_ = seedDialecticFacts(t, factStore, uid, 5)
	oldT := time.Now().Add(-1 * time.Hour)
	require.NoError(t, profileStore.Upsert(ctx, &model.UserMemoryProfile{
		UserID:                 uid,
		CachedInsight:          "another old insight",
		CachedInsightAt:        &oldT,
		CachedInsightFactCount: 2,
		TotalFacts:             5,
	}))

	// Build a string of > 800 runes by repeating a single rune.
	tooLong := strings.Repeat("长", 1500)
	mc := staticDialecticResp(tooLong)
	svc := newDialecticSvcForTest(t, factStore, profileStore, mc)

	svc.MaybeRecompute(ctx, uid)

	prof, err := profileStore.Get(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "another old insight", prof.CachedInsight, "too-long response must preserve old cache")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(0), snap.DialecticRuns[metrics.MemoryDialecticRun])
	assert.Equal(t, int64(1), snap.DialecticRuns[metrics.MemoryDialecticFailed])
}

// ─── Case 7: top facts limit honoured ────────────────────────────────────────

func TestMaybeRecompute_TopFactsRespectsLimit(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 48

	// Seed 50 facts; with TopFactsLimit=20 the prompt should only carry 20.
	_ = seedDialecticFacts(t, factStore, uid, 50)

	mc := staticDialecticResp(validInsightText)
	cadenceSvc := NewCadenceService(profileStore, DefaultCadenceConfig())
	svc := NewDialecticService(
		factStore, profileStore, cadenceSvc,
		DialecticConfig{
			TopFactsLimit:   20,
			MaxOutputTokens: 600,
			Temperature:     0.4,
			CallTimeout:     5 * time.Second,
		},
		WithDialecticChatFn(mc.fn()),
		WithDialecticExecutor(SyncDialecticExecutor),
	)

	svc.MaybeRecompute(ctx, uid)
	require.Equal(t, 1, mc.callCount())

	// Persisted CachedInsightFactCount should be 20 (= TopFactsLimit), not 50.
	prof, err := profileStore.Get(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, 20, prof.CachedInsightFactCount,
		"CachedInsightFactCount must reflect the *passed* fact count (limit applied)")

	// Verify prompt structure: count numbered bullets — should be exactly 20.
	prompt := mc.lastReq.Messages[0].Content.Text
	bulletCount := strings.Count(prompt, "\n20. [") // 20th bullet present
	assert.Equal(t, 1, bulletCount, "20th bullet expected in prompt")
	bullet21 := strings.Count(prompt, "\n21. [")
	assert.Equal(t, 0, bullet21, "no 21st bullet — limit must cap the input set")
}

// ─── Case 8: panic recovered ─────────────────────────────────────────────────

func TestMaybeRecompute_PanicRecovered(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 49

	_ = seedDialecticFacts(t, factStore, uid, 5)
	mc := panicDialecticResp("simulated LLM goroutine panic")
	svc := newDialecticSvcForTest(t, factStore, profileStore, mc)

	// Must not propagate panic to caller.
	require.NotPanics(t, func() {
		svc.MaybeRecompute(ctx, uid)
	})

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(0), snap.DialecticRuns[metrics.MemoryDialecticRun])
	assert.Equal(t, int64(1), snap.DialecticRuns[metrics.MemoryDialecticFailed], "panic must register a failed run")
}

// ─── Case 8b: Upsert fallback failure → counted + no panic ──────────────────

// failingUpsertProfileStore wraps a real IUserMemoryProfileStore but forces
// Upsert to return an error. Used to exercise the inner branch of
// recomputeInsightSafe where UpdateCachedInsight returns ErrRecordNotFound
// AND the defence-in-depth Upsert fallback ALSO fails — previously untested.
type failingUpsertProfileStore struct {
	store.IUserMemoryProfileStore
	upsertErr error
}

func (f *failingUpsertProfileStore) Upsert(_ context.Context, _ *model.UserMemoryProfile) error {
	return f.upsertErr
}

// TestMaybeRecompute_UpsertFallback_UpsertFails verifies the failure-path
// branch when:
//  1. UpdateCachedInsight returns gorm.ErrRecordNotFound (profile row absent)
//  2. The Upsert fallback also fails (DB write error)
//
// Expected: failed counter +1, no panic, no profile row created.
// Setting: facts are inserted directly via raw DB to bypass IUserMemoryFactStore.Create
// (which lazy-upserts a profile row through IncrTotalFacts). The profile row
// stays absent → UpdateCachedInsight hits "Where user_id=? + RowsAffected==0"
// → returns ErrRecordNotFound → triggers the Upsert fallback path.
func TestMaybeRecompute_UpsertFallback_UpsertFails(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, db := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 9001

	// Bypass factStore.Create — its tx contains IncrTotalFacts → OnConflict
	// DoNothing Create on user_memory_profile, which would seed a profile
	// row and defeat the NotFound branch we're trying to exercise. Raw insert
	// touches user_memory_facts only.
	for i := 0; i < 5; i++ {
		conf := 0.95 - float64(i)*0.01
		f := &model.UserMemoryFact{
			UUID:              fmt.Sprintf("dial-uf-%d-%d", uid, i),
			UserID:            uid,
			Content:           fmt.Sprintf("使用者背景 fact #%d", i),
			Category:          model.MemoryFactCategoryContext,
			Confidence:        conf,
			Importance:        0.50,
			SourceSessionID:   "sess-uf-test",
			SourceMessageUUID: fmt.Sprintf("msg-uf-%d", i),
			SourceExtractedAt: time.Now(),
			EmbeddingHash:     fmt.Sprintf("uf-hash-%d-%d", uid, i),
			IsArchived:        false,
		}
		require.NoError(t, db.WithContext(ctx).Create(f).Error)
	}
	// Sanity-check: facts visible via List, profile row absent.
	rows, err := factStore.List(ctx, uid, store.ListFactOpts{OrderBy: "confidence", Limit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 5)
	_, err = profileStore.Get(ctx, uid)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "test setup: profile must NOT exist")

	// Wrap real profile store with one whose Upsert always errors.
	wrapped := &failingUpsertProfileStore{
		IUserMemoryProfileStore: profileStore,
		upsertErr:               errors.New("simulated db write failure"),
	}

	mc := staticDialecticResp(validInsightText)
	cadenceSvc := NewCadenceService(wrapped, DefaultCadenceConfig())
	svc := NewDialecticService(
		factStore, wrapped, cadenceSvc,
		DefaultDialecticConfig(),
		WithDialecticChatFn(mc.fn()),
		WithDialecticExecutor(SyncDialecticExecutor),
	)

	// Must not panic even though both persist paths fail.
	require.NotPanics(t, func() {
		svc.MaybeRecompute(ctx, uid)
	})

	// LLM still got called once — failure is on the persist tail.
	assert.Equal(t, 1, mc.callCount(), "cadence ready & valid response should reach LLM exactly once")

	// Profile row should NOT exist (real Upsert never ran, fake one errored).
	_, err = profileStore.Get(ctx, uid)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "wrapped Upsert failed → no row persisted")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(0), snap.DialecticRuns[metrics.MemoryDialecticRun],
		"failed Upsert fallback must NOT count as a successful run")
	assert.Equal(t, int64(1), snap.DialecticRuns[metrics.MemoryDialecticFailed],
		"failed Upsert fallback must register a failed run")
	assert.Equal(t, int64(1), snap.DialecticDurationCount,
		"duration histogram observes even failed-persist runs")
}

// ─── Case 9: GetCachedInsight returns "" when empty ──────────────────────────

func TestGetCachedInsight_Empty_ReturnsEmptyString(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 50

	// Profile row exists but cached_insight is empty.
	require.NoError(t, profileStore.Upsert(ctx, &model.UserMemoryProfile{
		UserID:        uid,
		CachedInsight: "",
		TotalFacts:    0,
	}))

	svc := newDialecticSvcForTest(t, factStore, profileStore, staticDialecticResp("unused"))
	got := svc.GetCachedInsight(ctx, uid)
	assert.Equal(t, "", got, "empty cached_insight must return empty string")

	// And a user with no profile row at all.
	got = svc.GetCachedInsight(ctx, 99999)
	assert.Equal(t, "", got, "missing profile row must return empty string")
}

// ─── Case 10: GetCachedInsight returns the stored string ────────────────────

func TestGetCachedInsight_NonEmpty_ReturnsString(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 51

	now := time.Now()
	require.NoError(t, profileStore.Upsert(ctx, &model.UserMemoryProfile{
		UserID:                 uid,
		CachedInsight:          "stored insight content",
		CachedInsightAt:        &now,
		CachedInsightFactCount: 3,
	}))

	svc := newDialecticSvcForTest(t, factStore, profileStore, staticDialecticResp("unused"))
	got := svc.GetCachedInsight(ctx, uid)
	assert.Equal(t, "stored insight content", got)
}

// ─── Case 11: cross-user isolation ──────────────────────────────────────────

func TestGetCachedInsight_CrossUserIsolation(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uidA uint = 100
	const uidB uint = 200

	// Write insight only for A.
	now := time.Now()
	require.NoError(t, profileStore.Upsert(ctx, &model.UserMemoryProfile{
		UserID:                 uidA,
		CachedInsight:          "insight for A",
		CachedInsightAt:        &now,
		CachedInsightFactCount: 5,
	}))

	svc := newDialecticSvcForTest(t, factStore, profileStore, staticDialecticResp("unused"))

	assert.Equal(t, "insight for A", svc.GetCachedInsight(ctx, uidA), "A reads its own insight")
	assert.Equal(t, "", svc.GetCachedInsight(ctx, uidB),
		"B (no profile) must read empty — D7 B2B2C parent/child completely isolated")
}

// ─── Case 12: BuildInsightSection empty → empty ──────────────────────────────

func TestBuildInsightSection_Empty_ReturnsEmpty(t *testing.T) {
	factStore, profileStore, _ := newDialecticTestStores(t)
	cadenceSvc := NewCadenceService(profileStore, DefaultCadenceConfig())
	svc := NewDialecticService(factStore, profileStore, cadenceSvc, DefaultDialecticConfig())

	assert.Equal(t, "", svc.BuildInsightSection(""))
	assert.Equal(t, "", svc.BuildInsightSection("   "), "whitespace-only must also return empty")
	assert.Equal(t, "", svc.BuildInsightSection("\n\t"), "whitespace-only must also return empty")
}

// ─── Case 13: BuildInsightSection wraps with personal_context ───────────────

func TestBuildInsightSection_Wraps_With_PersonalContext(t *testing.T) {
	factStore, profileStore, _ := newDialecticTestStores(t)
	cadenceSvc := NewCadenceService(profileStore, DefaultCadenceConfig())
	svc := NewDialecticService(factStore, profileStore, cadenceSvc, DefaultDialecticConfig())

	out := svc.BuildInsightSection("X")
	assert.True(t, strings.HasPrefix(out, `<personal_context data-internal="true">`),
		"section must open with the scrubber-protected tag")
	assert.True(t, strings.HasSuffix(strings.TrimRight(out, "\n"), "</personal_context>"),
		"section must close with </personal_context>")
	assert.Contains(t, out, "【使用者画像】", "Layer A marker must be present")
	assert.Contains(t, out, "X", "insight body must be present")
}

// ─── Extra: prompt builder verifies Layer A wording ──────────────────────────

func TestBuildDialecticPrompt_LayerAWording(t *testing.T) {
	facts := []model.UserMemoryFact{
		{ID: 1, Category: "context", Confidence: 0.95, Content: "用户是医疗器械销售"},
		{ID: 2, Category: "preference", Confidence: 0.85, Content: "偏好简洁回答"},
	}
	prompt := buildDialecticPrompt(facts)

	// Layer A guard wording.
	assert.Contains(t, prompt, "使用者画像分析师")
	assert.Contains(t, prompt, "关于使用者本人的画像，不是使用者关注对象")
	assert.Contains(t, prompt, "不要描述使用者关注的客户 / 数据集 / 文档 / 产线等对象")
	// Multi-scenario few-shot.
	assert.Contains(t, prompt, "销售员场景")
	assert.Contains(t, prompt, "数据分析师场景")
	assert.Contains(t, prompt, "SOP 操作员场景")
	assert.Contains(t, prompt, "PPT 文员场景")
	// Bullet formatting.
	assert.Contains(t, prompt, "1. [context, conf=0.95] 用户是医疗器械销售")
	assert.Contains(t, prompt, "2. [preference, conf=0.85] 偏好简洁回答")
	// Output instruction tail.
	assert.Contains(t, prompt, "输出（无前缀、无标题，纯文本）：")
}

// ─── Extra: validInsight boundary ────────────────────────────────────────────

func TestValidInsight_Boundaries(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"empty", "", false},
		{"too short", strings.Repeat("a", 50), false},
		{"99 runes", strings.Repeat("a", 99), false},
		{"100 runes (lower bound)", strings.Repeat("a", 100), true},
		{"500 runes (typical)", strings.Repeat("a", 500), true},
		{"800 runes (upper bound)", strings.Repeat("a", 800), true},
		{"801 runes", strings.Repeat("a", 801), false},
		{"Chinese 100 runes", strings.Repeat("使", 100), true},
		{"Chinese 800 runes", strings.Repeat("使", 800), true},
		{"Chinese 801 runes", strings.Repeat("使", 801), false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, validInsight(tc.s))
		})
	}
}

// ─── Extra: LoadDialecticConfigFromViper ────────────────────────────────────

// dialecticStubViper is the dialectic-test analogue of cadence_test.stubViper
// (we can't reuse that name — would collide at package scope). Implements both
// GetInt and GetFloat64 since the dialectic loader needs float for temperature.
type dialecticStubViper struct {
	ints   map[string]int
	floats map[string]float64
}

func (s *dialecticStubViper) GetInt(k string) int { return s.ints[k] }
func (s *dialecticStubViper) GetFloat64(k string) float64 {
	if s.floats == nil {
		return 0
	}
	return s.floats[k]
}

func TestLoadDialecticConfigFromViper_OverridesAndDefaults(t *testing.T) {
	v := &dialecticStubViper{
		ints: map[string]int{
			"agent.memory.dialectic_top_facts_limit":      7,
			"agent.memory.dialectic_max_output_tokens":    1024,
			"agent.memory.dialectic_call_timeout_seconds": 45,
		},
		floats: map[string]float64{
			"agent.memory.dialectic_temperature": 0.7,
		},
	}
	cfg := LoadDialecticConfigFromViper(v)
	assert.Equal(t, 7, cfg.TopFactsLimit)
	assert.Equal(t, 1024, cfg.MaxOutputTokens)
	assert.Equal(t, 0.7, cfg.Temperature)
	assert.Equal(t, 45*time.Second, cfg.CallTimeout)

	// Empty viper → all defaults.
	cfg2 := LoadDialecticConfigFromViper(&dialecticStubViper{})
	assert.Equal(t, DefaultDialecticTopFactsLimit, cfg2.TopFactsLimit)
	assert.Equal(t, DefaultDialecticMaxOutputTokens, cfg2.MaxOutputTokens)
	assert.Equal(t, DefaultDialecticTemperature, cfg2.Temperature)
	assert.Equal(t, DefaultDialecticCallTimeout, cfg2.CallTimeout)
}

// ─── Extra: production-mode goroutine path smoke-test ────────────────────────

// TestMaybeRecompute_GoExecutor_ReturnsImmediately verifies that the default
// goExecutor path (no test override) returns to the caller immediately —
// MaybeRecompute must never block on the LLM. We use a sync.WaitGroup signal
// chat to detect the goroutine actually fired and waited the right amount.
func TestMaybeRecompute_GoExecutor_ReturnsImmediately(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	ctx := context.Background()
	const uid uint = 60

	_ = seedDialecticFacts(t, factStore, uid, 3)

	// Mock chat that signals on call.
	done := make(chan struct{})
	mc := newMockDialecticChat(func(_ int, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		defer close(done)
		// Simulate a brief LLM latency to ensure the goroutine path is taken.
		time.Sleep(10 * time.Millisecond)
		return &aiservice.ChatResponse{
			Content: validInsightText,
			Model:   "mock",
			Usage:   aiservice.TokenUsage{},
		}, nil
	})

	cadenceSvc := NewCadenceService(profileStore, DefaultCadenceConfig())
	svc := NewDialecticService(
		factStore, profileStore, cadenceSvc,
		DefaultDialecticConfig(),
		WithDialecticChatFn(mc.fn()),
		// no executor override — production goExecutor
	)

	start := time.Now()
	svc.MaybeRecompute(ctx, uid)
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 5*time.Millisecond,
		"MaybeRecompute must return ~immediately under goExecutor (got %s)", elapsed)

	// Wait for the goroutine.
	select {
	case <-done:
		// Good — wait for cache write to settle.
		time.Sleep(20 * time.Millisecond)
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not call LLM within 2s")
	}

	prof, err := profileStore.Get(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, validInsightText, prof.CachedInsight)
}

// ─── Extra: extractor integration via SetDialecticService ───────────────────

// TestExtractor_DialecticHook_FiresOnPersist verifies that ExtractorService
// .extract() calls dialecticSvc.MaybeRecompute after a successful persistFacts.
// Uses a recording stub for the dialectic interface so we don't need to spin
// up the full DialecticService for this contract test.
type recordingDialectic struct {
	mu      sync.Mutex
	calls   int
	lastUID uint
}

func (r *recordingDialectic) MaybeRecompute(_ context.Context, uid uint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.lastUID = uid
}

func (r *recordingDialectic) GetCachedInsight(_ context.Context, _ uint) string {
	return ""
}

func (r *recordingDialectic) BuildInsightSection(_ string) string {
	return ""
}

func (r *recordingDialectic) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestExtractor_DialecticHook_FiresOnPersist(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	mc := staticResp(`[
{"content":"使用者是数据分析师","category":"context","confidence":0.95}
]`)

	rec := &recordingDialectic{}
	svc := NewExtractorService(factStore, profileStore,
		WithExtractorChatFn(mc.fn()),
		WithExtractorWorkers(1),
		WithExtractorDialecticService(rec),
	)
	svc.Start(context.Background())
	defer svc.Stop()

	svc.Enqueue(77, "sess-hook", sampleMsgs(), false)
	waitForChatCalls(t, mc, 1, 2*time.Second)
	// Wait for persistFacts → SetDialecticService hook to run.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec.callCount() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	assert.Equal(t, 1, rec.callCount(), "extractor must invoke dialecticSvc.MaybeRecompute exactly once")
	assert.Equal(t, uint(77), rec.lastUID, "must forward correct user_id")
}

// TestExtractor_DialecticHook_NotFiredWhenZeroFacts verifies the hook is NOT
// called when persistFacts persists 0 facts (no new memory landed → nothing
// for dialectic to learn from since last run).
func TestExtractor_DialecticHook_NotFiredWhenZeroFacts(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profileStore, _ := newDialecticTestStores(t)
	// LLM returns empty array → 0 facts persisted.
	mc := staticResp(`[]`)

	rec := &recordingDialectic{}
	svc := NewExtractorService(factStore, profileStore,
		WithExtractorChatFn(mc.fn()),
		WithExtractorWorkers(1),
		WithExtractorDialecticService(rec),
	)
	svc.Start(context.Background())
	defer svc.Stop()

	svc.Enqueue(78, "sess-zero", sampleMsgs(), false)
	waitForChatCalls(t, mc, 1, 2*time.Second)

	// Polling negative-assertion: under CI load 50ms can be too tight to
	// reliably confirm the goroutine didn't fire. Walk a 200ms window and
	// fail fast the moment any unexpected call lands. Final assertion
	// after the window catches the steady-state "no call ever happened".
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if rec.callCount() > 0 {
			t.Fatalf("dialectic hook was called for zero-facts extraction (count=%d)", rec.callCount())
		}
		time.Sleep(10 * time.Millisecond)
	}

	assert.Equal(t, 0, rec.callCount(), "0-fact extraction must NOT fire dialectic hook")
}
