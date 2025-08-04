#!/bin/bash

# 对话功能测试脚本

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 配置
BASE_URL="http://localhost:8080"
WS_URL="ws://localhost:8080"

# 获取token的函数
get_token() {
    echo -e "${YELLOW}正在获取token...${NC}"
    
    # 这里需要根据实际的登录接口调整
    # 示例：使用微信登录获取token
    TOKEN=$(curl -s -X POST "${BASE_URL}/v1/wechat/login" \
        -H "Content-Type: application/json" \
        -d '{"code":"test_code"}' | jq -r '.data.token')
    
    if [ "$TOKEN" = "null" ] || [ -z "$TOKEN" ]; then
        echo -e "${RED}获取token失败${NC}"
        exit 1
    fi
    
    echo -e "${GREEN}获取token成功: ${TOKEN}${NC}"
}

# 测试创建会话
test_create_session() {
    echo -e "\n${YELLOW}测试创建会话...${NC}"
    
    RESPONSE=$(curl -s -X POST "${BASE_URL}/v1/chat/sessions" \
        -H "Authorization: Bearer ${TOKEN}" \
        -H "Content-Type: application/json" \
        -d '{"title":"测试对话"}')
    
    SESSION_ID=$(echo $RESPONSE | jq -r '.data.id')
    
    if [ "$SESSION_ID" != "null" ] && [ "$SESSION_ID" != "" ]; then
        echo -e "${GREEN}创建会话成功，ID: ${SESSION_ID}${NC}"
    else
        echo -e "${RED}创建会话失败: ${RESPONSE}${NC}"
    fi
}

# 测试获取会话列表
test_list_sessions() {
    echo -e "\n${YELLOW}测试获取会话列表...${NC}"
    
    RESPONSE=$(curl -s -X GET "${BASE_URL}/v1/chat/sessions" \
        -H "Authorization: Bearer ${TOKEN}")
    
    TOTAL=$(echo $RESPONSE | jq -r '.data.total')
    
    if [ "$TOTAL" != "null" ]; then
        echo -e "${GREEN}获取会话列表成功，总数: ${TOTAL}${NC}"
    else
        echo -e "${RED}获取会话列表失败: ${RESPONSE}${NC}"
    fi
}

# 测试获取会话详情
test_get_session() {
    if [ -z "$SESSION_ID" ]; then
        echo -e "${RED}没有可用的会话ID，跳过测试${NC}"
        return
    fi
    
    echo -e "\n${YELLOW}测试获取会话详情...${NC}"
    
    RESPONSE=$(curl -s -X GET "${BASE_URL}/v1/chat/sessions/${SESSION_ID}" \
        -H "Authorization: Bearer ${TOKEN}")
    
    TITLE=$(echo $RESPONSE | jq -r '.data.title')
    
    if [ "$TITLE" != "null" ]; then
        echo -e "${GREEN}获取会话详情成功，标题: ${TITLE}${NC}"
    else
        echo -e "${RED}获取会话详情失败: ${RESPONSE}${NC}"
    fi
}

# 测试更新会话
test_update_session() {
    if [ -z "$SESSION_ID" ]; then
        echo -e "${RED}没有可用的会话ID，跳过测试${NC}"
        return
    fi
    
    echo -e "\n${YELLOW}测试更新会话...${NC}"
    
    RESPONSE=$(curl -s -X PUT "${BASE_URL}/v1/chat/sessions/${SESSION_ID}" \
        -H "Authorization: Bearer ${TOKEN}" \
        -H "Content-Type: application/json" \
        -d '{"title":"更新后的测试对话"}')
    
    MESSAGE=$(echo $RESPONSE | jq -r '.data.message')
    
    if [ "$MESSAGE" != "null" ]; then
        echo -e "${GREEN}更新会话成功: ${MESSAGE}${NC}"
    else
        echo -e "${RED}更新会话失败: ${RESPONSE}${NC}"
    fi
}

# 测试获取会话消息
test_list_messages() {
    if [ -z "$SESSION_ID" ]; then
        echo -e "${RED}没有可用的会话ID，跳过测试${NC}"
        return
    fi
    
    echo -e "\n${YELLOW}测试获取会话消息...${NC}"
    
    RESPONSE=$(curl -s -X GET "${BASE_URL}/v1/chat/sessions/${SESSION_ID}/messages" \
        -H "Authorization: Bearer ${TOKEN}")
    
    TOTAL=$(echo $RESPONSE | jq -r '.data.total')
    
    if [ "$TOTAL" != "null" ]; then
        echo -e "${GREEN}获取会话消息成功，总数: ${TOTAL}${NC}"
    else
        echo -e "${RED}获取会话消息失败: ${RESPONSE}${NC}"
    fi
}

# 测试WebSocket连接
test_websocket() {
    echo -e "\n${YELLOW}测试WebSocket连接...${NC}"
    
    # 检查是否安装了websocat
    if ! command -v websocat &> /dev/null; then
        echo -e "${YELLOW}websocat未安装，跳过WebSocket测试${NC}"
        echo -e "${YELLOW}可以使用以下命令安装: brew install websocat${NC}"
        return
    fi
    
    # 发送测试消息
    echo '{"type":"message","content":"测试消息","timestamp":"2024-01-01T12:00:00Z"}' | \
    websocat -H "Authorization: Bearer ${TOKEN}" "${WS_URL}/v1/chat/ws" &
    
    WS_PID=$!
    sleep 2
    kill $WS_PID 2>/dev/null
    
    echo -e "${GREEN}WebSocket连接测试完成${NC}"
}

# 测试删除会话
test_delete_session() {
    if [ -z "$SESSION_ID" ]; then
        echo -e "${RED}没有可用的会话ID，跳过测试${NC}"
        return
    fi
    
    echo -e "\n${YELLOW}测试删除会话...${NC}"
    
    RESPONSE=$(curl -s -X DELETE "${BASE_URL}/v1/chat/sessions/${SESSION_ID}" \
        -H "Authorization: Bearer ${TOKEN}")
    
    MESSAGE=$(echo $RESPONSE | jq -r '.data.message')
    
    if [ "$MESSAGE" != "null" ]; then
        echo -e "${GREEN}删除会话成功: ${MESSAGE}${NC}"
    else
        echo -e "${RED}删除会话失败: ${RESPONSE}${NC}"
    fi
}

# 主函数
main() {
    echo -e "${GREEN}开始测试对话功能...${NC}"
    
    # 检查jq是否安装
    if ! command -v jq &> /dev/null; then
        echo -e "${RED}错误: jq未安装，请先安装jq${NC}"
        echo -e "${YELLOW}可以使用以下命令安装: brew install jq${NC}"
        exit 1
    fi
    
    # 获取token
    get_token
    
    # 运行测试
    test_create_session
    test_list_sessions
    test_get_session
    test_update_session
    test_list_messages
    test_websocket
    test_delete_session
    
    echo -e "\n${GREEN}对话功能测试完成！${NC}"
}

# 运行主函数
main 