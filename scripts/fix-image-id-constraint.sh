#!/bin/bash

# 修复 cards 表中 image_id 字段的外键约束问题
# 将 image_id 字段改为可空，因为AI生成的卡片可能不需要关联特定图片

echo "开始修复 cards 表的 image_id 字段约束..."

# 从配置文件读取数据库连接信息
DB_HOST=$(grep -A 5 "db:" config_local.yaml | grep "host:" | awk '{print $2}' | sed 's/127.0.0.1/localhost/')
DB_USER=$(grep -A 5 "db:" config_local.yaml | grep "username:" | awk '{print $2}')
DB_PASS=$(grep -A 5 "db:" config_local.yaml | grep "password:" | awk '{print $2}')
DB_NAME=$(grep -A 5 "db:" config_local.yaml | grep "database:" | awk '{print $2}')

echo "数据库连接信息:"
echo "Host: $DB_HOST"
echo "User: $DB_USER"
echo "Database: $DB_NAME"

# 检查MySQL连接
if ! mysql -h localhost -u "$DB_USER" -p"$DB_PASS" -e "USE $DB_NAME;" 2>/dev/null; then
    echo "错误: 无法连接到数据库"
    exit 1
fi

echo "连接数据库成功，开始执行迁移..."

# 执行SQL迁移
mysql -h localhost -u "$DB_USER" -p"$DB_PASS" "$DB_NAME" << 'EOF'
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
DESCRIBE cards;
EOF

if [ $? -eq 0 ]; then
    echo "✅ 数据库迁移成功完成！"
    echo "现在 cards 表的 image_id 字段可以为空，AI生成的卡片可以正常创建。"
else
    echo "❌ 数据库迁移失败，请检查错误信息。"
    exit 1
fi 