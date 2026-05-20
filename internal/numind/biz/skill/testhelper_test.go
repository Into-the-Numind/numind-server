package skill

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"numind-server/internal/pkg/model"
)

// newTestDB creates an isolated in-memory SQLite DB with the agent_definition
// and agent_definition_history tables migrated. Each test gets its own named
// DB so parallel tests do not share schema state.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "open sqlite in-memory DB")

	sqlDB, err := db.DB()
	require.NoError(t, err)
	// Single connection prevents "database is locked" under parallel read/write.
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, db.AutoMigrate(
		&model.AgentDefinition{},
		&model.AgentDefinitionHistory{},
	), "auto-migrate agent tables")

	return db
}
