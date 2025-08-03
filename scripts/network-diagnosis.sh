#!/bin/bash

# 网络诊断脚本 - 用于分析 MQTT 连接问题

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}网络诊断脚本${NC}"
echo "=============="

# 目标服务器
TARGET_HOST="49.233.219.254"
MQTT_PORT="1883"
HTTP_PORT="80"

echo -e "\n${YELLOW}1. 基础网络连接测试${NC}"
echo "========================"

# Ping 测试
echo -e "\n${GREEN}Ping 测试:${NC}"
if ping -c 5 "$TARGET_HOST" > /dev/null 2>&1; then
    echo -e "${GREEN}✓ Ping 成功${NC}"
    ping -c 5 "$TARGET_HOST" | tail -3
else
    echo -e "${RED}✗ Ping 失败${NC}"
fi

# Traceroute 测试
echo -e "\n${GREEN}Traceroute 测试:${NC}"
if command -v traceroute &> /dev/null; then
    traceroute -m 15 "$TARGET_HOST" 2>/dev/null | head -10
else
    echo -e "${YELLOW}traceroute 命令不可用${NC}"
fi

echo -e "\n${YELLOW}2. 端口连接测试${NC}"
echo "=================="

# MQTT 端口测试
echo -e "\n${GREEN}MQTT 端口 ($MQTT_PORT) 测试:${NC}"
if nc -z -w 5 "$TARGET_HOST" "$MQTT_PORT" 2>/dev/null; then
    echo -e "${GREEN}✓ MQTT 端口开放${NC}"
else
    echo -e "${RED}✗ MQTT 端口不可访问${NC}"
fi

# HTTP 端口测试
echo -e "\n${GREEN}HTTP 端口 ($HTTP_PORT) 测试:${NC}"
if nc -z -w 5 "$TARGET_HOST" "$HTTP_PORT" 2>/dev/null; then
    echo -e "${GREEN}✓ HTTP 端口开放${NC}"
else
    echo -e "${YELLOW}⚠ HTTP 端口不可访问${NC}"
fi

echo -e "\n${YELLOW}3. 网络延迟和丢包测试${NC}"
echo "========================"

# 使用 mtr 进行网络质量测试
if command -v mtr &> /dev/null; then
    echo -e "\n${GREEN}网络质量测试 (mtr):${NC}"
    mtr --report --report-cycles 10 "$TARGET_HOST" 2>/dev/null | head -15
else
    echo -e "\n${GREEN}网络延迟测试:${NC}"
    for i in {1..10}; do
        if ping -c 1 "$TARGET_HOST" > /dev/null 2>&1; then
            ping -c 1 "$TARGET_HOST" | grep "time=" | sed 's/.*time=\([0-9.]*\) ms.*/延迟: \1 ms/'
        else
            echo -e "${RED}第 $i 次 ping 失败${NC}"
        fi
        sleep 1
    done
fi

echo -e "\n${YELLOW}4. DNS 解析测试${NC}"
echo "=================="

# DNS 解析测试
echo -e "\n${GREEN}DNS 解析测试:${NC}"
if nslookup "$TARGET_HOST" > /dev/null 2>&1; then
    echo -e "${GREEN}✓ DNS 解析正常${NC}"
    nslookup "$TARGET_HOST" | grep -A 2 "Name:"
else
    echo -e "${RED}✗ DNS 解析失败${NC}"
fi

echo -e "\n${YELLOW}5. 网络接口信息${NC}"
echo "=================="

# 显示网络接口信息
echo -e "\n${GREEN}本地网络接口:${NC}"
if command -v ip &> /dev/null; then
    ip addr show | grep -E "inet.*scope global" | head -3
else
    ifconfig | grep -E "inet.*netmask" | head -3
fi

echo -e "\n${YELLOW}6. 网络连接统计${NC}"
echo "=================="

# 显示到目标主机的连接统计
echo -e "\n${GREEN}到目标主机的连接:${NC}"
if command -v ss &> /dev/null; then
    ss -tuln | grep "$TARGET_HOST" || echo "当前没有到目标主机的连接"
else
    netstat -an | grep "$TARGET_HOST" || echo "当前没有到目标主机的连接"
fi

echo -e "\n${YELLOW}7. 建议和解决方案${NC}"
echo "=================="

echo -e "\n${BLUE}如果网络测试正常但 MQTT 连接不稳定，建议:${NC}"
echo "1. 检查 MQTT broker 配置 (mosquitto.conf)"
echo "2. 增加 keepalive 时间到 60-120 秒"
echo "3. 检查防火墙设置"
echo "4. 考虑使用 MQTT over TLS"
echo "5. 监控 MQTT broker 的资源使用情况"

echo -e "\n${BLUE}如果网络测试异常，建议:${NC}"
echo "1. 联系网络管理员"
echo "2. 检查路由器/交换机配置"
echo "3. 考虑更换网络线路"
echo "4. 使用 VPN 或代理"

echo -e "\n${GREEN}诊断完成${NC}" 