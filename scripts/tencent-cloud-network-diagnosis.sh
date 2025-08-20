#!/bin/bash

echo "=== 腾讯云容器网络诊断脚本 ==="
echo "时间: $(date)"
echo ""

# 检查容器环境
echo "1. 容器环境检查"
echo "------------------------"
if [ -f /.dockerenv ]; then
    echo "✓ 运行在Docker容器中"
    echo "容器ID: $(hostname)"
else
    echo "✗ 未运行在Docker容器中"
fi
echo ""

# 检查网络接口
echo "2. 网络接口检查"
echo "------------------------"
echo "网络接口:"
ip addr show | grep -E "inet.*scope global" | head -5
echo ""

echo "路由表:"
ip route show | head -10
echo ""

# 检查DNS配置
echo "3. DNS配置检查"
echo "------------------------"
echo "DNS配置:"
cat /etc/resolv.conf 2>/dev/null || echo "无法读取DNS配置"
echo ""

# 检查网络连通性
echo "4. 网络连通性测试"
echo "------------------------"

# 测试DNS解析
echo "DNS解析测试:"
echo "火山引擎API:"
nslookup ark.cn-beijing.volces.com 2>/dev/null || echo "DNS解析失败"
echo ""

echo "千问API:"
nslookup dashscope.aliyuncs.com 2>/dev/null || echo "DNS解析失败"
echo ""

# 测试网络延迟
echo "网络延迟测试:"
echo "火山引擎API延迟:"
ping -c 3 ark.cn-beijing.volces.com 2>/dev/null | tail -2 || echo "ping失败，可能被防火墙阻止"
echo ""

echo "千问API延迟:"
ping -c 3 dashscope.aliyuncs.com 2>/dev/null | tail -2 || echo "ping失败，可能被防火墙阻止"
echo ""

# 测试端口连通性
echo "5. 端口连通性测试"
echo "------------------------"
echo "测试HTTPS端口(443):"
echo "火山引擎:"
nc -zv ark.cn-beijing.volces.com 443 2>&1 || echo "端口测试失败"
echo ""

echo "千问:"
nc -zv dashscope.aliyuncs.com 443 2>&1 || echo "端口测试失败"
echo ""

# HTTP连接测试
echo "6. HTTP连接测试"
echo "------------------------"
echo "测试火山引擎API连接:"
curl -v --connect-timeout 30 --max-time 60 \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-key" \
  -d '{"test": "connection"}' \
  "https://ark.cn-beijing.volces.com/api/v3/chat/completions" 2>&1 | head -20
echo ""

echo "测试千问API连接:"
curl -v --connect-timeout 30 --max-time 60 \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test-key" \
  -d '{"test": "connection"}' \
  "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions" 2>&1 | head -20
echo ""

# 检查网络配置
echo "7. 网络配置检查"
echo "------------------------"
echo "网络接口统计:"
netstat -i 2>/dev/null | head -5 || echo "netstat不可用"
echo ""

echo "网络连接状态:"
ss -s 2>/dev/null | head -5 || echo "ss不可用"
echo ""

# 检查代理设置
echo "8. 代理设置检查"
echo "------------------------"
echo "HTTP代理环境变量:"
echo "HTTP_PROXY: ${HTTP_PROXY:-未设置}"
echo "HTTPS_PROXY: ${HTTPS_PROXY:-未设置}"
echo "NO_PROXY: ${NO_PROXY:-未设置}"
echo ""

echo "系统代理配置:"
env | grep -i proxy | head -5
echo ""

# 检查防火墙状态
echo "9. 防火墙状态检查"
echo "------------------------"
if command -v ufw >/dev/null 2>&1; then
    echo "UFW状态:"
    ufw status
elif command -v iptables >/dev/null 2>&1; then
    echo "iptables规则数量:"
    iptables -L | wc -l
    echo "iptables规则预览:"
    iptables -L | head -10
else
    echo "未检测到常见防火墙"
fi
echo ""

# 检查Docker网络配置
echo "10. Docker网络配置检查"
echo "------------------------"
if command -v docker >/dev/null 2>&1; then
    echo "Docker网络列表:"
    docker network ls 2>/dev/null | head -5 || echo "无法获取Docker网络信息"
    
    echo "当前容器网络配置:"
    docker inspect $(hostname) 2>/dev/null | grep -A 10 "NetworkSettings" || echo "无法获取容器网络配置"
else
    echo "Docker命令不可用"
fi
echo ""

# 网络性能测试
echo "11. 网络性能测试"
echo "------------------------"
echo "网络带宽测试（简单版）:"
if command -v wget >/dev/null 2>&1; then
    echo "下载测试文件:"
    wget --timeout=30 --tries=1 -O /dev/null https://www.baidu.com 2>&1 | grep -E "Downloaded|failed|error" || echo "wget测试完成"
else
    echo "wget不可用"
fi
echo ""

echo "=== 诊断完成 ==="
echo ""
echo "如果发现网络问题，请检查："
echo "1. 腾讯云安全组出站规则"
echo "2. 腾讯云网络ACL配置"
echo "3. 容器网络模式（bridge/host/overlay）"
echo "4. 宿主机网络配置"
echo "5. DNS服务器设置"
echo "6. 防火墙规则"
echo "7. 代理设置"
echo ""
echo "建议的解决方案："
echo "1. 检查腾讯云控制台的安全组配置"
echo "2. 确保出站规则允许HTTPS(443)端口"
echo "3. 检查容器网络模式，建议使用host模式测试"
echo "4. 联系腾讯云技术支持"










