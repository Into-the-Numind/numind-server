-- Rollback for 20260523_154000_register_dialectic_profile.sql
-- 移除 Task 3.7 task_profile 占位行.

DELETE FROM task_profile WHERE task_id = 'agent.dialectic';
