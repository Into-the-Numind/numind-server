-- Rollback: 20260618_190000_register_meeting_summary_profile.sql
-- 删除 meeting.summary task profile。删后滚动摘要折叠会 ResolveTask 失败（best-effort 后台,
-- 仅 log 不影响转写/反馈）。如需恢复请重跑正向 migration 或把 summary.go 的 updateRunningSummary
-- 改回 profile.ChatbotStream。
DELETE FROM task_profile WHERE task_id = 'meeting.summary';
