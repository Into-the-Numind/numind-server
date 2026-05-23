-- Migration: register agent.memory_select task profile
-- Feature: agent-mode-v15-memory-layer-a (Task 3.4 — Top-5 Side-Query Selector)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-04-top5-selector.md
-- Rollback: 20260523_152000_register_memory_select_profile_rollback.sql
--
-- Task 3.4 SelectorService per-turn 选 ≤5 个最相关 fact 时通过
-- aiservice.Chat(ctx, "agent.memory_select", req) 调小 LLM (qwen-turbo /
-- deepseek-v3-2). 本 SQL 占位写一行 task_profile, 真实的 ai_service_route
-- 绑定由管理端 (admin) CRUD 配 (qwen-turbo 主路 + deepseek-v3-2 fallback).
--
-- D4 决策 (context.md): "推荐 qwen-turbo / deepseek-v3-2 — 每次 user turn 前
-- 调用要快, 任务简单 (选 5 个最相关 fact id)".
--
-- Idempotent: ON DUPLICATE KEY UPDATE updated_at only.

INSERT INTO task_profile (task_id, model_route, description, created_at, updated_at) VALUES
  ('agent.memory_select', 'qwen-turbo', 'V1.5 memory side-query selector (Layer A — pick top-5 user-self facts per turn) cache 30s + ≤5 shortcircuit + 3-layer fallback', NOW(), NOW())
ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at);
