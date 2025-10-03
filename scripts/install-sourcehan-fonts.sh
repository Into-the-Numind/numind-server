#!/bin/bash

# 安装思源宋体字体到本地系统（macOS）
# 用于保持本地开发环境与Docker环境的字体一致性

set -e

echo "🔧 开始安装思源宋体字体..."

# 创建临时目录
TEMP_DIR=$(mktemp -d)
echo "📁 创建临时目录: $TEMP_DIR"

# 切换到临时目录
cd "$TEMP_DIR"

# 下载字体文件
echo "📥 下载 SourceHanSerifSC-Regular.otf..."
curl -L -o "SourceHanSerifSC-Regular.otf" \
    "https://github.com/adobe-fonts/source-han-serif/raw/release/OTF/SimplifiedChinese/SourceHanSerifSC-Regular.otf"

echo "📥 下载 SourceHanSerifSC-Bold.otf..."
curl -L -o "SourceHanSerifSC-Bold.otf" \
    "https://github.com/adobe-fonts/source-han-serif/raw/release/OTF/SimplifiedChinese/SourceHanSerifSC-Bold.otf"

echo "📥 下载 SourceHanSerifSC-Medium.otf..."
curl -L -o "SourceHanSerifSC-Medium.otf" \
    "https://github.com/adobe-fonts/source-han-serif/raw/release/OTF/SimplifiedChinese/SourceHanSerifSC-Medium.otf"

echo "📥 下载 SourceHanSerifSC-SemiBold.otf..."
curl -L -o "SourceHanSerifSC-SemiBold.otf" \
    "https://github.com/adobe-fonts/source-han-serif/raw/release/OTF/SimplifiedChinese/SourceHanSerifSC-SemiBold.otf"

# 检查操作系统
if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS
    FONT_DIR="$HOME/Library/Fonts"
    echo "📍 检测到 macOS 系统，字体将安装到: $FONT_DIR"

    # 创建字体目录（如果不存在）
    mkdir -p "$FONT_DIR"

    # 复制字体文件
    echo "📋 复制字体文件..."
    cp *.otf "$FONT_DIR/"

    # 刷新字体缓存
    echo "🔄 刷新字体缓存..."
    # macOS 会自动检测新字体，但我们可以强制刷新
    if command -v fc-cache &> /dev/null; then
        fc-cache -fv
    fi

elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    # Linux
    FONT_DIR="$HOME/.local/share/fonts"
    echo "📍 检测到 Linux 系统，字体将安装到: $FONT_DIR"

    # 创建字体目录（如果不存在）
    mkdir -p "$FONT_DIR"

    # 复制字体文件
    echo "📋 复制字体文件..."
    cp *.otf "$FONT_DIR/"

    # 刷新字体缓存
    echo "🔄 刷新字体缓存..."
    fc-cache -fv
else
    echo "❌ 不支持的操作系统: $OSTYPE"
    exit 1
fi

# 清理临时目录
cd - > /dev/null
rm -rf "$TEMP_DIR"
echo "🧹 清理临时文件完成"

# 验证安装
echo ""
echo "✅ 字体安装完成！"
echo ""
echo "📝 验证安装的字体："

if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS 验证
    ls -la "$FONT_DIR"/SourceHanSerifSC-*.otf 2>/dev/null || echo "未找到字体文件"

    # 使用系统字体工具验证
    echo ""
    echo "系统字体列表中的思源宋体："
    system_profiler SPFontsDataType 2>/dev/null | grep -i "source.*han.*serif" || echo "系统暂未识别，可能需要重启应用"
else
    # Linux 验证
    fc-list | grep -i "source.*han.*serif" || echo "未找到字体"
fi

echo ""
echo "💡 提示："
echo "   1. 字体已安装到用户字体目录"
echo "   2. 重启 Chrome 浏览器以应用新字体"
echo "   3. 如果使用 Chrome headless，重启应用服务"
echo "   4. 可以在 Chrome DevTools 中验证字体加载情况"
echo ""
echo "🔍 Chrome DevTools 验证方法："
echo "   1. 打开渲染的页面"
echo "   2. 打开 DevTools (F12)"
echo "   3. 在 Elements 面板选中文字元素"
echo "   4. 在 Computed 面板查看 'Rendered Fonts'"
echo "   5. 应该显示 'SourceHanSerifSC' 而不是其他字体"