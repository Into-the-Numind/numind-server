-- SOP节点书签功能数据库迁移脚本
-- 创建时间: 2026-01-16
-- 说明: 为SOP系统添加节点书签保存与自动恢复功能

-- 1. 创建书签表
CREATE TABLE IF NOT EXISTS `sop_node_bookmark` (
  `id` bigint(20) unsigned NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `user_id` bigint(20) unsigned NOT NULL COMMENT '用户ID',
  `template_id` bigint(20) unsigned NOT NULL COMMENT 'SOP模板ID',
  `node_id` bigint(20) unsigned NOT NULL COMMENT '节点ID',
  `node_sort` int(11) NOT NULL COMMENT '节点排序（冗余，便于查询）',

  -- 保存的执行内容
  `input` longtext COLLATE utf8mb4_unicode_ci COMMENT '节点输入',
  `output` longtext COLLATE utf8mb4_unicode_ci COMMENT '节点输出',
  `thinking` longtext COLLATE utf8mb4_unicode_ci COMMENT 'AI思考过程',

  -- 来源信息（用于追溯）
  `source_run_id` bigint(20) unsigned DEFAULT NULL COMMENT '来源运行ID',
  `source_node_run_id` bigint(20) unsigned DEFAULT NULL COMMENT '来源节点运行ID',

  -- Token统计（用于成本展示）
  `prompt_tokens` int(11) DEFAULT 0 COMMENT '输入token数',
  `completion_tokens` int(11) DEFAULT 0 COMMENT '输出token数',
  `total_tokens` int(11) DEFAULT 0 COMMENT '总token数',

  -- 元数据
  `bookmark_name` varchar(255) COLLATE utf8mb4_unicode_ci DEFAULT NULL COMMENT '书签名称（可选）',
  `description` text COLLATE utf8mb4_unicode_ci COMMENT '书签描述（可选）',

  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP COMMENT '创建时间',
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP COMMENT '更新时间',
  `deleted_at` datetime DEFAULT NULL COMMENT '删除时间（软删除）',

  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_template_node` (`user_id`, `template_id`, `node_id`, `deleted_at`),
  KEY `idx_user_template` (`user_id`, `template_id`),
  KEY `idx_node_id` (`node_id`),
  KEY `idx_source_run` (`source_run_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='SOP节点书签表';

-- 2. 修改 sop_node_run 表，添加书签相关字段
ALTER TABLE `sop_node_run`
ADD COLUMN `from_bookmark` tinyint(1) DEFAULT 0 COMMENT '是否从书签恢复（0否1是）' AFTER `status`,
ADD COLUMN `bookmark_id` bigint(20) unsigned DEFAULT NULL COMMENT '关联的书签ID' AFTER `from_bookmark`,
ADD INDEX `idx_bookmark` (`bookmark_id`);

-- 验证表结构
SHOW CREATE TABLE `sop_node_bookmark`;
SHOW CREATE TABLE `sop_node_run`;

-- 查看新增的字段
SELECT COLUMN_NAME, COLUMN_TYPE, COLUMN_COMMENT
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND TABLE_NAME = 'sop_node_run'
  AND COLUMN_NAME IN ('from_bookmark', 'bookmark_id');
