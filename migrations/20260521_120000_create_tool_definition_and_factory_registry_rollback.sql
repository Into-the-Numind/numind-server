-- Rollback: agent-mode-tool-registry #3 — tool_definition + tool_factory_registry
-- Drop in reverse dependency order.

DROP TABLE IF EXISTS tool_factory_registry;
DROP TABLE IF EXISTS tool_definition;
