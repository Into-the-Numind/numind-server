-- 2026-05-28 Hotfix: 把 agent.sync_turn / agent.memory_extract 从 qwen-turbo
-- (ali-dashscope, service_id=20) 切到 deepseek-v4-pro (dmxapi, service_id=24).
--
-- 起因: dev 上 2026-05-28 阿里云 DashScope 报 HTTP 403 AllocationQuota.FreeTierOnly
-- ("The free tier of the model has been exhausted"), memory 子任务全部 fail. 主
-- chat agent.run 已经走 service_id=24 (dmxapi/deepseek-v4-pro, 见 task_profile id=15)
-- 所以这里把 memory 子任务对齐到同一服务即可解决。
--
-- 幂等: 仅当当前指向 service_id=20 (qwen-turbo) 时才迁移, 避免覆盖人工后续调整.
-- Rollback: 见同名 _rollback.sql.

UPDATE task_profile
  SET default_service_id = 24, updated_at = NOW()
  WHERE task_id IN ('agent.sync_turn', 'agent.memory_extract')
    AND default_service_id = 20;
