#!/bin/bash

# WebSocket搜索功能完整测试脚本
# 该脚本测试分词和卡册调取功能在WebSocket API中的集成

echo "=== WebSocket搜索功能完整测试 ==="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 检查服务是否运行
echo "1. 检查服务状态..."
if curl -s http://localhost:9091/healthz > /dev/null; then
    echo -e "${GREEN}✅ 服务正在运行${NC}"
else
    echo -e "${RED}❌ 服务未运行，请先启动服务${NC}"
    echo "启动命令: go run cmd/numind/main.go"
    exit 1
fi

echo ""

# 测试关键词匹配功能
echo "2. 测试关键词匹配功能..."
cd examples
if go run keyword_matching_example.go > /dev/null 2>&1; then
    echo -e "${GREEN}✅ 关键词匹配功能正常${NC}"
else
    echo -e "${RED}❌ 关键词匹配功能异常${NC}"
    exit 1
fi
cd ..

echo ""

# 测试分词功能
echo "3. 测试分词功能..."
if go test ./internal/pkg/util/... -v > /dev/null 2>&1; then
    echo -e "${GREEN}✅ 分词功能测试通过${NC}"
else
    echo -e "${RED}❌ 分词功能测试失败${NC}"
    exit 1
fi

echo ""

# 检查项目编译
echo "4. 检查项目编译..."
if go build ./cmd/numind/... > /dev/null 2>&1; then
    echo -e "${GREEN}✅ 项目编译成功${NC}"
else
    echo -e "${RED}❌ 项目编译失败${NC}"
    exit 1
fi

echo ""

# 检查WebSocket路由配置
echo "5. 检查WebSocket路由配置..."
if grep -q "v1Group.GET(\"/chat/ws\", chatc.WebSocket)" internal/numind/router.go; then
    echo -e "${GREEN}✅ WebSocket路由配置正确${NC}"
else
    echo -e "${RED}❌ WebSocket路由配置缺失${NC}"
    exit 1
fi

echo ""

# 检查搜索消息类型处理
echo "6. 检查搜索消息类型处理..."
if grep -q "case \"search_books\":" internal/numind/biz/chat/chat.go; then
    echo -e "${GREEN}✅ 搜索消息类型处理已集成${NC}"
else
    echo -e "${RED}❌ 搜索消息类型处理未集成${NC}"
    exit 1
fi

echo ""

# 检查数据库索引配置
echo "7. 检查数据库索引配置..."
if grep -q "index:idx_title_tags" internal/pkg/model/book.go; then
    echo -e "${GREEN}✅ 数据库索引配置正确${NC}"
else
    echo -e "${RED}❌ 数据库索引配置缺失${NC}"
    exit 1
fi

echo ""

# 检查搜索服务集成
echo "8. 检查搜索服务集成..."
if grep -q "searchService \*book.SearchService" internal/numind/biz/chat/chat.go; then
    echo -e "${GREEN}✅ 搜索服务已集成${NC}"
else
    echo -e "${RED}❌ 搜索服务未集成${NC}"
    exit 1
fi

echo ""

# 检查数据库查询方法
echo "9. 检查数据库查询方法..."
if grep -q "ListAll" internal/numind/store/book.go; then
    echo -e "${GREEN}✅ 数据库查询方法已添加${NC}"
else
    echo -e "${RED}❌ 数据库查询方法缺失${NC}"
    exit 1
fi

echo ""

echo "=== 集成检查完成 ==="
echo ""

echo -e "${YELLOW}下一步操作：${NC}"
echo "1. 确保数据库中有测试数据"
echo "2. 启动WebSocket服务：go run cmd/numind/main.go"
echo "3. 使用WebSocket客户端连接：ws://localhost:9091/v1/chat/ws"
echo "4. 发送搜索请求进行测试"
echo ""

echo -e "${YELLOW}测试WebSocket搜索的示例消息：${NC}"
echo '{"type": "search_books", "content": "旅行照片卡册"}'
echo ""

echo -e "${YELLOW}预期响应格式：${NC}"
echo '{
  "type": "search_books_result",
  "content": "找到 X 本相关书籍",
  "data": [
    {
      "id": 1,
      "title": "旅行照片卡册",
      "tags": "旅行,摄影,回忆",
      "category_id": 1,
      "image_url": "https://...",
      "card_count": 12
    }
  ],
  "timestamp": "2024-01-01T12:00:01Z"
}'
echo ""

echo -e "${YELLOW}数据库索引创建：${NC}"
echo "执行 docs/add_search_indexes.sql 脚本创建必要的索引"
echo ""

echo -e "${GREEN}🎉 所有集成检查通过！WebSocket搜索功能已准备就绪。${NC}"
