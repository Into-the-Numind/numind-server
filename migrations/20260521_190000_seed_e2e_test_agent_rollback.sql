-- Rollback for 20260521_190000_seed_e2e_test_agent.sql

DELETE FROM agent_definition_history WHERE agent_id = 99999;
DELETE FROM agent_definition WHERE id = 99999;
