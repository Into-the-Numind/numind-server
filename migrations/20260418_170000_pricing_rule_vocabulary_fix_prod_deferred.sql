-- PROD DEFERRED: Execute this AFTER prod code deploy and BEFORE smoke testing
-- new Gateway paths. Running against current prod will INSERT missing
-- pricing_rule rows with suffixed provider names and add thinking
-- suffix row. Idempotent via INSERT IGNORE / UPDATE-by-old-model-key.
--
-- IMPORTANT: Before running, verify prod llm_provider names match dev:
--   SELECT DISTINCT name FROM llm_provider WHERE is_active = TRUE;
-- Expected names on prod: dmxapi-ssvip, aihubmix (and others).
-- If names differ, adjust the INSERT IGNORE provider values below.
--
-- =============================================================================
-- Migration: pricing_rule vocabulary fix (T-arch-prereq) — PROD COPY
-- Date: 2026-04-18
-- =============================================================================

START TRANSACTION;

-- -----------------------------------------------------------------------
-- Fix 1: Rename claude-sonnet-4-6-think → claude-sonnet-4-6-thinking
-- -----------------------------------------------------------------------
UPDATE pricing_rule
SET model = 'claude-sonnet-4-6-thinking'
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'claude-sonnet-4-6-think'
  AND is_active = TRUE;

-- -----------------------------------------------------------------------
-- Fix 2: Add dmxapi-ssvip rows
-- -----------------------------------------------------------------------

-- dmxapi-ssvip: claude-sonnet-4-6 (flat)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, 'dmxapi-ssvip', model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'claude-sonnet-4-6'
  AND is_active = TRUE;

-- dmxapi-ssvip: claude-sonnet-4-6-thinking (flat)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, 'dmxapi-ssvip', model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'claude-sonnet-4-6-thinking'
  AND is_active = TRUE;

-- dmxapi-ssvip: deepseek-v3.2 (flat)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, 'dmxapi-ssvip', model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'deepseek-v3.2'
  AND is_active = TRUE;

-- dmxapi-ssvip: gemini-3.1-pro-preview (tiered_token)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, 'dmxapi-ssvip', model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'gemini-3.1-pro-preview'
  AND is_active = TRUE;

-- dmxapi-ssvip: gpt-5.4 (tiered_token)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, 'dmxapi-ssvip', model, billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'gpt-5.4'
  AND is_active = TRUE;

-- dmxapi-ssvip tiers: gemini-3.1-pro-preview
-- WHERE NOT EXISTS guard: pricing_rule_tier has no unique key on
-- (rule_id, token_type, min_tokens), so INSERT IGNORE would not prevent
-- duplicate tier rows on re-run. Explicit NOT EXISTS check ensures idempotency.
INSERT INTO pricing_rule_tier
  (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT
  new_rule.id,
  src_tier.token_type,
  src_tier.min_tokens,
  src_tier.max_tokens,
  src_tier.cost_per_mtok,
  src_tier.sell_per_mtok
FROM pricing_rule_tier src_tier
JOIN pricing_rule src_rule
  ON src_tier.rule_id = src_rule.id
  AND src_rule.service_type = 'llm_chat'
  AND src_rule.provider = 'aihubmix'
  AND src_rule.model = 'gemini-3.1-pro-preview'
  AND src_rule.is_active = TRUE
JOIN pricing_rule new_rule
  ON new_rule.service_type = 'llm_chat'
  AND new_rule.provider = 'dmxapi-ssvip'
  AND new_rule.model = 'gemini-3.1-pro-preview'
  AND new_rule.is_active = TRUE
WHERE NOT EXISTS (
  SELECT 1 FROM pricing_rule_tier t2
  WHERE t2.rule_id    = new_rule.id
    AND t2.token_type  = src_tier.token_type
    AND t2.min_tokens  = src_tier.min_tokens
);

-- dmxapi-ssvip tiers: gpt-5.4
INSERT INTO pricing_rule_tier
  (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT
  new_rule.id,
  src_tier.token_type,
  src_tier.min_tokens,
  src_tier.max_tokens,
  src_tier.cost_per_mtok,
  src_tier.sell_per_mtok
FROM pricing_rule_tier src_tier
JOIN pricing_rule src_rule
  ON src_tier.rule_id = src_rule.id
  AND src_rule.service_type = 'llm_chat'
  AND src_rule.provider = 'aihubmix'
  AND src_rule.model = 'gpt-5.4'
  AND src_rule.is_active = TRUE
JOIN pricing_rule new_rule
  ON new_rule.service_type = 'llm_chat'
  AND new_rule.provider = 'dmxapi-ssvip'
  AND new_rule.model = 'gpt-5.4'
  AND new_rule.is_active = TRUE
WHERE NOT EXISTS (
  SELECT 1 FROM pricing_rule_tier t2
  WHERE t2.rule_id    = new_rule.id
    AND t2.token_type  = src_tier.token_type
    AND t2.min_tokens  = src_tier.min_tokens
);

-- -----------------------------------------------------------------------
-- Fix 3: Add aihubmix thinking model variants
-- -----------------------------------------------------------------------

-- aihubmix: gemini-3.1-pro-preview-thinking (tiered_token)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, provider, 'gemini-3.1-pro-preview-thinking', billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'gemini-3.1-pro-preview'
  AND is_active = TRUE;

-- aihubmix: gemini-3.1-pro-preview-thinking tiers
INSERT INTO pricing_rule_tier
  (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT
  new_rule.id,
  src_tier.token_type,
  src_tier.min_tokens,
  src_tier.max_tokens,
  src_tier.cost_per_mtok,
  src_tier.sell_per_mtok
FROM pricing_rule_tier src_tier
JOIN pricing_rule src_rule
  ON src_tier.rule_id = src_rule.id
  AND src_rule.service_type = 'llm_chat'
  AND src_rule.provider = 'aihubmix'
  AND src_rule.model = 'gemini-3.1-pro-preview'
  AND src_rule.is_active = TRUE
JOIN pricing_rule new_rule
  ON new_rule.service_type = 'llm_chat'
  AND new_rule.provider = 'aihubmix'
  AND new_rule.model = 'gemini-3.1-pro-preview-thinking'
  AND new_rule.is_active = TRUE
WHERE NOT EXISTS (
  SELECT 1 FROM pricing_rule_tier t2
  WHERE t2.rule_id    = new_rule.id
    AND t2.token_type  = src_tier.token_type
    AND t2.min_tokens  = src_tier.min_tokens
);

-- aihubmix: deepseek-v3.2-thinking (flat)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, provider, 'deepseek-v3.2-thinking', billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'deepseek-v3.2'
  AND is_active = TRUE;

-- aihubmix: gpt-5.4-thinking (tiered_token)
INSERT IGNORE INTO pricing_rule
  (service_type, provider, model, billing_mode, flat_unit,
   input_price_per_m_tok, output_price_per_m_tok,
   sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   price_per_call, sell_price_per_call,
   price_per_gb, sell_price_per_gb,
   is_active, created_at, updated_at)
SELECT
  service_type, provider, 'gpt-5.4-thinking', billing_mode, flat_unit,
  input_price_per_m_tok, output_price_per_m_tok,
  sell_input_price_per_m_tok, sell_output_price_per_m_tok,
  price_per_call, sell_price_per_call,
  price_per_gb, sell_price_per_gb,
  is_active, NOW(), NOW()
FROM pricing_rule
WHERE service_type = 'llm_chat'
  AND provider = 'aihubmix'
  AND model = 'gpt-5.4'
  AND is_active = TRUE;

-- aihubmix: gpt-5.4-thinking tiers
INSERT INTO pricing_rule_tier
  (rule_id, token_type, min_tokens, max_tokens, cost_per_mtok, sell_per_mtok)
SELECT
  new_rule.id,
  src_tier.token_type,
  src_tier.min_tokens,
  src_tier.max_tokens,
  src_tier.cost_per_mtok,
  src_tier.sell_per_mtok
FROM pricing_rule_tier src_tier
JOIN pricing_rule src_rule
  ON src_tier.rule_id = src_rule.id
  AND src_rule.service_type = 'llm_chat'
  AND src_rule.provider = 'aihubmix'
  AND src_rule.model = 'gpt-5.4'
  AND src_rule.is_active = TRUE
JOIN pricing_rule new_rule
  ON new_rule.service_type = 'llm_chat'
  AND new_rule.provider = 'aihubmix'
  AND new_rule.model = 'gpt-5.4-thinking'
  AND new_rule.is_active = TRUE
WHERE NOT EXISTS (
  SELECT 1 FROM pricing_rule_tier t2
  WHERE t2.rule_id    = new_rule.id
    AND t2.token_type  = src_tier.token_type
    AND t2.min_tokens  = src_tier.min_tokens
);

COMMIT;
