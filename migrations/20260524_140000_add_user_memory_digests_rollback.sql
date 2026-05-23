-- Rollback for: 20260524_140000_add_user_memory_digests.sql
-- Feature: agent-mode-v15-memory-layer-a (Task 3.8)
-- 数据丢失警告: DROP TABLE 会丢失所有 cron 已生成的 digest 数据
-- (cost low: digest 可从 agent_run / 下层 digest 重新跑 cron 生成)
--
-- Order: 4 个表互相独立, FK 都指向 user(id) — drop 顺序任意, 但保留与 up 相反顺序惯例.

DROP TABLE IF EXISTS user_memory_digest_quarterly;
DROP TABLE IF EXISTS user_memory_digest_monthly;
DROP TABLE IF EXISTS user_memory_digest_weekly;
DROP TABLE IF EXISTS user_memory_digest_daily;
