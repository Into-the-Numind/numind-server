-- Rollback: 20260523_153000_create_agent_message_search.sql
-- Feature: agent-mode-v15-memory-layer-a (Task 3.5)
--
-- Drops agent_message_search table (search index — derived data, safe to drop).

DROP TABLE IF EXISTS agent_message_search;
