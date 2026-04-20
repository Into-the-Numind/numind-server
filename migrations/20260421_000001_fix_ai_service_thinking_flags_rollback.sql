-- Rollback: 20260421_000001_fix_ai_service_thinking_flags_rollback.sql
-- Feature:  aihubmix-protocol-audit (Task 7a rollback)
-- Date:     2026-04-21
--
-- 还原 migration 20260421_000001_fix_ai_service_thinking_flags.sql 执行前的
-- （buggy）DB 状态。注意：rollback 会**主动还原**到 preference.go:246 硬拒
-- thinking 变体 request 的 bug 状态——仅当 migration 执行后发现严重事故时才用。
--
-- P2-F pre-check guard（S3 v2 review 修订）：
-- rollback 前先确认 migration 已完整执行（6 行全部更新成目标值），否则 abort。
-- 防止"migration 只跑了 3 行" + "rollback 把另外 3 行也还原"导致半更新状态。

-- Pre-flight: 确认 migration 完整执行（6 行的目标值匹配）
-- 若少于 6 行 → 分母 ≤ 0 → 除零失败 → rollback abort
SELECT 1 / (
  (SELECT COUNT(*) FROM ai_service
   WHERE model_key = 'claude-sonnet-4-6-thinking' AND supports_thinking = 1 AND thinking_only = 1) +
  (SELECT COUNT(*) FROM ai_service
   WHERE model_key = 'deepseek-v3.2' AND supports_thinking = 1 AND thinking_only = 0) +
  (SELECT COUNT(*) FROM ai_service
   WHERE model_key = 'gpt-5.4' AND supports_thinking = 1 AND thinking_only = 0) +
  (SELECT COUNT(*) FROM ai_service
   WHERE model_key = 'gemini-3.1-pro-preview-thinking' AND supports_thinking = 1 AND thinking_only = 1) +
  (SELECT COUNT(*) FROM ai_service
   WHERE model_key = 'deepseek-v3.2-thinking' AND supports_thinking = 1 AND thinking_only = 1) +
  (SELECT COUNT(*) FROM ai_service
   WHERE model_key = 'gpt-5.4-thinking' AND supports_thinking = 1 AND thinking_only = 1)
  - 5
) AS rollback_prerequisite_check;
-- 期望 6 行全匹配 → 6-5=1 → 1/1 成功
-- 若少于 6 → 除零失败，rollback abort（保持现状即可，不会破坏）

-- 还原（6 行逐个）：
UPDATE ai_service SET supports_thinking = 0, thinking_only = 0
  WHERE model_key = 'claude-sonnet-4-6-thinking';
UPDATE ai_service SET supports_thinking = 1, thinking_only = 1
  WHERE model_key = 'deepseek-v3.2';
UPDATE ai_service SET supports_thinking = 1, thinking_only = 1
  WHERE model_key = 'gpt-5.4';
UPDATE ai_service SET supports_thinking = 0, thinking_only = 0
  WHERE model_key = 'gemini-3.1-pro-preview-thinking';
UPDATE ai_service SET supports_thinking = 0, thinking_only = 0
  WHERE model_key = 'deepseek-v3.2-thinking';
UPDATE ai_service SET supports_thinking = 0, thinking_only = 0
  WHERE model_key = 'gpt-5.4-thinking';
