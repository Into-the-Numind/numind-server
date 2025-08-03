#!/bin/bash

# Numind Server 部署脚本
# 支持滚动更新和健康检查

set -e  # 遇到错误立即退出

# 配置变量
CONTAINER_NAME="numind-server"
IMAGE_NAME="ghcr.io/into-the-numind/numind-server"
CONFIG_FILE="/app/config_dev.yaml"
PORT="9091"
HEALTH_ENDPOINT="/healthz"
HEALTH_TIMEOUT=60
HEALTH_INTERVAL=5

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 日志函数
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# 检查Docker是否运行
check_docker() {
    if ! docker info > /dev/null 2>&1; then
        log_error "Docker is not running or not accessible"
        exit 1
    fi
    log_success "Docker is running"
}

# 登录到GHCR
login_ghcr() {
    if [ -z "$GHCR_TOKEN" ]; then
        log_error "GHCR_TOKEN environment variable is not set"
        exit 1
    fi
    
    log_info "Logging in to GHCR..."
    echo "$GHCR_TOKEN" | docker login ghcr.io -u "$GITHUB_ACTOR" --password-stdin
    log_success "Successfully logged in to GHCR"
}

# 拉取最新镜像
pull_image() {
    local tag=$1
    log_info "Pulling image: $IMAGE_NAME:$tag"
    
    if docker pull "$IMAGE_NAME:$tag"; then
        log_success "Successfully pulled image: $IMAGE_NAME:$tag"
    else
        log_error "Failed to pull image: $IMAGE_NAME:$tag"
        exit 1
    fi
}

# 检查容器是否存在
container_exists() {
    docker ps -aq -f name="^$CONTAINER_NAME$" | grep -q .
}

# 检查容器是否运行
container_running() {
    docker ps -q -f name="^$CONTAINER_NAME$" | grep -q .
}

# 停止容器
stop_container() {
    if container_running; then
        log_info "Stopping container: $CONTAINER_NAME"
        if docker stop "$CONTAINER_NAME"; then
            log_success "Container stopped successfully"
        else
            log_error "Failed to stop container"
            exit 1
        fi
    fi
}

# 删除容器
remove_container() {
    if container_exists; then
        log_info "Removing container: $CONTAINER_NAME"
        if docker rm "$CONTAINER_NAME"; then
            log_success "Container removed successfully"
        else
            log_error "Failed to remove container"
            exit 1
        fi
    fi
}

# 启动新容器
start_container() {
    local tag=$1
    log_info "Starting new container: $CONTAINER_NAME"
    
    # 创建证书目录（如果不存在）
    sudo mkdir -p /opt/numind/config/cert
    sudo chown $USER:$USER /opt/numind/config/cert
    
    if docker run -d \
        --name "$CONTAINER_NAME" \
        -p "$PORT:$PORT" \
        --restart always \
        -v /opt/numind:/opt/numind:ro \
        --health-cmd="curl -f http://localhost:$PORT$HEALTH_ENDPOINT || exit 1" \
        --health-interval=30s \
        --health-timeout=10s \
        --health-retries=3 \
        "$IMAGE_NAME:$tag" \
        -c "$CONFIG_FILE"; then
        log_success "Container started successfully"
    else
        log_error "Failed to start container"
        exit 1
    fi
}

# 等待容器启动
wait_for_container() {
    log_info "Waiting for container to start..."
    sleep 10
    
    if ! container_running; then
        log_error "Container failed to start"
        docker logs "$CONTAINER_NAME" || true
        exit 1
    fi
    
    log_success "Container is running"
}

# 健康检查
health_check() {
    log_info "Performing health check..."
    
    local timeout=$HEALTH_TIMEOUT
    local interval=$HEALTH_INTERVAL
    
    while [ $timeout -gt 0 ]; do
        # 检查Docker健康状态
        if docker inspect --format='{{.State.Health.Status}}' "$CONTAINER_NAME" 2>/dev/null | grep -q "healthy"; then
            log_success "Health check passed!"
            return 0
        fi
        
        # 检查HTTP健康端点
        if curl -f "http://localhost:$PORT$HEALTH_ENDPOINT" > /dev/null 2>&1; then
            log_success "HTTP health check passed!"
            return 0
        fi
        
        log_info "Health check failed, retrying in ${interval}s... (${timeout}s remaining)"
        sleep $interval
        timeout=$((timeout - interval))
    done
    
    log_warning "Health check timeout, but container is running"
    return 0
}

# 显示容器状态
show_status() {
    log_info "Container status:"
    docker ps -f name="^$CONTAINER_NAME$" --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
    
    log_info "Container logs (last 20 lines):"
    docker logs --tail 20 "$CONTAINER_NAME" || true
}

# 回滚到上一个版本
rollback() {
    log_warning "Rolling back to previous version..."
    
    # 停止当前容器
    stop_container
    remove_container
    
    # 启动上一个版本（如果有的话）
    if docker images | grep -q "$IMAGE_NAME"; then
        local previous_tag=$(docker images "$IMAGE_NAME" --format "{{.Tag}}" | head -2 | tail -1)
        if [ -n "$previous_tag" ]; then
            log_info "Rolling back to tag: $previous_tag"
            start_container "$previous_tag"
            wait_for_container
            health_check
            show_status
        else
            log_error "No previous version found for rollback"
            exit 1
        fi
    else
        log_error "No images found for rollback"
        exit 1
    fi
}

# 主部署函数
deploy() {
    local tag=$1
    
    log_info "Starting deployment for tag: $tag"
    
    # 检查Docker
    check_docker
    
    # 登录GHCR
    login_ghcr
    
    # 拉取镜像
    pull_image "$tag"
    
    # 停止并删除旧容器
    stop_container
    remove_container
    
    # 启动新容器
    start_container "$tag"
    
    # 等待容器启动
    wait_for_container
    
    # 健康检查
    if health_check; then
        log_success "Deployment completed successfully!"
        show_status
    else
        log_error "Health check failed, rolling back..."
        rollback
    fi
}

# 显示帮助信息
show_help() {
    echo "Usage: $0 [OPTIONS] <tag>"
    echo ""
    echo "Options:"
    echo "  -h, --help     Show this help message"
    echo "  --rollback     Rollback to previous version"
    echo ""
    echo "Examples:"
    echo "  $0 develop     Deploy develop tag"
    echo "  $0 v1.0.0      Deploy specific version"
    echo "  $0 --rollback  Rollback to previous version"
}

# 主程序
main() {
    case "${1:-}" in
        -h|--help)
            show_help
            exit 0
            ;;
        --rollback)
            rollback
            exit 0
            ;;
        "")
            log_error "Tag is required"
            show_help
            exit 1
            ;;
        *)
            deploy "$1"
            ;;
    esac
}

# 执行主程序
main "$@" 