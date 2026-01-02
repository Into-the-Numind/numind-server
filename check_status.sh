#!/bin/bash

NEW_IP="62.234.165.77"

echo "======================================"
echo "   Numind 迁移 - 状态最后检查          "
echo "======================================"

ssh root@$NEW_IP << 'EOF'
    echo ">>> 1. 检查是否存在项目目录..."
    if [ -d "/root/numind-server" ]; then
        echo "✅ 项目目录已就位"
    else
        echo "❌ 目录不存在，我们需要重新解压！"
        tar -xzvf /root/numind_code.tar.gz -C /root/numind-server/
    fi

    echo -e "\n>>> 2. 检查程序运行情况..."
    docker ps

    echo -e "\n>>> 3. 如果没看到 Up 状态，我们将尝试最后一次启动..."
    cd /root/numind-server
    # 检查是否有 Up 的容器，如果没有则启动
    if [ -z "$(docker ps -q)" ]; then
        echo "发现服务没跑起来，正在手动点火..."
        docker-compose up -d
    fi

    # 4. 再次确认数据库数据
    echo -e "\n>>> 4. 确认数据库内容..."
    DB_NAME=$(docker ps --format '{{.Names}}' | grep mysql | head -n 1)
    if [ -n "$DB_NAME" ]; then
         # 稍微检查一下有没有表
         TABLE_COUNT=$(docker exec $DB_NAME mysql -u root -pNumind2025 -e "use \`numind-prod\`; show tables;" 2>/dev/null | wc -l)
         if [ "$TABLE_COUNT" -gt 1 ]; then
            echo "✅ 数据库已有数据 ($TABLE_COUNT 张表)"
         else
            echo "⚠️ 数据库好像是空的，正在导入..."
            docker exec -i $DB_NAME mysql -u root -pNumind2025 -e "CREATE DATABASE IF NOT EXISTS \`numind-prod\`;"
            docker exec -i $DB_NAME mysql -u root -pNumind2025 numind-prod < /root/numind_db_backup.sql
         fi
    fi
EOF
