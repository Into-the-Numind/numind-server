#!/bin/bash

# 权限修复验证脚本

set -e

echo "🔧 权限修复验证脚本"
echo "================================================================"

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
NC='\033[0m' # No Color

# 检查Docker容器中的权限
check_container_permissions() {
    echo -e "${BLUE}🔍 检查Docker容器中的权限...${NC}"
    
    # 检查容器是否运行
    if ! docker ps | grep -q numind-server; then
        echo -e "${RED}❌ Numind容器未运行${NC}"
        return 1
    fi
    
    echo -e "${BLUE}📊 检查目录权限:${NC}"
    
    # 检查关键目录权限
    docker exec numind-server ls -la /opt/numind/dev/image/upload/
    echo ""
    
    # 检查card目录权限
    docker exec numind-server ls -la /opt/numind/dev/image/upload/card/
    echo ""
    
    # 检查用户信息
    echo -e "${BLUE}👤 检查用户信息:${NC}"
    docker exec numind-server id
    echo ""
    
    # 测试创建目录
    echo -e "${BLUE}🧪 测试创建目录:${NC}"
    docker exec numind-server mkdir -p /opt/numind/dev/image/upload/card/test123
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}✅ 目录创建成功${NC}"
        docker exec numind-server rmdir /opt/numind/dev/image/upload/card/test123
    else
        echo -e "${RED}❌ 目录创建失败${NC}"
    fi
}

# 重新构建和启动容器
rebuild_container() {
    echo -e "${BLUE}🔨 重新构建容器...${NC}"
    
    # 停止现有容器
    docker-compose down
    
    # 清理旧镜像
    docker image prune -f
    
    # 重新构建
    docker-compose build --no-cache numind-server
    
    # 启动服务
    docker-compose up -d
    
    echo -e "${GREEN}✅ 容器重建完成${NC}"
}

# 监控应用日志
monitor_logs() {
    echo -e "${BLUE}📋 监控应用日志...${NC}"
    echo -e "${YELLOW}⏳ 等待应用启动...${NC}"
    
    # 等待应用启动
    sleep 10
    
    # 监控关键日志
    docker-compose logs -f numind-server | grep -E "(🔍 渲染-测量方案|❌ 渲染-测量方案|✅ 渲染-测量方案|⚠️ 渲染-测量方案)" --line-buffered
}

# 主函数
main() {
    case "${1:-check}" in
        "check")
            check_container_permissions
            ;;
        "rebuild")
            rebuild_container
            ;;
        "logs")
            monitor_logs
            ;;
        "all")
            rebuild_container
            sleep 5
            check_container_permissions
            ;;
        *)
            echo "用法: $0 [check|rebuild|logs|all]"
            echo "  check  - 检查容器权限"
            echo "  rebuild - 重新构建容器"
            echo "  logs   - 监控应用日志"
            echo "  all    - 执行完整流程"
            exit 1
            ;;
    esac
}

main "$@"
