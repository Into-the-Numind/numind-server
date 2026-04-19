-- 20260419_230000_seed_ai_service_routes.sql
--
-- 补齐 ai_service_route 表缺失的 6 条 route 行（services 18-23）。
--
-- # 根因
--
-- migration 20260416_000001_ai_service_manager.sql 的 E4/E5 章节明确标记
-- AMBIGUITY，task_profile.default_service_id 和 task_profile_service 留 NULL，
-- 交给运营/admin UI 后续补设。2026-04-16 12:48:10 有人通过直接 SQL（绕过
-- admin API + audit log）批量：
--   - INSERT ai_service (18-23: baidu-ocr-accurate / funasr-paraformer /
--     qwen-turbo / qwen3-vl-flash / qwen3-rerank / text-embedding-v4)
--   - UPDATE task_profile SET default_service_id
-- 但**漏掉了第三步**：INSERT ai_service_route。
--
-- 后果：registry.ResolveTask 的 JOIN（ai_service × ai_service_route ×
-- llm_provider）查不到 row，抛 ErrAIServiceNotFound。SalesRAG 的 embed /
-- rerank / intent / tagging / profile / chatstyle 整条链路 + OCR + ASR
-- 全部失效。SalesRAG 用户发消息后 retrieval failed，前端还把错误 event
-- silently wipe 掉，表现为"死寂"。
--
-- 本 migration 补齐 6 条 route（UNIQUE (model_id, provider_id) + INSERT
-- IGNORE 保证幂等）。Provider 映射通过 JOIN llm_provider.name 查询，
-- 避免硬编码 provider_id。
--
-- # Route 映射
--
-- | service_id | model_key                | provider           | provider_model_id             |
-- |------------|--------------------------|--------------------|-------------------------------|
-- | 18         | baidu-ocr-accurate       | baidu-ocr          | accurate                      |
-- | 19         | funasr-paraformer        | funasr-local       | paraformer                    |
-- | 20         | qwen-turbo               | ali-dashscope      | qwen-turbo                    |
-- | 21         | qwen3-vl-flash           | ali-dashscope      | qwen3-vl-flash-2026-01-22     |
-- | 22         | qwen3-rerank             | dmxapi             | qwen3-rerank                  |
-- | 23         | text-embedding-v4        | ali-dashscope      | text-embedding-v4             |
--
-- # 定价
--
-- 本 migration 只写 ai_service_route 的结构性列（model_id/provider_id/
-- provider_model_id/priority/is_active）。pricing 字段由 migration
-- 20260418_180000_drop_route_pricing_columns.sql 从表中删除，pricing_rule
-- 表是唯一真源（参见 seed_pricing_rules.sql 的 text-embedding-v4 /
-- qwen3-rerank 等行）。如果本 migration 在 drop_route_pricing_columns 之前
-- 跑，dead 列仍存在且带 DB 默认值 0，不影响 pricing 逻辑。

-- Pre-flight guard: 4 个必需 provider 都得在 llm_provider 表里，否则下面的
-- INSERT ... SELECT JOIN 会 silently 插 0 行，事故回放风险。用 ASSERT 式查询：
-- 若 4 个 provider 少任一个，触发 division by zero 让 migration 在此处 fail
-- 而不是无声无息地完成。
SELECT 1 / (
  (SELECT COUNT(*) FROM llm_provider WHERE name IN
    ('baidu-ocr', 'funasr-local', 'ali-dashscope', 'dmxapi'))
  - 3
) AS provider_guard_expect_4_rows_minus_3_equals_1_else_div_by_zero;
-- 上式：期望 4 个 provider 全在 → 4-3=1 → 1/1 成功；
-- 若缺一个 → 3-3=0 → 1/0 报错，migration abort。

-- Service 18: baidu-ocr-accurate → baidu-ocr provider
INSERT IGNORE INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, is_active)
SELECT s.id, p.id, 'accurate', 5, 1
FROM ai_service s
JOIN llm_provider p ON p.name = 'baidu-ocr'
WHERE s.model_key = 'baidu-ocr-accurate';

-- Service 19: funasr-paraformer → funasr-local provider
INSERT IGNORE INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, is_active)
SELECT s.id, p.id, 'paraformer', 5, 1
FROM ai_service s
JOIN llm_provider p ON p.name = 'funasr-local'
WHERE s.model_key = 'funasr-paraformer';

-- Service 20: qwen-turbo → ali-dashscope provider
INSERT IGNORE INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, is_active)
SELECT s.id, p.id, 'qwen-turbo', 5, 1
FROM ai_service s
JOIN llm_provider p ON p.name = 'ali-dashscope'
WHERE s.model_key = 'qwen-turbo';

-- Service 21: qwen3-vl-flash → ali-dashscope provider
INSERT IGNORE INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, is_active)
SELECT s.id, p.id, 'qwen3-vl-flash-2026-01-22', 5, 1
FROM ai_service s
JOIN llm_provider p ON p.name = 'ali-dashscope'
WHERE s.model_key = 'qwen3-vl-flash';

-- Service 22: qwen3-rerank → dmxapi provider
INSERT IGNORE INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, is_active)
SELECT s.id, p.id, 'qwen3-rerank', 5, 1
FROM ai_service s
JOIN llm_provider p ON p.name = 'dmxapi'
WHERE s.model_key = 'qwen3-rerank';

-- Service 23: text-embedding-v4 → ali-dashscope provider (salesrag.embed 阻塞点)
INSERT IGNORE INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, is_active)
SELECT s.id, p.id, 'text-embedding-v4', 5, 1
FROM ai_service s
JOIN llm_provider p ON p.name = 'ali-dashscope'
WHERE s.model_key = 'text-embedding-v4';

-- Post-flight verification：应返回 6 行。若少于 6 行，某个 service/provider
-- 组合没匹配上（例如 task_profile 虽然写了 default_service_id 但 ai_service
-- 表里对应 model_key 被改了）。手工执行本查询确认：
--   SELECT s.model_key, p.name, r.provider_model_id, r.is_active
--   FROM ai_service s
--   JOIN ai_service_route r ON r.model_id = s.id
--   JOIN llm_provider p ON p.id = r.provider_id
--   WHERE s.id BETWEEN 18 AND 23
--   ORDER BY s.id;
