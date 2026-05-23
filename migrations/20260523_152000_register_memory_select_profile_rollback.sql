-- Rollback for 20260523_152000_register_memory_select_profile.sql
-- 移除 Task 3.4 task_profile 占位行.

DELETE FROM task_profile WHERE task_id = 'agent.memory_select';
