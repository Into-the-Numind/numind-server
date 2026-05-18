-- +migrate Down
-- Rollback for sop-salesrag-parent-scope.
-- WARNING: 会重新打开数据泄露 bug, 仅 critical incident 使用.

UPDATE sop_template SET creator_user_id = NULL WHERE id IN (1, 2);
DROP TABLE IF EXISTS sales_agent_owner;
