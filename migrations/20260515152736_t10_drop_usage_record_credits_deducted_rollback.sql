-- T10 rollback: re-add credits_deducted column (data lost, rebuild from 0).
ALTER TABLE usage_record ADD COLUMN credits_deducted BIGINT NOT NULL DEFAULT 0 COMMENT 'historical field, deprecated';
