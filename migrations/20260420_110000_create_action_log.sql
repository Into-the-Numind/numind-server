-- action_log: 管理端敏感操作审计日志表
--
-- 背景：model.ActionLogM 定义于 2026-04-19（grant-membership feature，commit d6f8fe3），
-- 但当时仅靠 AutoMigrate 假设自动建表，实际 helper.go:159 的 autoMigrate 列表未包含此 model，
-- 导致 dev/qa/prod 从未创建此表。任何调用 GrantMembership 或 billing-mode-init admin
-- 端点的请求都会在 tx commit 时拿到 Error 1146: Table 'action_log' doesn't exist，
-- 整个授权或初始化操作 rollback。
--
-- 使用方：
--   - biz/credit/grant_membership.go:159  — parent 为 child 开通会员（action='grant_membership'）
--   - controller/v1/admin_migration/migrations.go:208 — admin 端 billing-mode 初始化
--
-- Schema 依据 model.ActionLogM struct：GORM uint → BIGINT UNSIGNED，
-- Action/Target NOT NULL，TargetID 为 *uint 所以允许 NULL。

CREATE TABLE IF NOT EXISTS action_log (
    id          BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id     BIGINT UNSIGNED NOT NULL              COMMENT '触发操作的用户 ID（parent/admin）',
    action      VARCHAR(100)    NOT NULL              COMMENT '操作类型，如 grant_membership / billing_mode_init',
    target      VARCHAR(100)    NOT NULL DEFAULT ''   COMMENT '操作对象类型，如 user',
    target_id   BIGINT UNSIGNED DEFAULT NULL          COMMENT '操作对象 ID，可为空',
    detail      TEXT                                  COMMENT '操作详情 JSON（product_type/months/reason 等）',
    created_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    KEY idx_user_id (user_id),
    KEY idx_action_created (action, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='管理端敏感操作审计日志（B2B grant_membership / billing 初始化等）';
