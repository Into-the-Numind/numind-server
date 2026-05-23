-- Migration: register agent.digest task profile
-- Feature: agent-mode-v15-memory-layer-a (Task 3.8 — 分层时间感知 cron-driven digest LLM)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-08-temporal-tree.md
-- Rollback: 20260524_141000_register_digest_profile_rollback.sql
--
-- Task 3.8 DigestGenerator 4 个 cron job 共用同一个 task profile (prompt 内容
-- 在 biz 层根据 granularity 切换). LLM 调用通过
-- aiservice.Chat(ctx, "agent.digest", req). task_profile_service (M:N) 绑
-- ai_service (qwen-plus 主路 + deepseek-v3-2 fallback), 管理端 CRUD 配.
--
-- D4 决策 (context.md): "dialectic / autocompact 等用 qwen-plus 或 deepseek-v3-2,
-- 不要 thinking model — 后台跑 cadence ≥ 5 min, 不阻塞用户". Daily digest 30 次/月
-- + weekly/monthly/quarterly 合计 5.33 次/月, 月成本 ≈ ¥0.10/用户.
--
-- ⚠️ 历史修正 (2026-05-23): 原 SQL 用 model_route 列名是 implementer 假设错误。
-- 真实 schema 是 task_profile(task_id, display_name, description, service_type,
-- requirements, default_service_id, user_selectable). 用 deepseek-v3.2 做默认
-- (现 dev 环境 qwen-plus 未 seed 到 ai_service; 管理端可后续切换).
--
-- Idempotent: INSERT IGNORE (UNIQUE on task_id 拦重复).

INSERT IGNORE INTO task_profile (task_id, display_name, description, service_type, requirements, default_service_id, user_selectable)
SELECT
  'agent.digest',
  'Agent Temporal Digest',
  'V1.5 Task 3.8 分层时间感知 digest 生成 (daily/weekly/monthly/quarterly cron, Asia/Shanghai). D4: NO thinking model.',
  'llm',
  JSON_OBJECT(),
  id,
  0
FROM ai_service
WHERE model_key = 'deepseek-v3.2'
LIMIT 1;
