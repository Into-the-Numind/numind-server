-- Rollback for 20260524_130000_register_skill_marketplace_sanitize_profile.sql
-- Removes the skill.marketplace.sanitize task profile registration.
-- 不影响 ai_service 表 (model_key='qwen-turbo' 由其它 task 共用, 不能误删).

DELETE FROM task_profile WHERE task_id = 'skill.marketplace.sanitize';
