-- Rollback 20260616_153000_deepseek_v4_flash_nothinking_style
--
-- Revert deepseek-v4-flash thinking_style back to '' (empty). With the adapter's
-- deactivation branch, '' means no enable_thinking field is sent → the provider
-- default (thinking ON) returns, restoring pre-fix behavior.

UPDATE ai_service
SET thinking_style = ''
WHERE model_key = 'deepseek-v4-flash';
