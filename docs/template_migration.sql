-- Template 表迁移文件
-- 创建时间: 2024-01-01
-- 描述: 创建模板管理表

-- 创建 template 表
CREATE TABLE IF NOT EXISTS `template` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT COMMENT '主键ID',
  `created_at` datetime(3) DEFAULT NULL COMMENT '创建时间',
  `updated_at` datetime(3) DEFAULT NULL COMMENT '更新时间',
  `deleted_at` datetime(3) DEFAULT NULL COMMENT '删除时间',
  `name` varchar(50) NOT NULL COMMENT '模板名称',
  `file` longtext NOT NULL COMMENT '模板文件内容',
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_template_name` (`name`),
  KEY `idx_template_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='模板表';

-- 插入示例数据
INSERT INTO `template` (`name`, `file`, `created_at`, `updated_at`) VALUES
('默认模板', '这是一个默认模板的内容', NOW(), NOW()),
('邮件模板', '邮件模板的内容', NOW(), NOW()),
('通知模板', '通知模板的内容', NOW(), NOW());

-- 如果需要删除表（谨慎使用）
-- DROP TABLE IF EXISTS `template`; 