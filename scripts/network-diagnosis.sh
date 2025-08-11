#!/bin/bash

echo "=== 腾讯云网络诊断脚本 ==="
echo "时间: $(date)"
echo ""

# 检查网络连通性
echo "1. 检查网络连通性"
echo "------------------------"

# 检查DNS解析
echo "DNS解析测试:"
echo "火山引擎API:"
nslookup ark.cn-beijing.volces.com
echo ""

echo "千问API:"
nslookup dashscope.aliyuncs.com
echo ""

# 检查HTTP连接
echo "2. HTTP连接测试"
echo "------------------------"

# 测试火山引擎API
echo "测试火山引擎API连接:"
curl -v --connect-timeout 30 --max-time 60 \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-key" \
  -d '{"test": "connection"}' \
  "https://ark.cn-beijing.volces.com/api/v3/chat/completions" 2>&1 | head -20
echo ""

# 测试千问API
echo "测试千问API连接:"
curl -v --connect-timeout 30 --max-time 60 \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-key" \
  -d '{"test": "connection"}' \
  "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions" 2>&1 | head -20
echo ""

# 检查网络延迟
echo "3. 网络延迟测试"
echo "------------------------"

echo "火山引擎API延迟:"
ping -c 5 ark.cn-beijing.volces.com 2>/dev/null || echo "ping失败，可能被防火墙阻止"
echo ""

echo "千问API延迟:"
ping -c 5 dashscope.aliyuncs.com 2>/dev/null || echo "ping失败，可能被防火墙阻止"
echo ""

# 检查端口连通性
echo "4. 端口连通性测试"
echo "------------------------"

echo "测试HTTPS端口(443):"
echo "火山引擎:"
nc -zv ark.cn-beijing.volces.com 443 2>&1 || echo "端口测试失败"
echo ""

echo "千问:"
nc -zv dashscope.aliyuncs.com 443 2>&1 || echo "端口测试失败"
echo ""

# 检查系统网络配置
echo "5. 系统网络配置"
echo "------------------------"

echo "网络接口:"
ip addr show | grep -E "inet.*scope global" | head -5
echo ""

echo "路由表:"
ip route show | head -10
echo ""

echo "DNS配置:"
cat /etc/resolv.conf 2>/dev/null || echo "无法读取DNS配置"
echo ""

# 检查防火墙状态
echo "6. 防火墙状态"
echo "------------------------"

if command -v ufw >/dev/null 2>&1; then
    echo "UFW状态:"
    ufw status
elif command -v iptables >/dev/null 2>&1; then
    echo "iptables规则数量:"
    iptables -L | wc -l
else
    echo "未检测到常见防火墙"
fi
echo ""

# 检查容器网络
echo "7. 容器网络检查"
echo "------------------------"

if [ -f /.dockerenv ]; then
    echo "运行在Docker容器中"
    echo "容器网络模式:"
    ip route show | grep default
    echo ""
    echo "容器DNS:"
    cat /etc/resolv.conf 2>/dev/null || echo "无法读取DNS配置"
else
    echo "未运行在Docker容器中"
fi
echo ""

# 检查代理设置
echo "8. 代理设置检查"
echo "------------------------"

echo "HTTP代理环境变量:"
echo "HTTP_PROXY: ${HTTP_PROXY:-未设置}"
echo "HTTPS_PROXY: ${HTTPS_PROXY:-未设置}"
echo "NO_PROXY: ${NO_PROXY:-未设置}"
echo ""

echo "=== 诊断完成 ==="
echo "如果发现网络问题，请检查："
echo "1. 腾讯云安全组设置"
echo "2. 容器网络配置"
echo "3. DNS服务器设置"
echo "4. 防火墙规则"
echo "5. 代理设置" 