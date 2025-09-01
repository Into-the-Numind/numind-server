#!/bin/bash

# WebSocket搜索功能快速启动脚本
# 该脚本帮助用户快速启动和测试搜索功能

echo "🚀 WebSocket搜索功能快速启动"
echo "================================"
echo ""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 检查Go环境
echo -e "${BLUE}1. 检查Go环境...${NC}"
if command -v go &> /dev/null; then
    echo -e "${GREEN}✅ Go已安装: $(go version)${NC}"
else
    echo -e "${RED}❌ Go未安装，请先安装Go${NC}"
    exit 1
fi

# 检查依赖
echo -e "${BLUE}2. 检查Go依赖...${NC}"
if go mod tidy &> /dev/null; then
    echo -e "${GREEN}✅ Go依赖检查通过${NC}"
else
    echo -e "${RED}❌ Go依赖检查失败${NC}"
    exit 1
fi

# 检查项目编译
echo -e "${BLUE}3. 检查项目编译...${NC}"
if go build ./cmd/numind/... &> /dev/null; then
    echo -e "${GREEN}✅ 项目编译成功${NC}"
else
    echo -e "${RED}❌ 项目编译失败${NC}"
    exit 1
fi

# 检查服务是否已运行
echo -e "${BLUE}4. 检查服务状态...${NC}"
if curl -s http://localhost:9091/healthz &> /dev/null; then
    echo -e "${GREEN}✅ 服务已在运行${NC}"
    SERVICE_RUNNING=true
else
    echo -e "${YELLOW}⚠️  服务未运行${NC}"
    SERVICE_RUNNING=false
fi

echo ""

# 如果服务未运行，询问是否启动
if [ "$SERVICE_RUNNING" = false ]; then
    echo -e "${YELLOW}是否要启动服务？(y/n)${NC}"
    read -r response
    if [[ "$response" =~ ^([yY][eE][sS]|[yY])$ ]]; then
        echo -e "${BLUE}启动服务...${NC}"
        echo "注意：服务将在后台启动，请等待几秒钟..."
        
        # 启动服务
        nohup go run cmd/numind/main.go > logs/numind.log 2>&1 &
        SERVICE_PID=$!
        
        echo -e "${GREEN}服务已启动，PID: $SERVICE_PID${NC}"
        echo "等待服务启动..."
        
        # 等待服务启动
        for i in {1..10}; do
            if curl -s http://localhost:9091/healthz &> /dev/null; then
                echo -e "${GREEN}✅ 服务启动成功！${NC}"
                break
            fi
            echo "等待服务启动... ($i/10)"
            sleep 2
        done
        
        if ! curl -s http://localhost:9091/healthz &> /dev/null; then
            echo -e "${RED}❌ 服务启动失败，请检查日志${NC}"
            echo "日志文件: logs/numind.log"
            exit 1
        fi
    else
        echo -e "${YELLOW}跳过服务启动${NC}"
    fi
fi

echo ""

# 运行集成检查
echo -e "${BLUE}5. 运行集成检查...${NC}"
if ./scripts/test_websocket_search.sh &> /dev/null; then
    echo -e "${GREEN}✅ 集成检查通过${NC}"
else
    echo -e "${RED}❌ 集成检查失败${NC}"
    echo "请查看详细输出: ./scripts/test_websocket_search.sh"
fi

echo ""

# 显示测试选项
echo -e "${BLUE}6. 选择测试方式:${NC}"
echo "1. 使用Python客户端测试 (推荐)"
echo "2. 使用WebSocket工具手动测试"
echo "3. 查看测试文档"
echo "4. 退出"

read -r choice

case $choice in
    1)
        echo -e "${BLUE}启动Python客户端测试...${NC}"
        if command -v python3 &> /dev/null; then
            if python3 -c "import websockets" &> /dev/null; then
                python3 scripts/websocket_client_test.py
            else
                echo -e "${YELLOW}安装Python依赖...${NC}"
                pip3 install websockets
                python3 scripts/websocket_client_test.py
            fi
        else
            echo -e "${RED}Python3未安装，请先安装Python3${NC}"
        fi
        ;;
    2)
        echo -e "${BLUE}手动测试说明:${NC}"
        echo "1. 连接WebSocket: ws://localhost:9091/v1/chat/ws"
        echo "2. 发送搜索请求:"
        echo '   {"type": "search_books", "content": "旅行照片卡册"}'
        echo "3. 查看响应结果"
        ;;
    3)
        echo -e "${BLUE}查看测试文档...${NC}"
        if command -v open &> /dev/null; then
            open docs/websocket_search_testing.md
        elif command -v xdg-open &> /dev/null; then
            xdg-open docs/websocket_search_testing.md
        else
            echo "文档位置: docs/websocket_search_testing.md"
            cat docs/websocket_search_testing.md | head -20
        fi
        ;;
    4)
        echo -e "${GREEN}退出脚本${NC}"
        exit 0
        ;;
    *)
        echo -e "${RED}无效选择${NC}"
        exit 1
        ;;
esac

echo ""
echo -e "${GREEN}🎉 快速启动完成！${NC}"
echo ""
echo -e "${YELLOW}有用的命令:${NC}"
echo "启动服务: go run cmd/numind/main.go"
echo "查看日志: tail -f logs/numind.log"
echo "集成检查: ./scripts/test_websocket_search.sh"
echo "客户端测试: python3 scripts/websocket_client_test.py"
echo ""
echo -e "${BLUE}WebSocket端点: ws://localhost:9091/v1/chat/ws${NC}"
