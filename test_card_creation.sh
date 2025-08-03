#!/bin/bash

# 测试卡册创建接口
echo "测试卡册创建接口..."

# 设置测试参数
TEXT="velit"
TEMPLATE_ID="92"

# 发送POST请求到卡册创建接口
curl -X POST "http://localhost:8080/v1/books" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer YOUR_JWT_TOKEN_HERE" \
  -d "{
    \"text\": \"$TEXT\",
    \"template_id\": \"$TEMPLATE_ID\"
  }" \
  -v

echo ""
echo "测试完成" 