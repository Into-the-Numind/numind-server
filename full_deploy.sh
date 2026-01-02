#!/bin/bash

# 变量定义
NEW_IP="62.234.165.77"
LOCAL_DIR="$(pwd)"

echo "======================================"
echo "   Numind 完整同步部署脚本 (修复 405)"
echo "======================================"

# 0. 准备本地 Nginx 和 SSL 目录 (如果 cert txt 存在)
if [ -f "youshu.asia.crt.txt" ] && [ -f "youshu.asia.key.txt" ]; then
    echo ">>> 检测到根目录证书，准备同步到 nginx 配置目录..."
    mkdir -p nginx/ssl
    cp youshu.asia.crt.txt nginx/ssl/cert.crt
    cp youshu.asia.key.txt nginx/ssl/cert.key
fi

# 1. 使用 rsync 同步除 git/logs 外的所有文件到服务器
#    这将确保服务器拥有最新的 Go 源代码 (用于编译) 和最新的 nginx 配置
echo ">>> 正在同步完整项目代码到服务器 (这可能需要几秒钟)..."
rsync -avz --progress \
    --exclude '.git' \
    --exclude '.DS_Store' \
    --exclude 'logs' \
    --exclude 'tmp' \
    --exclude 'node_modules' \
    --exclude 'numind' \
    --exclude 'numind-server' \
    "$LOCAL_DIR/" root@$NEW_IP:/root/numind-server/

# 2. 远程执行：重启服务
echo ">>> 正在远程重启 Docker 服务..."
ssh root@$NEW_IP << 'EOF'
    cd /root/numind-server
    
    # 确保 nginx 目录结构正确
    if [ ! -d "nginx/ssl" ]; then
        echo "警告: nginx/ssl 目录不存在，HTTPS 可能会失败。"
    fi

    # 停止并重新构建启动
    # 使用 --build 确保基于新上传的源码重新编译 Go Binary
    echo "正在重新构建并启动服务..."
    docker-compose down
    docker-compose up -d --build

    echo "--------------------------------------"
    docker ps
    echo "--------------------------------------"
EOF

echo "🏁 部署完成！请等待约 10-30 秒让服务完全启动，然后再次测试。"
