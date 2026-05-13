-- +migrate Up
-- sop-chatbot-visibility-scope: SOP / 智能体可见范围权限（实体视角白名单）
-- 见 docs/superpowers/specs/2026-05-13-sop-chatbot-visibility-scope-design.md §7.1
--
-- 加 visibility_restricted 短路字段 + 2 张白名单表 (sop/chatbot visibility grant).
-- 唯一索引故意不含 deleted_at, 配合 biz 层 Unscoped().Delete 物理删模式
-- (见 spec §2.2 索引说明 + §4.1.6 UpdateSopVisibility 伪代码).

ALTER TABLE sop_template
  ADD COLUMN visibility_restricted TINYINT(1) NOT NULL DEFAULT 0
  COMMENT '可见范围限制: 0=全部子用户可见; 1=仅 sop_visibility_grant 白名单子用户可见';

ALTER TABLE chatbot_config
  ADD COLUMN visibility_restricted TINYINT(1) NOT NULL DEFAULT 0
  COMMENT '可见范围限制: 0=全部子用户可见; 1=仅 chatbot_visibility_grant 白名单子用户可见';

CREATE TABLE IF NOT EXISTS sop_visibility_grant (
  id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  parent_user_id BIGINT UNSIGNED NOT NULL COMMENT '父账户 ID (caller, 冗余便于查询)',
  sub_user_id BIGINT UNSIGNED NOT NULL COMMENT '被授权可见的子用户 ID',
  sop_template_id BIGINT UNSIGNED NOT NULL COMMENT '受限可见的 SOP 模板 ID',
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  UNIQUE KEY idx_svg_sub_template_unique (sub_user_id, sop_template_id),
  KEY idx_svg_parent_sub (parent_user_id, sub_user_id),
  KEY idx_svg_template (sop_template_id),
  KEY idx_svg_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='SOP 可见范围授权（白名单）';

CREATE TABLE IF NOT EXISTS chatbot_visibility_grant (
  id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  parent_user_id BIGINT UNSIGNED NOT NULL COMMENT '父账户 ID (caller, 冗余便于查询)',
  sub_user_id BIGINT UNSIGNED NOT NULL COMMENT '被授权可见的子用户 ID',
  chatbot_id BIGINT UNSIGNED NOT NULL COMMENT '受限可见的 chatbot ID',
  created_at DATETIME(3) NOT NULL,
  updated_at DATETIME(3) NOT NULL,
  deleted_at DATETIME(3) NULL,
  UNIQUE KEY idx_cvg_sub_chatbot_unique (sub_user_id, chatbot_id),
  KEY idx_cvg_parent_sub (parent_user_id, sub_user_id),
  KEY idx_cvg_chatbot (chatbot_id),
  KEY idx_cvg_deleted (deleted_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
  COMMENT='Chatbot 可见范围授权（白名单）';
