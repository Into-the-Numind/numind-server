-- Rollback for 20260523_150000_add_profile_extraction_count.sql
-- 移除 Task 3.3 ExtractorService 用的累计计数列.

ALTER TABLE user_memory_profile
  DROP COLUMN extraction_count_since_rebuild;
