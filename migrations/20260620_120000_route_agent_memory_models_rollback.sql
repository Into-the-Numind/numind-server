-- Rollback: 把记忆快环节(extract/select/digest)从 deepseek-v4-flash 回退到 deepseek-v4-pro。
--
-- 注意: agent.memory_extract / agent.memory_select 的原始状态是 NULL(即 bug 本身),
-- 回滚【不】恢复 NULL —— 那会重新让写入侧瘫痪。改为回退到 deepseek-v4-pro
-- (agent.run 同款, 必定存在且 active)作为安全态。
-- agent.dialectic 本就指向 deepseek-v4-pro, 回滚不影响。
-- agent.embed 本迁移未动, 回滚也不涉及。

UPDATE task_profile
  SET default_service_id = (
        SELECT id FROM ai_service WHERE model_key = 'deepseek-v4-pro' AND is_active = 1 LIMIT 1
      ),
      updated_at = NOW()
  WHERE task_id IN ('agent.memory_extract', 'agent.memory_select', 'agent.digest')
    AND EXISTS (SELECT 1 FROM ai_service WHERE model_key = 'deepseek-v4-pro' AND is_active = 1);
