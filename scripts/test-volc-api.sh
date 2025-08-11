#!/bin/bash

echo "Testing Volc API directly..."

# 设置环境变量
export VOLC_API_KEY="f434efdd-6553-42fc-8c14-c1322475845f"
export VOLC_BASE_URL="https://ark.cn-beijing.volces.com/api/v3"
export VOLC_MODEL="gpt-3.5-turbo"

# 创建测试请求 - 非流式
cat > /tmp/volc_test.json << EOF
{
  "model": "gpt-3.5-turbo",
  "messages": [
    {"role": "user", "content": "请用一句话介绍火山引擎"}
  ],
  "max_tokens": 256,
  "temperature": 0.5
}
EOF

echo "Sending request to Volc API (non-streaming)..."
echo "Request body:"
cat /tmp/volc_test.json

echo -e "\nResponse:"
curl -s -X POST \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $VOLC_API_KEY" \
  -d @/tmp/volc_test.json \
  "$VOLC_BASE_URL/chat/completions"

echo -e "\n\nTest completed!"
rm -f /tmp/volc_test.json
