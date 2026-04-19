-- Rollback: Drop action_log table
-- 警告：rollback 后 grant_membership / billing_mode_init 会再次失败
DROP TABLE IF EXISTS action_log;
