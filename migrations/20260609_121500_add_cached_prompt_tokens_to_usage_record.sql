-- Cached prompt-token observability column on usage_record.
--
-- Additive: NOT NULL DEFAULT 0 = no cache = identical to pre-cache billing audit.
-- Source: OpenAI usage.prompt_tokens_details.cached_tokens; DeepSeek
-- prompt_cache_hit_tokens (both via the OpenAI-compatible DMXAPI endpoint).
-- NOTE: CI does NOT auto-run migrations (CLAUDE.md §5.2); apply via SSH before deploy.
ALTER TABLE usage_record ADD COLUMN IF NOT EXISTS cached_prompt_tokens INT NOT NULL DEFAULT 0
  COMMENT '缓存命中的输入 tokens 数（来自 provider usage）。0=无缓存命中';
