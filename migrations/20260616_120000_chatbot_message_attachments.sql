-- Feature: chatbot-image-recognition
-- Adds chatbot_message.attachments — a JSON array of lightweight image
-- attachment references ([{id, filename, mime_type}]) carried by a user message,
-- so uploaded images render as filename chips when the session is reloaded.
--
-- Only user messages may have a non-empty value; assistant messages stay NULL.
-- The Go model field uses `gorm:"serializer:json"` so reads/writes are automatic.
--
-- NOTE: chatbot_message is NOT in helper.go AutoMigrate, so this migration is the
-- ONLY way the column is created. dev/prod CI does NOT run migrations
-- automatically — run this manually via SSH before deploying (see CLAUDE.md §5.2
-- + memory project_dev_deploy_migration_gap).
--
-- Idempotency: MySQL does NOT support `ADD COLUMN IF NOT EXISTS` (errors 1064 on
-- mysql:8.4.2). Use the information_schema pre-check + prepared statement pattern.
-- Safe to re-run.

SET @col_exists := (SELECT COUNT(*) FROM information_schema.COLUMNS
                    WHERE TABLE_SCHEMA = DATABASE()
                      AND TABLE_NAME   = 'chatbot_message'
                      AND COLUMN_NAME  = 'attachments');
SET @sql := IF(@col_exists = 0,
               'ALTER TABLE chatbot_message ADD COLUMN attachments JSON NULL COMMENT ''image attachment refs [{id,filename,mime_type}] for reload display'' AFTER completion_tokens',
               'SELECT 1');
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
