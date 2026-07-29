-- Migration: remove official skill templates and repair tenant imported-skill visibility
-- Rollback: 20260729_141000_remove_official_skill_templates_rollback.sql
--
-- Product decision:
--   - skill_template rows are no longer offered in the official template library.
--   - Imported concrete skills belong to the importing institution.
--   - System-level official skills (parent_user_id=0) remain untouched.

UPDATE skill
SET visibility = 'institution',
    owner_user_id = IF(owner_user_id = 0, parent_user_id, owner_user_id)
WHERE visibility = 'official'
  AND parent_user_id <> 0
  AND source_type = 'imported_from_template';

DELETE FROM skill_template;
