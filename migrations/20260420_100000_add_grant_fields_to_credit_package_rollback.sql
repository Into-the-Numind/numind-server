-- Rollback: remove grant-source fields from credit_package
-- Counterpart to 20260420_100000_add_grant_fields_to_credit_package.sql

ALTER TABLE `credit_package`
    DROP INDEX `idx_grant_source_granter`,
    DROP COLUMN `granter_user_id`,
    DROP COLUMN `grant_source`;
