#!/bin/bash

# 删除cards表中的image_id字段和相关约束
echo "删除cards表中的image_id字段..."

# 连接到数据库并执行SQL
mysql -u root -p << EOF
USE numind;

-- 删除外键约束
ALTER TABLE cards DROP FOREIGN KEY cards_ibfk_3;

-- 删除image_id索引
ALTER TABLE cards DROP INDEX idx_image_id;

-- 删除image_id字段
ALTER TABLE cards DROP COLUMN image_id;

-- 删除其他不需要的字段
ALTER TABLE cards DROP COLUMN title;
ALTER TABLE cards DROP COLUMN content;
ALTER TABLE cards DROP COLUMN ocr_text;
ALTER TABLE cards DROP COLUMN card_type;
ALTER TABLE cards DROP COLUMN status;
ALTER TABLE cards DROP COLUMN source;

-- 删除相关索引
ALTER TABLE cards DROP INDEX idx_status;
ALTER TABLE cards DROP INDEX idx_card_type;

echo "cards表结构更新完成！"
EOF

echo "数据库迁移完成！" 