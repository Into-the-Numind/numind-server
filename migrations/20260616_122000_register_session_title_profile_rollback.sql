-- Rollback: unregister session.title task profile
-- Forward: 20260616_122000_register_session_title_profile.sql
--
-- Removing the row makes gateway.ResolveTask("session.title") fail again, so
-- sessiontitle.Generate degrades to a best-effort no-op (logged warn, no title,
-- no error to the user). Safe — the feature is non-critical.

DELETE FROM task_profile WHERE task_id = 'session.title';
