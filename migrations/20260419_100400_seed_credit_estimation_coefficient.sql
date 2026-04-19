-- Credits System: Seed R2 coefficients (initial conservative default)
-- Part of credits-system feature (Phase 0 契约冻结)
-- See spec §2.6 + §5.5 for R2 spike 产出验证 checklist
--
-- ============================================================
-- PROVENANCE (Track G R2 spike 产出)
-- ============================================================
-- Spike executed: 2026-04-19 01:53:35 CST on dev env (numind-mysql-dev 8.4.2, db=numind-dev)
-- Spike SQL:
--   SELECT provider, model, operation,
--          COUNT(*) AS sample_size,
--          AVG(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)) AS avg_ratio,
--          STDDEV_POP(completion_tokens * 1.0 / NULLIF(prompt_tokens, 0)) AS std_ratio
--   FROM usage_record
--   WHERE created_at > DATE_SUB(NOW(), INTERVAL 90 DAY)
--     AND prompt_tokens > 0 AND completion_tokens > 0
--   GROUP BY provider, model, operation;
--
-- Result summary:
--   Total rows in usage_record (90d window):  284 (278 with both tokens > 0)
--   Distinct (provider, model, operation):    37
--   Groups with sample_size >= 30:            2
--   - dmxapi/qwen-turbo-latest/salesrag_tagging: n=76, avg=0.0292, std=0.0059
--   - dmxapi/claude-sonnet-4-6/sop_node_execute: n=35, avg=1.8265, std=1.6511
--
-- SEMANTIC-OP MISMATCH (critical decision, see docs/credits-system-r2-spike-report.md §6):
--   usage_record.operation records provider-side implementation labels
--   (sop_node_execute, chatbot_chat, salesrag_tagging, sop_chat_stream, ali_vision_analyze)
--   while credit_estimation_coefficient.operation keys on spec §1.7 user-level
--   semantic operations (sop_run, sop_chat, salesrag_chat, profile_analysis,
--   file_parse, style_analysis, ocr). Example: salesrag_tagging's avg_ratio=0.0292
--   is a sub-step ratio — using it as salesrag_chat coefficient would under-reserve.
--   Therefore Phase 0 seed uses conservative defaults rather than cross-mapping.
--
-- Dev-env coverage gap: seed_pricing_rules.sql lists (volc/deepseek-v3-2-251201,
-- volc/doubao-seed-2-0-lite-260215, ali/qwen-vl-plus, ali/qwen3-vl,
-- volc/doubao-seed-1-8-251228, ali/text-embedding-v4, volc/doubao-embedding-vision-250615,
-- dmxapi/qwen3-rerank) — all under-sampled on dev. Production calibration (append-only
-- v2) scheduled 2-4 weeks post-launch per spec §5.5.
--
-- Seed decision (see report §7):
--   char_to_token_ratio      = 1.500 everywhere (zh token convention, not derivable from usage_record)
--   completion_prompt_ratio  = 0.500 (spec §5.5 conservative default — reserve more, reconcile refunds)
--   safety_buffer_pct        = 0.300 (3σ-equivalent upper bound per spec, safer for dev→prod drift)
--   change_reason            = 'initial conservative default (R2 spike 2026-04-19 lacked semantic-op-level data)'
--
-- Seed contents:
--   7 semantic-op × (provider, model) combinations (spec §2.6 示例对齐)
--   1 global fallback row (provider='', model='', operation='') for lookup misses
-- ============================================================

INSERT INTO credit_estimation_coefficient
    (provider, model, operation, char_to_token_ratio, completion_prompt_ratio, safety_buffer_pct, version, is_active, change_reason, updated_by)
VALUES
    -- Representative seed combinations (aligned with spec §2.6 example matrix).
    -- All use conservative defaults pending semantic-op-level calibration post-launch.
    ('ali',    'qwen-turbo',              'sop_run',         1.500, 0.500, 0.300, 1, 1, 'initial conservative default (R2 spike 2026-04-19 lacked semantic-op-level data)', 'system'),
    ('ali',    'qwen-plus',               'sop_run',         1.500, 0.500, 0.300, 1, 1, 'initial conservative default (R2 spike 2026-04-19 lacked semantic-op-level data)', 'system'),
    ('volc',   'deepseek-v3-2-251201',    'sop_run',         1.500, 0.500, 0.300, 1, 1, 'initial conservative default (R2 spike 2026-04-19 lacked semantic-op-level data)', 'system'),
    ('volc',   'glm-4-7-251222',          'sop_run',         1.500, 0.500, 0.300, 1, 1, 'initial conservative default (R2 spike 2026-04-19 lacked semantic-op-level data)', 'system'),
    ('ali',    'qwen-turbo',              'sop_chat',        1.500, 0.500, 0.300, 1, 1, 'initial conservative default (R2 spike 2026-04-19 lacked semantic-op-level data)', 'system'),
    ('ali',    'qwen-turbo',              'salesrag_chat',   1.500, 0.500, 0.300, 1, 1, 'initial conservative default (R2 spike 2026-04-19 lacked semantic-op-level data)', 'system'),
    ('dmxapi', 'qwen-turbo-latest',       'salesrag_chat',   1.500, 0.500, 0.300, 1, 1, 'initial conservative default (R2 spike 2026-04-19 lacked semantic-op-level data)', 'system'),

    -- Global fallback row: matched by application-layer when exact (provider, model, operation) lookup misses.
    -- See spec §2.3 + docs/credits-system-r2-spike-report.md §7.
    ('',       '',                        '',                1.500, 0.500, 0.300, 1, 1, 'global fallback default (R2 spike 2026-04-19)', 'system')
;
