-- Rollback: 把 agent.sync_turn / agent.memory_extract 还原到 qwen-turbo
-- (service_id=20). 仅当当前是 24 (deepseek-v4-pro) 时回滚.

UPDATE task_profile
  SET default_service_id = 20, updated_at = NOW()
  WHERE task_id IN ('agent.sync_turn', 'agent.memory_extract')
    AND default_service_id = 24;
