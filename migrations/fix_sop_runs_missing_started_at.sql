-- 修复：通过书签创建的 SOP Run 在 draft→running 转换时未设置 started_at
-- 用 created_at 回填缺失的 started_at
UPDATE sop_run
SET started_at = created_at
WHERE status IN ('succeeded', 'failed', 'running')
  AND started_at IS NULL;
