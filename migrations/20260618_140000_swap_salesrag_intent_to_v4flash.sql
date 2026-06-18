-- Swap salesrag.intent (the query rewriter) from qwen-turbo to deepseek-v4-flash.
-- qwen-turbo is discontinued by the provider. deepseek-v4-flash: dmxapi provider,
-- text-only, non-thinking (thinking_style=enable_thinking_kwarg → the gateway sends
-- chat_template_kwargs.enable_thinking:false for non-thinking callers like the rewriter;
-- supports_thinking MUST stay 1 so that disable-kwarg fires — setting it 0 would let
-- dmxapi default thinking ON). Pricing 0.85/1.70/0.02 RMB per M tok (cost=sell, no
-- markup, per deepseek-v4-pro / qwen-turbo-latest convention).
--
-- NOTE: applied LIVE to dev (numind-dev) + prod (numind-prod) on 2026-06-18 via direct
-- SQL. This file records it for qa / fresh-seed reproducibility. Idempotent (safe to re-run).

-- 1. Register deepseek-v4-flash ai_service
INSERT IGNORE INTO ai_service
  (model_key, display_name, is_thinking, supports_thinking, thinking_only, thinking_style,
   sort_order, is_active, service_type, latency_tier, quality_tier, capability_json)
VALUES
  ('deepseek-v4-flash','DeepSeek V4 Flash',0,1,0,'enable_thinking_kwarg',
   0,1,'llm','fast','standard',
   '{"features": {"tool_use": true, "streaming": true}, "capabilities": ["chat"], "context_window": 1000000, "input_modalities": ["text"], "max_output_tokens": 384000, "output_modalities": ["text"]}');

-- 1b. Force the thinking config correct even if v4-flash was pre-registered by an
-- earlier migration (e.g. 20260616 title profile) with an empty thinking_style.
-- supports_thinking MUST be 1 + thinking_style='enable_thinking_kwarg' so the gateway
-- sends enable_thinking:false for non-thinking callers (else dmxapi defaults thinking ON).
UPDATE ai_service SET supports_thinking=1, thinking_only=0, thinking_style='enable_thinking_kwarg'
WHERE model_key='deepseek-v4-flash';

-- 2. Route to dmxapi
INSERT INTO ai_service_route
  (model_id, provider_id, provider_model_id, priority, input_price_per_mtok, output_price_per_mtok, pricing_unit, is_active)
SELECT s.id, p.id, 'deepseek-v4-flash', 10, 0.85, 1.70, 'per_1m_tokens', 1
FROM ai_service s, llm_provider p
WHERE s.model_key='deepseek-v4-flash' AND p.name='dmxapi'
  AND NOT EXISTS (SELECT 1 FROM ai_service_route r WHERE r.model_id=s.id);

-- 3. Pricing (cost = sell, no markup; cache-hit 0.02)
INSERT INTO pricing_rule
  (service_type, provider, model, billing_mode, input_price_per_m_tok, output_price_per_m_tok,
   cached_input_price_per_m_tok, sell_input_price_per_m_tok, sell_output_price_per_m_tok,
   sell_cached_input_price_per_m_tok, credit_multiplier, is_active)
SELECT 'llm_chat','dmxapi','deepseek-v4-flash','flat',0.85,1.70,0.02,0.85,1.70,0.02,1.00,1
FROM DUAL
WHERE NOT EXISTS (SELECT 1 FROM pricing_rule WHERE provider='dmxapi' AND model='deepseek-v4-flash');

-- 4. Rebind salesrag.intent (rewriter) → deepseek-v4-flash
UPDATE task_profile_service tps
  JOIN task_profile tp ON tp.id = tps.task_profile_id
  JOIN ai_service v4 ON v4.model_key = 'deepseek-v4-flash'
SET tps.service_id = v4.id
WHERE tp.task_id = 'salesrag.intent';

-- 5. Remove qwen-turbo entirely (route → pricing → service)
DELETE r FROM ai_service_route r JOIN ai_service s ON s.id = r.model_id WHERE s.model_key = 'qwen-turbo';
DELETE FROM pricing_rule WHERE provider = 'ali' AND model = 'qwen-turbo';
DELETE FROM ai_service WHERE model_key = 'qwen-turbo';
