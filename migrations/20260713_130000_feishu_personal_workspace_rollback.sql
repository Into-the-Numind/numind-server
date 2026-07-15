-- Local pre-production rollback only. Data written after the forward migration is discarded.

UPDATE `agent_run`
SET `status` = 'terminated',
    `state_reason` = 'model_error',
    `ended_at` = COALESCE(`ended_at`, CURRENT_TIMESTAMP(3))
WHERE `state_reason` = 'external_resume_ready'
   OR state_reason LIKE 'ext_resume:%';

ALTER TABLE `agent_run`
  DROP CHECK `chk_ar_state_reason`,
  ADD CONSTRAINT `chk_ar_state_reason` CHECK (
    `state_reason` IS NULL OR
    `state_reason` IN (
      'completed', 'blocking_limit', 'image_error', 'model_error',
      'aborted_streaming', 'prompt_too_long', 'stop_hook_prevented',
      'aborted_tools', 'hook_stopped', 'max_turns', 'error_max_budget',
      'error_max_retries', 'next_turn', 'collapse_drain_retry',
      'reactive_compact_retry', 'max_output_escalate', 'max_output_recovery',
      'stop_hook_blocking', 'token_budget_continue', 'running',
      'waiting_for_user_choice', 'permission_denied', 'context_exhausted',
      'cancelled'
    )
  );

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

-- Local pre-production rollback only: restoring the legacy INT width is destructive
-- once any user_id exceeds UINT32. Do not execute this rollback in that case.
ALTER TABLE `user_third_party_account`
  MODIFY COLUMN `user_id` INT UNSIGNED NOT NULL;
