-- agent-mode-v2-skill-marketplace T01 forward migration
-- 创建 2 张新表：skill_marketplace / skill_subscription
-- 依赖 v2 #1 已 land：skill 表 + ENUM('generated','custom','imported_from_template','imported_from_marketplace') 第 4 值
-- 本 SQL 仅 DDL（建表 + 索引 + FK），可重入（IF NOT EXISTS）

CREATE TABLE IF NOT EXISTS skill_marketplace (
  id                       INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  publisher_user_id        INT UNSIGNED NOT NULL COMMENT '发布方父账户 user.id',
  source_skill_id          INT UNSIGNED NOT NULL COMMENT '发布方原 skill.id（追溯用，不级联）',
  name                     VARCHAR(100) NOT NULL COMMENT '上架名称（脱敏后等于原 skill.name 或编辑过的）',
  description              VARCHAR(500) NOT NULL DEFAULT '' COMMENT '简短描述（浏览页卡片显示）',
  when_to_use              VARCHAR(500) NOT NULL DEFAULT '' COMMENT '何时使用（FULLTEXT 参与）',
  sanitized_body_md        MEDIUMTEXT NOT NULL COMMENT '脱敏后的 markdown body，独立副本',
  allowed_tools            JSON NOT NULL DEFAULT (JSON_ARRAY()) COMMENT '工具白名单 []string，从原 skill 复制',
  category_tags            JSON NOT NULL DEFAULT (JSON_ARRAY()) COMMENT '分类标签 []string，发布方多选',
  is_public                TINYINT(1) NOT NULL DEFAULT 1 COMMENT '1=上架, 0=下架（软）',
  is_platform_recommended  TINYINT(1) NOT NULL DEFAULT 0 COMMENT 'admin 端打标',
  subscribe_count          INT UNSIGNED NOT NULL DEFAULT 0 COMMENT '订阅 +1 / 取消 -1',
  created_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at               DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  KEY idx_marketplace_publisher (publisher_user_id, is_public),
  KEY idx_marketplace_source (source_skill_id),
  KEY idx_marketplace_recommended (is_platform_recommended DESC, subscribe_count DESC, created_at DESC),
  FULLTEXT KEY ft_marketplace_search (name, description, when_to_use) /*!50700 WITH PARSER ngram */
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'agent-mode-v2-skill-marketplace: 跨租户 Skill 发布市场';

CREATE TABLE IF NOT EXISTS skill_subscription (
  id                  INT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
  subscriber_user_id  INT UNSIGNED NOT NULL COMMENT '订阅方父账户 user.id',
  marketplace_id      INT UNSIGNED NOT NULL COMMENT '订阅的 marketplace 项 id',
  cloned_skill_id     INT UNSIGNED NOT NULL COMMENT '订阅时复制到订阅方租户的 skill.id',
  subscribed_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uk_subscription_user_marketplace (subscriber_user_id, marketplace_id),
  KEY idx_subscription_subscriber (subscriber_user_id, subscribed_at DESC),
  KEY idx_subscription_marketplace (marketplace_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT 'agent-mode-v2-skill-marketplace: Skill 订阅关系';
