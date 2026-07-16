-- Protocol-v2 device authorization persists only encrypted resume credentials.
ALTER TABLE `feishu_auth_session`
  ADD COLUMN `protocol_version` TINYINT UNSIGNED NOT NULL DEFAULT 1 AFTER `expires_at`,
  ADD COLUMN `resume_credential_ciphertext` LONGBLOB NULL AFTER `protocol_version`,
  ADD COLUMN `resume_key_version` VARCHAR(32) NULL AFTER `resume_credential_ciphertext`,
  ADD COLUMN `resume_expires_at` DATETIME(3) NULL AFTER `resume_key_version`,
  ADD COLUMN `scope_hash` CHAR(64) NULL AFTER `resume_expires_at`;

-- A protocol-v1 user-auth worker cannot be resumed safely after this deploy.
UPDATE `feishu_auth_session`
SET `state` = 'superseded',
    `completed_at` = COALESCE(`completed_at`, CURRENT_TIMESTAMP(3)),
    `lease_owner` = '',
    `lease_until` = NULL
WHERE `protocol_version` = 1
  AND `phase` = 'user_auth'
  AND `state` = 'pending';
