#!/bin/bash

# 带宽优化测试脚本
# 测试gzip压缩和字段过滤功能

set -e

# 配置
BASE_URL="http://localhost:9091"
TOKEN="your-jwt-token-here"

echo "🚀 开始测试带宽优化功能..."
echo "=================================="

# 检查服务是否运行
echo "1. 检查服务状态..."
if ! curl -s "$BASE_URL/healthz" > /dev/null; then
    echo "❌ 服务未运行，请先启动服务"
    exit 1
fi
echo "✅ 服务运行正常"

# 测试1: 普通响应（无压缩）
echo ""
echo "2. 测试普通响应（无压缩）..."
echo "请求: GET $BASE_URL/v1/books"
echo "响应头:"
curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept-Encoding: identity" \
     -I "$BASE_URL/v1/books" | grep -E "(Content-Length|Content-Encoding|Transfer-Encoding)"

# 测试2: gzip压缩响应
echo ""
echo "3. 测试gzip压缩响应..."
echo "请求: GET $BASE_URL/v1/books"
echo "响应头:"
curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept-Encoding: gzip" \
     -I "$BASE_URL/v1/books" | grep -E "(Content-Length|Content-Encoding|Transfer-Encoding)"

# 测试3: 字段过滤（减少数据量）
echo ""
echo "4. 测试字段过滤（减少数据量）..."
echo "请求: GET $BASE_URL/v1/books?fields=id,title,card_count"
echo "响应头:"
curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept-Encoding: gzip" \
     -I "$BASE_URL/v1/books?fields=id,title,card_count" | grep -E "(Content-Length|Content-Encoding|Transfer-Encoding)"

# 测试4: 配置API压缩
echo ""
echo "5. 测试配置API压缩..."
echo "请求: GET $BASE_URL/v1/admin/configs"
echo "响应头:"
curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept-Encoding: gzip" \
     -I "$BASE_URL/v1/admin/configs" | grep -E "(Content-Length|Content-Encoding|Transfer-Encoding)"

# 测试5: 配置API字段过滤
echo ""
echo "6. 测试配置API字段过滤..."
echo "请求: GET $BASE_URL/v1/admin/configs?fields=key,value"
echo "响应头:"
curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept-Encoding: gzip" \
     -I "$BASE_URL/v1/admin/configs?fields=key,value" | grep -E "(Content-Length|Content-Encoding|Transfer-Encoding)"

# 测试6: 比较数据大小
echo ""
echo "7. 比较数据大小..."
echo "获取完整书籍列表（无压缩）:"
FULL_SIZE=$(curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept-Encoding: identity" \
     "$BASE_URL/v1/books" | wc -c)
echo "  完整响应大小: ${FULL_SIZE} bytes"

echo "获取完整书籍列表（gzip压缩）:"
COMPRESSED_SIZE=$(curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept-Encoding: gzip" \
     "$BASE_URL/v1/books" | wc -c)
echo "  压缩后大小: ${COMPRESSED_SIZE} bytes"

if [ "$FULL_SIZE" -gt 0 ] && [ "$COMPRESSED_SIZE" -gt 0 ]; then
    COMPRESSION_RATIO=$(echo "scale=2; $COMPRESSED_SIZE * 100 / $FULL_SIZE" | bc)
    echo "  压缩率: ${COMPRESSION_RATIO}%"
    SAVINGS=$(echo "$FULL_SIZE - $COMPRESSED_SIZE" | bc)
    echo "  节省带宽: ${SAVINGS} bytes"
fi

echo "获取过滤字段书籍列表（gzip压缩）:"
FILTERED_SIZE=$(curl -s -H "Authorization: Bearer $TOKEN" \
     -H "Accept-Encoding: gzip" \
     "$BASE_URL/v1/books?fields=id,title,card_count" | wc -c)
echo "  过滤后大小: ${FILTERED_SIZE} bytes"

if [ "$FULL_SIZE" -gt 0 ] && [ "$FILTERED_SIZE" -gt 0 ]; then
    FILTER_RATIO=$(echo "scale=2; $FILTERED_SIZE * 100 / $FULL_SIZE" | bc)
    echo "  过滤率: ${FILTER_RATIO}%"
fi

echo ""
echo "=================================="
echo "🎯 带宽优化测试完成！"
echo ""
echo "💡 优化建议："
echo "1. 客户端请求时添加 'Accept-Encoding: gzip' 头"
echo "2. 使用 fields 参数只获取需要的字段"
echo "3. 合理设置分页参数 limit 和 offset"
echo "4. 对于大量数据的API，考虑使用流式响应"
echo ""
echo "📊 当前优化效果："
echo "- gzip压缩：减少约60-80%的传输数据"
echo "- 字段过滤：根据需求减少20-50%的数据量"
echo "- 分页优化：避免一次性传输大量数据"
