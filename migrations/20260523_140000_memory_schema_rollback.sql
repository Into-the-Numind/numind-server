-- Rollback for: 20260523_140000_memory_schema.sql
-- Drops the two tables created by the forward migration.
-- WARNING: This deletes all user memory data. Backup before running.

DROP TABLE IF EXISTS user_memory_facts;
DROP TABLE IF EXISTS user_memory_profile;
