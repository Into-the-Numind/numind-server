-- Migration: register agent.dialectic task profile
-- Feature: agent-mode-v15-memory-layer-a (Task 3.7 — Dialectic Layer A Reasoning)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-07-dialectic.md
-- Rollback: 20260523_154000_register_dialectic_profile_rollback.sql
--
-- Task 3.7 DialecticService 后台 goroutine 基于 user 的 top-20 Layer A facts
-- (subject_id IS NULL — 关于使用 agent 的真实 user 本人, NOT 关于会话讨论的对象)
-- 推理 100-800 字的 "该使用者是谁 + 应当怎么个人化对待他" 画像, 写入
-- user_memory_profile.cached_insight (7 天软 cache, cadence 5 min cooldown 控制).
-- aiservice.Chat(ctx, "agent.dialectic", req) — task_profile_service (M:N) 绑
-- ai_service (qwen-plus 主路 + deepseek-v3-2 fallback), 管理端 CRUD 配.
--
-- D4 决策 (context.md): "推荐 qwen-plus / deepseek-v3-2 — 不要 thinking model"
-- dialectic 要稳定一致输出, 不要发散. 后台跑 cadence ≥ 5 min, 不阻塞用户.
--
-- ⚠️ 历史修正 (2026-05-23): 原 SQL 用 model_route 列名是 implementer 假设错误。
-- 真实 schema 是 task_profile(task_id, display_name, description, service_type,
-- requirements, default_service_id, user_selectable). 用 deepseek-v3.2 做默认
-- (现 dev 环境 qwen-plus 未 seed 到 ai_service; 管理端可后续切换).
--
-- Idempotent: INSERT IGNORE (UNIQUE on task_id 拦重复).

INSERT IGNORE INTO task_profile (task_id, display_name, description, service_type, requirements, default_service_id, user_selectable)
SELECT
  'agent.dialectic',
  'Agent Dialectic (Layer A)',
  'V1.5 Task 3.7 dialectic Layer A reasoning (使用者本人画像, NOT 会话对象画像) — async background goroutine, cadence gated, 7d cached_insight; D4 禁 thinking model.',
  'llm',
  JSON_OBJECT(),
  id,
  0
FROM ai_service
WHERE model_key = 'deepseek-v3.2'
LIMIT 1;
