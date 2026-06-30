-- Recalibrate v2.1.50 token estimation profiles to the current exact-profile
-- estimator scale.
--
-- The previous seed used inverse/conservative ratios derived from historical
-- fallback estimates. Historical fallback estimates included safety_multiplier
-- 1.30, while exact profiles run with safety_multiplier 1.00. Correct exact
-- multipliers are therefore derived as:
--   actual_prompt_tokens / (historical_estimated_before / 1.30)
--
-- Bucket values below use the historical p50 per raw prompt bucket to keep the
-- estimate close to provider-reported prompt tokens instead of reserve-heavy.

UPDATE token_estimation_profile
SET calibration_multiplier = 1.0400,
    profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-20260701', '$.calibration_buckets', CAST('[{"multiplier":0.95,"max_raw_tokens":10000},{"multiplier":1.146,"min_raw_tokens":10001,"max_raw_tokens":30000},{"multiplier":1.085,"min_raw_tokens":30001,"max_raw_tokens":80000},{"multiplier":0.976,"min_raw_tokens":80001}]' AS JSON)),
    change_reason = '20260701 p50 recalibration after v2.1.50 prod canary',
    updated_by = 'migration:20260701_token_estimation_profiles_p50'
WHERE is_active = 1
  AND provider IN ('dmxapi', 'aihubmix')
  AND model = 'deepseek-v4-pro';

UPDATE token_estimation_profile
SET calibration_multiplier = 1.3200,
    profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-20260701', '$.calibration_buckets', CAST('[{"multiplier":1.073,"max_raw_tokens":10000},{"multiplier":1.359,"min_raw_tokens":10001,"max_raw_tokens":30000},{"multiplier":1.963,"min_raw_tokens":30001,"max_raw_tokens":80000},{"multiplier":1.32,"min_raw_tokens":80001}]' AS JSON)),
    change_reason = '20260701 p50 recalibration after v2.1.50 prod canary',
    updated_by = 'migration:20260701_token_estimation_profiles_p50'
WHERE is_active = 1
  AND provider IN ('dmxapi', 'aihubmix')
  AND model = 'deepseek-v3.2-thinking';

UPDATE token_estimation_profile
SET calibration_multiplier = 1.7120,
    profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-20260701', '$.calibration_buckets', CAST('[{"multiplier":1.409,"max_raw_tokens":10000},{"multiplier":1.843,"min_raw_tokens":10001,"max_raw_tokens":30000},{"multiplier":1.982,"min_raw_tokens":30001,"max_raw_tokens":80000},{"multiplier":1.712,"min_raw_tokens":80001}]' AS JSON)),
    change_reason = '20260701 p50 recalibration after v2.1.50 prod canary',
    updated_by = 'migration:20260701_token_estimation_profiles_p50'
WHERE is_active = 1
  AND provider IN ('dmxapi', 'claude-native')
  AND model = 'claude-opus-4-7';

UPDATE token_estimation_profile
SET calibration_multiplier = 2.3000,
    profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-20260701', '$.calibration_buckets', CAST('[{"multiplier":2.896,"max_raw_tokens":10000},{"multiplier":2.003,"min_raw_tokens":10001,"max_raw_tokens":30000},{"multiplier":2.588,"min_raw_tokens":30001,"max_raw_tokens":80000},{"multiplier":2.3,"min_raw_tokens":80001}]' AS JSON)),
    change_reason = '20260701 p50 recalibration after v2.1.50 prod canary',
    updated_by = 'migration:20260701_token_estimation_profiles_p50'
WHERE is_active = 1
  AND provider IN ('dmxapi', 'aihubmix')
  AND model = 'claude-sonnet-4-6-thinking';

UPDATE token_estimation_profile
SET calibration_multiplier = 1.6370,
    profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-20260701', '$.calibration_buckets', CAST('[{"multiplier":1.523,"max_raw_tokens":10000},{"multiplier":2.09,"min_raw_tokens":10001,"max_raw_tokens":30000},{"multiplier":3.422,"min_raw_tokens":30001,"max_raw_tokens":80000},{"multiplier":1.637,"min_raw_tokens":80001}]' AS JSON)),
    change_reason = '20260701 p50 recalibration after v2.1.50 prod canary',
    updated_by = 'migration:20260701_token_estimation_profiles_p50'
WHERE is_active = 1
  AND provider IN ('dmxapi', 'claude-native')
  AND model = 'claude-opus-4-6';

UPDATE token_estimation_profile
SET calibration_multiplier = 1.2710,
    profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-20260701', '$.calibration_buckets', CAST('[{"multiplier":1.652,"max_raw_tokens":10000},{"multiplier":1.461,"min_raw_tokens":10001,"max_raw_tokens":30000},{"multiplier":1.266,"min_raw_tokens":30001,"max_raw_tokens":80000},{"multiplier":1.271,"min_raw_tokens":80001}]' AS JSON)),
    change_reason = '20260701 p50 recalibration after v2.1.50 prod canary',
    updated_by = 'migration:20260701_token_estimation_profiles_p50'
WHERE is_active = 1
  AND provider = 'dmxapi'
  AND model = 'gpt-5.5';

UPDATE token_estimation_profile
SET calibration_multiplier = 1.2380,
    profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-20260701', '$.calibration_buckets', CAST('[{"multiplier":1.077,"max_raw_tokens":10000},{"multiplier":1.257,"min_raw_tokens":10001,"max_raw_tokens":30000},{"multiplier":1.077,"min_raw_tokens":30001,"max_raw_tokens":80000},{"multiplier":1.238,"min_raw_tokens":80001}]' AS JSON)),
    change_reason = '20260701 p50 recalibration after v2.1.50 prod canary',
    updated_by = 'migration:20260701_token_estimation_profiles_p50'
WHERE is_active = 1
  AND provider = 'agnes-ai'
  AND model = 'agnes-2.0-flash';

UPDATE token_estimation_profile
SET calibration_multiplier = 1.2040,
    profile_json = JSON_SET(profile_json, '$.method', 'prod-bucketed-p50-20260701', '$.calibration_buckets', CAST('[{"multiplier":1.196,"max_raw_tokens":10000},{"multiplier":2.116,"min_raw_tokens":10001,"max_raw_tokens":30000},{"multiplier":1.204,"min_raw_tokens":30001,"max_raw_tokens":80000},{"multiplier":1.204,"min_raw_tokens":80001}]' AS JSON)),
    change_reason = '20260701 p50 recalibration after v2.1.50 prod canary',
    updated_by = 'migration:20260701_token_estimation_profiles_p50'
WHERE is_active = 1
  AND provider IN ('dmxapi', 'aihubmix')
  AND model = 'gemini-3.1-pro-preview';

UPDATE token_estimation_profile
SET calibration_multiplier = 1.3000,
    profile_json = JSON_SET(profile_json, '$.method', 'prod-provisional-p50-20260701', '$.calibration_buckets', CAST('[{"multiplier":1.3}]' AS JSON)),
    change_reason = '20260701 p50 recalibration after v2.1.50 prod canary; provisional no usable samples',
    updated_by = 'migration:20260701_token_estimation_profiles_p50'
WHERE is_active = 1
  AND ((provider IN ('dmxapi', 'aihubmix') AND model = 'gpt-5.4')
       OR (provider = 'ali-dashscope' AND model = 'qwen-turbo'));
