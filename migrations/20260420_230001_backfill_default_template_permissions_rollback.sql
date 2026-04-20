-- Rollback for migrations/20260420_230001_backfill_default_template_permissions.sql
--
-- WARNING：回滚策略采用"按时间窗口删除"，存在过度删除的小风险（P2-B review
-- 已评估）。缓解前提：
--   (1) backfill 在 <2 分钟的维护窗口内执行
--   (2) 窗口期间父账号不得做 grant/revoke 操作（运维公告 + UI 只读）
--   (3) 必须按下面的两步走执行，人工确认 Step 1 的 will_delete 数量合理
--       之后再运行 Step 2 的 DELETE
--
-- 使用方式：将 :backfill_start_ts / :backfill_end_ts 替换为 backfill
-- 迁移实际执行的起止时间（通常取 backfill 开始前一分钟到结束后一分钟的
-- 包络范围更安全）。
--
-- ============================================================================
-- Step 1 (dry-run)：打印待删除行数，人工确认合理后再执行 Step 2
-- ============================================================================

SELECT COUNT(*) AS will_delete
FROM `user_template_permission`
WHERE `created_at` BETWEEN :backfill_start_ts AND :backfill_end_ts
  AND `sub_user_id` IN (
    SELECT id FROM `user`
    WHERE parent_user_id IS NOT NULL
      AND deleted_at IS NULL
  );

-- ============================================================================
-- Step 2 (真删)：仅在 Step 1 的 will_delete 数量被人工确认合理后执行
-- ============================================================================

-- DELETE FROM `user_template_permission`
-- WHERE `created_at` BETWEEN :backfill_start_ts AND :backfill_end_ts
--   AND `sub_user_id` IN (
--     SELECT id FROM `user`
--     WHERE parent_user_id IS NOT NULL
--       AND deleted_at IS NULL
--   );
