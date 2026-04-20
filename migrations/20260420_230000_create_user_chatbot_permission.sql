-- Feature: child-run-permission (Task 1 / spec §3.1)
-- 新建 user_chatbot_permission 表：子账号 chatbot 运行白名单
--
-- 依赖：
--   * user.id          （子账号引用，逻辑层保证引用完整性，不加 FK）
--   * chatbot_config.id（chatbot 引用，逻辑层保证引用完整性，不加 FK）
--
-- 执行顺序：本迁移必须先于
--   migrations/20260420_230001_backfill_default_template_permissions.sql 执行。
--   （Backfill 只改 user_template_permission，与本表无直接数据依赖；但为了让
--    整个 feature 的表结构在 backfill 窗口之前先落库，按文件名序执行即可。）
--
-- 风格对齐：参考既有 user_template_permission（不硬绑 FK、UNIQUE 唯一约束幂等）。
-- 与 user_template_permission 不同的是本表不使用软删除（DELETE 直接真删行）。

CREATE TABLE IF NOT EXISTS `user_chatbot_permission` (
  `id`          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `sub_user_id` BIGINT UNSIGNED NOT NULL COMMENT '子账号 user.id',
  `chatbot_id`  BIGINT UNSIGNED NOT NULL COMMENT 'chatbot_config.id',
  `created_at`  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_ucp_sub_chatbot` (`sub_user_id`, `chatbot_id`),
  KEY `idx_ucp_chatbot` (`chatbot_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='子账号 chatbot 运行权限白名单';
