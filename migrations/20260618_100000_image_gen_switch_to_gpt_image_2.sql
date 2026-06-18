-- 文生图模型从 Gemini 切到 gpt-image-2（dmxapi OpenAI 兼容 /v1/images/generations）
--
-- 背景：dmxapi 的 Gemini 图像端点 /v1beta/models/<m>:generateContent 从国内 dev/prod
-- 网络不可达（直接 curl 正确 key 也 http_code=000 / 25s 超时无响应），文生图实际调不通。
-- 改用 dmxapi 的 gpt-image-2，走 OpenAI 兼容 POST /v1/images/generations
-- （Authorization: Bearer + body {model,prompt,size} + 响应 data[0].b64_json）。
-- dev 实测：http 200 / ~18s / 返回 2MB 真图。适配器 internal/pkg/aiservice/adapter/dmxapi.go
-- 的 ImageGen 已同步改写（端点/认证/body/响应解析全部换成 OpenAI-images 格式）。
--
-- ⚠️ dev/prod 部署不自动跑 migration（MEMORY: project_dev_deploy_migration_gap），须手工 SSH 执行。
-- 幂等：按当前值 'gemini-2.5-flash-image' 作守卫，二次执行匹配 0 行即 no-op。

-- 1) 路由 provider_model_id 决定请求 body 的 model 字段 —— 这是让调用真正生效的关键改动。
UPDATE ai_service_route r
  JOIN ai_service s ON r.model_id = s.id
SET r.provider_model_id = 'gpt-image-2'
WHERE s.service_type = 'image_gen'
  AND r.provider_model_id = 'gemini-2.5-flash-image';

-- 2) 服务 model_key / display_name 同步（标签 + 可观测一致性；任务解析按 service id，不受影响）。
UPDATE ai_service
SET model_key = 'gpt-image-2',
    display_name = 'GPT Image 2 (文生图)'
WHERE service_type = 'image_gen'
  AND model_key = 'gemini-2.5-flash-image';
