#!/bin/bash

# Docker 镜像清理脚本
# 清理镜像名为 <none> 的悬空镜像

set -e

# 配置变量
DRY_RUN=false
FORCE=false
LOG_FILE="/var/log/docker-cleanup.log"
MAX_AGE_DAYS=7  # 清理超过7天的悬空镜像

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 日志函数
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
    echo "$(date '+%Y-%m-%d %H:%M:%S') [INFO] $1" >> "$LOG_FILE"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
    echo "$(date '+%Y-%m-%d %H:%M:%S') [SUCCESS] $1" >> "$LOG_FILE"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
    echo "$(date '+%Y-%m-%d %H:%M:%S') [WARNING] $1" >> "$LOG_FILE"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
    echo "$(date '+%Y-%m-%d %H:%M:%S') [ERROR] $1" >> "$LOG_FILE"
}

# 显示帮助信息
show_help() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -h, --help          Show this help message"
    echo "  -d, --dry-run       Show what would be cleaned without actually cleaning"
    echo "  -f, --force         Force cleanup without confirmation"
    echo "  -a, --age DAYS      Clean images older than DAYS (default: 7)"
    echo "  -l, --log FILE      Specify log file (default: /var/log/docker-cleanup.log)"
    echo ""
    echo "Examples:"
    echo "  $0                  Clean dangling images with confirmation"
    echo "  $0 --dry-run        Show what would be cleaned"
    echo "  $0 --force          Clean without confirmation"
    echo "  $0 --age 3          Clean images older than 3 days"
    echo "  $0 -d -a 1          Dry run for images older than 1 day"
}

# 检查Docker是否运行
check_docker() {
    if ! docker info > /dev/null 2>&1; then
        log_error "Docker is not running or not accessible"
        exit 1
    fi
    log_success "Docker is running"
}

# 获取悬空镜像列表
get_dangling_images() {
    local age_filter=""
    if [ "$MAX_AGE_DAYS" -gt 0 ]; then
        age_filter="--filter \"until=${MAX_AGE_DAYS}d\""
    fi
    
    # 获取所有悬空镜像
    docker images -f "dangling=true" --format "table {{.ID}}\t{{.Repository}}\t{{.Tag}}\t{{.CreatedAt}}" $age_filter 2>/dev/null || true
}

# 获取悬空镜像ID列表
get_dangling_image_ids() {
    local age_filter=""
    if [ "$MAX_AGE_DAYS" -gt 0 ]; then
        age_filter="--filter \"until=${MAX_AGE_DAYS}d\""
    fi
    
    docker images -f "dangling=true" -q $age_filter 2>/dev/null || true
}

# 显示当前Docker使用情况
show_docker_usage() {
    log_info "Current Docker disk usage:"
    echo "=============================="
    
    # 显示磁盘使用情况
    docker system df --format "table {{.Type}}\t{{.TotalCount}}\t{{.Size}}\t{{.Reclaimable}}" 2>/dev/null || log_warning "Unable to get disk usage"
    
    echo ""
    
    # 显示悬空镜像数量
    local dangling_count=$(get_dangling_image_ids | wc -l)
    log_info "Found $dangling_count dangling images"
    
    if [ "$dangling_count" -gt 0 ]; then
        echo ""
        log_info "Dangling images to be cleaned:"
        echo "================================="
        get_dangling_images
    fi
    
    echo ""
}

# 清理悬空镜像
cleanup_dangling_images() {
    local image_ids=$(get_dangling_image_ids)
    local count=$(echo "$image_ids" | wc -l)
    
    if [ "$count" -eq 0 ]; then
        log_info "No dangling images found to clean"
        return 0
    fi
    
    log_info "Found $count dangling images to clean"
    
    if [ "$DRY_RUN" = true ]; then
        log_info "DRY RUN: Would clean the following images:"
        echo "$image_ids" | while read -r id; do
            if [ -n "$id" ]; then
                echo "  - $id"
            fi
        done
        return 0
    fi
    
    # 确认清理
    if [ "$FORCE" = false ]; then
        echo ""
        echo "The following images will be removed:"
        echo "$image_ids" | while read -r id; do
            if [ -n "$id" ]; then
                echo "  - $id"
            fi
        done
        echo ""
        read -p "Are you sure you want to continue? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            log_info "Cleanup cancelled by user"
            return 0
        fi
    fi
    
    # 执行清理
    log_info "Starting cleanup..."
    local cleaned_count=0
    local failed_count=0
    
    echo "$image_ids" | while read -r id; do
        if [ -n "$id" ]; then
            if docker rmi "$id" 2>/dev/null; then
                log_success "Removed image: $id"
                ((cleaned_count++))
            else
                log_error "Failed to remove image: $id"
                ((failed_count++))
            fi
        fi
    done
    
    log_success "Cleanup completed: $cleaned_count images removed, $failed_count failed"
}

# 清理未使用的容器
cleanup_unused_containers() {
    log_info "Cleaning up unused containers..."
    
    if [ "$DRY_RUN" = true ]; then
        local containers=$(docker container ls -a --filter "status=exited" --filter "status=created" -q 2>/dev/null || true)
        local count=$(echo "$containers" | wc -l)
        log_info "DRY RUN: Would clean $count unused containers"
        return 0
    fi
    
    local removed=$(docker container prune -f 2>/dev/null | grep -o '[0-9]*' | tail -1 || echo "0")
    log_success "Removed $removed unused containers"
}

# 清理未使用的网络
cleanup_unused_networks() {
    log_info "Cleaning up unused networks..."
    
    if [ "$DRY_RUN" = true ]; then
        local networks=$(docker network ls --filter "type=custom" -q 2>/dev/null || true)
        local count=$(echo "$networks" | wc -l)
        log_info "DRY RUN: Would clean $count unused networks"
        return 0
    fi
    
    local removed=$(docker network prune -f 2>/dev/null | grep -o '[0-9]*' | tail -1 || echo "0")
    log_success "Removed $removed unused networks"
}

# 清理构建缓存
cleanup_build_cache() {
    log_info "Cleaning up build cache..."
    
    if [ "$DRY_RUN" = true ]; then
        log_info "DRY RUN: Would clean build cache"
        return 0
    fi
    
    local removed=$(docker builder prune -f 2>/dev/null | grep -o '[0-9]*' | tail -1 || echo "0")
    log_success "Removed $removed build cache entries"
}

# 完整清理
full_cleanup() {
    log_info "Starting full Docker cleanup..."
    echo "=================================="
    
    show_docker_usage
    
    cleanup_dangling_images
    cleanup_unused_containers
    cleanup_unused_networks
    cleanup_build_cache
    
    echo ""
    log_info "Cleanup completed. Final disk usage:"
    echo "========================================"
    docker system df --format "table {{.Type}}\t{{.TotalCount}}\t{{.Size}}\t{{.Reclaimable}}" 2>/dev/null || log_warning "Unable to get final disk usage"
}

# 解析命令行参数
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            -h|--help)
                show_help
                exit 0
                ;;
            -d|--dry-run)
                DRY_RUN=true
                shift
                ;;
            -f|--force)
                FORCE=true
                shift
                ;;
            -a|--age)
                MAX_AGE_DAYS="$2"
                shift 2
                ;;
            -l|--log)
                LOG_FILE="$2"
                shift 2
                ;;
            *)
                log_error "Unknown option: $1"
                show_help
                exit 1
                ;;
        esac
    done
}

# 主程序
main() {
    # 创建日志目录
    mkdir -p "$(dirname "$LOG_FILE")"
    
    # 解析参数
    parse_args "$@"
    
    # 检查Docker
    check_docker
    
    # 执行清理
    full_cleanup
    
    log_success "Docker cleanup script completed"
}

# 执行主程序
main "$@" 