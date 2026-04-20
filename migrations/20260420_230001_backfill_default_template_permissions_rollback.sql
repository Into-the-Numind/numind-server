-- Rollback for migrations/20260420_230001_backfill_default_template_permissions.sql
--
-- WARNING：回滚策略采用"按时间窗口删除"，存在过度删除的小风险（P2-B review
-- 已评估）。缓解前提：
--   (1) backfill 在 <2 分钟的维护窗口内执行
--   (2) 窗口期间父账号不得做 grant/revoke 操作（运维公告 + UI 只读）
--   (3) 必须按下面的两步走执行，人工确认 Step 1 的 will_delete 数量合理
--       之后再运行 Step 2 的 DELETE
--
-- 使用方式（MySQL 8.x session variables）：
--   1. 先运行两条 SET @... 语句，把时间窗口替换成 backfill 实际执行的起止时间
--      （建议取 backfill 开始前一分钟到结束后一分钟的包络范围更安全）
--   2. 运行 Step 1 SELECT 看 will_delete 数量，人工确认
--   3. 取消 Step 2 DELETE 的注释，执行真删
--
-- 注意：Task 1 code-review P2-A 修复 —— 原用 `:backfill_start_ts` 是 Oracle/psql
-- 占位符，MySQL CLI 不支持；改用 SET @var 的 session variable 语法。
--
-- ============================================================================
-- 配置：替换下面两个时间戳为实际 backfill 执行窗口
-- ============================================================================

SET @backfill_start_ts = '2026-04-20 23:00:00.000';
SET @backfill_end_ts   = '2026-04-20 23:05:00.000';

-- ============================================================================
-- Step 1 (dry-run)：打印待删除行数，人工确认合理后再执行 Step 2
-- ============================================================================

SELECT COUNT(*) AS will_delete
FROM `user_template_permission`
WHERE `created_at` BETWEEN @backfill_start_ts AND @backfill_end_ts
  AND `sub_user_id` IN (
    SELECT id FROM `user`
    WHERE parent_user_id IS NOT NULL
      AND deleted_at IS NULL
  );

-- ============================================================================
-- Step 2 (真删)：仅在 Step 1 的 will_delete 数量被人工确认合理后执行
-- ============================================================================

-- DELETE FROM `user_template_permission`
-- WHERE `created_at` BETWEEN @backfill_start_ts AND @backfill_end_ts
--   AND `sub_user_id` IN (
--     SELECT id FROM `user`
--     WHERE parent_user_id IS NOT NULL
--       AND deleted_at IS NULL
--   );
