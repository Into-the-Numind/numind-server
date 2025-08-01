#!/bin/bash

# GitHub Actions 缓存清理脚本
# 用于解决 CI/CD 中的缓存冲突问题

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}GitHub Actions 缓存清理脚本${NC}"
echo "=================================="

# 检查是否安装了 GitHub CLI
if ! command -v gh &> /dev/null; then
    echo -e "${RED}错误: GitHub CLI (gh) 未安装${NC}"
    echo "请安装: https://cli.github.com/"
    exit 1
fi

# 检查是否已认证
if ! gh auth status &> /dev/null; then
    echo -e "${RED}错误: 未通过 GitHub CLI 认证${NC}"
    echo "请运行: gh auth login"
    exit 1
fi

# 获取仓库信息
REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner)
echo -e "${GREEN}仓库: ${REPO}${NC}"

# 清理缓存函数
clear_cache() {
    local cache_key=$1
    echo -e "\n${YELLOW}清理缓存: ${cache_key}${NC}"
    
    # 获取缓存列表
    cache_list=$(gh api repos/${REPO}/actions/caches --paginate | jq -r '.actions_caches[] | select(.key | contains("'$cache_key'")) | .id')
    
    if [ -z "$cache_list" ]; then
        echo "未找到匹配的缓存"
        return
    fi
    
    # 删除缓存
    echo "$cache_list" | while read cache_id; do
        if [ ! -z "$cache_id" ]; then
            echo "删除缓存 ID: $cache_id"
            gh api repos/${REPO}/actions/caches/$cache_id -X DELETE
        fi
    done
    
    echo -e "${GREEN}✓ 缓存清理完成${NC}"
}

# 清理所有缓存函数
clear_all_caches() {
    echo -e "\n${YELLOW}清理所有缓存...${NC}"
    
    # 获取所有缓存
    cache_list=$(gh api repos/${REPO}/actions/caches --paginate | jq -r '.actions_caches[].id')
    
    if [ -z "$cache_list" ]; then
        echo "没有找到任何缓存"
        return
    fi
    
    # 删除所有缓存
    echo "$cache_list" | while read cache_id; do
        if [ ! -z "$cache_id" ]; then
            echo "删除缓存 ID: $cache_id"
            gh api repos/${REPO}/actions/caches/$cache_id -X DELETE
        fi
    done
    
    echo -e "${GREEN}✓ 所有缓存清理完成${NC}"
}

# 显示缓存信息
show_caches() {
    echo -e "\n${YELLOW}当前缓存列表:${NC}"
    gh api repos/${REPO}/actions/caches --paginate | jq -r '.actions_caches[] | "\(.id) - \(.key) - \(.created_at) - \(.size_in_bytes) bytes"'
}

# 主菜单
while true; do
    echo -e "\n${YELLOW}选择操作:${NC}"
    echo "1) 显示所有缓存"
    echo "2) 清理 Go 模块缓存"
    echo "3) 清理 Docker 缓存"
    echo "4) 清理所有缓存"
    echo "5) 退出"
    
    read -p "请输入选择 (1-5): " choice
    
    case $choice in
        1)
            show_caches
            ;;
        2)
            clear_cache "go"
            ;;
        3)
            clear_cache "docker"
            ;;
        4)
            clear_all_caches
            ;;
        5)
            echo -e "${GREEN}退出脚本${NC}"
            exit 0
            ;;
        *)
            echo -e "${RED}无效选择，请重试${NC}"
            ;;
    esac
done 