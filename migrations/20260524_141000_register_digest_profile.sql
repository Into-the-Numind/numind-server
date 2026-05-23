-- Migration: register agent.digest task profile
-- Feature: agent-mode-v15-memory-layer-a (Task 3.8 — 分层时间感知 cron-driven digest LLM)
-- Spec: /Users/zhiyuchen/Downloads/有数-Agent-Mode-V1.5-NDF-spec/03-memory/task-08-temporal-tree.md
-- Rollback: 20260524_141000_register_digest_profile_rollback.sql
--
-- Task 3.8 DigestGenerator 4 个 cron job 共用同一个 task profile (prompt 内容
-- 在 biz 层根据 granularity 切换). LLM 调用通过
-- aiservice.Chat(ctx, "agent.digest", req) 路由到 qwen-plus 主路 + deepseek-v3-2
-- fallback. 占位写一行 task_profile, 真实 ai_service_route 绑定由管理端 CRUD 配.
--
-- D4 决策 (context.md): "dialectic / autocompact 等用 qwen-plus 或 deepseek-v3-2,
-- 不要 thinking model — 后台跑 cadence ≥ 5 min, 不阻塞用户". Daily digest 30 次/月
-- + weekly/monthly/quarterly 合计 5.33 次/月, 月成本 ≈ ¥0.10/用户.
--
-- Idempotent: ON DUPLICATE KEY UPDATE updated_at only.

INSERT INTO task_profile (task_id, model_route, description, created_at, updated_at) VALUES
  ('agent.digest', 'qwen-plus', 'V1.5 分层时间感知 digest 生成 (daily/weekly/monthly/quarterly cron — D4: NO thinking model)', NOW(), NOW())
ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at);
