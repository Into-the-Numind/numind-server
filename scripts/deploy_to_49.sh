#!/bin/bash
# 专门针对 49.233.219.254 的部署脚本

NEW_IP="49.233.219.254"
LOCAL_DIR=$(pwd)
REMOTE_DIR="/root/numind-server"

echo "======================================"
echo "   Numind 部署脚本 - 目标: $NEW_IP"
echo "======================================"

# 1. 同步代码
echo ">>> 正在同步代码到服务器..."
rsync -avz --progress \
    --exclude '.git' \
    --exclude '.DS_Store' \
    --exclude 'logs' \
    --exclude 'tmp' \
    --exclude 'node_modules' \
    --exclude '.cursor' \
    "$LOCAL_DIR/" root@$NEW_IP:$REMOTE_DIR/

# 2. 远程重启服务
echo ">>> 正在远程重启并重建服务..."
ssh root@$NEW_IP << EOF
    cd $REMOTE_DIR
    
    # 确保脚本可执行
    chmod +x numind start.sh
    
    # 使用 dev 配置文件
    docker-compose -f docker-compose.dev.yml down
    docker-compose -f docker-compose.dev.yml up -d --build
    
    echo "--------------------------------------"
    docker ps | grep numind
    echo "--------------------------------------"
EOF

echo "🏁 部署完成！请等待约 10-20 秒让服务完全启动。"
