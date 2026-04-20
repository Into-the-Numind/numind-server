package registry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newStoreTestDB creates an in-memory SQLite DB with the minimal schema required
// by GetResolvedRoute: ai_service + ai_service_route + llm_provider. We use
// explicit DDL (not AutoMigrate) so the test bypasses MySQL-specific column types
// on the full GORM models and stays focused on the thinking-flag read path.
func newStoreTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`
		CREATE TABLE ai_service (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			model_key         TEXT    NOT NULL,
			display_name      TEXT    NOT NULL DEFAULT '',
			service_type      TEXT    NOT NULL DEFAULT 'llm',
			capability_json   TEXT,
			latency_tier      TEXT    DEFAULT 'standard',
			quality_tier      TEXT    DEFAULT 'standard',
			tags              TEXT,
			deprecated_at     DATETIME,
			is_thinking       INTEGER DEFAULT 0,
			base_model_id     INTEGER,
			supports_thinking INTEGER NOT NULL DEFAULT 0,
			thinking_only     INTEGER NOT NULL DEFAULT 0,
			icon              TEXT,
			sort_order        INTEGER DEFAULT 0,
			is_active         INTEGER DEFAULT 1,
			created_at        DATETIME,
			updated_at        DATETIME
		)`).Error)

	require.NoError(t, db.Exec(`
		CREATE TABLE llm_provider (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			name         TEXT    NOT NULL,
			display_name TEXT    NOT NULL DEFAULT '',
			base_url     TEXT    NOT NULL DEFAULT '',
			api_key      TEXT    NOT NULL DEFAULT '',
			is_active    INTEGER DEFAULT 1,
			created_at   DATETIME,
			updated_at   DATETIME
		)`).Error)

	require.NoError(t, db.Exec(`
		CREATE TABLE ai_service_route (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			model_id          INTEGER NOT NULL,
			provider_id       INTEGER NOT NULL,
			provider_model_id TEXT    NOT NULL DEFAULT '',
			priority          INTEGER DEFAULT 0,
			is_active         INTEGER DEFAULT 1,
			created_at        DATETIME,
			updated_at        DATETIME
		)`).Error)

	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// seedRouteWithThinkingFlags inserts one minimal triple (ai_service + llm_provider
// + ai_service_route) for the given (modelKey, supportsThinking, thinkingOnly)
// and returns the freshly-created service ID.
func seedRouteWithThinkingFlags(t *testing.T, db *gorm.DB, modelKey string, supportsThinking, thinkingOnly bool) uint64 {
	t.Helper()

	supportsVal := 0
	if supportsThinking {
		supportsVal = 1
	}
	thinkingOnlyVal := 0
	if thinkingOnly {
		thinkingOnlyVal = 1
	}

	// Insert ai_service row.
	require.NoError(t, db.Exec(`
		INSERT INTO ai_service
		  (model_key, display_name, service_type, is_active, supports_thinking, thinking_only)
		VALUES
		  (?, ?, 'llm', 1, ?, ?)`,
		modelKey, modelKey+"-display", supportsVal, thinkingOnlyVal,
	).Error)

	var serviceID uint64
	require.NoError(t, db.Raw(`SELECT id FROM ai_service WHERE model_key = ?`, modelKey).Scan(&serviceID).Error)
	require.NotZero(t, serviceID)

	// Insert llm_provider row.
	providerName := "test-provider-" + modelKey
	require.NoError(t, db.Exec(`
		INSERT INTO llm_provider
		  (name, display_name, base_url, api_key, is_active)
		VALUES
		  (?, ?, 'https://example.invalid', 'test-key', 1)`,
		providerName, providerName+"-display",
	).Error)

	var providerID uint64
	require.NoError(t, db.Raw(`SELECT id FROM llm_provider WHERE name = ?`, providerName).Scan(&providerID).Error)
	require.NotZero(t, providerID)

	// Insert ai_service_route row.
	require.NoError(t, db.Exec(`
		INSERT INTO ai_service_route
		  (model_id, provider_id, provider_model_id, priority, is_active)
		VALUES
		  (?, ?, ?, 100, 1)`,
		serviceID, providerID, modelKey+"-provider-id",
	).Error)

	return serviceID
}

// TestStore_GetResolvedRoute_ReadsThinkingFlags verifies that SupportsThinking and
// ThinkingOnly are correctly read from ai_service via the JOIN query for the three
// representative combinations (optional, intrinsic, none). This guards the Task 6
// contract: adapter / middleware code relies on these two bools to decide whether
// to inject `thinking=true` and whether to skip `reasoning_effort=minimal` when the
// model always thinks.
func TestStore_GetResolvedRoute_ReadsThinkingFlags(t *testing.T) {
	cases := []struct {
		name                     string
		modelKey                 string
		seedSupportsThinking     bool
		seedThinkingOnly         bool
		expectedSupportsThinking bool
		expectedThinkingOnly     bool
	}{
		{
			name:                     "optional",
			modelKey:                 "model-optional",
			seedSupportsThinking:     true,
			seedThinkingOnly:         false,
			expectedSupportsThinking: true,
			expectedThinkingOnly:     false,
		},
		{
			name:                     "intrinsic",
			modelKey:                 "model-intrinsic",
			seedSupportsThinking:     true,
			seedThinkingOnly:         true,
			expectedSupportsThinking: true,
			expectedThinkingOnly:     true,
		},
		{
			name:                     "none",
			modelKey:                 "model-none",
			seedSupportsThinking:     false,
			seedThinkingOnly:         false,
			expectedSupportsThinking: false,
			expectedThinkingOnly:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newStoreTestDB(t)
			serviceID := seedRouteWithThinkingFlags(t, db, tc.modelKey, tc.seedSupportsThinking, tc.seedThinkingOnly)

			store := NewStore(db)

			// Verify GetResolvedRoute (by service ID) surfaces both flags.
			row, err := store.GetResolvedRoute(context.Background(), serviceID)
			require.NoError(t, err)
			require.NotNil(t, row)
			assert.Equal(t, tc.expectedSupportsThinking, row.SupportsThinking, "GetResolvedRoute SupportsThinking")
			assert.Equal(t, tc.expectedThinkingOnly, row.ThinkingOnly, "GetResolvedRoute ThinkingOnly")

			// Verify GetResolvedRouteByModelKey surfaces both flags identically.
			row2, err := store.GetResolvedRouteByModelKey(context.Background(), tc.modelKey)
			require.NoError(t, err)
			require.NotNil(t, row2)
			assert.Equal(t, tc.expectedSupportsThinking, row2.SupportsThinking, "GetResolvedRouteByModelKey SupportsThinking")
			assert.Equal(t, tc.expectedThinkingOnly, row2.ThinkingOnly, "GetResolvedRouteByModelKey ThinkingOnly")
		})
	}
}
