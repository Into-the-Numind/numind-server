-- Migration: register agent.memory_extract task profile
-- Feature: agent-mode-v15-memory-layer-a (Task 3.3 — LLM Extraction Async Pipeline)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-03-llm-extraction.md
-- Rollback: 20260523_151000_register_memory_extract_profile_rollback.sql
--
-- Task 3.3 ExtractorService 异步抽取 facts 时通过 aiservice.Chat(ctx, "agent.memory_extract", req)
-- 调小 LLM. 写入 task_profile 表 (主表) 定义任务. task_profile_service (M:N
-- 关联表) 把 task_profile 绑到具体 ai_service (主路 + fallback), 由管理端 CRUD 配.
--
-- D3 决策 (context.md): "推荐 deepseek-v3-2 / qwen-turbo — 便宜异步后台跑, 中文好".
--
-- ⚠️ 历史修正 (2026-05-23): 原 SQL 用 model_route 列名是 implementer 假设错误。
-- 真实 schema 是 task_profile(task_id, display_name, description, service_type,
-- requirements, default_service_id, user_selectable). default_service_id 是
-- ai_service.id 的 FK。本 SQL 用 qwen-turbo (model_key='qwen-turbo') 的 id 做
-- 默认服务；管理端可后续 fine-tune fallback 路由。
--
-- Idempotent: INSERT IGNORE (UNIQUE on task_id 拦重复).

INSERT IGNORE INTO task_profile (task_id, display_name, description, service_type, requirements, default_service_id, user_selectable)
SELECT
  'agent.memory_extract',
  'Agent Memory Extract',
  'V1.5 Task 3.3 memory async extraction (Layer A — facts about the user themselves) 30s debounce + threshold 0.7. D3: 推荐 deepseek-v3-2 / qwen-turbo.',
  'llm',
  JSON_OBJECT(),
  id,
  0
FROM ai_service
WHERE model_key = 'qwen-turbo'
  AND is_active = 1
  AND deprecated_at IS NULL
LIMIT 1;
