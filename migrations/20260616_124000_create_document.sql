-- 文档系统 v1：AI 生成产物的可编辑文档（document-system feature）
-- feature flag: features.document_system.enabled（prod 默认 off → 本表休眠不可达）
-- 手动执行（CI 不自动跑 migration，遵 dev-deploy-migration-gap）；仅在启用本功能的环境跑。
-- AutoMigrate 在 flag on 时也会建本表（helper.go 条件迁移）；本文件为权威 schema + 显式 ROW_FORMAT。

CREATE TABLE IF NOT EXISTS `document` (
  `id`                BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  `user_id`           INT UNSIGNED    NOT NULL,
  `parent_user_id`    INT UNSIGNED    NULL,                          -- B2B2C 上下文快照, v1 不用于共享
  `source_object_key` VARCHAR(512)    NOT NULL,                      -- COS object key (限 agent-outputs/{userID}/ 前缀), 稳定标识
  `source_run_id`     BIGINT UNSIGNED NULL,                          -- 弱关联 agent_run, 无 FK(避免耦合)
  `source_mime`       VARCHAR(128)    NULL,
  `title`             VARCHAR(255)    NOT NULL,
  `content_md`        MEDIUMTEXT      NOT NULL,                       -- 可编辑 markdown, <=2MB
  `parse_method`      VARCHAR(32)     NOT NULL DEFAULT 'direct',     -- direct|html|markitdown|qwen_long
  `created_at`        DATETIME(3)     NOT NULL,
  `updated_at`        DATETIME(3)     NOT NULL,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uniq_doc_user_source` (`user_id`, `source_object_key`),
  KEY `idx_doc_user_updated` (`user_id`, `updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci ROW_FORMAT=DYNAMIC;
-- ROW_FORMAT=DYNAMIC: source_object_key VARCHAR(512) utf8mb4 入唯一索引会撞 InnoDB COMPACT 767 字节限,
-- DYNAMIC(MySQL 8 默认)限 3072 字节足够。显式声明防旧实例默认 COMPACT。
