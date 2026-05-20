-- Rollback: remove 10 builtin skill templates seeded by 20260522_220300_seed_skill_template.sql
-- Feature: agent-mode-skill-system (#5/14)
DELETE FROM skill_template WHERE id BETWEEN 1 AND 10;
