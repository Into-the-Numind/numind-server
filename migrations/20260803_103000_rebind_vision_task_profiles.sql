-- Repair task profiles left bound to qwen3-vl-flash after the 2026-07-30
-- production reconcile retired that service. The original reconcile moved
-- task_profile_service fallback/allowed bindings but omitted the primary
-- task_profile.default_service_id references.
--
-- This migration is data-only and idempotent. It resolves services by stable
-- model_key so it works across environments with different numeric IDs.

DROP PROCEDURE IF EXISTS _mig_20260803_rebind_vision_task_profiles;
DELIMITER //

CREATE PROCEDURE _mig_20260803_rebind_vision_task_profiles()
BEGIN
  DECLARE EXIT HANDLER FOR SQLEXCEPTION
  BEGIN
    ROLLBACK;
    RESIGNAL;
  END;

  -- Fail closed unless the replacement service has a live Ali DashScope route.
  IF NOT EXISTS (
    SELECT 1
    FROM ai_service new_service
    JOIN ai_service_route route ON route.model_id = new_service.id
    JOIN llm_provider provider ON provider.id = route.provider_id
    WHERE new_service.model_key = 'qwen3.5-flash'
      AND new_service.is_active = 1
      AND new_service.deprecated_at IS NULL
      AND route.is_active = 1
      AND provider.name = 'ali-dashscope'
      AND provider.is_active = 1
  ) THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'rebind vision tasks: active qwen3.5-flash route missing';
  END IF;

  START TRANSACTION;

  -- Move every primary task binding that still points to the retired service.
  -- This covers salesrag.profile, salesrag.chatstyle, sop.vision, and any other
  -- internal task with the same stale binding without hard-coding task IDs.
  UPDATE task_profile profile
  JOIN ai_service old_service
    ON old_service.id = profile.default_service_id
   AND old_service.model_key = 'qwen3-vl-flash'
  JOIN ai_service new_service
    ON new_service.model_key = 'qwen3.5-flash'
   AND new_service.is_active = 1
   AND new_service.deprecated_at IS NULL
  SET profile.default_service_id = new_service.id,
      profile.updated_at = NOW()
  WHERE profile.default_service_id = old_service.id;

  -- Postcondition: no primary task profile may remain on the retired service.
  IF EXISTS (
    SELECT 1
    FROM task_profile profile
    JOIN ai_service old_service ON old_service.id = profile.default_service_id
    WHERE old_service.model_key = 'qwen3-vl-flash'
  ) THEN
    SIGNAL SQLSTATE '45000'
      SET MESSAGE_TEXT = 'rebind vision tasks: remaining_retired_vision_defaults';
  END IF;

  COMMIT;
END//

DELIMITER ;
CALL _mig_20260803_rebind_vision_task_profiles();
DROP PROCEDURE IF EXISTS _mig_20260803_rebind_vision_task_profiles;
