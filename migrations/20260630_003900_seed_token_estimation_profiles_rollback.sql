-- Rollback for 20260630_003900_seed_token_estimation_profiles.sql.
--
-- Deletes only the rows inserted by this migration. For each affected
-- (provider, model, service_type) key, it reactivates the newest previous
-- non-migration profile if one exists; otherwise runtime will use the existing
-- provider/global/default fallback profile.

DROP TEMPORARY TABLE IF EXISTS _token_profile_seed_20260630_keys;
CREATE TEMPORARY TABLE _token_profile_seed_20260630_keys (
  provider VARCHAR(50) NOT NULL,
  model VARCHAR(100) NOT NULL,
  PRIMARY KEY (provider, model)
);

INSERT INTO _token_profile_seed_20260630_keys (provider, model) VALUES
  ('dmxapi', 'deepseek-v4-pro'),
  ('aihubmix', 'deepseek-v4-pro'),
  ('dmxapi', 'deepseek-v3.2-thinking'),
  ('aihubmix', 'deepseek-v3.2-thinking'),
  ('dmxapi', 'claude-opus-4-7'),
  ('claude-native', 'claude-opus-4-7'),
  ('dmxapi', 'claude-sonnet-4-6-thinking'),
  ('aihubmix', 'claude-sonnet-4-6-thinking'),
  ('dmxapi', 'claude-opus-4-6'),
  ('claude-native', 'claude-opus-4-6'),
  ('dmxapi', 'gpt-5.5'),
  ('agnes-ai', 'agnes-2.0-flash'),
  ('dmxapi', 'gemini-3.1-pro-preview'),
  ('aihubmix', 'gemini-3.1-pro-preview'),
  ('dmxapi', 'gpt-5.4'),
  ('aihubmix', 'gpt-5.4'),
  ('ali-dashscope', 'qwen-turbo');

DELETE tep
FROM token_estimation_profile tep
JOIN _token_profile_seed_20260630_keys k
  ON k.provider = tep.provider
 AND k.model = tep.model
WHERE tep.updated_by = 'migration:20260630_token_estimation_profiles';

UPDATE token_estimation_profile tep
JOIN (
  SELECT MAX(tep2.id) AS id
  FROM token_estimation_profile tep2
  JOIN _token_profile_seed_20260630_keys k
    ON k.provider = tep2.provider
   AND k.model = tep2.model
  WHERE tep2.service_type = 'llm_chat'
    AND tep2.is_fallback = 0
    AND tep2.updated_by <> 'migration:20260630_token_estimation_profiles'
  GROUP BY tep2.provider, tep2.model, tep2.service_type
) latest ON latest.id = tep.id
SET tep.is_active = 1;

DROP TEMPORARY TABLE IF EXISTS _token_profile_seed_20260630_keys;
