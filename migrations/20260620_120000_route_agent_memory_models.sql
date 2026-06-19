-- 2026-06-20 Fix: 修复 Agent 记忆系统(V1.5)的模型路由断裂 + 按"快/思考"分工重新绑定。
--
-- 起因: dev 上 agent.memory_extract / agent.memory_select 的 task_profile.default_service_id
-- 为 NULL(原指向 service_id=20 qwen-turbo, 该 service 后被删 → FK ON DELETE SET NULL 置空),
-- 导致记忆抽取/选择无模型可解析 → 静默失败 → 写入侧瘫痪(约 3 周零新 fact),
-- 连带画像重建 / 辩证洞察从不触发(上游饿死)。
-- 诊断详见 docs/analysis/2026-06-19-记忆系统通电清单.html。
--
-- 修复策略(按环节性质分工):
--   * 快环节(抽取 extract / 选择 select / 摘要 digest) → deepseek-v4-flash【关思考】
--     - 关思考是自动的: 调用方均未设 req.Thinking(默认 false), adapter 对
--       thinking_style='enable_thinking_kwarg' 的 !Thinking 分支会发 chat_template_kwargs.enable_thinking=false
--       (与后台任务 session.title 同一条路径, 见 internal/pkg/aiservice/adapter/dmxapi.go)。
--     - 这三个环节是高频/热路径/纯抽取-排序-摘要, 不需要推理 → 用快模型省钱提速。
--   * 思考环节(辩证洞察 dialectic) → deepseek-v4-pro【thinking_only】
--     - 记忆系统中唯一真正在 facts 之上做推理的环节, 低频/后台/读走缓存, 质量优先。
--   * embedding(去重 agent.embed) → text-embedding-v4: 本迁移不动(保持现状)。
--
-- 可移植: 按 model_key 解析 service_id(dev/prod 的自增 id 可能不同), 且仅当目标 model 存在且 active 时才更新。
-- 幂等: 重复执行结果一致。无 schema 变更, 纯 task_profile 数据重绑。
-- Rollback: 见同名 _rollback.sql。

-- 快环节 → deepseek-v4-flash
UPDATE task_profile
  SET default_service_id = (
        SELECT id FROM ai_service WHERE model_key = 'deepseek-v4-flash' AND is_active = 1 LIMIT 1
      ),
      updated_at = NOW()
  WHERE task_id IN ('agent.memory_extract', 'agent.memory_select', 'agent.digest')
    AND EXISTS (SELECT 1 FROM ai_service WHERE model_key = 'deepseek-v4-flash' AND is_active = 1);

-- 思考环节 → deepseek-v4-pro
UPDATE task_profile
  SET default_service_id = (
        SELECT id FROM ai_service WHERE model_key = 'deepseek-v4-pro' AND is_active = 1 LIMIT 1
      ),
      updated_at = NOW()
  WHERE task_id = 'agent.dialectic'
    AND EXISTS (SELECT 1 FROM ai_service WHERE model_key = 'deepseek-v4-pro' AND is_active = 1);
