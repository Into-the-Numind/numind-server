-- 分类业务数据库迁移文件

-- 1. 创建分类表（如果不存在）
CREATE TABLE IF NOT EXISTS `category` (
    `id` BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    `name` VARCHAR(50) UNIQUE NOT NULL,
    `description` VARCHAR(255),
    `created_at` TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_name (`name`)
);

-- 2. 为books表添加category_id字段
ALTER TABLE `books` ADD COLUMN `category_id` BIGINT UNSIGNED NULL AFTER `cover_url`;
ALTER TABLE `books` ADD INDEX `idx_category_id` (`category_id`);
ALTER TABLE `books` ADD CONSTRAINT `fk_books_category` FOREIGN KEY (`category_id`) REFERENCES `category`(`id`) ON DELETE SET NULL;

-- 3. 插入一些默认分类
INSERT INTO `category` (`name`, `description`) VALUES 
('技术', '技术相关的内容'),
('生活', '日常生活相关的内容'),
('学习', '学习笔记和知识整理'),
('工作', '工作相关的内容'),
('娱乐', '娱乐休闲相关的内容')
ON DUPLICATE KEY UPDATE `description` = VALUES(`description`);

-- 4. 更新现有books表的category字段（可选）
-- 将现有的category字符串字段转换为对应的category_id
-- 这里需要根据实际情况调整

-- 5. 添加触发器，当分类被删除时，将相关书籍的category_id设为NULL
DELIMITER //
CREATE TRIGGER IF NOT EXISTS `update_books_category_on_delete`
AFTER DELETE ON `category`
FOR EACH ROW
BEGIN
    UPDATE `books` SET `category_id` = NULL WHERE `category_id` = OLD.id;
END//
DELIMITER ; 