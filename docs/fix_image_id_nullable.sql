-- 修复 cards 表中 image_id 字段的外键约束问题
-- 将 image_id 字段改为可空，因为AI生成的卡片可能不需要关联特定图片

-- 1. 删除现有的外键约束
ALTER TABLE cards DROP FOREIGN KEY fk_images_cards;

-- 2. 修改 image_id 字段为可空
ALTER TABLE cards MODIFY COLUMN image_id BIGINT UNSIGNED NULL;

-- 3. 重新添加外键约束，允许 NULL 值
ALTER TABLE cards ADD CONSTRAINT fk_images_cards 
FOREIGN KEY (image_id) REFERENCES images(id) ON DELETE CASCADE;

-- 4. 验证修改
-- 检查表结构
DESCRIBE cards; 