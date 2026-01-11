#!/bin/bash
# 数据库迁移执行脚本 - 在服务器49.233.219.254上运行

set -e  # 遇到错误立即退出

echo "=========================================="
echo "开始执行数据库迁移..."
echo "=========================================="

# 数据库配置
DB_HOST="localhost"
DB_USER="root"
DB_PASS="Numind2025"
DB_NAME="numind-dev"

# 执行迁移脚本
echo "正在执行迁移脚本..."
mysql -h ${DB_HOST} -u ${DB_USER} -p${DB_PASS} ${DB_NAME} << 'EOFMIGRATION'
-- 层级化客户管理系统 - 数据库迁移脚本
-- 执行日期: 2026-01-09
-- 描述: 添加客户层级管理和SOP运行统计功能

-- ============================================================================
-- 1. 扩展User表 - 添加客户层级和SOP统计字段
-- ============================================================================

ALTER TABLE `user` 
ADD COLUMN `parent_user_id` INT UNSIGNED NULL COMMENT '上级客户ID,NULL表示直接客户' AFTER `unionid`,
ADD COLUMN `total_sop_runs` INT NOT NULL DEFAULT 0 COMMENT '总SOP运行次数' AFTER `chat_num`,
ADD COLUMN `monthly_sop_runs` INT NOT NULL DEFAULT 0 COMMENT '当月SOP运行次数' AFTER `total_sop_runs`,
ADD COLUMN `monthly_reset_at` TIMESTAMP NULL COMMENT '上次月度重置时间' AFTER `monthly_sop_runs`,
ADD INDEX `idx_parent_user_id` (`parent_user_id`),
ADD INDEX `idx_total_sop_runs` (`total_sop_runs`),
ADD INDEX `idx_monthly_reset_at` (`monthly_reset_at`);

-- ============================================================================
-- 2. 创建UserTemplatePermission表 - 用户模板权限白名单
-- ============================================================================

CREATE TABLE IF NOT EXISTS `user_template_permission` (
  `id` INT UNSIGNED NOT NULL AUTO_INCREMENT,
  `parent_user_id` INT UNSIGNED NOT NULL COMMENT '直接客户ID',
  `sub_user_id` INT UNSIGNED NOT NULL COMMENT '二级客户ID',
  `template_id` INT UNSIGNED NOT NULL COMMENT 'SOP模板ID',
  `created_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` TIMESTAMP NULL DEFAULT NULL,
  PRIMARY KEY (`id`),
  UNIQUE INDEX `idx_sub_template_unique` (`sub_user_id`, `template_id`, `deleted_at`),
  INDEX `idx_parent_sub` (`parent_user_id`, `sub_user_id`),
  INDEX `idx_template` (`template_id`),
  INDEX `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='用户模板权限表(白名单)';

-- ============================================================================
-- 3. 初始化现有用户数据
-- ============================================================================

-- 3.1 所有现有用户默认为直接客户(parent_user_id保持NULL)
-- 3.2 初始化月度重置时间为本月1号零点(这样下个月1号零点会自动重置)
UPDATE `user` 
SET `monthly_reset_at` = DATE_FORMAT(NOW(), '%Y-%m-01 00:00:00')
WHERE `monthly_reset_at` IS NULL;

EOFMIGRATION

if [ $? -eq 0 ]; then
    echo "✅ 迁移脚本执行成功!"
else
    echo "❌ 迁移脚本执行失败!"
    exit 1
fi

echo ""
echo "=========================================="
echo "验证迁移结果..."
echo "=========================================="

# 验证user表新字段
echo "1. 检查user表结构:"
mysql -h ${DB_HOST} -u ${DB_USER} -p${DB_PASS} ${DB_NAME} -e "
SELECT COUNT(*) as total_users,
       COUNT(parent_user_id) as sub_users,
       COUNT(*) - COUNT(parent_user_id) as direct_users
FROM user
WHERE deleted_at IS NULL;
"

echo ""
echo "2. 查看前5个用户的新字段:"
mysql -h ${DB_HOST} -u ${DB_USER} -p${DB_PASS} ${DB_NAME} -e "
SELECT id, nickname, parent_user_id, total_sop_runs, monthly_sop_runs, 
       DATE_FORMAT(monthly_reset_at, '%Y-%m-%d %H:%i:%s') as monthly_reset_at 
FROM user 
LIMIT 5;
"

echo ""
echo "3. 验证user_template_permission表:"
mysql -h ${DB_HOST} -u ${DB_USER} -p${DB_PASS} ${DB_NAME} -e "
SHOW TABLES LIKE 'user_template_permission';
"

echo ""
echo "=========================================="
echo "✅ 数据库迁移完成!"
echo "=========================================="
