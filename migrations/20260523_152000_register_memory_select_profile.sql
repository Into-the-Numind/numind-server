-- Migration: register agent.memory_select task profile
-- Feature: agent-mode-v15-memory-layer-a (Task 3.4 — Top-5 Side-Query Selector)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-04-top5-selector.md
-- Rollback: 20260523_152000_register_memory_select_profile_rollback.sql
--
-- Task 3.4 SelectorService per-turn 选 ≤5 个最相关 fact 时通过
-- aiservice.Chat(ctx, "agent.memory_select", req) 调小 LLM. task_profile_service
-- (M:N) 把任务绑到 ai_service (qwen-turbo 主路 + deepseek-v3-2 fallback), 管理端 CRUD 配.
--
-- D4 决策 (context.md): "推荐 qwen-turbo / deepseek-v3-2 — 每次 user turn 前
-- 调用要快, 任务简单 (选 5 个最相关 fact id)".
--
-- ⚠️ 历史修正 (2026-05-23): 原 SQL 用 model_route 列名是 implementer 假设错误。
-- 真实 schema 是 task_profile(task_id, display_name, description, service_type,
-- requirements, default_service_id, user_selectable).
--
-- Idempotent: INSERT IGNORE (UNIQUE on task_id 拦重复).

INSERT IGNORE INTO task_profile (task_id, display_name, description, service_type, requirements, default_service_id, user_selectable)
SELECT
  'agent.memory_select',
  'Agent Memory Selector',
  'V1.5 Task 3.4 memory side-query selector (Layer A — pick top-5 user-self facts per turn) LRU cache 30s/1024 + ≤5 shortcircuit + 3-layer fallback. D4: 推荐 qwen-turbo / deepseek-v3-2.',
  'llm',
  JSON_OBJECT(),
  id,
  0
FROM ai_service
WHERE model_key = 'qwen-turbo'
LIMIT 1;
