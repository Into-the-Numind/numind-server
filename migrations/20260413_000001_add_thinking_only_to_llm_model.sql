-- Add thinking_only field to llm_model table
-- For models that only support thinking mode (e.g., QwQ, o3-mini)
-- These models always pass enable_thinking=true to the LLM API

ALTER TABLE llm_model ADD COLUMN thinking_only TINYINT(1) DEFAULT 0 AFTER supports_thinking;

-- Rollback: ALTER TABLE llm_model DROP COLUMN thinking_only;
