-- Rollback: 20260624_103000_seed_xhs_analyze.sql
-- 删除 xhs.note_analyze task profile。删后小红书选题 AI 富化会失败
-- (ResolveTask 找不到 profile → enrich_status=failed, 不阻塞入库)。
--
-- 注意: 不删 deepseek-v4-flash 的 pricing_rule —— 该定价为 session.title / salesrag.intent /
-- meeting.* 等多个 task 共享; 正向 migration 仅在其缺失时兜底插入 (WHERE NOT EXISTS),
-- 删除会误伤其它 task 的计费。
DELETE FROM task_profile WHERE task_id = 'xhs.note_analyze';
