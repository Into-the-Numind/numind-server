-- Rollback for 20260609_121500_add_cached_prompt_tokens_to_usage_record.sql
ALTER TABLE usage_record DROP COLUMN IF EXISTS cached_prompt_tokens;
