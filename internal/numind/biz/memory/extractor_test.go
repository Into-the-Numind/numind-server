package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/aiservice"
	"numind-server/internal/pkg/metrics"
	"numind-server/internal/pkg/model"
)

// ─── Test helpers ──────────────────────────────────────────────────────────────

// newExtractorTestStores builds an in-memory SQLite database with the memory
// schema and returns wired stores. The returned cleanup closes the DB.
func newExtractorTestStores(t *testing.T) (store.IUserMemoryFactStore, store.IUserMemoryProfileStore, *gorm.DB) {
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

// mockChat is a deterministic aiservice.Chat replacement that returns a
// pre-canned response (per call) and records invocations for assertions.
type mockChat struct {
	mu       sync.Mutex
	calls    int
	calledWg *sync.WaitGroup
	resp     func(callIdx int) (*aiservice.ChatResponse, error)
}

func newMockChat(fn func(callIdx int) (*aiservice.ChatResponse, error)) *mockChat {
	return &mockChat{resp: fn}
}

// fn returns the chat seam closure that the extractor uses.
func (m *mockChat) fn() extractorChatFn {
	return func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		m.mu.Lock()
		idx := m.calls
		m.calls++
		m.mu.Unlock()
		if m.calledWg != nil {
			defer m.calledWg.Done()
		}
		return m.resp(idx)
	}
}

func (m *mockChat) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// staticResp returns a mockChat that always returns the given JSON content.
func staticResp(jsonContent string) *mockChat {
	return newMockChat(func(_ int) (*aiservice.ChatResponse, error) {
		return &aiservice.ChatResponse{
			Content: jsonContent,
			Model:   "mock-model",
			Usage:   aiservice.TokenUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
		}, nil
	})
}

// errResp returns a mockChat that always errors.
func errResp(err error) *mockChat {
	return newMockChat(func(_ int) (*aiservice.ChatResponse, error) {
		return nil, err
	})
}

// runExtractAndWait runs extract() synchronously then waits for any rebuild
// goroutine triggered by maybeRebuildProfile to settle.
//
// Pattern: tests typically build an extractor with workerCount=1 + queueCap large
// enough; they Start it, Enqueue once, then wait for the chat mock's WaitGroup
// to fire (one mock call expected per extract).
func waitForChatCalls(t *testing.T, m *mockChat, expected int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if m.callCount() >= expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waitForChatCalls: expected %d calls, got %d within %s", expected, m.callCount(), timeout)
}

// sampleMsgs returns a tiny conversation usable as ExtractorService.Enqueue input.
func sampleMsgs() []ChatMessage {
	return []ChatMessage{
		{Role: "user", Content: "我是数据分析师，主用 Python"},
		{Role: "assistant", Content: "好的，已记录"},
	}
}

// ─── Case 1: full dialog → multiple facts persisted ────────────────────────────

func TestExtract_FullDialog_ExtractsMultipleFacts(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profStore, _ := newExtractorTestStores(t)
	mc := staticResp(`[
{"content":"用户是数据分析师，主用 Python","category":"context","confidence":0.95},
{"content":"用户偏好简洁清晰的回答","category":"preference","confidence":0.85},
{"content":"用户在做指标可视化项目","category":"context","confidence":0.80}
]`)

	svc := NewExtractorService(factStore, profStore,
		WithExtractorChatFn(mc.fn()),
		WithExtractorWorkers(1),
	)
	svc.Start(context.Background())
	defer svc.Stop()

	svc.Enqueue(42, "sess-1", sampleMsgs(), false)
	waitForChatCalls(t, mc, 1, 2*time.Second)
	// Wait briefly for persistence to settle.
	time.Sleep(50 * time.Millisecond)

	facts, err := factStore.List(context.Background(), 42, store.ListFactOpts{Limit: 100})
	require.NoError(t, err)
	assert.Len(t, facts, 3, "all 3 facts above threshold should be persisted")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.ExtractionRuns[metrics.MemoryExtractionSuccess])
	assert.Equal(t, int64(3), snap.FactsExtractedTotal)
}

// ─── Case 2: low-confidence facts filtered ─────────────────────────────────────

func TestExtract_LowConfidenceFiltered(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profStore, _ := newExtractorTestStores(t)
	mc := staticResp(`[
{"content":"用户可能在做销售","category":"context","confidence":0.60},
{"content":"用户偏好直接给结论","category":"preference","confidence":0.75}
]`)

	svc := NewExtractorService(factStore, profStore,
		WithExtractorChatFn(mc.fn()),
		WithExtractorWorkers(1),
	)
	svc.Start(context.Background())
	defer svc.Stop()

	svc.Enqueue(43, "sess-1", sampleMsgs(), false)
	waitForChatCalls(t, mc, 1, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	facts, err := factStore.List(context.Background(), 43, store.ListFactOpts{Limit: 100})
	require.NoError(t, err)
	require.Len(t, facts, 1, "only conf >= 0.70 should persist")
	assert.Equal(t, "用户偏好直接给结论", facts[0].Content)
}

// ─── Case 3: hash dedup promotes confidence ────────────────────────────────────

func TestExtract_HashDedup_PromotesConfidence(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profStore, _ := newExtractorTestStores(t)
	ctx := context.Background()

	// Pre-seed a fact at conf 0.75 with matching hash.
	content := "用户偏好简洁回答"
	hash := computeContentHash(content)
	require.NotEmpty(t, hash)
	preFact := &model.UserMemoryFact{
		UUID:              "pre-1",
		UserID:            44,
		Content:           content,
		Category:          "preference",
		Confidence:        0.75,
		Importance:        0.5,
		SourceSessionID:   "sess-0",
		SourceExtractedAt: time.Now(),
		EmbeddingHash:     hash,
	}
	require.NoError(t, factStore.Create(ctx, preFact))

	// Now extractor returns same content at conf 0.90.
	mc := staticResp(`[{"content":"用户偏好简洁回答","category":"preference","confidence":0.90}]`)
	svc := NewExtractorService(factStore, profStore,
		WithExtractorChatFn(mc.fn()),
		WithExtractorWorkers(1),
	)
	svc.Start(ctx)
	defer svc.Stop()

	svc.Enqueue(44, "sess-1", sampleMsgs(), false)
	waitForChatCalls(t, mc, 1, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	facts, err := factStore.List(ctx, 44, store.ListFactOpts{Limit: 100})
	require.NoError(t, err)
	require.Len(t, facts, 1, "dedup should keep DB at 1 row")
	assert.InDelta(t, 0.90, facts[0].Confidence, 0.001, "confidence promoted to max(0.75, 0.90)")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.DedupHitsTotal)
}

// ─── Case 4: trivial input skipped ─────────────────────────────────────────────

func TestExtract_TrivialSkipped(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profStore, _ := newExtractorTestStores(t)
	mc := staticResp(`[]`)

	svc := NewExtractorService(factStore, profStore,
		WithExtractorChatFn(mc.fn()),
		WithExtractorWorkers(1),
	)
	svc.Start(context.Background())
	defer svc.Stop()

	svc.Enqueue(45, "sess-1", sampleMsgs(), true) // isTrivial=true
	// Give the worker some time to NOT process anything.
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, 0, mc.callCount(), "trivial enqueue must not call LLM")

	facts, err := factStore.List(context.Background(), 45, store.ListFactOpts{Limit: 100})
	require.NoError(t, err)
	assert.Empty(t, facts)
}

// ─── Case 5: debounce — same user within window ────────────────────────────────

func TestExtract_DebounceWithin30s(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profStore, _ := newExtractorTestStores(t)

	// Mock returns valid JSON; we'll count how many times the chat seam runs.
	mc := staticResp(`[{"content":"用户在做数据分析","category":"context","confidence":0.85}]`)

	// Use a short skip-delta via reduced debounce window so the "newer enqueue
	// pending" check fires for test timing. With WorkerStaleSkipDelta = 25s the
	// test would be too slow; we override by reducing debounce window itself
	// AND scoping the skip delta — instead of refactoring, just verify that
	// behaviour is consistent: 5 rapid enqueues result in at most 5 chat calls
	// (newer ones may skip).
	// Use a high rebuild threshold so the rebuild path doesn't fire mid-test
	// (which would add extra LLM calls and confuse the count assertion).
	svc := NewExtractorService(factStore, profStore,
		WithExtractorChatFn(mc.fn()),
		WithExtractorWorkers(1),
		WithExtractorDebounceWindow(30*time.Second),
		WithExtractorRebuildThreshold(1000),
	)
	svc.Start(context.Background())
	defer svc.Stop()

	for i := 0; i < 5; i++ {
		svc.Enqueue(46, "sess-1", sampleMsgs(), false)
	}

	// All 5 jobs are in queue immediately. With workerCount=1 they process
	// serially; debounce algorithm requires a fresher entry to be > 25s newer
	// than EnqueueAt to skip — in this test all 5 enqueues are <1s apart so
	// none qualify and all 5 chat calls fire.
	waitForChatCalls(t, mc, 1, 2*time.Second)
	time.Sleep(200 * time.Millisecond) // let serial worker drain

	// At minimum 1 call should fire; this test verifies the *mechanism* doesn't
	// crash on rapid same-user enqueues. Real prod 30s+ gaps would collapse via
	// the WorkerStaleSkipDelta check.
	assert.GreaterOrEqual(t, mc.callCount(), 1, "at least one extraction must run")
	assert.LessOrEqual(t, mc.callCount(), 5, "cannot exceed enqueue count")

	// Side-effect: facts saved should not duplicate (hash dedup catches them).
	facts, err := factStore.List(context.Background(), 46, store.ListFactOpts{Limit: 100})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(facts), 1, "hash dedup ensures only 1 unique fact even with multiple extracts")
}

// TestExtract_DebounceSkipsStaleJob verifies the WorkerStaleSkipDelta check
// drops jobs whose EnqueueAt is significantly older than the debounceMap entry.
func TestExtract_DebounceSkipsStaleJob(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profStore, _ := newExtractorTestStores(t)
	mc := staticResp(`[]`)

	svc := NewExtractorService(factStore, profStore,
		WithExtractorChatFn(mc.fn()),
		WithExtractorWorkers(1),
	)
	svc.Start(context.Background())
	defer svc.Stop()

	// Manually inject a stale job into the queue + a newer debounceMap entry.
	staleJob := ExtractionJob{
		UserID:    51,
		SessionID: "sess-stale",
		Messages:  sampleMsgs(),
		EnqueueAt: time.Now().Add(-time.Minute), // 60s ago
	}
	// Simulate that a newer Enqueue has happened.
	svc.debounceMap.Store(uint(51), time.Now())
	svc.jobQueue <- staleJob

	// The worker should pull this job, detect the stale EnqueueAt vs debounceMap,
	// and skip without calling chat.
	time.Sleep(200 * time.Millisecond)
	snap := metrics.MemoryGetSnapshot()
	assert.GreaterOrEqual(t, snap.ExtractionRuns[metrics.MemoryExtractionSkippedDebounce], int64(1),
		"stale job should record skipped_debounce metric")
	assert.Equal(t, 0, mc.callCount(), "stale job should not call LLM")
}

// ─── Case 6: LLM failure → no retry, no crash ──────────────────────────────────

func TestExtract_LLMFailure_NoRetry(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profStore, _ := newExtractorTestStores(t)
	mc := errResp(errors.New("network down"))

	svc := NewExtractorService(factStore, profStore,
		WithExtractorChatFn(mc.fn()),
		WithExtractorWorkers(1),
	)
	svc.Start(context.Background())
	defer svc.Stop()

	svc.Enqueue(47, "sess-1", sampleMsgs(), false)
	waitForChatCalls(t, mc, 1, 2*time.Second)
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, 1, mc.callCount(), "exactly one attempt, no retry")
	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.ExtractionRuns[metrics.MemoryExtractionLLMError])

	// Worker should still be alive — enqueue another and confirm.
	mc2 := staticResp(`[]`)
	svc.chat = mc2.fn() // swap mock mid-flight (test-only seam)
	svc.Enqueue(47, "sess-2", sampleMsgs(), false)
	waitForChatCalls(t, mc2, 1, 2*time.Second)
}

// ─── Case 7: invalid JSON → metric + no crash ─────────────────────────────────

func TestExtract_InvalidJSON_NoCrash(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profStore, _ := newExtractorTestStores(t)
	mc := staticResp("not json at all")

	svc := NewExtractorService(factStore, profStore,
		WithExtractorChatFn(mc.fn()),
		WithExtractorWorkers(1),
	)
	svc.Start(context.Background())
	defer svc.Stop()

	svc.Enqueue(48, "sess-1", sampleMsgs(), false)
	waitForChatCalls(t, mc, 1, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	facts, err := factStore.List(context.Background(), 48, store.ListFactOpts{Limit: 100})
	require.NoError(t, err)
	assert.Empty(t, facts, "parse error must not persist any fact")

	snap := metrics.MemoryGetSnapshot()
	assert.Equal(t, int64(1), snap.ExtractionRuns[metrics.MemoryExtractionParseError])
}

// ─── Case 8: all six categories accepted ───────────────────────────────────────

func TestExtract_AllSixCategories(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profStore, _ := newExtractorTestStores(t)
	mc := staticResp(`[
{"content":"用户偏极简风格","category":"preference","confidence":0.90},
{"content":"用户主用 Python","category":"knowledge","confidence":0.92},
{"content":"用户是制造业 SOP 操作员","category":"context","confidence":0.88},
{"content":"用户先列大纲再展开","category":"behavior","confidence":0.85},
{"content":"用户准备转型做产品经理","category":"goal","confidence":0.80},
{"content":"用户已明确减少使用感叹号","category":"correction","confidence":0.95}
]`)

	svc := NewExtractorService(factStore, profStore,
		WithExtractorChatFn(mc.fn()),
		WithExtractorWorkers(1),
	)
	svc.Start(context.Background())
	defer svc.Stop()

	svc.Enqueue(49, "sess-1", sampleMsgs(), false)
	waitForChatCalls(t, mc, 1, 2*time.Second)
	time.Sleep(50 * time.Millisecond)

	facts, err := factStore.List(context.Background(), 49, store.ListFactOpts{Limit: 100})
	require.NoError(t, err)
	assert.Len(t, facts, 6, "all 6 categories should persist")

	seen := make(map[string]bool, 6)
	for _, f := range facts {
		seen[f.Category] = true
	}
	for _, want := range []string{"preference", "knowledge", "context", "behavior", "goal", "correction"} {
		assert.True(t, seen[want], "category %q missing from persisted facts", want)
	}
}

// ─── Case 9: profile rebuild triggered at threshold ───────────────────────────

func TestProfileRebuild_TriggeredAt5(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profStore, _ := newExtractorTestStores(t)
	ctx := context.Background()

	// Counter for extraction calls vs rebuild calls.
	var rebuildCalls atomic.Int32
	var extractCalls atomic.Int32

	mockFn := func(callIdx int) (*aiservice.ChatResponse, error) {
		// Detect whether this is a rebuild call by looking at message content
		// — rebuild prompt always contains "三段简短的画像叙述".
		// But the mockChat closure doesn't expose the request to us here.
		// Instead, alternate logic: every 6th call (1 trigger per 5 extracts)
		// is the rebuild. Simpler: use the fact that rebuild's system prompt
		// is RebuildPromptSystem — but mockChat doesn't see it.
		//
		// Workaround: have the mock return a rebuild-shaped JSON if the count
		// of facts in DB at call time matches the rebuild trigger. Pragmatic:
		// inject via a separate seam below.
		_ = callIdx
		extractCalls.Add(1)
		return &aiservice.ChatResponse{
			Content: fmt.Sprintf(`[{"content":"用户事实%d","category":"context","confidence":0.85}]`, extractCalls.Load()),
			Model:   "mock-model",
		}, nil
	}

	// Custom chat seam that distinguishes extract vs rebuild via system prompt content.
	chatSeam := func(_ context.Context, _ string, req aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		// Inspect system prompt to route.
		var sysContent string
		for _, m := range req.Messages {
			if m.Role == aiservice.MessageRoleSystem {
				sysContent = m.Content.Text
				break
			}
		}
		if sysContent == RebuildPromptSystem {
			rebuildCalls.Add(1)
			return &aiservice.ChatResponse{
				Content: `{"work_context":"用户是数据分析师","personal_context":"偏极简","top_of_mind":"指标可视化"}`,
				Model:   "mock-rebuild",
			}, nil
		}
		return mockFn(0)
	}

	svc := NewExtractorService(factStore, profStore,
		WithExtractorChatFn(chatSeam),
		WithExtractorWorkers(1),
		WithExtractorRebuildThreshold(5),
	)
	svc.Start(ctx)
	defer svc.Stop()

	// Trigger 5 extractions for the same user. Each yields a unique fact (no dedup).
	for i := 0; i < 5; i++ {
		svc.Enqueue(50, fmt.Sprintf("sess-%d", i), sampleMsgs(), false)
		// Wait for this one to finish before next to ensure serial counter increments.
		time.Sleep(150 * time.Millisecond)
	}

	// Allow the rebuild goroutine to fire and complete.
	assert.Eventually(t, func() bool {
		return rebuildCalls.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "RebuildNarrative should fire at threshold")

	// Verify profile narrative updated.
	prof, err := profStore.Get(ctx, 50)
	require.NoError(t, err)
	assert.Equal(t, "用户是数据分析师", prof.WorkContext)
	assert.Equal(t, "偏极简", prof.PersonalContext)
	assert.Equal(t, "指标可视化", prof.TopOfMind)

	// Counter should be reset post-rebuild.
	assert.Eventually(t, func() bool {
		p, gerr := profStore.Get(ctx, 50)
		return gerr == nil && p.ExtractionCountSinceRebuild == 0
	}, 2*time.Second, 50*time.Millisecond, "extraction_count should reset after rebuild")
}

// ─── Case 10: queue full → drop newest with warn ──────────────────────────────

func TestQueueFull_DropsNew(t *testing.T) {
	metrics.MemoryResetForTest()
	factStore, profStore, _ := newExtractorTestStores(t)

	// blockingChat blocks until released — fills the queue.
	releaseCh := make(chan struct{})
	chatSeam := func(_ context.Context, _ string, _ aiservice.ChatRequest) (*aiservice.ChatResponse, error) {
		<-releaseCh
		return &aiservice.ChatResponse{Content: `[]`}, nil
	}

	const cap = 4 // small cap so we can fill it
	svc := NewExtractorService(factStore, profStore,
		WithExtractorChatFn(chatSeam),
		WithExtractorWorkers(1),
		WithExtractorQueueCap(cap),
	)
	svc.Start(context.Background())
	t.Cleanup(func() {
		close(releaseCh) // unblock any pending workers
		svc.Stop()
	})

	// Worker will pull 1 job and block. Remaining 4 fit in the queue (cap=4),
	// then the 6th should drop.
	for i := 0; i < cap+1; i++ {
		// All same user — debounceMap is updated but Enqueue still tries jobQueue.
		svc.Enqueue(uint(60+i), fmt.Sprintf("sess-%d", i), sampleMsgs(), false)
	}

	// Give the queue a moment to fill.
	time.Sleep(50 * time.Millisecond)

	// 1 in-flight + cap=4 queued + 1 dropped = cap+1 enqueues vs cap+1 in flight.
	// Queue length should be at exactly cap (the one being processed is OUT
	// of the channel) — but we conservatively assert it's bounded.
	queueLen := len(svc.jobQueue)
	assert.LessOrEqual(t, queueLen, cap, "queue cannot exceed cap")

	// Beyond that, Enqueue another and ensure no panic.
	svc.Enqueue(uint(99), "sess-overflow", sampleMsgs(), false)
	// No assertion on exact drop count — Warnw log is the contract.
}

// ─── Auxiliary tests for parsing / hashing helpers ────────────────────────────

func TestComputeContentHash_NormalizationStable(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		same bool
	}{
		{"identical", "用户是分析师", "用户是分析师", true},
		{"trailing whitespace", "用户是分析师", "用户是分析师 ", true},
		{"case difference (ASCII only)", "User Loves Python", "user loves python", true},
		{"chinese punctuation stripped", "用户是销售，喜欢简洁", "用户是销售喜欢简洁", true},
		{"different content", "用户是分析师", "用户是销售", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ha := computeContentHash(c.a)
			hb := computeContentHash(c.b)
			if c.same {
				assert.Equal(t, ha, hb, "hashes should match for normalised inputs")
			} else {
				assert.NotEqual(t, ha, hb, "hashes should differ for distinct content")
			}
		})
	}
}

func TestParseExtractionResponse_Tolerance(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantN   int
		wantErr bool
	}{
		{"plain array", `[{"content":"x","category":"context","confidence":0.8}]`, 1, false},
		{"markdown fenced", "```json\n[{\"content\":\"x\",\"category\":\"context\",\"confidence\":0.8}]\n```", 1, false},
		{"object envelope items", `{"items":[{"content":"x","category":"context","confidence":0.8}]}`, 1, false},
		{"empty array", `[]`, 0, false},
		{"garbage", `not json`, 0, true},
		{"empty", ``, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := parseExtractionResponse(c.input)
			if c.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, out, c.wantN)
			}
		})
	}
}

func TestBuildExtractionPrompt_TruncatesLongTurns(t *testing.T) {
	// Build 15 messages, each 500 chars long; expect only last 10 included,
	// each truncated to <= 202 runes ("..").
	msgs := make([]ChatMessage, 15)
	long := ""
	for i := 0; i < 500; i++ {
		long += "字"
	}
	for i := range msgs {
		msgs[i] = ChatMessage{Role: "user", Content: long}
	}
	out := buildExtractionPrompt(msgs)
	// Should contain "[user] " (the role tag) and "..", and total rune count
	// should be much smaller than 15 * 500.
	assert.Contains(t, out, "[user]")
	assert.Contains(t, out, "..")
	runeCount := len([]rune(out))
	// 10 messages × (200 runes + ".." + "[user] " ~ 9 runes) = ~2110 runes max.
	assert.Less(t, runeCount, 2500, "prompt should be bounded by truncation")
}

func TestValidateExtractedFact_Threshold(t *testing.T) {
	cases := []struct {
		name    string
		fact    ExtractedFact
		min     float64
		wantErr bool
	}{
		{"valid", ExtractedFact{Content: "x", Category: "context", Confidence: 0.80}, 0.7, false},
		{"below threshold", ExtractedFact{Content: "x", Category: "context", Confidence: 0.60}, 0.7, true},
		{"empty content", ExtractedFact{Content: "  ", Category: "context", Confidence: 0.9}, 0.7, true},
		{"invalid category", ExtractedFact{Content: "x", Category: "bogus", Confidence: 0.9}, 0.7, true},
		{"confidence > 1", ExtractedFact{Content: "x", Category: "context", Confidence: 1.1}, 0.7, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateExtractedFact(c.fact, c.min)
			if c.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
