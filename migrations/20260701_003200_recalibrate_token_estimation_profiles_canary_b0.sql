-- Fine-tune the <=10k raw-token bucket using live prod chatbot canary runs
-- after v2.1.50 deployment and the p50 recalibration.

UPDATE token_estimation_profile
SET profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-canary-b0-20260701', '$.calibration_buckets[0].multiplier', 0.748),
    change_reason = '20260701 live prod chatbot canary b0 fine-tune',
    updated_by = 'migration:20260701_token_estimation_profiles_canary_b0'
WHERE is_active = 1
  AND provider IN ('dmxapi', 'aihubmix')
  AND model = 'deepseek-v4-pro';

UPDATE token_estimation_profile
SET profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-canary-b0-20260701', '$.calibration_buckets[0].multiplier', 0.826),
    change_reason = '20260701 live prod chatbot canary b0 fine-tune',
    updated_by = 'migration:20260701_token_estimation_profiles_canary_b0'
WHERE is_active = 1
  AND provider IN ('dmxapi', 'aihubmix')
  AND model = 'gemini-3.1-pro-preview';

UPDATE token_estimation_profile
SET profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-canary-b0-20260701', '$.calibration_buckets[0].multiplier', 0.841),
    change_reason = '20260701 live prod chatbot canary b0 fine-tune',
    updated_by = 'migration:20260701_token_estimation_profiles_canary_b0'
WHERE is_active = 1
  AND provider = 'agnes-ai'
  AND model = 'agnes-2.0-flash';

UPDATE token_estimation_profile
SET profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-canary-b0-20260701', '$.calibration_buckets[0].multiplier', 1.384),
    change_reason = '20260701 live prod chatbot canary b0 fine-tune',
    updated_by = 'migration:20260701_token_estimation_profiles_canary_b0'
WHERE is_active = 1
  AND provider IN ('dmxapi', 'claude-native')
  AND model = 'claude-opus-4-6';

UPDATE token_estimation_profile
SET profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-canary-b0-20260701', '$.calibration_buckets[0].multiplier', 0.850),
    change_reason = '20260701 live prod chatbot canary b0 fine-tune',
    updated_by = 'migration:20260701_token_estimation_profiles_canary_b0'
WHERE is_active = 1
  AND provider IN ('dmxapi', 'aihubmix')
  AND model = 'claude-sonnet-4-6-thinking';
