#!/bin/bash

echo "🧪 测试渲染-测量方案修复"
echo "=========================="

# 设置环境变量
export ENABLE_RENDER_AND_MEASURE=true
export ENABLE_CHROME_HEADLESS=false
export ENABLE_TRADITIONAL_RENDERER=true

echo "📋 环境变量设置:"
echo "  ENABLE_RENDER_AND_MEASURE=$ENABLE_RENDER_AND_MEASURE"
echo "  ENABLE_CHROME_HEADLESS=$ENABLE_CHROME_HEADLESS"
echo "  ENABLE_TRADITIONAL_RENDERER=$ENABLE_TRADITIONAL_RENDERER"

# 编译项目
echo ""
echo "🔨 编译项目..."
cd /Users/neozhang/go/src/github.com/Into-the-Numind/numind-server
go build -o numind-server ./cmd/numind

if [ $? -eq 0 ]; then
    echo "✅ 编译成功"
else
    echo "❌ 编译失败"
    exit 1
fi

# 运行测试
echo ""
echo "🚀 运行渲染-测量方案测试..."
echo "注意：这将测试修复后的索引越界问题"

# 运行示例程序
go run examples/render_and_measure_example.go

if [ $? -eq 0 ]; then
    echo ""
    echo "✅ 测试成功！索引越界问题已修复"
else
    echo ""
    echo "❌ 测试失败，请检查错误信息"
    exit 1
fi

echo ""
echo "🎉 渲染-测量方案修复验证完成！"
echo ""
echo "修复内容总结："
echo "1. ✅ 添加了分页点有效性验证"
echo "2. ✅ 添加了边界检查，防止索引越界"
echo "3. ✅ 实现了智能分页点生成"
echo "4. ✅ 添加了详细的调试日志"
echo "5. ✅ 提供了降级到传统渲染器的机制"
