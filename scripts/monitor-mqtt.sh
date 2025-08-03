#!/bin/bash

# MQTT 连接监控脚本

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}MQTT 连接监控脚本${NC}"
echo "=================="

# MQTT 配置
MQTT_BROKER="49.233.219.254"
MQTT_PORT="1883"
MQTT_USERNAME="admin"
MQTT_PASSWORD="public"
CLIENT_ID="monitor-$(date +%s)"

echo -e "\n${YELLOW}测试 MQTT 连接...${NC}"

# 使用 mosquitto_pub 测试连接
if command -v mosquitto_pub &> /dev/null; then
    echo -e "${GREEN}使用 mosquitto_pub 测试连接${NC}"
    
    # 测试连接
    if mosquitto_pub -h "$MQTT_BROKER" -p "$MQTT_PORT" -u "$MQTT_USERNAME" -P "$MQTT_PASSWORD" \
        -i "$CLIENT_ID" -t "test/connection" -m "test message" --repeat 1 --repeat-delay 1; then
        echo -e "${GREEN}✓ MQTT 连接正常${NC}"
    else
        echo -e "${RED}✗ MQTT 连接失败${NC}"
        exit 1
    fi
else
    echo -e "${YELLOW}mosquitto_pub 未安装，使用 curl 测试${NC}"
    
    # 使用 curl 测试 MQTT over WebSocket（如果支持）
    if curl -s --connect-timeout 5 "http://$MQTT_BROKER:8080" > /dev/null 2>&1; then
        echo -e "${GREEN}✓ MQTT WebSocket 端口可访问${NC}"
    else
        echo -e "${YELLOW}MQTT WebSocket 端口不可访问${NC}"
    fi
fi

echo -e "\n${YELLOW}检查 MQTT 服务状态...${NC}"

# 检查 MQTT 端口是否开放
if nc -z "$MQTT_BROKER" "$MQTT_PORT" 2>/dev/null; then
    echo -e "${GREEN}✓ MQTT 端口 $MQTT_PORT 开放${NC}"
else
    echo -e "${RED}✗ MQTT 端口 $MQTT_PORT 不可访问${NC}"
fi

echo -e "\n${YELLOW}网络延迟测试...${NC}"

# 测试网络延迟
if ping -c 3 "$MQTT_BROKER" > /dev/null 2>&1; then
    echo -e "${GREEN}✓ 网络连接正常${NC}"
    
    # 显示延迟统计
    echo -e "\n${YELLOW}网络延迟统计:${NC}"
    ping -c 5 "$MQTT_BROKER" | tail -2
else
    echo -e "${RED}✗ 网络连接失败${NC}"
fi

echo -e "\n${YELLOW}建议的优化措施:${NC}"
echo "1. 确保 MQTT broker 配置了适当的 keepalive 设置"
echo "2. 检查网络防火墙设置"
echo "3. 考虑使用 MQTT over TLS 以提高安全性"
echo "4. 监控 MQTT broker 的资源使用情况"

echo -e "\n${GREEN}监控完成${NC}" 