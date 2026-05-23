-- Rollback for 20260523_151000_register_memory_extract_profile.sql
-- 移除 Task 3.3 task_profile 占位行.

DELETE FROM task_profile WHERE task_id = 'agent.memory_extract';
