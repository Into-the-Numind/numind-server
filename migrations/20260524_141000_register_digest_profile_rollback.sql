-- Rollback for: 20260524_141000_register_digest_profile.sql
-- Feature: agent-mode-v15-memory-layer-a (Task 3.8)

DELETE FROM task_profile WHERE task_id = 'agent.digest';
