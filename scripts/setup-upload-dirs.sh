#!/bin/bash

# 设置上传目录权限脚本
# 用于在部署时自动创建必要的目录并设置正确的权限

set -e

echo "Setting up upload directories..."

# 创建图片上传目录
mkdir -p ./images/upload/avatars
mkdir -p /opt/numind/image/upload/avatars

# 设置权限
chmod -R 755 ./images/upload
chmod -R 755 /opt/numind/image/upload

# 如果使用Docker，设置容器用户权限
if [ -n "$DOCKER_USER" ]; then
    chown -R $DOCKER_USER:$DOCKER_USER ./images/upload
    chown -R $DOCKER_USER:$DOCKER_USER /opt/numind/image/upload
else
    # 默认使用1000:1000（常见的Docker用户ID）
    chown -R 1000:1000 ./images/upload 2>/dev/null || true
    chown -R 1000:1000 /opt/numind/image/upload 2>/dev/null || true
fi

echo "Upload directories setup completed!"
echo "Directories created:"
echo "  - ./images/upload/avatars"
echo "  - /opt/numind/image/upload/avatars" 