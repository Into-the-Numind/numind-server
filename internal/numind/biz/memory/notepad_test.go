package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/numind/store"
	"numind-server/internal/pkg/model"
)

// newTestDB creates a file-backed SQLite DB with WAL mode and AutoMigrates the given models.
// File DB (not :memory:) ensures cross-goroutine isolation when -race is on.
func newTestDB(t *testing.T, models ...interface{}) *gorm.DB {
	t.Helper()
	tmp := t.TempDir()
	dsn := tmp + "/test.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(models...))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// newTestNotepad returns a fresh Notepad backed by an in-process SQLite DB.
func newTestNotepad(t *testing.T) Notepad {
	t.Helper()
	db := newTestDB(t, &model.UserGlobalMemory{})
	return NewNotepad(store.NewUserGlobalMemoryStore(db))
}

func ptr[T any](v T) *T { return &v }

// ---------------------------------------------------------------------------
// Write tests
// ---------------------------------------------------------------------------

// TestNotepad_Write_HappyPath verifies that a valid Write call persists the
// HTML-escaped value and returns no error.
func TestNotepad_Write_HappyPath(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	err := np.Write(ctx, 1, KindFact, "lang", "<Go> & Rust", WriteOpts{})
	require.NoError(t, err)

	item, err := np.Read(ctx, 1, "lang")
	require.NoError(t, err)
	require.NotNil(t, item)
	// Value stored via EscapeForStorage — HTML entities present
	assert.Equal(t, EscapeForStorage("<Go> & Rust"), item.Content)
}

// TestNotepad_Write_KindInvalid verifies that kind=summary (L1-only) is rejected.
func TestNotepad_Write_KindInvalid(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	err := np.Write(ctx, 1, KindSummary, "k", "v", WriteOpts{})
	require.ErrorIs(t, err, ErrMemoryKindInvalid)
}

// TestNotepad_Write_KeyTooLong verifies that a key >100 chars is rejected.
func TestNotepad_Write_KeyTooLong(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	longKey := strings.Repeat("a", 101)
	err := np.Write(ctx, 1, KindFact, longKey, "v", WriteOpts{})
	require.ErrorIs(t, err, ErrMemoryKeyTooLong)
}

// TestNotepad_Write_ValueTooLong verifies that a value >1024 chars is rejected.
func TestNotepad_Write_ValueTooLong(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	longVal := strings.Repeat("x", 1025)
	err := np.Write(ctx, 1, KindFact, "k", longVal, WriteOpts{})
	require.ErrorIs(t, err, ErrMemoryValueTooLong)
}

// TestNotepad_Write_UserRequired verifies that userID=0 is rejected.
func TestNotepad_Write_UserRequired(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	err := np.Write(ctx, 0, KindFact, "k", "v", WriteOpts{})
	require.ErrorIs(t, err, ErrMemoryUserRequired)
}

// TestNotepad_Write_Upsert verifies that writing the same key twice results in
// exactly one row with the updated value.
func TestNotepad_Write_Upsert(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	require.NoError(t, np.Write(ctx, 1, KindFact, "greeting", "hello", WriteOpts{}))
	require.NoError(t, np.Write(ctx, 1, KindFact, "greeting", "world", WriteOpts{}))

	items, err := np.ListByKind(ctx, 1, KindFact, 10)
	require.NoError(t, err)
	assert.Len(t, items, 1, "Upsert should not create a second row for the same key")
	assert.Equal(t, EscapeForStorage("world"), items[0].Content)
}

// TestNotepad_Write_ConfidenceZero verifies that an explicit confidence=0.0 is
// persisted as-is (P2-2 decision: Confidence==&0.0 is a valid low-confidence value).
func TestNotepad_Write_ConfidenceZero(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	err := np.Write(ctx, 1, KindFact, "low-conf", "uncertain value", WriteOpts{
		Confidence: ptr(0.0),
	})
	require.NoError(t, err)

	item, err := np.Read(ctx, 1, "low-conf")
	require.NoError(t, err)
	require.NotNil(t, item)
	assert.Equal(t, 0.0, item.Confidence, "explicit confidence=0.0 must be stored as 0.0, not defaulted to 1.0")
}

// ---------------------------------------------------------------------------
// Read tests
// ---------------------------------------------------------------------------

// TestNotepad_Read_HappyPath verifies a successful Read after Write.
func TestNotepad_Read_HappyPath(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	require.NoError(t, np.Write(ctx, 42, KindPreference, "theme", "dark", WriteOpts{}))

	item, err := np.Read(ctx, 42, "theme")
	require.NoError(t, err)
	require.NotNil(t, item)
	assert.Equal(t, KindPreference, item.Kind)
	assert.Equal(t, "theme", item.KeyName)
	assert.Equal(t, EscapeForStorage("dark"), item.Content)
}

// TestNotepad_Read_NotFound verifies that Read returns (nil, nil) for a missing key.
func TestNotepad_Read_NotFound(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	item, err := np.Read(ctx, 1, "nonexistent-key")
	require.NoError(t, err)
	assert.Nil(t, item, "missing key must return nil item, not an error")
}

// ---------------------------------------------------------------------------
// ListByKind test
// ---------------------------------------------------------------------------

// TestNotepad_ListByKind verifies that ListByKind filters by kind and respects limit.
func TestNotepad_ListByKind(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	// Write 3 facts and 1 decision for user 1
	require.NoError(t, np.Write(ctx, 1, KindFact, "f1", "fact one", WriteOpts{}))
	require.NoError(t, np.Write(ctx, 1, KindFact, "f2", "fact two", WriteOpts{}))
	require.NoError(t, np.Write(ctx, 1, KindFact, "f3", "fact three", WriteOpts{}))
	require.NoError(t, np.Write(ctx, 1, KindDecision, "d1", "decided", WriteOpts{}))

	// Limit 2 facts
	facts, err := np.ListByKind(ctx, 1, KindFact, 2)
	require.NoError(t, err)
	assert.Len(t, facts, 2, "limit=2 should return exactly 2 facts")
	for _, f := range facts {
		assert.Equal(t, KindFact, f.Kind)
	}

	// Decision should not appear in fact list
	decisions, err := np.ListByKind(ctx, 1, KindDecision, 10)
	require.NoError(t, err)
	assert.Len(t, decisions, 1)
	assert.Equal(t, KindDecision, decisions[0].Kind)
}

// ---------------------------------------------------------------------------
// Delete test
// ---------------------------------------------------------------------------

// TestNotepad_Delete verifies that Delete removes the entry and subsequent Read
// returns (nil, nil).
func TestNotepad_Delete(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	require.NoError(t, np.Write(ctx, 1, KindIssue, "bug-42", "reproducible", WriteOpts{}))

	item, err := np.Read(ctx, 1, "bug-42")
	require.NoError(t, err)
	require.NotNil(t, item, "entry must exist before deletion")

	require.NoError(t, np.Delete(ctx, 1, "bug-42"))

	gone, err := np.Read(ctx, 1, "bug-42")
	require.NoError(t, err)
	assert.Nil(t, gone, "entry must not exist after deletion")
}

// ---------------------------------------------------------------------------
// Cross-user isolation test
// ---------------------------------------------------------------------------

// TestNotepad_CrossUserIsolation verifies that user 2 cannot read entries
// written by user 1 under the same key.
func TestNotepad_CrossUserIsolation(t *testing.T) {
	np := newTestNotepad(t)
	ctx := context.Background()

	require.NoError(t, np.Write(ctx, 1, KindFact, "secret", "user1 data", WriteOpts{}))

	// User 2 should not see user 1's entry
	item, err := np.Read(ctx, 2, "secret")
	require.NoError(t, err)
	assert.Nil(t, item, "user 2 must not read user 1's memory entry")

	// ListByKind for user 2 should also return 0 items
	items, err := np.ListByKind(ctx, 2, KindFact, 10)
	require.NoError(t, err)
	assert.Empty(t, items, "user 2 must not list user 1's fact entries")
}
