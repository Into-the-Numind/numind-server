#!/bin/bash

# ============================================================
#  莫小派 - 本地一键启动（前端 + 后端）
#
#  用法：  ./scripts/run_local_all.sh          启动全部（前端 V3 + 后端 + 辅助服务）
#          ./scripts/run_local_all.sh backend   只启动后端
#          ./scripts/run_local_all.sh frontend  只启动前端 V3
#
#  后端使用 air 热重载：改了 Go 代码保存后几秒自动生效，无需手动重启
#  前端使用 Vite：改了 Vue 代码保存后浏览器自动刷新
#
#  按 Ctrl+C 停止所有服务
# ============================================================

set -e

# --- 路径配置 ---
# 脚本在 numind-server/scripts/ 下
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SERVER_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
PROJECT_ROOT="$(cd "$SERVER_DIR/.." && pwd)"
WEB_V3_DIR="$PROJECT_ROOT/numind-web-v3"
AIR_BIN="$HOME/go/bin/air"

# --- 颜色输出 ---
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
RED='\033[0;31m'
NC='\033[0m'

# --- 记录所有子进程 PID ---
PIDS=()

cleanup() {
    echo ""
    echo -e "${YELLOW}正在停止所有服务...${NC}"
    for pid in "${PIDS[@]}"; do
        kill "$pid" 2>/dev/null
    done
    # 确保 air 的子进程也被清理
    pkill -f "air.*config_local" 2>/dev/null || true
    echo -e "${GREEN}已全部停止。${NC}"
}
trap cleanup EXIT INT TERM

# --- 启动后端 ---
start_backend() {
    echo -e "${CYAN}========================================${NC}"
    echo -e "${CYAN}  启动后端服务${NC}"
    echo -e "${CYAN}========================================${NC}"

    cd "$SERVER_DIR"

    # 环境变量（从原 run_local_api.sh 搬过来）
    mkdir -p ./data/image
    export NUMIND_DB_MAX_CONNECTION_LIFE_TIME="30s"
    export PATH="/opt/homebrew/Caskroom/miniconda/base/bin:$PATH"
    export WECOM_CORP_ID="wwb71317627b6b70d8"
    export WECOM_SECRET="7-55a-RDZgDzC5jhH4YjxF6zwtFRO0Mwj5_6TxQGUtc"
    export WECOM_POLLER_ENABLED="true"
    export MYSQL_DSN="root:Numind2025@tcp(49.233.219.254:13306)/numind-dev?charset=utf8mb4&parseTime=True&loc=Local"

    # 1) 企微消息轮询（后台）
    echo -e "${GREEN}[1/3] 启动 wecom-agent ...${NC}"
    go run cmd/wecom-agent/main.go &
    PIDS+=($!)

    # 2) 语义切分服务（后台）
    echo -e "${GREEN}[2/3] 启动 semantic-server（模型加载中，请等待几秒）...${NC}"
    python3 scripts/semantic_server.py > semantic_server.log 2>&1 &
    PIDS+=($!)
    sleep 5

    # 3) 主 API —— 用 air 热重载
    echo -e "${GREEN}[3/3] 启动 numind-server（air 热重载模式）...${NC}"
    echo -e "${YELLOW}    改了 Go 代码保存后会自动重新编译，几秒生效${NC}"

    if [ -x "$AIR_BIN" ]; then
        $AIR_BIN -- --config config_local.yaml &
        PIDS+=($!)
    else
        echo -e "${RED}未找到 air，回退到普通模式（无热重载）${NC}"
        echo -e "${RED}安装 air：go install github.com/air-verse/air@latest${NC}"
        go run cmd/numind/main.go --config config_local.yaml &
        PIDS+=($!)
    fi

    echo ""
    echo -e "${GREEN}后端已启动：http://localhost:9091${NC}"
}

# --- 启动前端 V3 ---
start_frontend() {
    echo -e "${CYAN}========================================${NC}"
    echo -e "${CYAN}  启动前端 V3${NC}"
    echo -e "${CYAN}========================================${NC}"

    cd "$WEB_V3_DIR"

    # 检查 node_modules
    if [ ! -d "node_modules" ]; then
        echo -e "${YELLOW}首次运行，安装依赖中...${NC}"
        npm install
    fi

    echo -e "${GREEN}启动 Vite 开发服务器...${NC}"
    echo -e "${YELLOW}    改了 Vue 代码保存后浏览器自动刷新${NC}"
    npm run dev &
    PIDS+=($!)

    echo ""
    echo -e "${GREEN}前端已启动：http://localhost:5173${NC}"
}

# --- 主逻辑 ---
MODE="${1:-all}"

case "$MODE" in
    backend)
        start_backend
        ;;
    frontend)
        start_frontend
        ;;
    all|*)
        start_backend
        echo ""
        start_frontend
        ;;
esac

echo ""
echo -e "${CYAN}========================================${NC}"
echo -e "${CYAN}  全部就绪！${NC}"
echo -e "${CYAN}========================================${NC}"
echo ""
echo -e "  ${GREEN}前端访问地址（复制到浏览器打开）：${NC}"
echo ""
echo "  http://localhost:5173"
echo ""
echo -e "  后端 API：${GREEN}http://localhost:9091${NC}"
echo -e "  旧版前端：直接浏览器打开 numind-web/ 下的 HTML 文件"
echo ""
echo -e "  ${YELLOW}改了代码只需保存，前后端都会自动刷新${NC}"
echo -e "  ${YELLOW}按 Ctrl+C 停止所有服务${NC}"
echo ""

# 保持脚本运行，等待 Ctrl+C
wait
