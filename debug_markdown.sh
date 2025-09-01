#!/bin/bash

echo "🔍 开始调试 Markdown 处理流程"

# 1. 编译所有相关模块
echo "🔨 编译 Markdown 相关模块..."
go build ./internal/numind/biz/markdown/ && echo "✅ Markdown 模块编译成功" || echo "❌ Markdown 模块编译失败"

# 2. 测试简单的 curl 请求
echo "🌐 测试简单请求..."
curl -X POST 'http://localhost:9091/v1/books' \
--header 'Content-Type: application/json' \
--header 'Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3NTU3NDY3NjUsIm9wZW5pZCI6IjY2NiIsInVzZXJfaWQiOjJ9.yOH5ULjrWL3o-eUI-l4Vrxf0wK-_X7UsPc-Wq2qYHR0' \
--data-raw '{
    "text": "测试Markdown处理：魅力的本质在于真实的自我接纳。这是一个简单的测试文本。",
    "template_id": 1
}' | jq .

echo "✅ 调试完成，请查看日志输出"
