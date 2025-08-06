#!/bin/bash

# 简化的聊天功能测试脚本

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 配置
BASE_URL="http://localhost:9091"
WS_URL="ws://localhost:9091"

echo -e "${GREEN}开始测试聊天功能...${NC}"

# 1. 测试服务器是否运行
echo -e "\n${YELLOW}1. 测试服务器连接...${NC}"
if curl -s "${BASE_URL}/health" > /dev/null 2>&1; then
    echo -e "${GREEN}✓ 服务器运行正常${NC}"
else
    echo -e "${RED}✗ 服务器未运行，请先启动服务器${NC}"
    echo -e "${YELLOW}启动命令: go run cmd/numind/main.go --config=config_local.yaml${NC}"
    exit 1
fi

# 2. 测试WebSocket连接
echo -e "\n${YELLOW}2. 测试WebSocket连接...${NC}"
if command -v websocat &> /dev/null; then
    echo -e "${GREEN}✓ websocat已安装${NC}"
    echo -e "${YELLOW}可以使用以下命令测试WebSocket:${NC}"
    echo -e "${YELLOW}websocat ws://localhost:9091/v1/chat/ws${NC}"
else
    echo -e "${YELLOW}websocat未安装，可以使用以下命令安装:${NC}"
    echo -e "${YELLOW}brew install websocat${NC}"
fi

# 3. 显示API端点信息
echo -e "\n${YELLOW}3. 聊天功能API端点:${NC}"
echo -e "${GREEN}WebSocket连接:${NC} ${WS_URL}/v1/chat/ws"
echo -e "${GREEN}创建会话:${NC} POST ${BASE_URL}/v1/chat/sessions"
echo -e "${GREEN}获取会话列表:${NC} GET ${BASE_URL}/v1/chat/sessions"
echo -e "${GREEN}获取会话详情:${NC} GET ${BASE_URL}/v1/chat/sessions/{id}"
echo -e "${GREEN}更新会话:${NC} PUT ${BASE_URL}/v1/chat/sessions/{id}"
echo -e "${GREEN}删除会话:${NC} DELETE ${BASE_URL}/v1/chat/sessions/{id}"
echo -e "${GREEN}获取会话消息:${NC} GET ${BASE_URL}/v1/chat/sessions/{id}/messages"

# 4. 显示测试示例
echo -e "\n${YELLOW}4. 测试示例:${NC}"
echo -e "${GREEN}创建会话示例:${NC}"
echo 'curl -X POST "http://localhost:9091/v1/chat/sessions" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Content-Type: application/json" \
  -d '"'"'{"title":"测试对话"}'"'"

echo -e "\n${GREEN}WebSocket消息示例:${NC}"
echo '{"type":"message","content":"你好，AI助手","timestamp":"2024-01-01T12:00:00Z"}'

echo -e "\n${GREEN}测试完成！${NC}" 