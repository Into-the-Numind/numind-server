#!/bin/bash

# 实际渲染测试脚本
# 测试封面卡片的实际渲染过程

echo "=== 实际渲染测试 ==="

# 测试1: 检查Chrome是否可用
echo "检查Chrome是否可用..."
if [ -f "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" ]; then
    echo "✅ Google Chrome 可用 (macOS)"
    CHROME_BIN="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
elif command -v google-chrome &> /dev/null; then
    echo "✅ Google Chrome 可用 (Linux)"
    CHROME_BIN="google-chrome"
elif command -v chromium-browser &> /dev/null; then
    echo "✅ Chromium 可用 (Linux)"
    CHROME_BIN="chromium-browser"
elif command -v chromium &> /dev/null; then
    echo "✅ Chromium 可用 (Linux)"
    CHROME_BIN="chromium"
else
    echo "❌ Chrome/Chromium 不可用"
    exit 1
fi

# 测试2: 创建测试HTML文件
echo "创建测试HTML文件..."
cat > test_cover_rendering.html << 'EOF'
<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <title>Cover Card Test</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        html {
            width: 1080px;
            height: 1440px;
            margin: 0;
            padding: 0;
        }
        
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif;
            background: #2c3e50;
            color: #ffffff;
            width: 1080px;
            height: 1440px;
            overflow: hidden;
            position: relative;
        }
        
        .cover-container {
            width: 100%;
            height: 100%;
            display: flex;
            flex-direction: column;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
        }
        
        .image-section {
            flex: 1;
            display: flex;
            align-items: center;
            justify-content: center;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            position: relative;
            overflow: hidden;
            width: 100%;
            min-height: 50%;
        }
        
        .title-section {
            flex: 1;
            display: flex;
            align-items: center;
            justify-content: center;
            background: linear-gradient(135deg, #764ba2 0%, #667eea 100%);
            padding: 20px;
            box-sizing: border-box;
            width: 100%;
            min-height: 50%;
        }
        
        .title {
            font-size: 64px;
            font-weight: bold;
            color: #ffffff;
            line-height: 1.4;
            margin: 0;
            text-shadow: 0 2px 4px rgba(0,0,0,0.5);
        }
        
        .size-indicator {
            position: absolute;
            top: 10px;
            right: 10px;
            background: rgba(0,0,0,0.7);
            color: white;
            padding: 5px 10px;
            border-radius: 5px;
            font-size: 14px;
            font-family: monospace;
        }
        
        .aspect-ratio-indicator {
            position: absolute;
            top: 50px;
            right: 10px;
            background: rgba(0,0,0,0.7);
            color: white;
            padding: 5px 10px;
            border-radius: 5px;
            font-size: 14px;
            font-family: monospace;
        }
    </style>
</head>
<body>
    <div class="cover-container">
        <div class="image-section">
            <div class="size-indicator">尺寸: 1080×1440</div>
            <div class="aspect-ratio-indicator">比例: 3:4</div>
        </div>
        <div class="title-section">
            <div class="title-container">
                <h1 class="title">魅力的本质</h1>
            </div>
        </div>
    </div>
    
    <script>
        // 显示实际尺寸信息
        console.log('页面尺寸:', window.innerWidth, 'x', window.innerHeight);
        console.log('文档尺寸:', document.documentElement.offsetWidth, 'x', document.documentElement.offsetHeight);
        console.log('body尺寸:', document.body.offsetWidth, 'x', document.body.offsetHeight);
        
        // 计算宽高比
        const width = document.documentElement.offsetWidth;
        const height = document.documentElement.offsetHeight;
        const aspectRatio = width / height;
        console.log('宽高比:', aspectRatio.toFixed(3));
        
        // 更新显示
        document.querySelector('.size-indicator').textContent = `实际: ${width}×${height}`;
        document.querySelector('.aspect-ratio-indicator').textContent = `比例: ${aspectRatio.toFixed(3)}`;
    </script>
</body>
</html>
EOF

echo "✅ 测试HTML文件已创建"

# 测试3: 使用Chrome渲染测试
echo "使用Chrome渲染测试..."
"$CHROME_BIN" --headless \
    --no-sandbox \
    --disable-dev-shm-usage \
    --disable-gpu \
    --disable-web-security \
    --screenshot=test_cover_output.png \
    --window-size=1080,1440 \
    test_cover_rendering.html

if [ $? -eq 0 ]; then
    echo "✅ Chrome渲染成功"
    
    # 检查输出文件
    if [ -f "test_cover_output.png" ]; then
        echo "✅ 输出图片文件已生成"
        
        # 获取图片信息
        if command -v identify &> /dev/null; then
            echo "图片信息:"
            identify test_cover_output.png
        elif command -v file &> /dev/null; then
            echo "文件信息:"
            file test_cover_output.png
        else
            echo "图片文件大小: $(ls -lh test_cover_output.png | awk '{print $5}')"
        fi
    else
        echo "❌ 输出图片文件未生成"
    fi
else
    echo "❌ Chrome渲染失败"
fi

# 测试4: 清理测试文件
echo "清理测试文件..."
rm -f test_cover_rendering.html
rm -f test_cover_output.png

echo "=== 测试完成 ==="
