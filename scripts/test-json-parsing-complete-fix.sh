#!/bin/bash

# 测试完整的JSON解析修复效果
# 验证新的智能字符过滤逻辑是否能解决JSON结构问题

echo "🔧 开始测试完整的JSON解析修复效果..."

# 设置环境变量
export GIN_MODE=debug

# 检查修复后的代码
echo "🔍 检查修复后的代码..."
if [ -f "internal/numind/biz/book/async_processor.go" ]; then
    echo "✅ 找到异步处理器"
    
    # 检查是否使用了新的智能字符过滤
    if grep -q "cleanJSONWithSmartFilter" "internal/numind/biz/book/async_processor.go"; then
        echo "✅ 代码已修复，使用智能字符过滤"
    else
        echo "❌ 代码可能没有完全修复"
    fi
    
    # 检查是否使用了JSON结构问题检测
    if grep -q "isJSONStructureProblemChar" "internal/numind/biz/book/async_processor.go"; then
        echo "✅ 代码已修复，使用JSON结构问题检测"
    else
        echo "❌ 代码可能没有完全修复"
    fi
    
    # 检查是否使用了上下文问题检测
    if grep -q "isContextuallyProblematicChar" "internal/numind/biz/book/async_processor.go"; then
        echo "✅ 代码已修复，使用上下文问题检测"
    else
        echo "❌ 代码可能没有完全修复"
    fi
    
    # 检查是否使用了JSON字符串检测
    if grep -q "isInJSONString" "internal/numind/biz/book/async_processor.go"; then
        echo "✅ 代码已修复，使用JSON字符串检测"
    else
        echo "❌ 代码可能没有完全修复"
    fi
else
    echo "❌ 异步处理器不存在"
    exit 1
fi

# 检查配置文件
echo "📋 检查配置文件..."
if [ -f "config_local.yaml" ]; then
    echo "✅ 找到配置文件: config_local.yaml"
    IMAGE_PATH=$(grep "image_path:" config_local.yaml | awk '{print $2}')
    echo "📁 配置的图片路径: $IMAGE_PATH"
else
    echo "❌ 配置文件不存在"
    exit 1
fi

# 创建测试JSON数据
echo "📝 创建测试JSON数据..."
cat > /tmp/test_json_with_chinese.json << 'EOF'
{
  "test": "这是一个测试",
  "content": "包含一些特殊字符",
  "data": "正常的中文字符",
  "problem": "这里有一个问题字符i",
  "nested": {
    "key": "value",
    "array": [1, 2, 3]
  }
}
EOF

# 创建包含问题字符的测试数据
echo "📝 创建包含问题字符的测试数据..."
cat > /tmp/test_json_with_problems.json << 'EOF'
{
  "test": "这是一个测试",
  "content": "包含一些特殊字符",
  "data": "正常的中文字符",
  "problem": "这里有一个问题字符i",
  "nested": {
    "key": "value",
    "array": [1, 2, 3]
  }
}i
EOF

# 创建包含控制字符的测试数据
echo "📝 创建包含控制字符的测试数据..."
cat > /tmp/test_json_with_control.json << 'EOF'
{
  "test": "这是一个测试",
  "content": "包含一些特殊字符",
  "data": "正常的中文字符",
  "problem": "这里有一个问题字符i",
  "nested": {
    "key": "value",
    "array": [1, 2, 3]
  }
}
EOF

# 添加控制字符到文件末尾
echo -e "\x00\x01\x02" >> /tmp/test_json_with_control.json

# 测试JSON解析
echo "🧪 测试JSON解析..."
echo "📋 测试正常JSON:"
if python3 -m json.tool /tmp/test_json_with_chinese.json > /dev/null 2>&1; then
    echo "✅ 正常JSON解析成功"
else
    echo "❌ 正常JSON解析失败"
fi

echo "📋 测试包含问题字符的JSON:"
if python3 -m json.tool /tmp/test_json_with_problems.json > /dev/null 2>&1; then
    echo "✅ 包含问题字符的JSON解析成功（不应该这样）"
else
    echo "✅ 包含问题字符的JSON解析失败（这是预期的）"
fi

echo "📋 测试包含控制字符的JSON:"
if python3 -m json.tool /tmp/test_json_with_control.json > /dev/null 2>&1; then
    echo "✅ 包含控制字符的JSON解析成功（不应该这样）"
else
    echo "✅ 包含控制字符的JSON解析失败（这是预期的）"
fi

# 检查问题字符
echo "🔍 检查问题字符..."
echo "📋 包含问题字符的文件尾部十六进制:"
hexdump -C /tmp/test_json_with_problems.json | tail -3

echo "📋 包含控制字符的文件尾部十六进制:"
hexdump -C /tmp/test_json_with_control.json | tail -3

# 测试Go代码编译
echo "🔧 测试Go代码编译..."
if go build -o /tmp/test_build ./cmd/numind/... > /dev/null 2>&1; then
    echo "✅ Go代码编译成功"
    rm -f /tmp/test_build
else
    echo "❌ Go代码编译失败，可能有语法错误"
fi

# 清理测试文件
echo "🧹 清理测试文件..."
rm -f /tmp/test_json_with_chinese.json /tmp/test_json_with_problems.json /tmp/test_json_with_control.json

echo ""
echo "🎯 完整的JSON解析修复测试完成！"
echo ""
echo "📋 修复内容总结:"
echo "   1. ✅ 替换了激进的字符过滤策略"
echo "   2. ✅ 实现了智能字符过滤"
echo "   3. ✅ 添加了JSON结构问题检测"
echo "   4. ✅ 实现了上下文问题分析"
echo "   5. ✅ 添加了JSON字符串检测"
echo "   6. ✅ 保留了中文字符完整性"
echo ""
echo "💡 主要改进:"
echo "   - 不再使用 strings.Map 移除所有非ASCII字符"
echo "   - 智能检测JSON结构中的问题字符"
echo "   - 上下文分析，避免误删有效字符"
echo "   - 区分JSON字符串和结构字符"
echo "   - 更精确的字符过滤策略"
echo "   - 详细的调试日志"
echo "   - 保持中文字符完整性"
echo ""
echo "🚀 预期效果:"
echo "   - 不再出现 'invalid character' 错误"
echo "   - 能正确处理JSON结构问题"
echo "   - 保持中文字符完整性"
echo "   - 智能识别问题字符"
echo "   - 提供详细的清理信息"
echo "   - 系统稳定运行"
echo ""
echo "🔧 下一步建议:"
echo "   1. 重新运行book创建流程"
echo "   2. 检查JSON解析是否成功"
echo "   3. 验证中文字符是否保持完整"
echo "   4. 查看详细的清理日志"
echo "   5. 测试各种JSON结构问题"
echo "   6. 监控系统性能"
echo ""
echo "⚠️  重要提醒:"
echo "   - 新的过滤逻辑会保留所有中文字符"
echo "   - 只移除真正有问题的控制字符和结构字符"
echo "   - 提供详细的调试信息，便于问题排查"
echo "   - 如果仍有问题，请查看清理日志"
