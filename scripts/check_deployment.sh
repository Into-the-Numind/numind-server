#!/bin/bash
# ============================================
# Numind Server 部署检查脚本
# 用于在服务器上检查部署状态和模型下载情况
# ============================================

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

CONTAINER_NAME=""
ENV=""

# 显示帮助
show_help() {
    echo "用法: $0 [选项]"
    echo ""
    echo "选项:"
    echo "  -e, --env <dev|qa|prod>   指定环境 (默认: dev)"
    echo "  -c, --container <name>    指定容器名"
    echo "  -h, --help               显示帮助"
    echo ""
    echo "示例:"
    echo "  $0 -e dev"
    echo "  $0 -c numind-server-prod"
}

# 解析参数
while [[ $# -gt 0 ]]; do
    case $1 in
        -e|--env)
            ENV="$2"
            shift 2
            ;;
        -c|--container)
            CONTAINER_NAME="$2"
            shift 2
            ;;
        -h|--help)
            show_help
            exit 0
            ;;
        *)
            echo "未知选项: $1"
            show_help
            exit 1
            ;;
    esac
done

# 设置默认值
if [ -z "$ENV" ]; then
    ENV="dev"
fi

if [ -z "$CONTAINER_NAME" ]; then
    case $ENV in
        dev)
            CONTAINER_NAME="numind-server-dev"
            ;;
        qa)
            CONTAINER_NAME="numind-server-qa"
            ;;
        prod)
            CONTAINER_NAME="numind-server-prod"
            ;;
        *)
            CONTAINER_NAME="numind-server-$ENV"
            ;;
    esac
fi

echo ""
echo "╔════════════════════════════════════════════════════════╗"
echo "║         🔍 Numind Server 部署检查                      ║"
echo "╚════════════════════════════════════════════════════════╝"
echo ""
echo "环境: $ENV"
echo "容器: $CONTAINER_NAME"
echo ""

# 检查容器是否运行
check_container() {
    echo -e "${BLUE}[检查 1/5]${NC} 容器运行状态..."
    
    if ! docker ps -q -f name="$CONTAINER_NAME" | grep -q .; then
        echo -e "${RED}❌ 容器未运行: $CONTAINER_NAME${NC}"
        echo ""
        echo "查看所有容器:"
        docker ps -a | grep numind || echo "没有 numind 相关容器"
        exit 1
    fi
    
    echo -e "${GREEN}✅ 容器运行正常${NC}"
    docker ps -f name="$CONTAINER_NAME" --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
    echo ""
}

# 检查健康状态
check_health() {
    echo -e "${BLUE}[检查 2/5]${NC} 健康检查..."
    
    # 获取容器端口
    PORT=$(docker inspect "$CONTAINER_NAME" --format='{{range $p, $conf := .NetworkSettings.Ports}}{{if eq $p "9091/tcp"}}{{range $conf}}{{if .HostPort}}{{.HostPort}}{{end}}{{end}}{{end}}{{end}}')
    
    if [ -z "$PORT" ]; then
        PORT="9091"
    fi
    
    if curl -f "http://localhost:$PORT/healthz" > /dev/null 2>&1; then
        echo -e "${GREEN}✅ 健康检查通过 (端口: $PORT)${NC}"
    else
        echo -e "${YELLOW}⚠️ 健康检查未响应 (端口: $PORT)${NC}"
        echo "这通常是正常的，应用可能还在启动中"
    fi
    echo ""
}

# 检查 Python 依赖
check_python_deps() {
    echo -e "${BLUE}[检查 3/5]${NC} Python 依赖..."
    
    if docker exec "$CONTAINER_NAME" python3 -c "import sentence_transformers" 2>/dev/null; then
        echo -e "${GREEN}✅ sentence_transformers 已安装${NC}"
    else
        echo -e "${RED}❌ sentence_transformers 未安装${NC}"
    fi
    
    if docker exec "$CONTAINER_NAME" python3 -c "import numpy" 2>/dev/null; then
        echo -e "${GREEN}✅ numpy 已安装${NC}"
    else
        echo -e "${RED}❌ numpy 未安装${NC}"
    fi
    echo ""
}

# 检查模型
check_model() {
    echo -e "${BLUE}[检查 4/5]${NC} 语义切分模型..."
    
    # 检查模型目录
    if docker exec "$CONTAINER_NAME" test -d /app/model_cache/sentence_transformers/BAAI__bge-small-zh 2>/dev/null; then
        echo -e "${GREEN}✅ 模型目录存在${NC}"
        
        # 检查文件大小
        SIZE=$(docker exec "$CONTAINER_NAME" du -sh /app/model_cache/sentence_transformers/BAAI__bge-small-zh 2>/dev/null | cut -f1)
        echo "   模型大小: $SIZE"
        
        # 检查关键文件
        for file in config.json pytorch_model.bin tokenizer.json; do
            if docker exec "$CONTAINER_NAME" test -f "/app/model_cache/sentence_transformers/BAAI__bge-small-zh/$file" 2>/dev/null || \
               docker exec "$CONTAINER_NAME" test -f "/app/model_cache/sentence_transformers/BAAI__bge-small-zh/${file}.safetensors" 2>/dev/null; then
                echo -e "   ${GREEN}✓${NC} $file"
            else
                echo -e "   ${YELLOW}○${NC} $file (可能使用其他格式)"
            fi
        done
    else
        echo -e "${YELLOW}⚠️ 模型未下载${NC}"
        echo "   系统将在首次使用时自动下载，或使用以下命令手动下载："
        echo "   docker exec $CONTAINER_NAME python3 -c \"from sentence_transformers import SentenceTransformer; SentenceTransformer('BAAI/bge-small-zh')\""
    fi
    echo ""
}

# 检查日志
check_logs() {
    echo -e "${BLUE}[检查 5/5]${NC} 近期日志..."
    echo ""
    
    # 显示最近启动日志
    docker logs --tail 30 "$CONTAINER_NAME" 2>&1 | grep -E "(INFO|WARN|ERROR|启动|模型|语义)" || docker logs --tail 20 "$CONTAINER_NAME" 2>&1
    
    echo ""
}

# 主流程
main() {
    check_container
    check_health
    check_python_deps
    check_model
    check_logs
    
    echo ""
    echo "╔════════════════════════════════════════════════════════╗"
    echo "║                  检查完成！                            ║"
    echo "╚════════════════════════════════════════════════════════╝"
    echo ""
    echo "常用命令:"
    echo "  查看日志: docker logs -f $CONTAINER_NAME"
    echo "  进入容器: docker exec -it $CONTAINER_NAME bash"
    echo "  重启容器: docker restart $CONTAINER_NAME"
    echo ""
}

main
