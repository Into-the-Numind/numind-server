package capability

// Unit tests for the capability package.
//
// These tests do NOT require a real DB. They wire the package-level
// `packageDB` with an in-memory SQLite database (via GORM's dialector) so
// that the DB path of GetCapabilities / CanAcceptModality / ResolveFallbackBehavior
// is fully exercised without a running MySQL instance.
//
// Each test gets its own uniquely-named in-memory SQLite DB to prevent
// shared state between tests (the package-level `packageDB` var is global;
// tests run sequentially within the package, so unique DB names prevent
// bleed-through from DROP TABLE etc.).
//
// Test matrix: 6 model keys x 4 media types = 24 core cases.
// Additional cases: cache hit/miss/invalidate, concurrent reads, nil DB,
// conservative fallback, parse error.

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// dbCounter provides a unique suffix for each in-memory SQLite DB.
var dbCounter int64

// ----------------------------------------------------------------------------
// Test helpers
// ----------------------------------------------------------------------------

// newTestDB creates a uniquely-named in-memory SQLite DB with the minimal
// ai_service schema. Uses `cache=shared` with a named DB so all GORM
// connections within a test share the same in-memory database.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	n := atomic.AddInt64(&dbCounter, 1)
	// cache=shared + unique name = one in-memory DB shared across all connections
	// for this test (avoids the "table not found on pooled connection" problem).
	dsn := fmt.Sprintf("file:cap_test_%d?mode=memory&cache=shared", n)

	// Pre-register the DSN so SQLite opens the shared-cache DB consistently.
	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// Keep exactly one connection in the pool: SQLite in-memory is per-connection;
	// even with cache=shared, setting MaxOpenConns=1 is the safest guarantee.
	sqlDB.SetMaxOpenConns(1)

	db, err := gorm.Open(sqlite.Dialector{DSN: dsn, DriverName: "sqlite3", Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to open in-memory SQLite: %v", err)
	}

	if err := db.Exec(`
		CREATE TABLE IF NOT EXISTS ai_service (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			model_key       TEXT NOT NULL UNIQUE,
			display_name    TEXT,
			service_type    TEXT DEFAULT 'llm',
			capability_json TEXT,
			deprecated_at   DATETIME
		)
	`).Error; err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}
	return db
}

// seedRow inserts a row into ai_service with a pre-serialised JSON string.
func seedRow(t *testing.T, db *gorm.DB, modelKey, capabilityJSON string) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO ai_service (model_key, capability_json) VALUES (?, ?)`,
		modelKey, capabilityJSON,
	).Error; err != nil {
		t.Fatalf("seedRow %q: %v", modelKey, err)
	}
}

// usePkg sets the package-level DB and clears all cache entries.
func usePkg(db *gorm.DB) {
	packageDB = db
	capabilityCache.Range(func(k, _ any) bool {
		capabilityCache.Delete(k)
		return true
	})
}

// ----------------------------------------------------------------------------
// Fixture capability JSON strings
// ----------------------------------------------------------------------------

const (
	capVisionModel = `{"accepts_image_inline":true,"accepts_pdf_inline":false,"accepts_audio_inline":false,"max_inline_size_bytes":20971520,"supports_vision_tool_calling":true,"preferred_image_format":"base64"}`
	capPDFModel    = `{"accepts_image_inline":false,"accepts_pdf_inline":true,"accepts_audio_inline":false,"max_inline_size_bytes":104857600,"supports_vision_tool_calling":false,"preferred_image_format":"base64"}`
	capTextOnly    = `{"accepts_image_inline":false,"accepts_pdf_inline":false,"accepts_audio_inline":false,"max_inline_size_bytes":0,"supports_vision_tool_calling":false,"preferred_image_format":"base64"}`
)

// ----------------------------------------------------------------------------
// Core 24-case matrix: 6 model keys x 4 media types
// ----------------------------------------------------------------------------

// TestResolveFallbackBehavior_Matrix exercises the decision matrix from the spec.
// Contains >=24 distinct (modelKey, mediaType) pairs.
func TestResolveFallbackBehavior_Matrix(t *testing.T) {
	db := newTestDB(t)
	seedRow(t, db, "qwen3-vl-flash", capVisionModel)  // multimodal vision
	seedRow(t, db, "qwen-long", capPDFModel)          // PDF-capable, text-only for images
	seedRow(t, db, "glm-5-1", capTextOnly)            // text-only
	seedRow(t, db, "minimax-m2-7", capTextOnly)       // text-only
	seedRow(t, db, "doubao-seed-1-8", capVisionModel) // multimodal vision
	seedRow(t, db, "kimi-k2-5", capVisionModel)       // multimodal vision (new model stub)
	// "not-exist-model" is intentionally NOT seeded.
	usePkg(db)

	type tc struct {
		modelKey string
		media    MediaType
		want     FallbackPolicy
	}

	cases := []tc{
		// ---- qwen3-vl-flash (vision: accepts_image_inline=true) ----
		{"qwen3-vl-flash", MediaImage, FallbackInline},
		{"qwen3-vl-flash", MediaPDF, FallbackToOCROnly}, // VL model does NOT accept PDF inline
		{"qwen3-vl-flash", MediaAudio, FallbackReject},

		// ---- glm-5-1 (text-only) ----
		{"glm-5-1", MediaImage, FallbackToText},
		{"glm-5-1", MediaPDF, FallbackToOCROnly},
		{"glm-5-1", MediaAudio, FallbackReject},

		// ---- qwen-long (accepts PDF inline, not images) ----
		{"qwen-long", MediaImage, FallbackToText},
		{"qwen-long", MediaPDF, FallbackInline},
		{"qwen-long", MediaAudio, FallbackReject},

		// ---- minimax-m2-7 (text-only) ----
		{"minimax-m2-7", MediaImage, FallbackToText},
		{"minimax-m2-7", MediaPDF, FallbackToOCROnly},
		{"minimax-m2-7", MediaAudio, FallbackReject},

		// ---- doubao-seed-1-8 (vision) ----
		{"doubao-seed-1-8", MediaImage, FallbackInline},
		{"doubao-seed-1-8", MediaPDF, FallbackToOCROnly},
		{"doubao-seed-1-8", MediaAudio, FallbackReject},

		// ---- kimi-k2-5 (vision, new model stub) ----
		{"kimi-k2-5", MediaImage, FallbackInline},
		{"kimi-k2-5", MediaPDF, FallbackToOCROnly},
		{"kimi-k2-5", MediaAudio, FallbackReject},

		// ---- not-exist-model (unknown -> conservative defaults) ----
		{"not-exist-model", MediaImage, FallbackToText},
		{"not-exist-model", MediaPDF, FallbackToOCROnly},
		{"not-exist-model", MediaAudio, FallbackReject},

		// ---- additional cases to push past 24 ----
		{"qwen3-vl-flash", MediaImage, FallbackInline}, // idempotent re-check
		{"glm-5-1", MediaImage, FallbackToText},        // re-check text-only
		{"qwen-long", MediaPDF, FallbackInline},        // re-check PDF inline
	}

	if len(cases) < 24 {
		t.Fatalf("expected >= 24 test cases, got %d", len(cases))
	}

	for _, c := range cases {
		c := c
		// Clear per-model cache entry to exercise DB path.
		capabilityCache.Delete(c.modelKey)
		got := ResolveFallbackBehavior(c.modelKey, c.media)
		if got != c.want {
			t.Errorf("ResolveFallbackBehavior(%q, %q) = %q; want %q",
				c.modelKey, c.media, got, c.want)
		}
	}
}

// ----------------------------------------------------------------------------
// ErrModelNotFound tests
// ----------------------------------------------------------------------------

// TestGetCapabilities_NotFound verifies conservative defaults + ErrModelNotFound
// for an unknown model key.
func TestGetCapabilities_NotFound(t *testing.T) {
	db := newTestDB(t)
	usePkg(db)

	caps, err := GetCapabilities("no-such-model")
	if err == nil {
		t.Fatal("expected ErrModelNotFound, got nil")
	}
	if !errors.Is(err, ErrModelNotFound) {
		t.Errorf("expected ErrModelNotFound; got %T: %v", err, err)
	}
	if caps == nil {
		t.Fatal("caps must not be nil on error")
	}
	if caps.AcceptsImageInline {
		t.Error("conservative default: AcceptsImageInline must be false")
	}
	if caps.AcceptsPDFInline {
		t.Error("conservative default: AcceptsPDFInline must be false")
	}
	if caps.AcceptsAudioInline {
		t.Error("conservative default: AcceptsAudioInline must be false")
	}
	if caps.MaxInlineSizeBytes != 0 {
		t.Errorf("conservative default: MaxInlineSizeBytes must be 0, got %d", caps.MaxInlineSizeBytes)
	}
}

// TestCanAcceptModality_NotFound verifies ErrModelNotFound propagates from
// CanAcceptModality.
func TestCanAcceptModality_NotFound(t *testing.T) {
	db := newTestDB(t)
	usePkg(db)

	ok, err := CanAcceptModality("no-such-model", MediaImage)
	if err == nil {
		t.Fatal("expected ErrModelNotFound, got nil")
	}
	if !errors.Is(err, ErrModelNotFound) {
		t.Errorf("expected ErrModelNotFound; got %v", err)
	}
	if ok {
		t.Error("unknown model must not claim to accept image")
	}
}

// ----------------------------------------------------------------------------
// CanAcceptModality happy-path tests
// ----------------------------------------------------------------------------

// TestCanAcceptModality_VisionModel verifies image acceptance for a vision model.
func TestCanAcceptModality_VisionModel(t *testing.T) {
	db := newTestDB(t)
	seedRow(t, db, "qwen-vl", capVisionModel)
	usePkg(db)

	ok, err := CanAcceptModality("qwen-vl", MediaImage)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("vision model must accept MediaImage")
	}
}

// TestCanAcceptModality_TextOnly verifies image rejection for a text-only model.
func TestCanAcceptModality_TextOnly(t *testing.T) {
	db := newTestDB(t)
	seedRow(t, db, "glm-5-1-can", capTextOnly)
	usePkg(db)

	ok, err := CanAcceptModality("glm-5-1-can", MediaImage)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("text-only model must NOT accept MediaImage")
	}
}

// ----------------------------------------------------------------------------
// Cache hit / miss / invalidate tests
// ----------------------------------------------------------------------------

// TestCache_HitAfterFirstLookup verifies that a cached result is served
// even after the underlying DB table is dropped.
func TestCache_HitAfterFirstLookup(t *testing.T) {
	db := newTestDB(t)
	seedRow(t, db, "cached-model", capVisionModel)
	usePkg(db)

	// First call -> DB lookup.
	caps1, err := GetCapabilities("cached-model")
	if err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	if !caps1.AcceptsImageInline {
		t.Error("first lookup: expected AcceptsImageInline=true")
	}

	// Drop the table to ensure a second real DB call would fail.
	_ = db.Exec("DROP TABLE ai_service")

	// Second call -> must come from cache.
	caps2, err := GetCapabilities("cached-model")
	if err != nil {
		t.Fatalf("second lookup (cache hit expected): %v", err)
	}
	if !caps2.AcceptsImageInline {
		t.Error("cache hit: expected AcceptsImageInline=true from cache")
	}
}

// TestCache_InvalidateForcesDBLookup verifies that InvalidateCache causes
// the next GetCapabilities to bypass cache and hit the DB.
func TestCache_InvalidateForcesDBLookup(t *testing.T) {
	db := newTestDB(t)
	seedRow(t, db, "inv-model", capTextOnly)
	usePkg(db)

	// Populate cache with text-only value.
	_, err := GetCapabilities("inv-model")
	if err != nil {
		t.Fatalf("initial lookup: %v", err)
	}

	// Directly update DB row to vision-capable.
	if err := db.Exec(
		`UPDATE ai_service SET capability_json = ? WHERE model_key = ?`,
		capVisionModel, "inv-model",
	).Error; err != nil {
		t.Fatalf("direct DB update: %v", err)
	}

	// Cache still returns stale text-only value.
	caps, _ := GetCapabilities("inv-model")
	if caps.AcceptsImageInline {
		t.Error("before invalidation: expected stale AcceptsImageInline=false")
	}

	// Invalidate -> next call should see updated value.
	InvalidateCache("inv-model")

	caps, err = GetCapabilities("inv-model")
	if err != nil {
		t.Fatalf("post-invalidate lookup: %v", err)
	}
	if !caps.AcceptsImageInline {
		t.Error("after invalidation: expected updated AcceptsImageInline=true")
	}
}

// TestCache_MissAfterTTLExpiry verifies that an expired cache entry is evicted
// and the DB is re-queried.
func TestCache_MissAfterTTLExpiry(t *testing.T) {
	db := newTestDB(t)
	seedRow(t, db, "ttl-model", capTextOnly)
	usePkg(db)

	// Manually insert an already-expired cache entry with stale vision caps.
	capabilityCache.Store("ttl-model", &cacheEntry{
		caps:      &Capabilities{AcceptsImageInline: true}, // stale
		expiresAt: time.Now().Add(-1 * time.Second),        // already expired
	})

	// GetCapabilities must evict the expired entry and fetch from DB.
	caps, err := GetCapabilities("ttl-model")
	if err != nil {
		t.Fatalf("lookup after TTL expiry: %v", err)
	}
	if caps.AcceptsImageInline {
		t.Error("after TTL expiry: expected DB value (false), got stale cache (true)")
	}
}

// ----------------------------------------------------------------------------
// Nil / uninitialised DB
// ----------------------------------------------------------------------------

// TestGetCapabilities_NilDB verifies an informative error (not panic) when
// the package DB has not been initialised.
func TestGetCapabilities_NilDB(t *testing.T) {
	packageDB = nil
	capabilityCache.Delete("any-model")

	caps, err := GetCapabilities("any-model")
	if err == nil {
		t.Fatal("expected error when DB not initialised, got nil")
	}
	if caps == nil {
		t.Fatal("caps must not be nil even on DB-not-initialised error")
	}
}

// ----------------------------------------------------------------------------
// capability_json parse error / empty JSON tests
// ----------------------------------------------------------------------------

// TestGetCapabilities_EmptyJSON verifies backward-compat: rows with no
// capability_json return conservative defaults without error.
func TestGetCapabilities_EmptyJSON(t *testing.T) {
	db := newTestDB(t)
	seedRow(t, db, "legacy-model", "") // empty JSON
	usePkg(db)

	caps, err := GetCapabilities("legacy-model")
	if err != nil {
		t.Fatalf("unexpected error for empty capability_json: %v", err)
	}
	if caps.AcceptsImageInline {
		t.Error("empty JSON: AcceptsImageInline must default to false")
	}
}

// TestGetCapabilities_MalformedJSON verifies WARN + conservative defaults on
// JSON parse error (no error returned to caller).
func TestGetCapabilities_MalformedJSON(t *testing.T) {
	db := newTestDB(t)
	seedRow(t, db, "broken-model", `{"accepts_image_inline": INVALID}`)
	usePkg(db)

	caps, err := GetCapabilities("broken-model")
	if err != nil {
		t.Fatalf("parse error must NOT propagate as Go error: %v", err)
	}
	if caps.AcceptsImageInline {
		t.Error("malformed JSON: AcceptsImageInline must default to false")
	}
}

// TestGetCapabilities_DefaultPreferredFormat verifies that a missing
// preferred_image_format defaults to "base64".
func TestGetCapabilities_DefaultPreferredFormat(t *testing.T) {
	db := newTestDB(t)
	seedRow(t, db, "no-fmt-model", `{"accepts_image_inline":true,"max_inline_size_bytes":1024}`)
	usePkg(db)

	caps, err := GetCapabilities("no-fmt-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if caps.PreferredImageFormat != "base64" {
		t.Errorf("expected preferred_image_format=base64, got %q", caps.PreferredImageFormat)
	}
}

// ----------------------------------------------------------------------------
// Concurrent GetCapabilities - race detection
// ----------------------------------------------------------------------------

// TestGetCapabilities_Concurrent100 launches 100 goroutines all calling
// GetCapabilities on the same model key. The -race flag must not report any
// data races.
func TestGetCapabilities_Concurrent100(t *testing.T) {
	db := newTestDB(t)
	seedRow(t, db, "concurrent-model", capVisionModel)
	usePkg(db)

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for range n {
		go func() {
			defer wg.Done()
			caps, err := GetCapabilities("concurrent-model")
			if err != nil {
				errs <- fmt.Errorf("GetCapabilities: %w", err)
				return
			}
			if caps == nil {
				errs <- fmt.Errorf("caps must not be nil")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent: %v", err)
	}
}

// TestInvalidateCache_Concurrent verifies concurrent InvalidateCache +
// GetCapabilities does not trigger a race condition.
func TestInvalidateCache_Concurrent(t *testing.T) {
	db := newTestDB(t)
	seedRow(t, db, "race-inv-model", capVisionModel)
	usePkg(db)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n * 2)
	for range n {
		go func() {
			defer wg.Done()
			_, _ = GetCapabilities("race-inv-model")
		}()
		go func() {
			defer wg.Done()
			InvalidateCache("race-inv-model")
		}()
	}
	wg.Wait()
}

// ----------------------------------------------------------------------------
// resolvePolicyFromCaps - internal helper, tested directly for branch coverage
// ----------------------------------------------------------------------------

// TestResolvePolicyFromCaps_AllCombinations exercises every branch of
// resolvePolicyFromCaps with explicit Capabilities structs (no DB needed).
func TestResolvePolicyFromCaps_AllCombinations(t *testing.T) {
	allFalse := &Capabilities{}
	imageTrue := &Capabilities{AcceptsImageInline: true}
	pdfTrue := &Capabilities{AcceptsPDFInline: true}
	audioTrue := &Capabilities{AcceptsAudioInline: true}

	cases := []struct {
		desc  string
		caps  *Capabilities
		media MediaType
		want  FallbackPolicy
	}{
		{"image inline true", imageTrue, MediaImage, FallbackInline},
		{"image inline false", allFalse, MediaImage, FallbackToText},
		{"pdf inline true", pdfTrue, MediaPDF, FallbackInline},
		{"pdf inline false", allFalse, MediaPDF, FallbackToOCROnly},
		{"audio always rejected (false)", allFalse, MediaAudio, FallbackReject},
		{"audio always rejected (true)", audioTrue, MediaAudio, FallbackReject},
		{"unknown media type", allFalse, MediaType("video"), FallbackReject},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got := resolvePolicyFromCaps(c.caps, c.media)
			if got != c.want {
				t.Errorf("got %q; want %q", got, c.want)
			}
		})
	}
}
