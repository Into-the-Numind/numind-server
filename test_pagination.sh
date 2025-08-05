#!/bin/bash

# 分页功能测试脚本

BASE_URL="http://localhost:8080/v1"
TOKEN="your_token_here"  # 请替换为实际的token

echo "=== 分页功能测试 ==="

# 1. 获取配置
echo "1. 获取分页配置..."
curl -X GET "${BASE_URL}/pagination/config" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" | jq '.'

echo -e "\n"

# 1.1. 获取样式配置
echo "1.1. 获取样式配置..."
curl -X GET "${BASE_URL}/pagination/style-config" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" | jq '.'

echo -e "\n"

# 2. 测试分页功能
echo "2. 测试分页功能..."
curl -X GET "${BASE_URL}/pagination/test" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" | jq '.'

echo -e "\n"

# 3. 自定义分页测试
echo "3. 自定义分页测试..."
curl -X POST "${BASE_URL}/pagination/paginate" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d '{
    "elements": [
      {
        "type": "title",
        "content": "联机时代的独立思考者：未来竞争力进化论"
      },
      {
        "type": "subtitle",
        "content": "未来职业竞争力的关键要素"
      },
      {
        "type": "body",
        "content": "这个时代需要每个人都成为联机的独立思考者，融合全球智慧与个人洞察力。"
      },
      {
        "type": "body",
        "content": "在人工智能盛行、行业无边界的时代，最具竞争力的人能够：用机器学习处理信息，用大脑整合创新思想，用系统思维解决复杂问题。"
      },
      {
        "type": "list",
        "content": [
          "我今天做的事，机器能做吗？",
          "我今天做的事，会被外包吗？",
          "我今天做的事，明天会做得更好吗？"
        ]
      },
      {
        "type": "subtitle",
        "content": "认知方式的革命性转变"
      },
      {
        "type": "body",
        "content": "读100本书并试图记住，就像非要背下整本电话簿才开始拨号。未来核心认知能力应包含：信息搜索能力、深度思考能力、趋势洞察能力。"
      },
      {
        "type": "quote",
        "content": "人类记住知识的方式持续了两千多年，而近20年内新认知方式突然成为主流——这种变化是不连续的、跳跃式的。"
      }
    ]
  }' | jq '.'

echo -e "\n"

# 4. 从JSON字符串分页测试
echo "4. 从JSON字符串分页测试..."
curl -X POST "${BASE_URL}/pagination/paginate-json" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d 'json=[
    {
      "type": "title",
      "content": "测试标题"
    },
    {
      "type": "body",
      "content": "这是一段测试正文内容，用于验证分页功能是否正常工作。"
    }
  ]' | jq '.'

echo -e "\n=== 测试完成 ===" 