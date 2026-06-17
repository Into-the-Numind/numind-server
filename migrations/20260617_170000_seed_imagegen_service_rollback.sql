-- 20260617_170000_seed_imagegen_service_rollback.sql
--
-- 回滚 20260617_170000_seed_imagegen_service.sql：移除 image_gen 文生图服务的
-- registry 注册（task_profile / ai_service_route / pricing_rule / ai_service）。
-- 回滚后 image_gen 工具调用 aiservice.ImageGen 会 ResolveTask 失败 →
-- 工具映射为软错误（不杀 run），不会扣费、不会裸 HTTP（代码已切换到网关）。
--
-- 删除顺序：先删引用方（task_profile / route / pricing），最后删 ai_service。

-- 1. task_profile
DELETE FROM task_profile WHERE task_id = 'agent.image_gen';

-- 2. ai_service_route（按 model_key JOIN 定位）
DELETE r FROM ai_service_route r
JOIN ai_service s ON s.id = r.model_id
WHERE s.model_key = 'gemini-2.5-flash-image';

-- 3. pricing_rule
DELETE FROM pricing_rule
WHERE service_type = 'image_gen'
  AND provider = 'dmxapi'
  AND model = 'gemini-2.5-flash-image';

-- 4. ai_service
DELETE FROM ai_service WHERE model_key = 'gemini-2.5-flash-image';
