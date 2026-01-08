#!/bin/bash

# 变量定义
NEW_IP="62.234.165.77"
LOCAL_DIR="/Users/zhiyuchen/Desktop/莫小派合作/numind-server/numind-server"

echo "======================================"
echo "   Numind 迁移 - 本地文件直送启动      "
echo "======================================"

# 1. 在新服务器创建目录
ssh root@$NEW_IP "mkdir -p /root/numind-server"

# 2. 从您的 Mac 上传关键文件到新服务器
echo ">>> 正在从 Mac 传送配置文件到新服务器..."
scp "$LOCAL_DIR/docker-compose.yml" root@$NEW_IP:/root/numind-server/
scp "$LOCAL_DIR/config_prod.yaml" root@$NEW_IP:/root/numind-server/
scp "$LOCAL_DIR/Dockerfile" root@$NEW_IP:/root/numind-server/

# 3. 远程执行启动和数据导入
echo ">>> 正在远程启动并灌入数据..."
ssh root@$NEW_IP << 'EOF'
    cd /root/numind-server
    
    # 修正配置文件
    sed -i 's/62.234.165.77:13306/mysql:3306/g' config_prod.yaml
    sed -i 's/62.234.165.77/redis/g' config_prod.yaml
    sed -i 's/port: 26739/port: 6379/g' config_prod.yaml
    
    # 手动创建 nginx 目录防止报错 (因为本地似乎没看到这个文件夹)
    mkdir -p nginx && touch nginx/nginx.conf

    # 启动
    docker-compose up -d
    
    echo "等待数据库启动..."
    sleep 15
    
    # 导入数据 (2.2M 的 SQL 文件之前已经在 /root 下了)
    DB_NAME=$(docker ps --format '{{.Names}}' | grep mysql | head -n 1)
    if [ -n "$DB_NAME" ]; then
        docker exec -i $DB_NAME mysql -u root -pNumind2025 -e "CREATE DATABASE IF NOT EXISTS \`numind-prod\`;"
        docker exec -i $DB_NAME mysql -u root -pNumind2025 numind-prod < /root/numind_db_backup.sql
        echo "✅ 数据库同步成功！"
    else
        echo "❌ 数据库启动失败，请检查端口 3306 是否被占用。"
    fi

    echo -e "\n--------------------------------------"
    docker ps
    echo "--------------------------------------"
EOF

echo "🏁 部署尝试完成！请尝试访问新服务器 IP。"
