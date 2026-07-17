-- WARNING: protocol-v1 session superseding is irreversible. Production must
-- not use this destructive rollback; deploy a forward repair migration instead.
ALTER TABLE `feishu_auth_session`
  DROP COLUMN IF EXISTS `scope_hash`,
  DROP COLUMN IF EXISTS `resume_expires_at`,
  DROP COLUMN IF EXISTS `resume_key_version`,
  DROP COLUMN IF EXISTS `resume_credential_ciphertext`,
  DROP COLUMN IF EXISTS `protocol_version`;
