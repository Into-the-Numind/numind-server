-- Fix: Claude thinking variant needs -thinking suffix in provider_model_id
-- DMXAPI uses model name suffix to activate thinking mode for Claude models
-- Without the suffix, DMXAPI treats it as a regular (non-thinking) call

UPDATE llm_model_provider
SET provider_model_id = CONCAT(provider_model_id, '-thinking'),
    updated_at = NOW()
WHERE model_id IN (
    SELECT id FROM llm_model WHERE is_thinking = true AND model_key LIKE '%claude%'
)
AND provider_model_id NOT LIKE '%-thinking';
