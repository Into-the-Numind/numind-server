-- 20260419_230000_seed_ai_service_routes_rollback.sql
--
-- 回滚 seed_ai_service_routes 插入的 6 条 ai_service_route 行。
--
-- 通过 (model_id, provider_model_id) 精确匹配本 migration 写入的 row。
-- 不使用 id 范围删除（安全起见不破坏其他人可能通过 admin UI 创建的 route）。

DELETE r FROM ai_service_route r
JOIN ai_service s ON s.id = r.model_id
JOIN llm_provider p ON p.id = r.provider_id
WHERE (s.model_key, p.name, r.provider_model_id) IN (
  ('baidu-ocr-accurate',  'baidu-ocr',     'accurate'),
  ('funasr-paraformer',   'funasr-local',  'paraformer'),
  ('qwen-turbo',          'ali-dashscope', 'qwen-turbo'),
  ('qwen3-vl-flash',      'ali-dashscope', 'qwen3-vl-flash-2026-01-22'),
  ('qwen3-rerank',        'dmxapi',        'qwen3-rerank'),
  ('text-embedding-v4',   'ali-dashscope', 'text-embedding-v4')
);
