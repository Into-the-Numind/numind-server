#!/bin/bash

# 最终的AI对话功能测试脚本

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 配置
BASE_URL="http://localhost:9091"
TOKEN=""

# 打印带颜色的消息
print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

print_error() {
    echo -e "${RED}✗ $1${NC}"
}

print_info() {
    echo -e "${BLUE}ℹ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠ $1${NC}"
}

# 获取微信token
get_wechat_token() {
    print_info "使用微信登录获取token..."
    
    RESPONSE=$(curl -s -X POST "${BASE_URL}/v1/wechat/login" \
        -H "Content-Type: application/json" \
        -d '{"code":"98","phone_code":""}')
    
    TOKEN=$(echo $RESPONSE | jq -r '.data.access_token' 2>/dev/null)
    
    if [ "$TOKEN" != "null" ] && [ -n "$TOKEN" ]; then
        print_success "获取微信token成功"
        return 0
    else
        print_error "获取token失败"
        return 1
    fi
}

# 测试创建会话
test_create_session() {
    print_info "测试创建会话..."
    
    RESPONSE=$(curl -s -X POST "${BASE_URL}/v1/chat/sessions" \
        -H "Authorization: Bearer ${TOKEN}" \
        -H "Content-Type: application/json" \
        -d '{"title":"AI对话测试"}')
    
    SESSION_ID=$(echo $RESPONSE | jq -r '.data.ID' 2>/dev/null)
    
    if [ "$SESSION_ID" != "null" ] && [ -n "$SESSION_ID" ]; then
        print_success "创建会话成功，ID: ${SESSION_ID}"
        return 0
    else
        print_error "创建会话失败"
        return 1
    fi
}

# 测试获取会话列表
test_list_sessions() {
    print_info "测试获取会话列表..."
    
    RESPONSE=$(curl -s -X GET "${BASE_URL}/v1/chat/sessions" \
        -H "Authorization: Bearer ${TOKEN}")
    
    TOTAL=$(echo $RESPONSE | jq -r '.data.total' 2>/dev/null)
    
    if [ "$TOTAL" != "null" ]; then
        print_success "获取会话列表成功，总数: ${TOTAL}"
        return 0
    else
        print_error "获取会话列表失败"
        return 1
    fi
}

# 测试获取会话详情
test_get_session() {
    if [ -z "$SESSION_ID" ]; then
        print_warning "没有可用的会话ID，跳过测试"
        return 1
    fi
    
    print_info "测试获取会话详情..."
    
    RESPONSE=$(curl -s -X GET "${BASE_URL}/v1/chat/sessions/${SESSION_ID}" \
        -H "Authorization: Bearer ${TOKEN}")
    
    TITLE=$(echo $RESPONSE | jq -r '.data.title' 2>/dev/null)
    
    if [ "$TITLE" != "null" ]; then
        print_success "获取会话详情成功，标题: ${TITLE}"
        return 0
    else
        print_error "获取会话详情失败"
        return 1
    fi
}

# 测试更新会话
test_update_session() {
    if [ -z "$SESSION_ID" ]; then
        print_warning "没有可用的会话ID，跳过测试"
        return 1
    fi
    
    print_info "测试更新会话..."
    
    RESPONSE=$(curl -s -X PUT "${BASE_URL}/v1/chat/sessions/${SESSION_ID}" \
        -H "Authorization: Bearer ${TOKEN}" \
        -H "Content-Type: application/json" \
        -d '{"title":"更新后的AI对话测试"}')
    
    MESSAGE=$(echo $RESPONSE | jq -r '.data.message' 2>/dev/null)
    
    if [ "$MESSAGE" != "null" ]; then
        print_success "更新会话成功: ${MESSAGE}"
        return 0
    else
        print_error "更新会话失败"
        return 1
    fi
}

# 测试获取会话消息
test_list_messages() {
    if [ -z "$SESSION_ID" ]; then
        print_warning "没有可用的会话ID，跳过测试"
        return 1
    fi
    
    print_info "测试获取会话消息..."
    
    RESPONSE=$(curl -s -X GET "${BASE_URL}/v1/chat/sessions/${SESSION_ID}/messages" \
        -H "Authorization: Bearer ${TOKEN}")
    
    TOTAL=$(echo $RESPONSE | jq -r '.data.total' 2>/dev/null)
    
    if [ "$TOTAL" != "null" ]; then
        print_success "获取会话消息成功，总数: ${TOTAL}"
        return 0
    else
        print_error "获取会话消息失败"
        return 1
    fi
}

# 测试WebSocket连接
test_websocket() {
    print_info "测试WebSocket连接..."
    
    if ! command -v websocat &> /dev/null; then
        print_warning "websocat未安装，跳过WebSocket测试"
        return 1
    fi
    
    print_info "发送测试消息到WebSocket..."
    
    # 发送消息并等待响应
    RESPONSE=$(echo '{"type":"message","content":"你好，AI助手","timestamp":"2024-01-01T12:00:00Z"}' | \
    websocat ws://localhost:9091/v1/chat/ws -H "Authorization: Bearer ${TOKEN}" --timeout 5 2>/dev/null)
    
    if [ -n "$RESPONSE" ]; then
        print_success "WebSocket连接测试成功"
        echo "AI回复: $RESPONSE"
        return 0
    else
        print_error "WebSocket连接测试失败"
        return 1
    fi
}

# 测试删除会话
test_delete_session() {
    if [ -z "$SESSION_ID" ]; then
        print_warning "没有可用的会话ID，跳过测试"
        return 1
    fi
    
    print_info "测试删除会话..."
    
    RESPONSE=$(curl -s -X DELETE "${BASE_URL}/v1/chat/sessions/${SESSION_ID}" \
        -H "Authorization: Bearer ${TOKEN}")
    
    MESSAGE=$(echo $RESPONSE | jq -r '.data.message' 2>/dev/null)
    
    if [ "$MESSAGE" != "null" ]; then
        print_success "删除会话成功: ${MESSAGE}"
        return 0
    else
        print_error "删除会话失败"
        return 1
    fi
}

# 显示API文档
show_api_docs() {
    print_info "AI对话功能API文档:"
    echo -e "${BLUE}WebSocket连接:${NC} ws://localhost:9091/v1/chat/ws"
    echo -e "${BLUE}创建会话:${NC} POST ${BASE_URL}/v1/chat/sessions"
    echo -e "${BLUE}获取会话列表:${NC} GET ${BASE_URL}/v1/chat/sessions"
    echo -e "${BLUE}获取会话详情:${NC} GET ${BASE_URL}/v1/chat/sessions/{id}"
    echo -e "${BLUE}更新会话:${NC} PUT ${BASE_URL}/v1/chat/sessions/{id}"
    echo -e "${BLUE}删除会话:${NC} DELETE ${BASE_URL}/v1/chat/sessions/{id}"
    echo -e "${BLUE}获取会话消息:${NC} GET ${BASE_URL}/v1/chat/sessions/{id}/messages"
    echo -e "${BLUE}获取会话及消息:${NC} GET ${BASE_URL}/v1/chat/sessions/{id}/with-messages"
}

# 主函数
main() {
    echo -e "${GREEN}=== AI对话功能完整测试 ===${NC}"
    
    # 检查jq是否安装
    if ! command -v jq &> /dev/null; then
        print_error "jq未安装，请先安装jq"
        print_info "可以使用以下命令安装: brew install jq"
        exit 1
    fi
    
    # 检查服务器是否运行
    if ! curl -s "${BASE_URL}/healthz" > /dev/null 2>&1; then
        print_error "服务器未运行，请先启动服务器"
        print_info "启动命令: go run cmd/numind/main.go --config=config_local.yaml"
        exit 1
    fi
    
    print_success "服务器运行正常"
    
    # 获取token
    if ! get_wechat_token; then
        print_error "无法获取有效token，测试终止"
        exit 1
    fi
    
    # 运行测试
    test_create_session
    test_list_sessions
    test_get_session
    test_update_session
    test_list_messages
    test_websocket
    test_delete_session
    
    echo -e "\n${GREEN}=== AI对话功能测试完成！ ===${NC}"
    
    # 显示API文档
    echo -e "\n"
    show_api_docs
}

# 运行主函数
main 