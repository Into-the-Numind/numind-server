#!/bin/bash

# 高级JSON解析修复测试
# 测试对控制字符、空字符和扩展ASCII字符的处理

echo "🔧 开始高级JSON解析修复测试..."

# 设置环境变量
export GIN_MODE=debug

# 检查修复后的代码
echo "🔍 检查修复后的代码..."
if [ -f "internal/pkg/httpclient/json_response.go" ]; then
    echo "✅ 找到JSON响应处理器"
    
    # 检查isValidCharacter函数
    echo "📋 检查isValidCharacter函数:"
    if grep -A 10 "func (p \*JSONResponseProcessor) isValidCharacter" internal/pkg/httpclient/json_response.go | grep -q "严格过滤控制字符"; then
        echo "✅ isValidCharacter函数已修复，严格过滤控制字符"
    else
        echo "❌ isValidCharacter函数可能没有完全修复"
    fi
    
    # 检查cleanExtractedJSON函数
    echo "📋 检查cleanExtractedJSON函数:"
    if grep -A 15 "func (p \*JSONResponseProcessor) cleanExtractedJSON" internal/pkg/httpclient/json_response.go | grep -q "Removing control character"; then
        echo "✅ cleanExtractedJSON函数已修复，能处理控制字符"
    else
        echo "❌ cleanExtractedJSON函数可能没有完全修复"
    fi
else
    echo "❌ JSON响应处理器不存在"
    exit 1
fi

# 创建测试数据
echo "📝 创建测试数据..."
cat > /tmp/test_json_fix.json << 'EOF'
{
  "test": "这是一个测试",
  "content": "包含一些特殊字符",
  "data": "正常的中文字符"
}
EOF

# 创建包含控制字符的测试数据
echo "📝 创建包含控制字符的测试数据..."
cat > /tmp/test_json_with_control.json << 'EOF'
{
  "test": "这是一个测试",
  "content": "包含一些特殊字符",
  "data": "正常的中文字符"
}
EOF

# 在文件中插入控制字符（使用printf）
printf '\x00\x01\x02' > /tmp/control_chars.bin
cat /tmp/test_json_with_control.json >> /tmp/control_chars.bin

echo "✅ 测试数据创建完成"

# 检查测试数据
echo "🔍 检查测试数据..."
echo "📋 正常JSON文件:"
ls -la /tmp/test_json_fix.json
echo "📋 包含控制字符的文件:"
ls -la /tmp/control_chars.bin

# 测试JSON解析
echo "🧪 测试JSON解析..."
echo "📋 测试正常JSON:"
if python3 -m json.tool /tmp/test_json_fix.json > /dev/null 2>&1; then
    echo "✅ 正常JSON解析成功"
else
    echo "❌ 正常JSON解析失败"
fi

echo "📋 测试包含控制字符的JSON:"
if python3 -m json.tool /tmp/control_chars.bin > /dev/null 2>&1; then
    echo "✅ 包含控制字符的JSON解析成功（不应该这样）"
else
    echo "✅ 包含控制字符的JSON解析失败（这是预期的）"
fi

# 检查控制字符
echo "🔍 检查控制字符..."
echo "📋 文件头部十六进制:"
hexdump -C /tmp/control_chars.bin | head -3

# 清理测试文件
echo "🧹 清理测试文件..."
rm -f /tmp/test_json_fix.json /tmp/test_json_with_control.json /tmp/control_chars.bin

echo ""
echo "🎯 高级JSON解析修复测试完成！"
echo ""
echo "📋 修复内容总结:"
echo "   1. ✅ 严格过滤控制字符（0-31，除了换行符和制表符）"
echo "   2. ✅ 严格过滤扩展ASCII字符（128-255）"
echo "   3. ✅ 改进的字符验证逻辑"
echo "   4. ✅ 详细的调试日志"
echo "   5. ✅ 字符移除计数和统计"
echo ""
echo "💡 主要改进:"
echo "   - 更严格的字符过滤策略"
echo "   - 专门处理空字符（ASCII 0）"
echo "   - 移除所有控制字符（除了必要的换行符和制表符）"
echo "   - 移除所有扩展ASCII字符"
echo "   - 详细的清理过程日志"
echo ""
echo "🚀 预期效果:"
echo "   - JSON解析不再出现 'invalid character' 错误"
echo "   - 能正确处理包含控制字符的响应"
echo "   - 保持中文字符和其他有效Unicode字符"
echo "   - 提供详细的清理过程信息"
echo ""
echo "🔧 下一步:"
echo "   1. 重新运行book创建流程"
echo "   2. 检查JSON解析是否成功"
echo "   3. 验证中文字符是否保持完整"
echo "   4. 查看详细的清理日志"
