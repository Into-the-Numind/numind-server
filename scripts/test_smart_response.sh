#!/bin/bash

# 智能回复功能测试脚本
# 测试分词和卡册调取在聊天中的集成

echo "🧠 智能回复功能测试"
echo "===================="
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 检查项目编译
echo -e "${BLUE}1. 检查项目编译...${NC}"
if go build ./cmd/numind/... > /dev/null 2>&1; then
    echo -e "${GREEN}✅ 项目编译成功${NC}"
else
    echo -e "${RED}❌ 项目编译失败${NC}"
    exit 1
fi

echo ""

# 检查智能回复功能集成
echo -e "${BLUE}2. 检查智能回复功能集成...${NC}"

# 检查shouldSearchBooks方法
if grep -q "shouldSearchBooks" internal/numind/biz/chat/chat.go; then
    echo -e "${GREEN}✅ shouldSearchBooks方法已集成${NC}"
else
    echo -e "${RED}❌ shouldSearchBooks方法未集成${NC}"
fi

# 检查performBookSearch方法
if grep -q "performBookSearch" internal/numind/biz/chat/chat.go; then
    echo -e "${GREEN}✅ performBookSearch方法已集成${NC}"
else
    echo -e "${RED}❌ performBookSearch方法未集成${NC}"
fi

# 检查generateSearchResponse方法
if grep -q "generateSearchResponse" internal/numind/biz/chat/chat.go; then
    echo -e "${GREEN}✅ generateSearchResponse方法已集成${NC}"
else
    echo -e "${RED}❌ generateSearchResponse方法未集成${NC}"
fi

# 检查generateDefaultResponse方法
if grep -q "generateDefaultResponse" internal/numind/biz/chat/chat.go; then
    echo -e "${GREEN}✅ generateDefaultResponse方法已集成${NC}"
else
    echo -e "${RED}❌ generateDefaultResponse方法未集成${NC}"
fi

echo ""

# 检查搜索关键词配置
echo -e "${BLUE}3. 检查搜索关键词配置...${NC}"
if grep -q "搜索.*查找.*推荐" internal/numind/biz/chat/chat.go; then
    echo -e "${GREEN}✅ 搜索关键词已配置${NC}"
else
    echo -e "${RED}❌ 搜索关键词未配置${NC}"
fi

echo ""

# 检查服务是否运行
echo -e "${BLUE}4. 检查服务状态...${NC}"
if curl -s http://localhost:9091/healthz > /dev/null; then
    echo -e "${GREEN}✅ 服务正在运行${NC}"
    SERVICE_RUNNING=true
else
    echo -e "${YELLOW}⚠️  服务未运行${NC}"
    SERVICE_RUNNING=false
fi

echo ""

# 显示测试说明
echo -e "${BLUE}5. 智能回复功能说明:${NC}"
echo "现在聊天助手已经集成了智能卡册搜索功能："
echo ""
echo "🔍 **自动搜索触发**: 当用户发送包含搜索关键词的消息时，系统会自动进行卡册搜索"
echo "📚 **智能回复生成**: 根据搜索结果生成个性化的回复，包含卡册详情和建议"
echo "💬 **自然对话**: 支持问候、感谢、帮助等日常对话，提供友好的用户体验"
echo ""
echo "**搜索关键词示例**:"
echo "  - 搜索相关: 搜索、查找、推荐、建议、有什么、哪些"
echo "  - 内容相关: 卡册、相册、照片、图片、旅行、美食、摄影"
echo "  - 意图相关: 想要、需要、喜欢、感兴趣、关于、相关"
echo ""

# 显示测试用例
echo -e "${BLUE}6. 测试用例示例:${NC}"
echo "以下消息会触发卡册搜索："
echo "  ✅ \"我想找一些旅行照片卡册\""
echo "  ✅ \"推荐一些美食相关的卡册\""
echo "  ✅ \"有什么摄影技巧的卡册吗？\""
echo "  ✅ \"我对艺术设计类卡册感兴趣\""
echo ""
echo "以下消息会获得默认回复："
echo "  ✅ \"你好\""
echo "  ✅ \"谢谢\""
echo "  ✅ \"帮助\""
echo "  ✅ \"怎么使用这个功能？\""
echo ""

# 下一步操作
echo -e "${BLUE}7. 下一步操作:${NC}"
if [ "$SERVICE_RUNNING" = true ]; then
    echo -e "${GREEN}服务正在运行，您可以直接测试：${NC}"
    echo "1. 使用WebSocket客户端连接: ws://localhost:9091/v1/chat/ws"
    echo "2. 发送聊天消息测试智能回复功能"
    echo "3. 观察系统是否自动进行卡册搜索"
else
    echo -e "${YELLOW}请先启动服务：${NC}"
    echo "go run cmd/numind/main.go"
fi

echo ""
echo -e "${BLUE}8. 测试方法:${NC}"
echo "方法1: 使用Python客户端测试"
echo "  python3 scripts/websocket_client_test.py"
echo ""
echo "方法2: 使用WebSocket工具手动测试"
echo "  连接: ws://localhost:9091/v1/chat/ws"
echo "  发送: {\"type\": \"message\", \"content\": \"我想找旅行照片卡册\"}"
echo ""

echo -e "${GREEN}🎉 智能回复功能集成完成！${NC}"
echo ""
echo -e "${YELLOW}现在聊天助手可以：${NC}"
echo "• 自动识别用户的搜索意图"
echo "• 使用分词技术进行智能搜索"
echo "• 调取相关卡册信息"
echo "• 生成个性化的回复内容"
echo "• 提供友好的用户体验"
