-- Credits System: Seed R2 coefficients (PLACEHOLDER — Track G will replace with real values from spike)
-- Part of credits-system feature (Phase 0 契约冻结)
-- See spec §2.6 + §5.5 for R2 spike产出验证 checklist
--
-- Spike SQL template (Track G executes on dev/prod):
--   SELECT provider, model, operation,
--          AVG(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)) AS avg_ratio,
--          STDDEV_POP(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)) AS std_ratio,
--          COUNT(*) AS sample_size
--   FROM usage_record
--   WHERE created_at > DATE_SUB(NOW(), INTERVAL 90 DAY)
--     AND prompt_tokens > 0 AND completion_tokens > 0
--   GROUP BY provider, model, operation
--   HAVING COUNT(*) >= 30;
--
-- Sample < 30 组合用保守默认 (1.500, 0.500, 0.300)。

-- PLACEHOLDER seed (7 rows). Track G replaces with real spike-derived data before Phase 1 merge.
INSERT INTO credit_estimation_coefficient
    (provider, model, operation, char_to_token_ratio, completion_prompt_ratio, safety_buffer_pct, version, is_active, change_reason, updated_by)
VALUES
    ('ali',    'qwen-turbo',              'sop_run',       1.500, 0.500, 0.200, 1, 1, 'initial from S3 spike', 'system'),
    ('ali',    'qwen-plus',               'sop_run',       1.500, 0.450, 0.200, 1, 1, 'initial from S3 spike', 'system'),
    ('volc',   'deepseek-v3-2-251201',    'sop_run',       1.500, 0.400, 0.200, 1, 1, 'initial from S3 spike', 'system'),
    ('volc',   'glm-4-7-251222',          'sop_run',       1.500, 0.400, 0.200, 1, 1, 'initial from S3 spike', 'system'),
    ('ali',    'qwen-turbo',              'sop_chat',      1.500, 0.300, 0.200, 1, 1, 'initial from S3 spike', 'system'),
    ('ali',    'qwen-turbo',              'salesrag_chat', 1.500, 0.600, 0.250, 1, 1, 'initial from S3 spike', 'system'),
    ('dmxapi', 'qwen-turbo-latest',       'salesrag_chat', 1.500, 0.600, 0.250, 1, 1, 'initial from S3 spike', 'system')
;
