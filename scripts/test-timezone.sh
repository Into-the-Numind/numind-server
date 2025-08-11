#!/bin/bash

# 测试容器时区设置脚本
echo "=== 测试容器时区设置 ==="

# 检查当前系统时区
echo "当前系统时区:"
date
echo "TZ环境变量: $TZ"
echo "时区文件:"
ls -la /etc/localtime
cat /etc/timezone 2>/dev/null || echo "timezone文件不存在"

# 检查Go程序中的时区
echo -e "\n=== Go程序时区信息 ==="
echo "当前时间: $(date)"
echo "UTC时间: $(date -u)"
echo "时区信息: $(timedatectl status 2>/dev/null || echo 'timedatectl不可用')"

# 检查Alpine Linux特定信息
if [ -f /etc/alpine-release ]; then
    echo -e "\n=== Alpine Linux信息 ==="
    echo "Alpine版本: $(cat /etc/alpine-release)"
    echo "已安装的时区包:"
    apk list --installed | grep tzdata || echo "tzdata未安装"
fi

echo -e "\n=== 时区设置完成 ==="
