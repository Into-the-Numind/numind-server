#!/bin/bash

NEW_IP="62.234.165.77"

echo "======================================"
echo "   服务器内网连通性深度测试            "
echo "======================================"

ssh root@$NEW_IP << 'EOF'
    echo ">>> 1. 测试服务器自己访问自己 (80端口)..."
    curl -I http://localhost
    
    echo -e "\n>>> 2. 检查 80 端口到底是谁在占用..."
    lsof -i:80
    
    echo -e "\n>>> 3. 查看后端服务 (9091) 的健康状况..."
    docker ps | grep numind-server
    # 尝试直接请求后端接口
    curl -v http://localhost:9091/healthz
    
    echo -e "\n>>> 4. 检查宝塔防火墙状态..."
    if command -v bt &> /dev/null; then
        echo "发现宝塔面板，正在检查其规则..."
        # 宝塔有时会自带一个系统防火墙管理
        ufw status || iptables -L -n | grep 80
    fi
    
    echo -e "\n>>> 5. 检查系统日志中的网络拦截信息..."
    dmesg | tail -n 10
EOF
