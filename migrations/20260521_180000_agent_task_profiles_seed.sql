-- Agent Mode #14/14 e2e rollout — seed 7 task profile rows for agent-mode LLM/embed routing.
-- Idempotent: ON DUPLICATE KEY UPDATE updates updated_at only; routing values preserved on re-run.
-- Pre-condition: task_profile table exists (managed by ai-service-manager feature).
-- Companion: rollback file removes only the 7 new rows.

INSERT INTO task_profile (task_id, model_route, description, created_at, updated_at) VALUES
  ('agent.run',                  'qwen-turbo',         'Agent ReAct main LLM call',                          NOW(), NOW()),
  ('agent.embed',                'text-embedding-v4',  'Agent memory L1/L2 retrieval embedder',              NOW(), NOW()),
  ('agent.sync_turn',            'qwen-turbo',         'Agent memory turn summary extraction',               NOW(), NOW()),
  ('agent.compact',              'qwen-plus',          'Agent context compaction (BASE_COMPACT_PROMPT)',     NOW(), NOW()),
  ('agent.narration_fallback',   'qwen-turbo',         'Agent narration LLM dynamic generation',             NOW(), NOW()),
  ('agent.injection_check',      'qwen-turbo',         'Agent compliance injection classifier (fail-deny)',  NOW(), NOW()),
  ('agent.permission_check',     'qwen-turbo',         'Agent permission L3 auto-mode classifier (fail-allow)', NOW(), NOW())
ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at);
