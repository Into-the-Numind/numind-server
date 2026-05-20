-- Rollback for agent-mode #9 compact ALTER agent_run
-- 顺序：先 DROP compact_summary 再 DROP compact_state（与添加顺序相反）

ALTER TABLE agent_run
  DROP COLUMN compact_summary,
  DROP COLUMN compact_state;
