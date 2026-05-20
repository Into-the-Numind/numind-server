-- Rollback for agent-mode #9 compact ALTER agent_run
-- 顺序与 spec §2.1 一致（compact_state 先 / compact_summary 后），
-- 两列互不依赖，MySQL 内 DROP 顺序在同一 ALTER 中等价

ALTER TABLE agent_run
  DROP COLUMN compact_state,
  DROP COLUMN compact_summary;
