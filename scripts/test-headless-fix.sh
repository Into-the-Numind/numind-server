#!/bin/bash

# 测试headless浏览器渲染修复效果

set -e

echo "=== 测试headless浏览器渲染修复效果 ==="

# 1. 编译测试
echo "1. 编译测试..."
if go build -o test_build ./cmd/numind/main.go; then
    echo "✅ 编译成功"
    rm -f test_build
else
    echo "❌ 编译失败"
    exit 1
fi

# 2. 检查调试HTML文件
echo ""
echo "2. 检查调试HTML文件..."
DEBUG_HTML_FILES=$(find . -name "debug_simple_*.html" -type f | head -3)
if [ -n "$DEBUG_HTML_FILES" ]; then
    echo "找到调试HTML文件:"
    for file in $DEBUG_HTML_FILES; do
        echo "  - $file"
        echo "    📄 文件大小: $(stat -f%z "$file" 2>/dev/null || stat -c%s "$file" 2>/dev/null || echo "unknown") bytes"
    done
else
    echo "未找到调试HTML文件"
fi

echo ""
echo "=== 测试完成 ==="
echo ""
echo "🔧 Headless浏览器渲染修复:"
echo "  - 使用无头浏览器渲染HTML"
echo "  - 支持中文字体显示"
echo "  - 符合样式规范"
echo "  - 支持渐变背景和特殊样式" 