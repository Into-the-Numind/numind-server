-- Seed token estimation profiles calibrated from prod context_budget_event samples.
--
-- Source: prod rows where estimated_before > 0 and actual_prompt_tokens > 0.
-- ratio = actual_prompt_tokens / estimated_before. The profile-level multiplier
-- keeps the model p95 coverage target, while calibration_buckets tighten the
-- estimate by raw prompt size so long prompts are not over-reserved by a single
-- all-size multiplier.
--
-- Rows with sample_count=0 are provisional exact profiles for models that were
-- either called without usable actual token data or are currently selectable in
-- SOP/chatbot but lack enough calibration samples.
--
-- NOTE: CI does not auto-run migrations (CLAUDE.md §5.2); apply manually via SSH.
-- Post-apply verification:
--   SELECT provider, model, safety_multiplier, calibration_multiplier,
--          calibration_sample_count, is_active, updated_by
--   FROM token_estimation_profile
--   WHERE updated_by = 'migration:20260630_token_estimation_profiles'
--   ORDER BY model, provider;

DROP TEMPORARY TABLE IF EXISTS _token_profile_seed_20260630;
CREATE TEMPORARY TABLE _token_profile_seed_20260630 (
  provider VARCHAR(50) NOT NULL,
  model VARCHAR(100) NOT NULL,
  model_family VARCHAR(80) NOT NULL,
  safety_multiplier DECIMAL(8,4) NOT NULL,
  calibration_multiplier DECIMAL(8,4) NOT NULL,
  calibration_sample_count INT NOT NULL,
  calibration_p50_abs_error DECIMAL(8,4) NULL,
  calibration_p90_abs_error DECIMAL(8,4) NULL,
  calibration_p99_under_ratio DECIMAL(8,4) NULL,
  bucket_json JSON NOT NULL
);

INSERT INTO _token_profile_seed_20260630 (
  provider, model, model_family, safety_multiplier, calibration_multiplier,
  calibration_sample_count, calibration_p50_abs_error, calibration_p90_abs_error,
  calibration_p99_under_ratio, bucket_json
) VALUES
  ('dmxapi', 'deepseek-v4-pro', 'deepseek', 1.0000, 1.4800, 3050, 0.8000, 1.2600, 1.7700,
   '[{"max_raw_tokens":10000,"multiplier":1.45},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":1.52},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":1.23},{"min_raw_tokens":80001,"multiplier":0.95}]'),
  ('aihubmix', 'deepseek-v4-pro', 'deepseek', 1.0000, 1.4800, 3050, 0.8000, 1.2600, 1.7700,
   '[{"max_raw_tokens":10000,"multiplier":1.45},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":1.52},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":1.23},{"min_raw_tokens":80001,"multiplier":0.95}]'),
  ('dmxapi', 'deepseek-v3.2-thinking', 'deepseek', 1.0000, 1.6700, 1102, 1.0200, 1.6300, 1.6800,
   '[{"max_raw_tokens":10000,"multiplier":1.16},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":1.49},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":1.67},{"min_raw_tokens":80001,"multiplier":0.94}]'),
  ('aihubmix', 'deepseek-v3.2-thinking', 'deepseek', 1.0000, 1.6700, 1102, 1.0200, 1.6300, 1.6800,
   '[{"max_raw_tokens":10000,"multiplier":1.16},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":1.49},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":1.67},{"min_raw_tokens":80001,"multiplier":0.94}]'),
  ('dmxapi', 'claude-opus-4-7', 'claude', 1.0000, 2.1900, 98, 1.3200, 1.8400, 2.5700,
   '[{"max_raw_tokens":10000,"multiplier":1.48},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":2.50},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":1.57},{"min_raw_tokens":80001,"multiplier":2.05}]'),
  ('claude-native', 'claude-opus-4-7', 'claude', 1.0000, 2.1900, 98, 1.3200, 1.8400, 2.5700,
   '[{"max_raw_tokens":10000,"multiplier":1.48},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":2.50},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":1.57},{"min_raw_tokens":80001,"multiplier":2.05}]'),
  ('dmxapi', 'claude-sonnet-4-6-thinking', 'claude', 1.0000, 2.8100, 49, 1.7700, 2.7800, 2.8600,
   '[{"max_raw_tokens":10000,"multiplier":2.58},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":2.84},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":2.66},{"min_raw_tokens":80001,"multiplier":2.81}]'),
  ('aihubmix', 'claude-sonnet-4-6-thinking', 'claude', 1.0000, 2.8100, 49, 1.7700, 2.7800, 2.8600,
   '[{"max_raw_tokens":10000,"multiplier":2.58},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":2.84},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":2.66},{"min_raw_tokens":80001,"multiplier":2.81}]'),
  ('dmxapi', 'claude-opus-4-6', 'claude', 1.0000, 2.6300, 24, 1.2600, 2.3800, 2.6400,
   '[{"max_raw_tokens":10000,"multiplier":1.29},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":1.75},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":2.64},{"min_raw_tokens":80001,"multiplier":2.63}]'),
  ('claude-native', 'claude-opus-4-6', 'claude', 1.0000, 2.6300, 24, 1.2600, 2.3800, 2.6400,
   '[{"max_raw_tokens":10000,"multiplier":1.29},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":1.75},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":2.64},{"min_raw_tokens":80001,"multiplier":2.63}]'),
  ('dmxapi', 'gpt-5.5', 'openai', 1.0000, 1.2900, 16, 0.9800, 1.2800, 1.3000,
   '[{"max_raw_tokens":10000,"multiplier":1.30},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":1.14},{"min_raw_tokens":30001,"multiplier":0.98}]'),
  ('agnes-ai', 'agnes-2.0-flash', 'agnes', 1.0000, 1.4800, 65, 0.9500, 1.2300, 1.5600,
   '[{"max_raw_tokens":10000,"multiplier":1.19},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":1.47},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":1.46},{"min_raw_tokens":80001,"multiplier":1.48}]'),
  ('dmxapi', 'gemini-3.1-pro-preview', 'gemini', 1.0000, 1.6700, 14, 0.9300, 1.6400, 1.6900,
   '[{"max_raw_tokens":10000,"multiplier":0.93},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":1.68},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":1.60},{"min_raw_tokens":80001,"multiplier":1.67}]'),
  ('aihubmix', 'gemini-3.1-pro-preview', 'gemini', 1.0000, 1.6700, 14, 0.9300, 1.6400, 1.6900,
   '[{"max_raw_tokens":10000,"multiplier":0.93},{"min_raw_tokens":10001,"max_raw_tokens":30000,"multiplier":1.68},{"min_raw_tokens":30001,"max_raw_tokens":80000,"multiplier":1.60},{"min_raw_tokens":80001,"multiplier":1.67}]'),
  ('dmxapi', 'gpt-5.4', 'openai', 1.0000, 1.3100, 0, NULL, NULL, NULL,
   '[{"multiplier":1.31}]'),
  ('aihubmix', 'gpt-5.4', 'openai', 1.0000, 1.3100, 0, NULL, NULL, NULL,
   '[{"multiplier":1.31}]'),
  ('ali-dashscope', 'qwen-turbo', 'qwen', 1.0000, 1.3000, 0, NULL, NULL, NULL,
   '[{"multiplier":1.30}]');

DELETE FROM token_estimation_profile
WHERE updated_by = 'migration:20260630_token_estimation_profiles';

UPDATE token_estimation_profile tep
JOIN _token_profile_seed_20260630 seed
  ON seed.provider = tep.provider
 AND seed.model = tep.model
SET tep.is_active = 0
WHERE tep.service_type = 'llm_chat'
  AND tep.is_fallback = 0
  AND tep.is_active = 1;

INSERT INTO token_estimation_profile (
  provider,
  model,
  model_family,
  service_type,
  profile_json,
  safety_multiplier,
  calibration_multiplier,
  calibration_sample_count,
  calibration_p50_abs_error,
  calibration_p90_abs_error,
  calibration_p99_under_ratio,
  version,
  is_active,
  is_fallback,
  change_reason,
  updated_by,
  created_at,
  updated_at
)
SELECT
  provider,
  model,
  model_family,
  'llm_chat',
  JSON_OBJECT(
    'method', 'prod-bucketed-p95-20260630',
    'message_overhead_tokens', 4,
    'fragment_overhead_tokens', 2,
    'classes', JSON_OBJECT(
      'en', JSON_OBJECT('token_per_char', 0.25),
      'zh', JSON_OBJECT('token_per_char', 0.60),
      'code', JSON_OBJECT('token_per_char', 0.30),
      'json', JSON_OBJECT('token_per_char', 0.25),
      'markdown_table', JSON_OBJECT('token_per_char', 0.50),
      'symbol', JSON_OBJECT('token_per_char', 0.20),
      'mixed', JSON_OBJECT('token_per_char', 0.45)
    ),
    'calibration_buckets', JSON_EXTRACT(bucket_json, '$')
  ),
  safety_multiplier,
  calibration_multiplier,
  calibration_sample_count,
  calibration_p50_abs_error,
  calibration_p90_abs_error,
  calibration_p99_under_ratio,
  1,
  1,
  0,
  'prod bucketed p95 calibration from context_budget_event 2026-06-30',
  'migration:20260630_token_estimation_profiles',
  NOW(3),
  NOW(3)
FROM _token_profile_seed_20260630;

DROP TEMPORARY TABLE IF EXISTS _token_profile_seed_20260630;
