#!/bin/bash

# 测试书籍图片下载功能
echo "测试书籍图片下载功能..."

# 设置API地址
API_URL="http://localhost:9091"

# 获取用户token（需要先登录）
echo "请先确保已经登录并获取到token"
echo "示例: curl -X POST $API_URL/v1/users/login -H 'Content-Type: application/json' -d '{\"username\":\"test\",\"password\":\"test123\"}'"

# 测试创建书籍（需要替换为实际的token）
TOKEN="your_token_here"

echo "创建书籍测试..."
curl -X POST "$API_URL/v1/books" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "text": "请为我生成一个关于春天的卡册，包含标题、正文和图片描述",
    "template_id": "template_001"
  }'

echo ""
echo "测试完成！"
echo "请检查以下路径是否有图片文件："
echo "- ./images/upload/book/{book_id}/"
echo "- 检查数据库中book记录的ImageUrl字段是否更新为本地路径"
