#!/bin/bash

# 测试书籍列表API乱码问题修复
# 验证响应是否正常显示

set -e

# 配置
BASE_URL="http://localhost:9091"
TOKEN="your-jwt-token-here"

echo "🔍 测试书籍列表API乱码问题修复..."
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
echo "2. 测试书籍列表API（无压缩）..."
echo "请求: GET $BASE_URL/v1/books"
echo "响应头:"
curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept-Encoding: identity" \
     -I "$BASE_URL/v1/books" | grep -E "(Content-Type|Content-Length)"

echo ""
echo "3. 测试响应内容..."
echo "获取书籍列表数据:"
response=$(curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept-Encoding: identity" \
     "$BASE_URL/v1/books")

# 检查响应是否为有效的JSON
if echo "$response" | jq . > /dev/null 2>&1; then
    echo "✅ 响应是有效的JSON格式"
    echo "响应结构:"
    echo "$response" | jq '. | keys'
    
    # 检查是否有code字段
    if echo "$response" | jq -e '.code' > /dev/null 2>&1; then
        code=$(echo "$response" | jq -r '.code')
        echo "✅ 响应包含code字段: $code"
        
        if [ "$code" = "0" ]; then
            echo "✅ API调用成功"
            
            # 检查data字段
            if echo "$response" | jq -e '.data' > /dev/null 2>&1; then
                echo "✅ 响应包含data字段"
                
                # 检查books数组
                if echo "$response" | jq -e '.data.books' > /dev/null 2>&1; then
                    book_count=$(echo "$response" | jq '.data.books | length')
                    echo "✅ 找到 $book_count 本书籍"
                    
                    # 显示第一本书的基本信息
                    if [ "$book_count" -gt 0 ]; then
                        echo "第一本书信息:"
                        echo "$response" | jq '.data.books[0] | {id, title, card_count, created_at}'
                    fi
                else
                    echo "❌ 响应中缺少books数组"
                fi
            else
                echo "❌ 响应中缺少data字段"
            fi
        else
            echo "❌ API调用失败，code: $code"
            message=$(echo "$response" | jq -r '.message // "未知错误"')
            echo "错误信息: $message"
        fi
    else
        echo "❌ 响应中缺少code字段"
    fi
else
    echo "❌ 响应不是有效的JSON格式"
    echo "原始响应内容:"
    echo "$response"
    
    # 检查是否有乱码字符
    if echo "$response" | grep -q -P '[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]'; then
        echo "⚠️  检测到可能的乱码字符"
    fi
fi

echo ""
echo "4. 测试字段过滤功能..."
echo "请求: GET $BASE_URL/v1/books?fields=id,title,card_count"
filtered_response=$(curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept-Encoding: identity" \
     "$BASE_URL/v1/books?fields=id,title,card_count")

if echo "$filtered_response" | jq . > /dev/null 2>&1; then
    echo "✅ 字段过滤响应是有效的JSON格式"
else
    echo "❌ 字段过滤响应不是有效的JSON格式"
fi

echo ""
echo "=================================="
echo "🎯 测试完成！"
echo ""
echo "💡 如果仍有问题，请检查："
echo "1. 服务是否重启"
echo "2. 数据库连接是否正常"
echo "3. 用户认证是否有效"
echo "4. 日志中是否有错误信息"
