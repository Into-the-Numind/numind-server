-- 20260729_180000_route_vision_to_qwen35_flash.sql
--
-- Switch image understanding fallback to qwen3.5-flash on Ali DashScope.
--
-- Context:
-- - Uploaded image fallback should use a single VLM pass, not Baidu OCR + VLM.
-- - The previous DashScope vision provider model id is being retired, so the
--   service registry needs the new effective model name.
--
-- Idempotency:
-- - ai_service.model_key is UNIQUE.
-- - ai_service_route has uk_model_provider(model_id, provider_id).
-- - task_profile rows are updated by stable task_id / service id joins.

-- Guard: ali-dashscope must exist, otherwise INSERT...SELECT would silently no-op.
SELECT 1 / (SELECT COUNT(*) FROM llm_provider WHERE name = 'ali-dashscope')
  AS provider_guard_expect_ali_dashscope_present_else_div_by_zero;

-- 1. Register the new Qwen vision service name.
INSERT INTO ai_service (
  model_key,
  display_name,
  service_type,
  capability_json,
  latency_tier,
  quality_tier,
  is_thinking,
  supports_thinking,
  thinking_only,
  is_active,
  created_at,
  updated_at
) VALUES (
  'qwen3.5-flash',
  'qwen3.5-flash',
  'llm',
  JSON_OBJECT(
    'input_modalities', JSON_ARRAY('text', 'image'),
    'output_modalities', JSON_ARRAY('text'),
    'accepts_image_inline', TRUE,
    'accepts_pdf_inline', FALSE,
    'accepts_audio_inline', FALSE,
    'max_inline_size_bytes', 20971520,
    'supports_vision_tool_calling', TRUE,
    'preferred_image_format', 'base64',
    'capabilities', JSON_ARRAY('chat')
  ),
  'fast',
  'standard',
  0,
  0,
  0,
  1,
  NOW(3),
  NOW(3)
) ON DUPLICATE KEY UPDATE
  display_name = VALUES(display_name),
  service_type = VALUES(service_type),
  capability_json = VALUES(capability_json),
  latency_tier = VALUES(latency_tier),
  quality_tier = VALUES(quality_tier),
  is_thinking = VALUES(is_thinking),
  supports_thinking = VALUES(supports_thinking),
  thinking_only = VALUES(thinking_only),
  is_active = 1,
  deprecated_at = NULL,
  updated_at = NOW(3);

-- 2. Bind qwen3.5-flash to Ali DashScope. The provider-side model id is also
-- qwen3.5-flash.
INSERT IGNORE INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, is_active, created_at, updated_at)
SELECT s.id, p.id, 'qwen3.5-flash', 100, 1, NOW(3), NOW(3)
FROM ai_service s
JOIN llm_provider p ON p.name = 'ali-dashscope'
WHERE s.model_key = 'qwen3.5-flash';

UPDATE ai_service_route r
JOIN ai_service s ON s.id = r.model_id
JOIN llm_provider p ON p.id = r.provider_id
SET r.provider_model_id = 'qwen3.5-flash',
    r.priority = GREATEST(COALESCE(r.priority, 0), 100),
    r.is_active = 1,
    r.updated_at = NOW(3)
WHERE s.model_key = 'qwen3.5-flash'
  AND p.name = 'ali-dashscope';

-- 3. Ensure the attachment vision task exists and routes to qwen3.5-flash.
INSERT IGNORE INTO task_profile (
  task_id,
  display_name,
  description,
  service_type,
  requirements,
  default_service_id,
  user_selectable
)
SELECT
  'attachment.vision_describe',
  '附件视觉描述',
  '上传图片时异步用 Qwen 视觉模型生成图片文字描述，供单模态模型 fallback 使用',
  'llm',
  JSON_OBJECT('input_modalities', JSON_ARRAY('text', 'image')),
  s.id,
  0
FROM ai_service s
WHERE s.model_key = 'qwen3.5-flash'
  AND s.is_active = 1
  AND s.deprecated_at IS NULL;

UPDATE task_profile tp
JOIN ai_service s ON s.model_key = 'qwen3.5-flash'
  AND s.is_active = 1
  AND s.deprecated_at IS NULL
SET tp.default_service_id = s.id,
    tp.description = '上传图片时异步用 Qwen 视觉模型生成图片文字描述，供单模态模型 fallback 使用',
    tp.requirements = JSON_OBJECT('input_modalities', JSON_ARRAY('text', 'image')),
    tp.updated_at = NOW()
WHERE tp.task_id = 'attachment.vision_describe';

-- 4. Move any internal task that still points at the retiring qwen3-vl-flash
-- service id to qwen3.5-flash. This keeps other vision-ish internal profiles
-- from continuing to call the old provider model id after it is retired.
UPDATE task_profile tp
JOIN ai_service old_s ON old_s.model_key = 'qwen3-vl-flash'
JOIN ai_service new_s ON new_s.model_key = 'qwen3.5-flash'
  AND new_s.is_active = 1
  AND new_s.deprecated_at IS NULL
SET tp.default_service_id = new_s.id,
    tp.updated_at = NOW()
WHERE tp.default_service_id = old_s.id;

-- Also move fallback/allowed profile bindings. If the new binding already
-- exists for the same profile + role, delete the duplicate old binding first to
-- avoid uk_profile_service_role conflicts.
DELETE old_bind
FROM task_profile_service old_bind
JOIN ai_service old_s ON old_s.id = old_bind.service_id
  AND old_s.model_key = 'qwen3-vl-flash'
JOIN ai_service new_s ON new_s.model_key = 'qwen3.5-flash'
JOIN task_profile_service new_bind ON new_bind.task_profile_id = old_bind.task_profile_id
  AND new_bind.service_id = new_s.id
  AND new_bind.role = old_bind.role;

UPDATE task_profile_service tps
JOIN ai_service old_s ON old_s.id = tps.service_id
  AND old_s.model_key = 'qwen3-vl-flash'
JOIN ai_service new_s ON new_s.model_key = 'qwen3.5-flash'
  AND new_s.is_active = 1
  AND new_s.deprecated_at IS NULL
SET tps.service_id = new_s.id
WHERE tps.service_id = old_s.id;

-- 5. Keep the old row as historical metadata, but do not route new calls to it.
UPDATE ai_service_route r
JOIN ai_service s ON s.id = r.model_id
JOIN llm_provider p ON p.id = r.provider_id
SET r.is_active = 0,
    r.updated_at = NOW(3)
WHERE s.model_key = 'qwen3-vl-flash'
  AND p.name = 'ali-dashscope';

UPDATE ai_service
SET is_active = 0,
    deprecated_at = COALESCE(deprecated_at, NOW(3)),
    updated_at = NOW(3)
WHERE model_key = 'qwen3-vl-flash';

-- 6. Pricing lookup follows provider + effective model_key.
INSERT INTO pricing_rule (
  service_type, provider, model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok, price_per_call, price_per_gb,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok, sell_price_per_call, sell_price_per_gb,
  is_active, created_at, updated_at
) VALUES (
  'llm_vision', 'ali-dashscope', 'qwen3.5-flash', 'flat', 'call',
  0.15, 1.50, 0, 0,
  0.15, 1.50, 0, 0,
  1, NOW(), NOW()
) ON DUPLICATE KEY UPDATE
  input_price_per_m_tok = VALUES(input_price_per_m_tok),
  output_price_per_m_tok = VALUES(output_price_per_m_tok),
  sell_input_price_per_m_tok = VALUES(sell_input_price_per_m_tok),
  sell_output_price_per_m_tok = VALUES(sell_output_price_per_m_tok),
  is_active = 1,
  updated_at = NOW();
