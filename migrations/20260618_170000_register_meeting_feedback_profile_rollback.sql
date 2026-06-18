-- Rollback: 20260618_170000_register_meeting_feedback_profile.sql
-- 删除 meeting.feedback task profile。删后会议实时反馈会回退/失败（ResolveTask 找不到 profile），
-- 如需恢复请重跑正向 migration 或把代码 feedback.go 改回 profile.ChatbotStream。
DELETE FROM task_profile WHERE task_id = 'meeting.feedback';
