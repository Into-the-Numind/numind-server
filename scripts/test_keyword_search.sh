#!/bin/bash

# 关键词搜索功能测试脚本
# 该脚本测试WebSocket搜索功能是否正常工作

echo "=== 关键词搜索功能测试 ==="
echo ""

# 检查服务是否运行
echo "1. 检查服务状态..."
if curl -s http://localhost:9091/healthz > /dev/null; then
    echo "✅ 服务正在运行"
else
    echo "❌ 服务未运行，请先启动服务"
    echo "启动命令: go run cmd/numind/main.go"
    exit 1
fi

echo ""

# 测试关键词匹配功能
echo "2. 测试关键词匹配功能..."
cd examples
if go run keyword_matching_example.go > /dev/null 2>&1; then
    echo "✅ 关键词匹配功能正常"
else
    echo "❌ 关键词匹配功能异常"
    exit 1
fi
cd ..

echo ""

# 测试分词功能
echo "3. 测试分词功能..."
if go test ./internal/pkg/util/... -v > /dev/null 2>&1; then
    echo "✅ 分词功能测试通过"
else
    echo "❌ 分词功能测试失败"
    exit 1
fi

echo ""

# 检查数据库索引
echo "4. 检查数据库索引..."
echo "请确保在数据库中创建了以下索引："
echo "  - idx_title_tags: Title和Tags字段的复合索引"
echo "  - idx_status: Status字段索引"
echo ""
echo "SQL语句："
echo "  CREATE INDEX idx_title_tags ON book(title, tags);"
echo "  CREATE INDEX idx_status ON book(status);"

echo ""

echo "=== 测试完成 ==="
echo ""
echo "下一步："
echo "1. 启动WebSocket服务"
echo "2. 使用WebSocket客户端发送搜索请求"
echo "3. 验证搜索结果"
echo ""
echo "示例WebSocket消息："
echo '{"type": "search_books", "content": "旅行照片卡册"}'
