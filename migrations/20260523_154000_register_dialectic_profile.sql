-- Migration: register agent.dialectic task profile
-- Feature: agent-mode-v15-memory-layer-a (Task 3.7 — Dialectic Layer A Reasoning)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-07-dialectic.md
-- Rollback: 20260523_154000_register_dialectic_profile_rollback.sql
--
-- Task 3.7 DialecticService 后台 goroutine 基于 user 的 top-20 Layer A facts
-- (subject_id IS NULL — 关于使用 agent 的真实 user 本人, NOT 关于会话讨论的对象)
-- 推理 100-800 字的 "该使用者是谁 + 应当怎么个人化对待他" 画像, 写入
-- user_memory_profile.cached_insight (7 天软 cache, cadence 5 min cooldown 控制).
-- aiservice.Chat(ctx, "agent.dialectic", req) — 本 SQL 仅占位 task_profile,
-- 真实 ai_service_route 绑定由管理端 (admin) CRUD 配 (qwen-plus 主路 +
-- deepseek-v3-2 fallback).
--
-- D4 决策 (context.md): "推荐 qwen-plus / deepseek-v3-2 — 不要 thinking model"
-- dialectic 要稳定一致输出, 不要发散. 后台跑 cadence ≥ 5 min, 不阻塞用户.
--
-- Idempotent: ON DUPLICATE KEY UPDATE updated_at only.

INSERT INTO task_profile (task_id, model_route, description, created_at, updated_at) VALUES
  ('agent.dialectic', 'qwen-plus', 'V1.5 dialectic Layer A reasoning (使用者本人画像, NOT 会话对象画像) — async background goroutine, cadence gated, 7d cached_insight; D4 禁 thinking model', NOW(), NOW())
ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at);
