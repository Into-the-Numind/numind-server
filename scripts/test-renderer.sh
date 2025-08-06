#!/bin/bash

# 渲染器测试脚本

set -e

echo "=== 渲染器测试 ==="

echo "1. 编译项目..."
go build -o test_renderer cmd/numind/main.go

echo "2. 检查渲染器代码..."
if grep -q "NewRenderer" internal/numind/controller/v1/book/create.go; then
    echo "✅ 书籍创建控制器使用正确的渲染器"
else
    echo "❌ 书籍创建控制器渲染器配置错误"
fi

if grep -q "NewRenderer" internal/numind/controller/v1/card/render.go; then
    echo "✅ 卡片渲染控制器使用正确的渲染器"
else
    echo "❌ 卡片渲染控制器渲染器配置错误"
fi

echo "3. 检查字体加载逻辑..."
if grep -q "loadChineseFont" internal/numind/biz/card/renderer.go; then
    echo "✅ 字体加载函数存在"
else
    echo "❌ 字体加载函数缺失"
fi

echo "4. 检查系统字体..."
if [ -f "/System/Library/Fonts/STHeiti Light.ttc" ]; then
    echo "✅ STHeiti Light 字体存在"
else
    echo "❌ STHeiti Light 字体不存在"
fi

if [ -f "/System/Library/Fonts/ArialHB.ttc" ]; then
    echo "✅ ArialHB 字体存在"
else
    echo "❌ ArialHB 字体不存在"
fi

echo ""
echo "=== 测试完成 ==="
echo "如果所有检查都通过，渲染器应该能够正常工作"
echo "如果字体加载失败，系统会回退到基本字体" 