-- Rollback for 20260521_190100_seed_e2e_compliance_rule.sql

DELETE FROM compliance_rule WHERE parent_user_id = 1 AND pattern = '竞品X' AND rule_type = 'forbid_phrase';
