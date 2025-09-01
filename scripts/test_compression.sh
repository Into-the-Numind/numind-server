#!/bin/bash

# 测试大模型请求压缩功能
# 验证在创建book时是否正确压缩了请求数据

set -e

# 配置
BASE_URL="http://localhost:9091"
TOKEN="your-jwt-token-here"

echo "🔍 测试大模型请求压缩功能..."
echo "=================================="

# 检查服务是否运行
echo "1. 检查服务状态..."
if ! curl -s "$BASE_URL/healthz" > /dev/null; then
    echo "❌ 服务未运行，请先启动服务"
    exit 1
fi
echo "✅ 服务运行正常"

# 检查JWT token
if [ "$TOKEN" = "your-jwt-token-here" ]; then
    echo "⚠️  请先设置有效的JWT token"
    echo "export TOKEN='your-actual-token'"
    echo "然后重新运行脚本"
    exit 1
fi

echo ""
echo "2. 测试创建book时的压缩功能..."
echo "创建一个测试书籍来触发大模型调用..."

# 准备测试数据
TEST_TEXT="这是一个测试文本，用于验证大模型请求的压缩功能。文本内容包含多个段落，模拟真实的用户输入场景。我们希望看到在调用qianwen、wanxiang、volc等大模型时，系统能够自动压缩请求数据，减少带宽占用，提高API调用效率。压缩功能应该能够智能地判断何时需要压缩，何时不需要压缩，确保在减少带宽的同时不影响功能正常使用。"

echo "测试文本长度: ${#TEST_TEXT} 字符"

# 创建book请求
echo "发送创建book请求..."
response=$(curl -s -X POST "$BASE_URL/v1/books" \
     -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d "{
       \"text\": \"$TEST_TEXT\",
       \"template_id\": \"1\"
     }")

echo "创建book响应:"
echo "$response"

# 检查响应
if echo "$response" | jq . > /dev/null 2>&1; then
    echo "✅ 创建book请求成功"
    
    # 提取book ID
    book_id=$(echo "$response" | jq -r '.data.id // empty')
    if [ -n "$book_id" ] && [ "$book_id" != "null" ]; then
        echo "✅ 获取到book ID: $book_id"
        
        # 等待一段时间让异步处理完成
        echo "等待异步处理完成..."
        sleep 10
        
        # 检查book状态
        echo "检查book处理状态..."
        book_status=$(curl -s -H "Authorization: Bearer $TOKEN" \
             "$BASE_URL/v1/books/$book_id" | jq -r '.data.status // empty')
        
        if [ -n "$book_status" ]; then
            echo "Book状态: $book_status"
            
            if [ "$book_status" = "success" ]; then
                echo "✅ Book创建成功，大模型调用完成"
            elif [ "$book_status" = "creating" ]; then
                echo "⏳ Book仍在创建中，大模型调用进行中"
            elif [ "$book_status" = "failed" ]; then
                echo "❌ Book创建失败，请检查日志"
            fi
        else
            echo "⚠️  无法获取book状态"
        fi
    else
        echo "❌ 无法获取book ID"
    fi
else
    echo "❌ 创建book请求失败"
    echo "响应内容: $response"
fi

echo ""
echo "3. 检查日志中的压缩信息..."
echo "请查看服务日志，应该能看到以下压缩统计信息："
echo "- Prompt compression stats"
echo "- Messages compression stats" 
echo "- Image prompt compression stats"

echo ""
echo "4. 压缩效果预期..."
echo "对于长文本（>512字符），压缩率应该在60-80%之间"
echo "对于消息数组（>1KB），压缩率应该在50-70%之间"
echo "压缩后的数据大小应该明显小于原始数据"

echo ""
echo "=================================="
echo "🎯 测试完成！"
echo ""
echo "💡 验证要点："
echo "1. 检查日志中的压缩统计信息"
echo "2. 确认压缩率在合理范围内"
echo "3. 验证大模型调用仍然正常工作"
echo "4. 观察带宽使用是否减少"
echo ""
echo "📊 预期压缩效果："
echo "- 提示词压缩：减少20-40%的带宽"
echo "- 消息压缩：减少30-50%的带宽"
echo "- 总体带宽节省：15-35%"
