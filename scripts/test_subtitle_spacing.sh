#!/bin/bash

# 测试副标题间距修复的脚本

echo "=================================="
echo "🎯 测试副标题间距修复"
echo "=================================="

# 设置基础URL和认证信息
BASE_URL="http://localhost:8080"
TOKEN="your_jwt_token_here"

echo ""
echo "1. 检查当前配置..."
echo "请确认以下配置已正确设置："
echo "- 副标题上间距: 30rpx"
echo "- 副标题下间距: 25rpx"
echo "- 其他元素下间距: 30rpx（与副标题上间距一致）"

echo ""
echo "2. 创建测试book..."
echo "使用包含多个副标题的文本内容创建book，观察间距是否一致"

echo ""
echo "3. 验证渲染效果..."
echo "检查生成的图片中："
echo "- 标题到副标题的间距应该是30rpx"
echo "- 列表到副标题的间距应该是30rpx"
echo "- 正文到副标题的间距应该是30rpx"
echo "- 所有副标题的上间距应该完全一致"

echo ""
echo "4. 关键检查点..."
echo "✅ 副标题'未来的职业通用竞争力'的上间距"
echo "✅ 副标题'未来世界的认知能力转变'的上间距"
echo "✅ 两个间距应该完全一致（30rpx）"

echo ""
echo "5. 如果间距仍然不一致，请检查："
echo "- 渲染器是否正确应用了MarginTop"
echo "- 分页引擎是否正确计算了元素高度"
echo "- 是否有其他CSS或样式覆盖"

echo ""
echo "=================================="
echo "🎯 测试完成！"
echo ""
echo "💡 验证要点："
echo "1. 确认配置中的MarginTop和MarginBottom设置"
echo "2. 检查渲染器是否正确应用了这些设置"
echo "3. 验证生成的图片中副标题间距是否一致"
echo "4. 如果问题仍然存在，检查是否有其他因素影响"

echo ""
echo "🔧 技术细节："
echo "- 分页引擎计算高度时包含MarginTop和MarginBottom"
echo "- 渲染器渲染时应用MarginTop到y坐标"
echo "- 所有元素类型都应该正确应用这些间距设置"
