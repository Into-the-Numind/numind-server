-- Fix: align task_profile.salesrag.embed + ai_service.text-embedding-v4 dimension
-- with the existing DashVector collection `sales_rag_prod` (2048 dim).
--
-- Root cause: 2026-04-16 ai_service_manager migration (20260416_000001) seeded
-- dimension=1024 (qwen text-embedding-v4 default), but the prod vector
-- collection was created earlier with 2048 dim. The capability-matching
-- algorithm (profile/capability.go) does exact-match on dimension, so
-- embeddings produced at 1024 dim cannot be inserted into the 2048 collection.
--
-- Fix: update both DB rows to dimension=2048 and pin the code-side EmbedRequest
-- to Dimension=2048 (biz.go), so DashScope returns 2048-dim vectors that match
-- the collection schema. text-embedding-v4 supports 64/128/256/512/768/1024/
-- 1536/2048 via explicit `dimension` parameter.
--
-- No rollback needed: restoring 1024 reproduces the bug. To physically revert
-- the schema would require rebuilding the DashVector collection.

UPDATE task_profile
SET requirements = JSON_SET(requirements, '$.dimension', 2048),
    description  = '销售知识库文档向量化，embedding 维度锁定为 2048（DashVector collection sales_rag_prod）'
WHERE task_id = 'salesrag.embed';

UPDATE ai_service
SET capability_json = JSON_SET(capability_json, '$.dimension', 2048)
WHERE model_key = 'text-embedding-v4';
