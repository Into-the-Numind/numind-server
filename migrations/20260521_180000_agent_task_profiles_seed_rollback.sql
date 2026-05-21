-- Rollback for 20260521_180000_agent_task_profiles_seed.sql
-- Removes the 7 agent-mode task profile rows.

DELETE FROM task_profile WHERE task_id IN (
  'agent.run',
  'agent.embed',
  'agent.sync_turn',
  'agent.compact',
  'agent.narration_fallback',
  'agent.injection_check',
  'agent.permission_check'
);
