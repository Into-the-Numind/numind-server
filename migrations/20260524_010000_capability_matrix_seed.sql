-- Migration: capability matrix seed — backfill existing models + add 6 new model stubs
-- Feature: agent-mode-v15-multimodal task 1.1 (Capability Matrix 重构)
-- Idempotent: UPDATE uses WHERE clause on model_key; INSERT uses ON DUPLICATE KEY UPDATE.
-- NOTE: capability_json uses MySQL JSON column syntax; SQLite not supported here.
--
-- Rollback (manual):
--   UPDATE ai_service SET capability_json = NULL
--     WHERE model_key IN (
--       'qwen3-vl-flash-2026-01-22', 'qwen-vl-plus', 'doubao-seed-1-8-251228',
--       'qwen-long', 'qwen-turbo', 'qwen-turbo-latest', 'qwen-plus',
--       'deepseek-v3-2-251201', 'deepseek-v3-250324', 'glm-4-7-251222',
--       'doubao-seed-2-0-lite-260215', 'doubao-seed-1-6-flash-250828'
--     );
--   DELETE FROM ai_service
--     WHERE model_key IN (
--       'mimo-v2-5-pro', 'kimi-k2-5', 'kimi-k2-6',
--       'glm-5-1', 'minimax-m2-7', 'qwen-3-7-max'
--     );

-- ============================================================================
-- Part 1: UPDATE existing 12 models with structured capability_json
-- (spec text says "11 existing models"; actual count is 12 because qwen-turbo-latest
-- via DMXAPI is included alongside qwen-turbo. 6 new model stubs in Part 2.)
-- ============================================================================

-- Multimodal vision models: accepts images inline (20 MB limit, base64 format)
UPDATE ai_service
SET capability_json = JSON_SET(
    IFNULL(capability_json, '{}'),
    '$.accepts_image_inline',        TRUE,
    '$.accepts_pdf_inline',          FALSE,
    '$.accepts_audio_inline',        FALSE,
    '$.max_inline_size_bytes',       20971520,
    '$.supports_vision_tool_calling',TRUE,
    '$.preferred_image_format',      'base64'
)
WHERE model_key IN ('qwen3-vl-flash-2026-01-22', 'qwen-vl-plus', 'doubao-seed-1-8-251228');

-- qwen-long: accepts PDF inline (100 MB limit), text-only for images
UPDATE ai_service
SET capability_json = JSON_SET(
    IFNULL(capability_json, '{}'),
    '$.accepts_image_inline',        FALSE,
    '$.accepts_pdf_inline',          TRUE,
    '$.accepts_audio_inline',        FALSE,
    '$.max_inline_size_bytes',       104857600,
    '$.supports_vision_tool_calling',FALSE,
    '$.preferred_image_format',      'base64'
)
WHERE model_key = 'qwen-long';

-- Text-only LLM models: no inline modalities
UPDATE ai_service
SET capability_json = JSON_SET(
    IFNULL(capability_json, '{}'),
    '$.accepts_image_inline',        FALSE,
    '$.accepts_pdf_inline',          FALSE,
    '$.accepts_audio_inline',        FALSE,
    '$.max_inline_size_bytes',       0,
    '$.supports_vision_tool_calling',FALSE,
    '$.preferred_image_format',      'base64'
)
WHERE model_key IN (
    'qwen-turbo',
    'qwen-turbo-latest',
    'qwen-plus',
    'deepseek-v3-2-251201',
    'deepseek-v3-250324',
    'glm-4-7-251222',
    'doubao-seed-2-0-lite-260215',
    'doubao-seed-1-6-flash-250828'
);

-- ============================================================================
-- Part 2: INSERT 6 new model stubs
-- ============================================================================
-- These are stub entries for provider routing setup by task 1.3/1.4.
-- service_type, latency_tier, quality_tier follow existing model conventions.
-- Routes (ai_service_route rows) are NOT inserted here — they must be added
-- via admin UI or a separate route seed once providers are configured.
-- ============================================================================

-- mimo-v2-5-pro: MiMo V2.5 Pro (小米, multimodal — accepts images inline)
INSERT INTO ai_service (
    model_key, display_name, service_type,
    capability_json,
    latency_tier, quality_tier,
    is_active, is_thinking, supports_thinking, thinking_only,
    sort_order, created_at, updated_at
) VALUES (
    'mimo-v2-5-pro',
    'MiMo V2.5 Pro',
    'llm',
    JSON_OBJECT(
        'accepts_image_inline',         TRUE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        20971520,
        'supports_vision_tool_calling', TRUE,
        'preferred_image_format',       'base64'
    ),
    'standard', 'standard',
    TRUE, FALSE, FALSE, FALSE,
    200, NOW(), NOW()
) ON DUPLICATE KEY UPDATE
    capability_json = JSON_OBJECT(
        'accepts_image_inline',         TRUE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        20971520,
        'supports_vision_tool_calling', TRUE,
        'preferred_image_format',       'base64'
    ),
    updated_at = NOW();

-- kimi-k2-5: Kimi K2.5 (月之暗面, multimodal — accepts images inline)
INSERT INTO ai_service (
    model_key, display_name, service_type,
    capability_json,
    latency_tier, quality_tier,
    is_active, is_thinking, supports_thinking, thinking_only,
    sort_order, created_at, updated_at
) VALUES (
    'kimi-k2-5',
    'Kimi K2.5',
    'llm',
    JSON_OBJECT(
        'accepts_image_inline',         TRUE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        20971520,
        'supports_vision_tool_calling', TRUE,
        'preferred_image_format',       'base64'
    ),
    'standard', 'standard',
    TRUE, FALSE, FALSE, FALSE,
    210, NOW(), NOW()
) ON DUPLICATE KEY UPDATE
    capability_json = JSON_OBJECT(
        'accepts_image_inline',         TRUE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        20971520,
        'supports_vision_tool_calling', TRUE,
        'preferred_image_format',       'base64'
    ),
    updated_at = NOW();

-- kimi-k2-6: Kimi K2.6 (月之暗面, multimodal — accepts images inline)
INSERT INTO ai_service (
    model_key, display_name, service_type,
    capability_json,
    latency_tier, quality_tier,
    is_active, is_thinking, supports_thinking, thinking_only,
    sort_order, created_at, updated_at
) VALUES (
    'kimi-k2-6',
    'Kimi K2.6',
    'llm',
    JSON_OBJECT(
        'accepts_image_inline',         TRUE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        20971520,
        'supports_vision_tool_calling', TRUE,
        'preferred_image_format',       'base64'
    ),
    'standard', 'standard',
    TRUE, FALSE, FALSE, FALSE,
    220, NOW(), NOW()
) ON DUPLICATE KEY UPDATE
    capability_json = JSON_OBJECT(
        'accepts_image_inline',         TRUE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        20971520,
        'supports_vision_tool_calling', TRUE,
        'preferred_image_format',       'base64'
    ),
    updated_at = NOW();

-- glm-5-1: GLM 5.1 (智谱, text-only — no inline modalities)
INSERT INTO ai_service (
    model_key, display_name, service_type,
    capability_json,
    latency_tier, quality_tier,
    is_active, is_thinking, supports_thinking, thinking_only,
    sort_order, created_at, updated_at
) VALUES (
    'glm-5-1',
    'GLM 5.1',
    'llm',
    JSON_OBJECT(
        'accepts_image_inline',         FALSE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        0,
        'supports_vision_tool_calling', FALSE,
        'preferred_image_format',       'base64'
    ),
    'standard', 'standard',
    TRUE, FALSE, FALSE, FALSE,
    230, NOW(), NOW()
) ON DUPLICATE KEY UPDATE
    capability_json = JSON_OBJECT(
        'accepts_image_inline',         FALSE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        0,
        'supports_vision_tool_calling', FALSE,
        'preferred_image_format',       'base64'
    ),
    updated_at = NOW();

-- minimax-m2-7: MiniMax M2.7 (text-only — no inline modalities)
INSERT INTO ai_service (
    model_key, display_name, service_type,
    capability_json,
    latency_tier, quality_tier,
    is_active, is_thinking, supports_thinking, thinking_only,
    sort_order, created_at, updated_at
) VALUES (
    'minimax-m2-7',
    'MiniMax M2.7',
    'llm',
    JSON_OBJECT(
        'accepts_image_inline',         FALSE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        0,
        'supports_vision_tool_calling', FALSE,
        'preferred_image_format',       'base64'
    ),
    'standard', 'standard',
    TRUE, FALSE, FALSE, FALSE,
    240, NOW(), NOW()
) ON DUPLICATE KEY UPDATE
    capability_json = JSON_OBJECT(
        'accepts_image_inline',         FALSE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        0,
        'supports_vision_tool_calling', FALSE,
        'preferred_image_format',       'base64'
    ),
    updated_at = NOW();

-- qwen-3-7-max: Qwen 3.7 Max (text-only — no inline modalities)
INSERT INTO ai_service (
    model_key, display_name, service_type,
    capability_json,
    latency_tier, quality_tier,
    is_active, is_thinking, supports_thinking, thinking_only,
    sort_order, created_at, updated_at
) VALUES (
    'qwen-3-7-max',
    'Qwen 3.7 Max',
    'llm',
    JSON_OBJECT(
        'accepts_image_inline',         FALSE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        0,
        'supports_vision_tool_calling', FALSE,
        'preferred_image_format',       'base64'
    ),
    'standard', 'standard',
    TRUE, FALSE, FALSE, FALSE,
    250, NOW(), NOW()
) ON DUPLICATE KEY UPDATE
    capability_json = JSON_OBJECT(
        'accepts_image_inline',         FALSE,
        'accepts_pdf_inline',           FALSE,
        'accepts_audio_inline',         FALSE,
        'max_inline_size_bytes',        0,
        'supports_vision_tool_calling', FALSE,
        'preferred_image_format',       'base64'
    ),
    updated_at = NOW();
