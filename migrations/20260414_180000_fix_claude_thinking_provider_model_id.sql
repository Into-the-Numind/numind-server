-- Claude thinking variant uses -thinking suffix for DMXAPI activation
-- DMXAPI's /v1/messages endpoint does NOT transparently return thinking blocks,
-- so we use the -thinking model name suffix via /v1/chat/completions instead.

UPDATE llm_model_provider
SET provider_model_id = CONCAT(provider_model_id, '-thinking'),
    updated_at = NOW()
WHERE model_id IN (
    SELECT id FROM llm_model WHERE is_thinking = true AND model_key LIKE '%claude%'
)
AND provider_model_id NOT LIKE '%-thinking';
