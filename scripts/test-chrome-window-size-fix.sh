#!/bin/bash

# 测试Chrome窗口大小修复脚本
# 验证chrome_headless_renderer.go中的--window-size参数是否正确设置

echo "=== 测试Chrome窗口大小修复 ==="

# 测试1: 编译检查
echo "检查代码编译..."
cd internal/numind/biz/card
go build .
cd ../..

# 测试2: 检查Chrome启动参数
echo "检查Chrome启动参数..."
if grep -q "window-size" internal/numind/biz/card/chrome_headless_renderer.go; then
    echo "✅ --window-size参数已添加"
    
    # 显示具体的参数设置
    echo "参数设置详情:"
    grep -A 5 -B 5 "window-size" internal/numind/biz/card/chrome_headless_renderer.go
else
    echo "❌ --window-size参数缺失"
    exit 1
fi

# 测试3: 检查参数格式
echo "检查参数格式..."
if grep -q "fmt.Sprintf.*window-size.*r.config.Card.Width.*r.config.Card.Height" internal/numind/biz/card/chrome_headless_renderer.go; then
    echo "✅ 参数格式正确，使用配置中的卡片尺寸"
else
    echo "❌ 参数格式不正确"
    exit 1
fi

# 测试4: 对比其他渲染器
echo "对比其他渲染器的窗口大小设置..."
echo "cover_renderer.go:"
grep -n "window-size" internal/numind/biz/card/cover_renderer.go

echo "render_and_measure_renderer.go:"
grep -n "window-size" internal/numind/biz/card/render_and_measure_renderer.go

echo "headless_renderer.go:"
grep -n "window-size" internal/numind/biz/card/headless_renderer.go

# 测试5: 验证配置值
echo "验证配置值..."
cd internal/numind/biz/pagination
go run -c "package main; import 'numind-server/internal/numind/biz/pagination'; func main() { config := pagination.GetDefaultConfig(); println('默认卡片尺寸:', config.Card.Height, 'x', config.Card.Width) }"
cd ../..

echo "=== 测试完成 ==="
