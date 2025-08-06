#!/bin/bash

echo "=== 测试Docker容器中的headless浏览器渲染 ==="

# 1. 构建Docker镜像
echo "1. 构建Docker镜像..."
docker build -t numind-server:test .

if [ $? -eq 0 ]; then
    echo "✅ Docker镜像构建成功"
else
    echo "❌ Docker镜像构建失败"
    exit 1
fi

# 2. 检查镜像中的Chrome安装
echo ""
echo "2. 检查镜像中的Chrome安装..."
docker run --rm numind-server:test which chromium-browser

if [ $? -eq 0 ]; then
    echo "✅ Chrome已正确安装"
else
    echo "❌ Chrome安装有问题"
fi

# 3. 检查环境变量
echo ""
echo "3. 检查环境变量..."
docker run --rm numind-server:test env | grep -E "(CHROME|CHROMIUM)"

# 4. 测试Chrome版本
echo ""
echo "4. 测试Chrome版本..."
docker run --rm numind-server:test chromium-browser --version

echo ""
echo "=== 测试完成 ==="
echo ""
echo "🐳 Docker部署注意事项:"
echo "  - 已添加Chrome/Chromium依赖"
echo "  - 已设置headless环境变量"
echo "  - 已优化Chrome启动参数"
echo "  - 支持中文字体渲染" 