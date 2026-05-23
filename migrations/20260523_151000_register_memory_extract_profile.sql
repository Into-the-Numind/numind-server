-- Migration: register agent.memory_extract task profile
-- Feature: agent-mode-v15-memory-layer-a (Task 3.3 — LLM Extraction Async Pipeline)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-03-llm-extraction.md
-- Rollback: 20260523_151000_register_memory_extract_profile_rollback.sql
--
-- Task 3.3 ExtractorService 异步抽取 facts 时通过 aiservice.Chat(ctx, "agent.memory_extract", req)
-- 调小 LLM (deepseek-v3-2 / qwen-turbo). 本 SQL 占位写一行 task_profile, 真实的 ai_service_route
-- 绑定由管理端 (admin) CRUD 配 (deepseek-v3-2 主路 + qwen-turbo fallback).
--
-- D3 决策 (context.md): "推荐 deepseek-v3-2 / qwen-turbo — 便宜异步后台跑, 中文好".
--
-- Idempotent: ON DUPLICATE KEY UPDATE updated_at only.

INSERT INTO task_profile (task_id, model_route, description, created_at, updated_at) VALUES
  ('agent.memory_extract', 'deepseek-v3-2', 'V1.5 memory async extraction (Layer A — user-self facts) 30s debounce + threshold 0.7', NOW(), NOW())
ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at);
