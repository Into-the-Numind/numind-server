-- Book Template Migration
-- 创建时间: 2024-01-01
-- 描述: 为book表添加template_id字段

-- 为book表添加template_id字段
ALTER TABLE `book` 
ADD COLUMN `template_id` varchar(50) DEFAULT NULL COMMENT '模板ID' 
AFTER `category_name`;

-- 添加索引（可选）
-- ALTER TABLE `book` ADD INDEX `idx_template_id` (`template_id`);

-- 如果需要回滚
-- ALTER TABLE `book` DROP COLUMN `template_id`; 