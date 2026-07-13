-- Local pre-production rollback only. Data written after the forward migration is discarded.

DROP TABLE IF EXISTS `feishu_operation`;
DROP TABLE IF EXISTS `feishu_auth_session`;
DROP TABLE IF EXISTS `feishu_cli_vault`;

ALTER TABLE `agent_run`
  DROP COLUMN `pending_external_action_at`,
  DROP COLUMN `pending_external_action_json`;

ALTER TABLE `user_third_party_account`
  DROP COLUMN `generation`,
  DROP COLUMN `last_error_code`,
  DROP COLUMN `last_success_at`,
  DROP COLUMN `capability_state_json`,
  DROP COLUMN `granted_scopes_json`,
  DROP COLUMN `lark_cli_version`,
  DROP COLUMN `connection_state`;
