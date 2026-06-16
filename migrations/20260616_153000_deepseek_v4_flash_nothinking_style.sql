-- 20260616_153000_deepseek_v4_flash_nothinking_style
--
-- Make deepseek-v4-flash (the session.title model) run NON-thinking.
--
-- Problem: deepseek-v4-flash at DMXAPI defaults thinking ON. The title call
-- (sessiontitle.Generate) never opts into thinking, but the adapter previously had
-- no "deactivation" path, so the bare request hit the provider default and ran with
-- a chain-of-thought (reasoning_tokens ~150-170, ~4.6s, occasionally >8s → timeout →
-- agent title fell back to the raw prompt instead of a smart summary).
--
-- Fix (paired with dmxapi.go adapter change): when a route's thinking_style is
-- 'enable_thinking_kwarg' and the caller did NOT opt into thinking, the adapter now
-- sends chat_template_kwargs.enable_thinking=false. This migration switches
-- deepseek-v4-flash to that style so the deactivation field is emitted.
--
-- Empirically verified against DMXAPI (probe 2026-06-16): enable_thinking=false →
-- reasoning_content empty, ~1.7-2.6s, title quality unchanged.
--
-- Blast radius: thinking_style='enable_thinking_kwarg' only changes wire behavior for
-- callers that explicitly set thinking on/off. chatbot/sop force Thinking=true
-- (llmrouter default) so they keep getting enable_thinking=true (unchanged). The new
-- false branch only fires for backend non-thinking calls (today: session.title).
-- deepseek-v4-pro is thinking_only=1 → unaffected (guarded by !ThinkingOnly in adapter).
--
-- Idempotent (UPDATE by model_key). Must run AFTER 20260616_140000 (the INSERT).
-- ⚠️ Dev / Prod 部署后必须手动 SSH 跑此 SQL (CI 不跑 migrations).

UPDATE ai_service
SET thinking_style = 'enable_thinking_kwarg'
WHERE model_key = 'deepseek-v4-flash';
