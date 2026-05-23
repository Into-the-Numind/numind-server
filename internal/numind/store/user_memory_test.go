package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"numind-server/internal/pkg/errno"
	"numind-server/internal/pkg/model"
)

// ─── Test helpers ──────────────────────────────────────────────────────────────

// newTestUserMemoryStores builds in-memory SQLite + AutoMigrate'd memory tables
// and returns the two store implementations.
func newTestUserMemoryStores(t *testing.T) (IUserMemoryProfileStore, IUserMemoryFactStore, *gorm.DB) {
	t.Helper()
	db := newTestDB(t, &model.UserMemoryProfile{}, &model.UserMemoryFact{})
	return NewUserMemoryProfileStore(db), NewUserMemoryFactStore(db), db
}

// sampleFact constructs a valid V1.5 Layer A fact (SubjectID=nil).
func sampleFact(userID uint, uuid, content, category string, confidence float64) *model.UserMemoryFact {
	return &model.UserMemoryFact{
		UUID:              uuid,
		UserID:            userID,
		SubjectID:         nil, // V1.5 Layer A: always nil
		Content:           content,
		Category:          category,
		Confidence:        confidence,
		Importance:        0.50,
		SourceSessionID:   "sess-1",
		SourceMessageUUID: "msg-1",
		SourceExtractedAt: time.Now(),
		EmbeddingHash:     "",
		IsArchived:        false,
	}
}

// ptr returns a pointer to its argument; used to construct V2 Layer B test values.
func ptr[T any](v T) *T { return &v }

// ─── Case 1: profile CRUD ─────────────────────────────────────────────────────

// TestUserMemory_ProfileCRUD verifies Upsert creates / updates and Get returns the latest.
func TestUserMemory_ProfileCRUD(t *testing.T) {
	ps, _, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	// Get on missing → ErrRecordNotFound
	_, err := ps.Get(ctx, 100)
	require.Error(t, err)
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound), "Get on missing profile must return ErrRecordNotFound; got %v", err)

	// Upsert new
	p := &model.UserMemoryProfile{
		UserID:          100,
		WorkContext:     "医疗器械销售",
		PersonalContext: "偏好简洁话术",
	}
	require.NoError(t, ps.Upsert(ctx, p))

	got, err := ps.Get(ctx, 100)
	require.NoError(t, err)
	assert.Equal(t, "医疗器械销售", got.WorkContext)
	assert.Equal(t, "偏好简洁话术", got.PersonalContext)

	// Upsert update
	p2 := &model.UserMemoryProfile{
		UserID:          100,
		WorkContext:     "医疗器械销售-资深",
		PersonalContext: "偏好简洁话术",
		TopOfMind:       "跟进XX医院CT订单",
	}
	require.NoError(t, ps.Upsert(ctx, p2))

	got, err = ps.Get(ctx, 100)
	require.NoError(t, err)
	assert.Equal(t, "医疗器械销售-资深", got.WorkContext)
	assert.Equal(t, "跟进XX医院CT订单", got.TopOfMind)
}

// ─── Case 2: fact CRUD + total_facts maintenance ─────────────────────────────

// TestUserMemory_FactCRUD_TotalFactsIncrement verifies Create / BatchCreate
// trigger same-tx total_facts increment.
func TestUserMemory_FactCRUD_TotalFactsIncrement(t *testing.T) {
	ps, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	// Create single → total_facts = 1
	f1 := sampleFact(100, "uuid-1", "user is a medical sales rep", "knowledge", 0.95)
	require.NoError(t, fs.Create(ctx, f1))
	require.NotZero(t, f1.ID, "Create must populate ID")

	got, err := fs.GetByUUID(ctx, "uuid-1")
	require.NoError(t, err)
	assert.Equal(t, "user is a medical sales rep", got.Content)

	p, err := ps.Get(ctx, 100)
	require.NoError(t, err)
	assert.Equal(t, 1, p.TotalFacts, "total_facts must be 1 after single Create")

	// BatchCreate 3 → total_facts += 3
	batch := []model.UserMemoryFact{
		*sampleFact(100, "uuid-2", "prefers terse", "preference", 0.85),
		*sampleFact(100, "uuid-3", "tracking XX hospital", "context", 0.78),
		*sampleFact(100, "uuid-4", "goal: hit Q2 quota", "goal", 0.80),
	}
	require.NoError(t, fs.BatchCreate(ctx, batch))

	p, err = ps.Get(ctx, 100)
	require.NoError(t, err)
	assert.Equal(t, 4, p.TotalFacts, "total_facts must be 4 after Create+BatchCreate(3)")
}

// ─── Case 3: List opts (categories, MinConfidence, IncludeArchived) ──────────

// TestUserMemory_ListOpts verifies category / confidence filters and archived hiding.
func TestUserMemory_ListOpts(t *testing.T) {
	ps, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	// 10 facts, mixed confidence (0.60-0.95), 3 categories.
	facts := []model.UserMemoryFact{
		*sampleFact(1, "u-01", "pref-low", "preference", 0.62),
		*sampleFact(1, "u-02", "pref-mid", "preference", 0.75),
		*sampleFact(1, "u-03", "pref-high", "preference", 0.90),
		*sampleFact(1, "u-04", "know-low", "knowledge", 0.65),
		*sampleFact(1, "u-05", "know-mid", "knowledge", 0.80),
		*sampleFact(1, "u-06", "know-high", "knowledge", 0.95),
		*sampleFact(1, "u-07", "ctx-low", "context", 0.60),
		*sampleFact(1, "u-08", "ctx-mid", "context", 0.78),
		*sampleFact(1, "u-09", "ctx-high", "context", 0.88),
		*sampleFact(1, "u-10", "ctx-extra", "context", 0.92),
	}
	require.NoError(t, fs.BatchCreate(ctx, facts))

	// Sanity: total_facts == 10 after BatchCreate
	p, err := ps.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 10, p.TotalFacts, "total_facts must be 10 after BatchCreate(10)")

	// MinConfidence=0.80 → 5 hits (0.90, 0.80, 0.95, 0.88, 0.92)
	hi, err := fs.List(ctx, 1, ListFactOpts{MinConfidence: 0.80})
	require.NoError(t, err)
	assert.Len(t, hi, 5, "MinConfidence=0.80 should return 5 facts with confidence>=0.80")
	for _, f := range hi {
		assert.GreaterOrEqual(t, f.Confidence, 0.80)
	}

	// Categories=["preference"] → 3 hits
	prefs, err := fs.List(ctx, 1, ListFactOpts{Categories: []string{"preference"}})
	require.NoError(t, err)
	assert.Len(t, prefs, 3, "preference category should return 3 facts")
	for _, f := range prefs {
		assert.Equal(t, "preference", f.Category)
	}

	// IncludeArchived=false (default) hides archived rows.
	// Archive one fact via UUID lookup (single source of truth for the row's ID).
	// idempotency tested separately in TestUserMemory_Archive_Idempotent.
	first, err := fs.GetByUUID(ctx, "u-01")
	require.NoError(t, err)
	require.NoError(t, fs.Archive(ctx, first.ID))

	all, err := fs.List(ctx, 1, ListFactOpts{IncludeArchived: false, Limit: 100})
	require.NoError(t, err)
	for _, f := range all {
		assert.False(t, f.IsArchived, "default List must hide archived")
	}

	allInc, err := fs.List(ctx, 1, ListFactOpts{IncludeArchived: true, Limit: 100})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(allInc), len(all)+1, "IncludeArchived=true should include archived rows")

	// total_facts must decrement by exactly 1 after archiving 1 alive row.
	p, err = ps.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 9, p.TotalFacts, "total_facts must be 10-1=9 after archiving one alive fact")
}

// ─── Case 4: OrderBy whitelist (defense in depth against SQL injection) ──────

// TestUserMemory_ListOrderByWhitelist confirms only "confidence" / "recency" /
// "importance" are honored; everything else falls back silently.
func TestUserMemory_ListOrderByWhitelist(t *testing.T) {
	_, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	require.NoError(t, fs.Create(ctx, sampleFact(1, "fa", "a", "knowledge", 0.85)))
	require.NoError(t, fs.Create(ctx, sampleFact(1, "fb", "b", "knowledge", 0.95)))
	require.NoError(t, fs.Create(ctx, sampleFact(1, "fc", "c", "knowledge", 0.75)))

	// Whitelisted: confidence DESC → expect b, a, c
	out, err := fs.List(ctx, 1, ListFactOpts{OrderBy: "confidence"})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(out), 3)
	assert.Equal(t, "b", out[0].Content, "confidence order: highest first")

	// Whitelisted: importance / recency must not error
	_, err = fs.List(ctx, 1, ListFactOpts{OrderBy: "recency"})
	require.NoError(t, err)
	_, err = fs.List(ctx, 1, ListFactOpts{OrderBy: "importance"})
	require.NoError(t, err)

	// SQL-injection attempt → silently falls back to confidence (no error, no panic)
	injected, err := fs.List(ctx, 1, ListFactOpts{OrderBy: "content; DROP TABLE user_memory_facts; --"})
	require.NoError(t, err, "non-whitelisted OrderBy must NOT error (silent fallback to confidence)")
	require.GreaterOrEqual(t, len(injected), 3, "table must still exist after injection attempt")
	assert.Equal(t, "b", injected[0].Content, "fallback order = confidence DESC")
}

// ─── Case 5: UpdateUsage batch ────────────────────────────────────────────────

// TestUserMemory_UpdateUsage_Batch verifies batch last_used_at + use_count++ update.
func TestUserMemory_UpdateUsage_Batch(t *testing.T) {
	_, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	f1 := sampleFact(1, "uuid-a", "a", "knowledge", 0.85)
	f2 := sampleFact(1, "uuid-b", "b", "knowledge", 0.85)
	f3 := sampleFact(1, "uuid-c", "c", "knowledge", 0.85)
	require.NoError(t, fs.Create(ctx, f1))
	require.NoError(t, fs.Create(ctx, f2))
	require.NoError(t, fs.Create(ctx, f3))

	before := time.Now()
	require.NoError(t, fs.UpdateUsage(ctx, []uint64{f1.ID, f2.ID, f3.ID}))

	for _, uuid := range []string{"uuid-a", "uuid-b", "uuid-c"} {
		got, err := fs.GetByUUID(ctx, uuid)
		require.NoError(t, err)
		require.NotNil(t, got.LastUsedAt, "last_used_at must be set after UpdateUsage")
		assert.WithinDuration(t, before, *got.LastUsedAt, 5*time.Second)
		assert.Equal(t, 1, got.UseCount, "use_count must increment by 1")
	}

	// Empty IDs → no-op (no error)
	require.NoError(t, fs.UpdateUsage(ctx, []uint64{}))
}

// ─── Case 6: BulkArchiveByConfidence + total_facts decrement ─────────────────

// TestUserMemory_BulkArchiveByConfidence archives facts below threshold and
// decrements total_facts accordingly.
func TestUserMemory_BulkArchiveByConfidence(t *testing.T) {
	ps, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	// 5 facts: 0.65, 0.72, 0.85, 0.90, 0.55
	facts := []model.UserMemoryFact{
		*sampleFact(1, "u-65", "low", "knowledge", 0.65),
		*sampleFact(1, "u-72", "border-pass", "knowledge", 0.72),
		*sampleFact(1, "u-85", "mid", "knowledge", 0.85),
		*sampleFact(1, "u-90", "high", "knowledge", 0.90),
		*sampleFact(1, "u-55", "very-low", "knowledge", 0.55),
	}
	require.NoError(t, fs.BatchCreate(ctx, facts))

	p, err := ps.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 5, p.TotalFacts)

	// threshold=0.70 → archive 0.65 and 0.55 = 2 rows
	n, err := fs.BulkArchiveByConfidence(ctx, 1, 0.70)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "expected 2 facts with confidence < 0.70")

	p, err = ps.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 3, p.TotalFacts, "total_facts must be 5-2=3 after BulkArchive")

	// Verify the right rows were archived
	c, err := fs.CountByUser(ctx, 1, false)
	require.NoError(t, err)
	assert.Equal(t, 3, c, "alive count must be 3")
	cAll, err := fs.CountByUser(ctx, 1, true)
	require.NoError(t, err)
	assert.Equal(t, 5, cAll, "total count including archived must be 5")

	// GDPR-style sweep: threshold>1.0 → archive all remaining alive
	n2, err := fs.BulkArchiveByConfidence(ctx, 1, 1.01)
	require.NoError(t, err)
	assert.Equal(t, 3, n2, "GDPR sweep should archive remaining 3")

	p, err = ps.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 0, p.TotalFacts, "after GDPR sweep total_facts=0")
}

// ─── Case 7: dedup via embedding_hash ────────────────────────────────────────

// TestUserMemory_DedupEmbeddingHash verifies FindByEmbedHash finds same user's
// fact but NOT cross-user.
func TestUserMemory_DedupEmbeddingHash(t *testing.T) {
	_, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	f := sampleFact(1, "uuid-a", "content", "knowledge", 0.85)
	f.EmbeddingHash = "abc123"
	require.NoError(t, fs.Create(ctx, f))

	// Same user, same hash → hit
	got, err := fs.FindByEmbedHash(ctx, 1, "abc123")
	require.NoError(t, err)
	assert.Equal(t, "uuid-a", got.UUID)

	// Different user, same hash → miss
	_, err = fs.FindByEmbedHash(ctx, 2, "abc123")
	require.Error(t, err)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound), "different user must not see another's hash")

	// Same user, different hash → miss
	_, err = fs.FindByEmbedHash(ctx, 1, "different")
	require.Error(t, err)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))

	// Empty hash → miss (defensive)
	_, err = fs.FindByEmbedHash(ctx, 1, "")
	require.Error(t, err)
}

// ─── Case 8: Cross-user isolation ────────────────────────────────────────────

// TestUserMemory_CrossUserIsolation verifies List / CountByUser are strictly
// scoped to userID (D7: B2B2C 父子完全隔离).
func TestUserMemory_CrossUserIsolation(t *testing.T) {
	_, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	// User A: 5 facts; User B: 0.
	for i := 0; i < 5; i++ {
		require.NoError(t, fs.Create(ctx, sampleFact(100, fmt.Sprintf("a-%d", i), "content", "knowledge", 0.85)))
	}

	listB, err := fs.List(ctx, 200, ListFactOpts{Limit: 100})
	require.NoError(t, err)
	assert.Empty(t, listB, "user 200 must not see user 100's facts")

	countB, err := fs.CountByUser(ctx, 200, true)
	require.NoError(t, err)
	assert.Equal(t, 0, countB, "user 200 count must be 0")

	countA, err := fs.CountByUser(ctx, 100, false)
	require.NoError(t, err)
	assert.Equal(t, 5, countA, "user 100 count must be 5")
}

// ─── Case 9: GDPR cascade ────────────────────────────────────────────────────

// TestUserMemory_GDPR_ArchiveAllForUser exercises the GDPR "forget me" path:
// BulkArchiveByConfidence(userID, 1.01) archives all rows for that user.
//
// SQLite does NOT enforce FK CASCADE by default. The real CASCADE behavior is
// validated against MySQL in dev/prod; here we test the application-level
// "forget me" path (which is the actual code path used by /v1/users/me/forget).
func TestUserMemory_GDPR_ArchiveAllForUser(t *testing.T) {
	ps, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		require.NoError(t, fs.Create(ctx, sampleFact(100, fmt.Sprintf("g-%d", i), "content", "knowledge", 0.85)))
	}
	pre, err := fs.CountByUser(ctx, 100, false)
	require.NoError(t, err)
	assert.Equal(t, 3, pre)

	// "Forget me" sweep: threshold > 1.0 archives all alive
	n, err := fs.BulkArchiveByConfidence(ctx, 100, 1.01)
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	alive, err := fs.CountByUser(ctx, 100, false)
	require.NoError(t, err)
	assert.Equal(t, 0, alive, "no alive facts after forget-me sweep")

	p, err := ps.Get(ctx, 100)
	require.NoError(t, err)
	assert.Equal(t, 0, p.TotalFacts, "total_facts driven to 0 after forget-me sweep")
}

// ─── Case 10: default:false bool safety (database.md §6) ─────────────────────

// TestUserMemory_DefaultFalseBoolSafety verifies that IsArchived=false created
// via Create persists as DB row is_archived=false (no `default:true` gotcha).
func TestUserMemory_DefaultFalseBoolSafety(t *testing.T) {
	_, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	f := sampleFact(1, "uuid-b", "fresh fact", "knowledge", 0.85)
	require.False(t, f.IsArchived, "test precondition: IsArchived starts as false")
	require.NoError(t, fs.Create(ctx, f))

	got, err := fs.GetByUUID(ctx, "uuid-b")
	require.NoError(t, err)
	assert.False(t, got.IsArchived, "is_archived=false must persist as false (no default:true gotcha)")
}

// ─── Case 11: Concurrent IncrTotalFacts ──────────────────────────────────────

// TestUserMemory_ConcurrentIncrTotalFacts verifies that 10 goroutines concurrently
// Create'ing produce total_facts == 10 (no lost updates).
func TestUserMemory_ConcurrentIncrTotalFacts(t *testing.T) {
	ps, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	const N = 10
	var wg sync.WaitGroup
	errCh := make(chan error, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f := sampleFact(42, fmt.Sprintf("c-%d", i), "concurrent fact", "knowledge", 0.85)
			if err := fs.Create(ctx, f); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent Create failed: %v", err)
	}

	p, err := ps.Get(ctx, 42)
	require.NoError(t, err)
	assert.Equal(t, N, p.TotalFacts, "concurrent IncrTotalFacts must NOT lose updates")

	count, err := fs.CountByUser(ctx, 42, false)
	require.NoError(t, err)
	assert.Equal(t, N, count, "actual row count matches counter")
}

// ─── Case 11b: GetByIDs (Task 3.4 selector cache hit path) ───────────────────

// TestUserMemory_GetByIDs verifies the new batch-fetch method used by Task 3.4
// SelectorService when a 30s LRU cache hit returns a set of fact IDs.
// Spec: 03-memory/task-04-top5-selector.md §Step 1.
func TestUserMemory_GetByIDs(t *testing.T) {
	_, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	// Seed 5 facts for user 77.
	created := make([]uint64, 0, 5)
	for i := 0; i < 5; i++ {
		f := sampleFact(77, fmt.Sprintf("g-%d", i), fmt.Sprintf("content-%d", i), "context", 0.85-float64(i)*0.05)
		require.NoError(t, fs.Create(ctx, f))
		created = append(created, f.ID)
	}

	// Case A: empty ids → empty slice (no SQL).
	got, err := fs.GetByIDs(ctx, 77, nil)
	require.NoError(t, err)
	assert.Empty(t, got)

	// Case B: exact ids preserve order.
	wantOrder := []uint64{created[3], created[0], created[4], created[1]}
	got, err = fs.GetByIDs(ctx, 77, wantOrder)
	require.NoError(t, err)
	require.Len(t, got, 4)
	for i, id := range wantOrder {
		assert.Equal(t, id, got[i].ID, "order must match input ids")
	}

	// Case C: mix of known + unknown — unknown silently dropped.
	mixed := []uint64{created[2], 99999, created[0]}
	got, err = fs.GetByIDs(ctx, 77, mixed)
	require.NoError(t, err)
	require.Len(t, got, 2, "unknown IDs silently dropped")
	assert.Equal(t, created[2], got[0].ID)
	assert.Equal(t, created[0], got[1].ID)
}

// TestUserMemory_GetByIDs_CrossUserIsolation verifies the defense-in-depth
// user_id filter on GetByIDs: passing IDs that belong to another user must
// silently drop them, not leak cross-user facts.
//
// Reason: cache key in SelectorService already binds (userID:inputHash), so
// the cache-hit path is safe today. But this test pins the contract — future
// callers that pass GetByIDs(ctx, X, fromSomewhereElse) get cross-user defense
// for free.
func TestUserMemory_GetByIDs_CrossUserIsolation(t *testing.T) {
	_, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	// Seed user 100 with 2 facts.
	u100 := []uint64{}
	for i := 0; i < 2; i++ {
		f := sampleFact(100, fmt.Sprintf("u100-%d", i), fmt.Sprintf("u100 content %d", i), "context", 0.80)
		require.NoError(t, fs.Create(ctx, f))
		u100 = append(u100, f.ID)
	}
	// Seed user 200 with 2 facts.
	u200 := []uint64{}
	for i := 0; i < 2; i++ {
		f := sampleFact(200, fmt.Sprintf("u200-%d", i), fmt.Sprintf("u200 content %d", i), "context", 0.80)
		require.NoError(t, fs.Create(ctx, f))
		u200 = append(u200, f.ID)
	}

	// Caller passes user 100's id list but with userID=200 → must return empty.
	got, err := fs.GetByIDs(ctx, 200, u100)
	require.NoError(t, err)
	assert.Empty(t, got, "user 200 must not see user 100's facts")

	// Caller mixes user 100 and user 200 ids, queries as user 100 → only 100's facts.
	mixed := []uint64{u100[0], u200[0], u100[1], u200[1]}
	got, err = fs.GetByIDs(ctx, 100, mixed)
	require.NoError(t, err)
	require.Len(t, got, 2, "user 100 only sees own facts in mixed query")
	for _, f := range got {
		assert.Equal(t, uint(100), f.UserID, "row must belong to user 100")
	}
	// Order preserved: u100[0] first, u100[1] second.
	assert.Equal(t, u100[0], got[0].ID)
	assert.Equal(t, u100[1], got[1].ID)
}

// ─── Case 12: Category CHECK constraint (MySQL only — skipped in SQLite) ─────

// TestUserMemory_CategoryCheckConstraint_MySQLOnly documents the CHECK constraint
// behavior under MySQL. SQLite parses CHECK but pre-3.25 does not enforce it for
// all paths, so we skip here. Verified manually via dev migration.
func TestUserMemory_CategoryCheckConstraint_MySQLOnly(t *testing.T) {
	t.Skip("SQLite (AutoMigrate path) does not enforce CHECK on category enum; verified on dev MySQL after migration.")
}

// ─── Case 13: V1.5 enforces subject_id = NULL ────────────────────────────────

// TestUserMemory_V15_RejectsSubjectID verifies Create/BatchCreate fail with
// ErrLayerBNotSupported when SubjectID is non-nil; and succeed when nil.
func TestUserMemory_V15_RejectsSubjectID(t *testing.T) {
	_, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	// Create with non-nil SubjectID → ErrLayerBNotSupported
	bad := sampleFact(1, "uuid-bad", "should fail", "knowledge", 0.85)
	bad.SubjectID = ptr("cust-123")
	err := fs.Create(ctx, bad)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrLayerBNotSupported), "Create must return ErrLayerBNotSupported on non-nil SubjectID; got %v", err)

	// BatchCreate with one non-nil SubjectID rejects whole batch
	batch := []model.UserMemoryFact{
		*sampleFact(1, "uuid-a", "good", "knowledge", 0.85),
		*sampleFact(1, "uuid-b", "good-too", "knowledge", 0.85),
	}
	batch[1].SubjectID = ptr("cust-999")
	err = fs.BatchCreate(ctx, batch)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errno.ErrLayerBNotSupported), "BatchCreate must reject whole batch on any non-nil SubjectID")

	// Verify nothing was written (rejected before INSERT)
	c, err := fs.CountByUser(ctx, 1, true)
	require.NoError(t, err)
	assert.Equal(t, 0, c, "rejected BatchCreate must not insert any rows")

	// SubjectID=nil → success
	good := sampleFact(1, "uuid-good", "good", "knowledge", 0.85)
	good.SubjectID = nil
	require.NoError(t, fs.Create(ctx, good))

	got, err := fs.GetByUUID(ctx, "uuid-good")
	require.NoError(t, err)
	assert.Nil(t, got.SubjectID, "DB row subject_id must persist as NULL")
}

// ─── Case 14: B2B2C parent/child isolation (D7) ──────────────────────────────

// TestUserMemory_D7_ParentChildIsolation verifies parent's List/Count does NOT
// include child user's facts. (The store interface signatures only take userID;
// the compile-time guarantee is checked via the absence of parentUserID in
// IUserMemoryProfileStore / IUserMemoryFactStore — see user_memory.go.)
func TestUserMemory_D7_ParentChildIsolation(t *testing.T) {
	_, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	// Parent userID=100 stores 2 facts.
	require.NoError(t, fs.Create(ctx, sampleFact(100, "p-1", "parent fact 1", "knowledge", 0.85)))
	require.NoError(t, fs.Create(ctx, sampleFact(100, "p-2", "parent fact 2", "knowledge", 0.85)))

	// Child userID=101 stores 3 facts (in real B2B2C, child has parent_user_id=100;
	// but memory schema deliberately does NOT carry that relationship).
	require.NoError(t, fs.Create(ctx, sampleFact(101, "c-1", "child fact 1", "preference", 0.85)))
	require.NoError(t, fs.Create(ctx, sampleFact(101, "c-2", "child fact 2", "preference", 0.85)))
	require.NoError(t, fs.Create(ctx, sampleFact(101, "c-3", "child fact 3", "preference", 0.85)))

	// Parent List → only parent's 2 facts; NEVER child's.
	parentList, err := fs.List(ctx, 100, ListFactOpts{Limit: 100})
	require.NoError(t, err)
	assert.Len(t, parentList, 2, "parent (user 100) must see exactly 2 facts; NOT see child's facts")
	for _, f := range parentList {
		assert.Equal(t, uint(100), f.UserID, "parent List must only contain parent's facts")
	}

	// Child List → only child's 3 facts.
	childList, err := fs.List(ctx, 101, ListFactOpts{Limit: 100})
	require.NoError(t, err)
	assert.Len(t, childList, 3, "child (user 101) must see exactly 3 facts")
	for _, f := range childList {
		assert.Equal(t, uint(101), f.UserID, "child List must only contain child's facts")
	}

	// Compile-time invariant: the interface signatures take userID only.
	// Demonstrated by the fact that this test never passes parentUserID anywhere.
	// (Adding parentUserID-aware signatures would be a breaking API change requiring
	// product review per the D7 spec — see user_memory.go interface doc.)

	// Cross-check counts
	pc, err := fs.CountByUser(ctx, 100, false)
	require.NoError(t, err)
	assert.Equal(t, 2, pc)
	cc, err := fs.CountByUser(ctx, 101, false)
	require.NoError(t, err)
	assert.Equal(t, 3, cc)
}

// ─── Bonus: UpdateCachedInsight + UpdateImportance + Archive idempotent ──────

// TestUserMemory_UpdateCachedInsight verifies dialectic cache field update.
func TestUserMemory_UpdateCachedInsight(t *testing.T) {
	ps, _, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	require.NoError(t, ps.Upsert(ctx, &model.UserMemoryProfile{UserID: 100}))

	require.NoError(t, ps.UpdateCachedInsight(ctx, 100, "user is a senior sales rep with strong domain knowledge", 7))

	got, err := ps.Get(ctx, 100)
	require.NoError(t, err)
	assert.Equal(t, "user is a senior sales rep with strong domain knowledge", got.CachedInsight)
	assert.Equal(t, 7, got.CachedInsightFactCount)
	require.NotNil(t, got.CachedInsightAt)
}

// TestUserMemory_UpdateImportance verifies importance update.
func TestUserMemory_UpdateImportance(t *testing.T) {
	_, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	f := sampleFact(1, "uuid-imp", "fact", "knowledge", 0.85)
	require.NoError(t, fs.Create(ctx, f))

	require.NoError(t, fs.UpdateImportance(ctx, f.ID, 0.95))

	got, err := fs.GetByUUID(ctx, "uuid-imp")
	require.NoError(t, err)
	assert.InDelta(t, 0.95, got.Importance, 0.001)

	// Non-existent → ErrRecordNotFound
	err = fs.UpdateImportance(ctx, 99999, 0.50)
	require.Error(t, err)
	assert.True(t, errors.Is(err, gorm.ErrRecordNotFound))
}

// TestUserMemory_Archive_Idempotent verifies double-Archive does not double-decrement total_facts.
func TestUserMemory_Archive_Idempotent(t *testing.T) {
	ps, fs, _ := newTestUserMemoryStores(t)
	ctx := context.Background()

	f := sampleFact(1, "uuid-arch", "fact", "knowledge", 0.85)
	require.NoError(t, fs.Create(ctx, f))

	p, err := ps.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, p.TotalFacts)

	require.NoError(t, fs.Archive(ctx, f.ID))
	p, err = ps.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 0, p.TotalFacts, "first archive decrements total_facts to 0")

	// Second archive → no-op (must not double-decrement)
	require.NoError(t, fs.Archive(ctx, f.ID))
	p, err = ps.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, 0, p.TotalFacts, "second archive must NOT decrement again (idempotent)")
}

// ─── Case 13: CountFactsByUserInRange (Task 3.8 daily digest) ────────────────

// TestUserMemory_CountFactsByUserInRange verifies the date-windowed count
// excludes facts outside the window, archived facts, and other users' facts.
// Used by daily digest to compute extracted_facts_count efficiently
// (DB-side filter, no List+client-side scan).
func TestUserMemory_CountFactsByUserInRange(t *testing.T) {
	_, fs, db := newTestUserMemoryStores(t)
	ctx := context.Background()

	base := time.Date(2026, 5, 22, 0, 0, 0, 0, time.UTC)

	// Seed 4 alive facts for user 50 at well-spaced timestamps so each lives in
	// a distinct hour bucket — easy to verify range slicing.
	mkFact := func(uuid string, userID uint, at time.Time, archived bool) {
		f := sampleFact(userID, uuid, "content", "knowledge", 0.85)
		f.SourceExtractedAt = at
		f.IsArchived = archived
		// Bypass Create to set IsArchived directly (Create defaults to false).
		require.NoError(t, db.Create(f).Error)
	}
	mkFact("u50-08", 50, base.Add(8*time.Hour), false)   // 08:00 — in window
	mkFact("u50-10", 50, base.Add(10*time.Hour), false)  // 10:00 — in window
	mkFact("u50-22", 50, base.Add(22*time.Hour), false)  // 22:00 — in window
	mkFact("u50-arc", 50, base.Add(11*time.Hour), true)  // 11:00 — archived (excluded)
	mkFact("u51-09", 51, base.Add(9*time.Hour), false)   // wrong user (excluded)
	mkFact("u50-pre", 50, base.Add(-1*time.Hour), false) // before window
	mkFact("u50-pst", 50, base.Add(25*time.Hour), false) // after window

	// Window: full target day [base, base+24h).
	n, err := fs.CountFactsByUserInRange(ctx, 50, base, base.Add(24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(3), n, "3 alive in-window facts for user 50")

	// Narrower window: [base+9h, base+12h) → only the 10:00 fact (08:00 below, 11:00 archived, 22:00 above).
	n, err = fs.CountFactsByUserInRange(ctx, 50, base.Add(9*time.Hour), base.Add(12*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "only u50-10 falls in [09:00, 12:00)")

	// User without facts → 0.
	n, err = fs.CountFactsByUserInRange(ctx, 99, base, base.Add(24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}
