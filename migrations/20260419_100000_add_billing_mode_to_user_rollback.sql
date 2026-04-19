-- Rollback: Remove billing_mode field from user table
DROP INDEX idx_user_billing_mode ON `user`;
ALTER TABLE `user` DROP COLUMN billing_mode;
