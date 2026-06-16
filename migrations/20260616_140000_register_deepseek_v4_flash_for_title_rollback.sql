-- Rollback: revert session.title back to agnes-2.0-flash and deactivate deepseek-v4-flash route
-- Forward: 20260616_140000_register_deepseek_v4_flash_for_title.sql

-- Repoint session.title back to agnes-2.0-flash (the prior model).
UPDATE task_profile tp
JOIN ai_service s ON s.model_key = 'agnes-2.0-flash' AND s.is_active = 1
SET tp.default_service_id = s.id
WHERE tp.task_id = 'session.title';

-- Deactivate the deepseek-v4-flash route (keep the rows for history).
UPDATE ai_service_route r
JOIN ai_service s ON s.id = r.model_id AND s.model_key = 'deepseek-v4-flash'
SET r.is_active = 0;
UPDATE ai_service SET is_active = 0 WHERE model_key = 'deepseek-v4-flash';
