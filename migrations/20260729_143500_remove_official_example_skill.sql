-- Migration: remove seeded official example skill from the Skill list
-- Rollback: 20260729_143500_remove_official_example_skill_rollback.sql
--
-- Product decision:
--   The placeholder "官方示例技能" was only a visibility demo seed. It should not
--   appear in parent-account Skill configuration.

DELETE FROM skill
WHERE visibility = 'official'
  AND parent_user_id = 0
  AND owner_user_id = 0
  AND name = '官方示例技能'
  AND source_type = 'custom';
