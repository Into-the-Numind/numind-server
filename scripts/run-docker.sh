#!/bin/bash

# Docker 运行脚本 - 映射服务器 /opt/numind 目录

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${YELLOW}Numind Server Docker 运行脚本${NC}"
echo "================================"

# 默认配置
CONTAINER_NAME="numind-server"
IMAGE_NAME="numind-server:latest"
HOST_PORT="9091"
CONTAINER_PORT="9091"
HOST_CONFIG_DIR="/opt/numind"
CONTAINER_CONFIG_DIR="/opt/numind"
CONFIG_FILE="config_dev.yaml"

# 显示帮助信息
show_help() {
    echo -e "${BLUE}使用方法:${NC}"
    echo "  $0 [选项]"
    echo ""
    echo -e "${BLUE}选项:${NC}"
    echo "  -h, --help              显示此帮助信息"
    echo "  -n, --name NAME         容器名称 (默认: numind-server)"
    echo "  -p, --port PORT         主机端口 (默认: 9091)"
    echo "  -c, --config FILE       配置文件 (默认: config_dev.yaml)"
    echo "  -d, --dir DIR           主机配置目录 (默认: /opt/numind)"
    echo "  --prod                  使用生产环境配置"
    echo "  --build                 构建镜像"
    echo "  --stop                  停止并删除容器"
    echo "  --logs                  查看容器日志"
    echo "  --status                查看容器状态"
    echo ""
    echo -e "${BLUE}示例:${NC}"
    echo "  $0 --prod                    # 使用生产环境配置运行"
    echo "  $0 -p 9092                  # 使用端口 9092 运行"
    echo "  $0 -d /home/numind/config   # 使用自定义配置目录"
    echo "  $0 --build                  # 构建镜像"
    echo "  $0 --stop                   # 停止容器"
}

# 检查容器是否存在
container_exists() {
    docker ps -a --format "table {{.Names}}" | grep -q "^${CONTAINER_NAME}$"
}

# 检查容器是否运行
container_running() {
    docker ps --format "table {{.Names}}" | grep -q "^${CONTAINER_NAME}$"
}

# 停止并删除容器
stop_container() {
    if container_exists; then
        echo -e "${YELLOW}停止容器: ${CONTAINER_NAME}${NC}"
        docker stop ${CONTAINER_NAME} 2>/dev/null || true
        echo -e "${YELLOW}删除容器: ${CONTAINER_NAME}${NC}"
        docker rm ${CONTAINER_NAME} 2>/dev/null || true
        echo -e "${GREEN}容器已停止并删除${NC}"
    else
        echo -e "${YELLOW}容器不存在: ${CONTAINER_NAME}${NC}"
    fi
}

# 构建镜像
build_image() {
    echo -e "${YELLOW}构建 Docker 镜像...${NC}"
    docker build -t ${IMAGE_NAME} .
    echo -e "${GREEN}镜像构建完成: ${IMAGE_NAME}${NC}"
}

# 运行容器
run_container() {
    echo -e "${YELLOW}检查主机配置目录...${NC}"
    
    # 检查主机配置目录是否存在
    if [ ! -d "${HOST_CONFIG_DIR}" ]; then
        echo -e "${RED}错误: 主机配置目录不存在: ${HOST_CONFIG_DIR}${NC}"
        echo -e "${YELLOW}请创建目录并放置证书文件:${NC}"
        echo "  sudo mkdir -p ${HOST_CONFIG_DIR}/config/cert"
        echo "  sudo chown \$USER:\$USER ${HOST_CONFIG_DIR}"
        echo "  # 然后将证书文件放置到 ${HOST_CONFIG_DIR}/config/cert/ 目录"
        exit 1
    fi
    
    # 检查证书文件
    if [ ! -f "${HOST_CONFIG_DIR}/config/cert/apiclient_key.pem" ]; then
        echo -e "${YELLOW}警告: 商户私钥文件不存在: ${HOST_CONFIG_DIR}/config/cert/apiclient_key.pem${NC}"
    fi
    
    if [ ! -f "${HOST_CONFIG_DIR}/config/cert/wechatpay_cert.pem" ]; then
        echo -e "${YELLOW}警告: 微信支付证书不存在: ${HOST_CONFIG_DIR}/config/cert/wechatpay_cert.pem${NC}"
    fi
    
    # 停止现有容器
    if container_exists; then
        echo -e "${YELLOW}停止现有容器...${NC}"
        docker stop ${CONTAINER_NAME} 2>/dev/null || true
        docker rm ${CONTAINER_NAME} 2>/dev/null || true
    fi
    
    echo -e "${YELLOW}启动容器...${NC}"
    echo -e "${BLUE}容器名称: ${CONTAINER_NAME}${NC}"
    echo -e "${BLUE}端口映射: ${HOST_PORT}:${CONTAINER_PORT}${NC}"
    echo -e "${BLUE}配置目录映射: ${HOST_CONFIG_DIR}:${CONTAINER_CONFIG_DIR}${NC}"
    echo -e "${BLUE}配置文件: ${CONFIG_FILE}${NC}"
    
    # 运行容器
    docker run -d \
        --name ${CONTAINER_NAME} \
        -p ${HOST_PORT}:${CONTAINER_PORT} \
        -p 9092:9092 \
        -v ${HOST_CONFIG_DIR}:${CONTAINER_CONFIG_DIR}:ro \
        -v /etc/ssl/certimate/youshu.asia:/etc/ssl/certimate/youshu.asia:ro \
        -e GIN_MODE=release \
        ${IMAGE_NAME} \
        -c /app/${CONFIG_FILE}
    
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}容器启动成功!${NC}"
        echo -e "${BLUE}容器状态:${NC}"
        docker ps --filter "name=${CONTAINER_NAME}"
        echo ""
        echo -e "${BLUE}查看日志:${NC}"
        echo "  docker logs -f ${CONTAINER_NAME}"
        echo ""
        echo -e "${BLUE}测试健康检查:${NC}"
        echo "  curl http://localhost:${HOST_PORT}/healthz"
    else
        echo -e "${RED}容器启动失败${NC}"
        exit 1
    fi
}

# 查看容器状态
show_status() {
    if container_exists; then
        echo -e "${BLUE}容器状态:${NC}"
        docker ps -a --filter "name=${CONTAINER_NAME}"
        echo ""
        echo -e "${BLUE}容器日志 (最近 20 行):${NC}"
        docker logs --tail 20 ${CONTAINER_NAME} 2>/dev/null || echo "无法获取日志"
    else
        echo -e "${YELLOW}容器不存在: ${CONTAINER_NAME}${NC}"
    fi
}

# 查看容器日志
show_logs() {
    if container_exists; then
        echo -e "${BLUE}容器日志:${NC}"
        docker logs -f ${CONTAINER_NAME}
    else
        echo -e "${YELLOW}容器不存在: ${CONTAINER_NAME}${NC}"
    fi
}

# 解析命令行参数
while [[ $# -gt 0 ]]; do
    case $1 in
        -h|--help)
            show_help
            exit 0
            ;;
        -n|--name)
            CONTAINER_NAME="$2"
            shift 2
            ;;
        -p|--port)
            HOST_PORT="$2"
            shift 2
            ;;
        -c|--config)
            CONFIG_FILE="$2"
            shift 2
            ;;
        -d|--dir)
            HOST_CONFIG_DIR="$2"
            shift 2
            ;;
        --prod)
            CONFIG_FILE="config_prod.yaml"
            shift
            ;;
        --build)
            build_image
            exit 0
            ;;
        --stop)
            stop_container
            exit 0
            ;;
        --status)
            show_status
            exit 0
            ;;
        --logs)
            show_logs
            exit 0
            ;;
        *)
            echo -e "${RED}未知选项: $1${NC}"
            show_help
            exit 1
            ;;
    esac
done

# 主逻辑
echo -e "${YELLOW}检查 Docker 镜像...${NC}"
if ! docker images | grep -q "${IMAGE_NAME}"; then
    echo -e "${YELLOW}镜像不存在，正在构建...${NC}"
    build_image
fi

run_container 