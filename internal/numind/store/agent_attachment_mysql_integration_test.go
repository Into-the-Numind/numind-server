//go:build mysqlintegration

package store

import (
	"context"
	"os"
	"strings"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAgentAttachmentStoreReadsLegacyNullableParsedFields(t *testing.T) {
	const integrationDatabasePrefix = "numind_attachment_integration_"

	dsn := os.Getenv("NUMIND_ATTACHMENT_MYSQL_DSN")
	if dsn == "" {
		t.Skip("NUMIND_ATTACHMENT_MYSQL_DSN is required for the MySQL 8 integration gate")
	}
	driverConfig, err := drivermysql.ParseDSN(dsn)
	require.NoError(t, err)
	require.Truef(
		t,
		strings.HasPrefix(driverConfig.DBName, integrationDatabasePrefix) &&
			len(driverConfig.DBName) > len(integrationDatabasePrefix),
		"refusing destructive integration setup outside a dedicated %s* database",
		integrationDatabasePrefix,
	)
	db, err := gorm.Open(
		mysql.Open(driverConfig.FormatDSN()),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)},
	)
	require.NoError(t, err)

	var existingTableCount int64
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'agent_attachment'
	`).Scan(&existingTableCount).Error)
	require.Zero(t, existingTableCount, "dedicated integration database must start empty")
	t.Cleanup(func() { _ = db.Exec("DROP TABLE IF EXISTS agent_attachment").Error })
	require.NoError(t, db.Exec(`
		CREATE TABLE agent_attachment (
			id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
			user_id BIGINT UNSIGNED NOT NULL,
			url TEXT NOT NULL,
			parsed_content LONGTEXT NULL,
			parsed_content_sha256 VARCHAR(71) NULL DEFAULT NULL,
			parsed_content_byte_size BIGINT NULL DEFAULT 0,
			parsed_page_count BIGINT NULL DEFAULT 0,
			parsed_at DATETIME(3) NULL
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
	`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO agent_attachment (
			id, user_id, url, parsed_content, parsed_content_sha256,
			parsed_content_byte_size, parsed_page_count, parsed_at
		) VALUES (1, 7, 'https://invalid.example/legacy', NULL, NULL, NULL, NULL, NULL)
	`).Error)

	got, err := newAgentAttachmentStore(db).GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.Nil(t, got.ParsedContent)
	require.Empty(t, got.ParsedContentSHA256)
	require.Zero(t, got.ParsedContentByteSize)
	require.Zero(t, got.ParsedPageCount)
	require.Nil(t, got.ParsedAt)
}
