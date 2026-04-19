-- Add XHS account binding fields to monitor_config
-- UP:
ALTER TABLE monitor_config ADD COLUMN xhs_cookies TEXT AFTER notify_on_update;
ALTER TABLE monitor_config ADD COLUMN xhs_nickname VARCHAR(200) AFTER xhs_cookies;
ALTER TABLE monitor_config ADD COLUMN xhs_user_id VARCHAR(100) AFTER xhs_nickname;

-- Rollback (manual):
-- ALTER TABLE monitor_config DROP COLUMN xhs_cookies;
-- ALTER TABLE monitor_config DROP COLUMN xhs_nickname;
-- ALTER TABLE monitor_config DROP COLUMN xhs_user_id;
