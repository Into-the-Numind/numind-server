#!/bin/bash

# Numind Server 部署测试脚本

set -e

# 配置变量
CONTAINER_NAME="numind-server"
PORT="9091"
HEALTH_ENDPOINT="/healthz"
TIMEOUT=30

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

# 检查容器是否运行
check_container_running() {
    if docker ps -q -f name="^$CONTAINER_NAME$" | grep -q .; then
        return 0
    else
        return 1
    fi
}

# 检查容器健康状态
check_container_health() {
    local health_status=$(docker inspect --format='{{.State.Health.Status}}' "$CONTAINER_NAME" 2>/dev/null)
    if [ "$health_status" = "healthy" ]; then
        return 0
    else
        return 1
    fi
}

# 检查HTTP健康端点
check_http_health() {
    if curl -f "http://localhost:$PORT$HEALTH_ENDPOINT" > /dev/null 2>&1; then
        return 0
    else
        return 1
    fi
}

# 显示容器信息
show_container_info() {
    log_info "Container Information:"
    echo "========================"
    
    # 基本信息
    echo "Container Name: $CONTAINER_NAME"
    echo "Port: $PORT"
    echo "Health Endpoint: $HEALTH_ENDPOINT"
    echo ""
    
    # 容器状态
    if check_container_running; then
        log_success "Container is running"
        docker ps -f name="^$CONTAINER_NAME$" --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}\t{{.Image}}"
    else
        log_error "Container is not running"
        return 1
    fi
    
    echo ""
    
    # 健康状态
    if check_container_health; then
        log_success "Docker health check: PASSED"
    else
        log_warning "Docker health check: FAILED or UNKNOWN"
    fi
    
    if check_http_health; then
        log_success "HTTP health check: PASSED"
    else
        log_error "HTTP health check: FAILED"
        return 1
    fi
    
    echo ""
}

# 测试API端点
test_api_endpoints() {
    log_info "Testing API endpoints..."
    echo "=========================="
    
    # 测试健康检查端点
    log_info "Testing health check endpoint..."
    if response=$(curl -s "http://localhost:$PORT$HEALTH_ENDPOINT" 2>/dev/null); then
        log_success "Health check response: $response"
    else
        log_error "Health check failed"
        return 1
    fi
    
    # 测试根端点
    log_info "Testing root endpoint..."
    if curl -s "http://localhost:$PORT/" > /dev/null 2>&1; then
        log_success "Root endpoint is accessible"
    else
        log_warning "Root endpoint is not accessible"
    fi
    
    echo ""
}

# 检查容器日志
check_container_logs() {
    log_info "Recent container logs (last 10 lines):"
    echo "========================================"
    docker logs --tail 10 "$CONTAINER_NAME" 2>/dev/null || log_warning "Unable to get container logs"
    echo ""
}

# 检查资源使用
check_resource_usage() {
    log_info "Container resource usage:"
    echo "==========================="
    docker stats --no-stream "$CONTAINER_NAME" 2>/dev/null || log_warning "Unable to get resource usage"
    echo ""
}

# 主测试函数
run_tests() {
    log_info "Starting deployment tests..."
    echo "================================"
    
    # 等待容器启动
    log_info "Waiting for container to be ready..."
    local timeout=$TIMEOUT
    while [ $timeout -gt 0 ]; do
        if check_container_running && check_http_health; then
            break
        fi
        sleep 2
        timeout=$((timeout - 2))
    done
    
    if [ $timeout -le 0 ]; then
        log_error "Container failed to start within $TIMEOUT seconds"
        check_container_logs
        return 1
    fi
    
    # 运行测试
    show_container_info
    test_api_endpoints
    check_container_logs
    check_resource_usage
    
    log_success "All tests passed! Deployment is successful."
}

# 显示帮助信息
show_help() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -h, --help     Show this help message"
    echo "  --logs         Show container logs only"
    echo "  --status       Show container status only"
    echo "  --health       Test health endpoints only"
    echo ""
    echo "Examples:"
    echo "  $0             Run all tests"
    echo "  $0 --logs      Show container logs"
    echo "  $0 --status    Show container status"
}

# 主程序
main() {
    case "${1:-}" in
        -h|--help)
            show_help
            exit 0
            ;;
        --logs)
            check_container_logs
            exit 0
            ;;
        --status)
            show_container_info
            exit 0
            ;;
        --health)
            test_api_endpoints
            exit 0
            ;;
        "")
            run_tests
            ;;
        *)
            log_error "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
}

# 执行主程序
main "$@" 