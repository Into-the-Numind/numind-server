#!/bin/bash
#
# Python 依赖安装脚本
# 用途: 为 Sales RAG 的文档解析安装 Python 依赖
#
# 使用方法:
#   chmod +x install_python_deps.sh
#   ./install_python_deps.sh
#

set -e  # 遇到错误立即退出

echo "=========================================="
echo "  Sales RAG Python 依赖安装脚本"
echo "=========================================="
echo ""

# 检测操作系统
if [[ "$OSTYPE" == "linux-gnu"* ]]; then
    OS="linux"
elif [[ "$OSTYPE" == "darwin"* ]]; then
    OS="macos"
else
    echo "❌ 不支持的操作系统: $OSTYPE"
    exit 1
fi

echo "📋 检测到操作系统: $OS"
echo ""

# 1. 检查 Python3
echo "1️⃣ 检查 Python3..."
if command -v python3 &> /dev/null; then
    PYTHON_VERSION=$(python3 --version)
    echo "✅ $PYTHON_VERSION"
else
    echo "❌ Python3 未安装"
    echo ""
    echo "请先安装 Python3:"
    if [[ "$OS" == "linux" ]]; then
        echo "  sudo apt-get update"
        echo "  sudo apt-get install -y python3 python3-pip"
    else
        echo "  brew install python3"
    fi
    exit 1
fi
echo ""

# 2. 检查 pip3
echo "2️⃣ 检查 pip3..."
if command -v pip3 &> /dev/null; then
    PIP_VERSION=$(pip3 --version)
    echo "✅ $PIP_VERSION"
else
    echo "❌ pip3 未安装"
    echo ""
    echo "请先安装 pip3:"
    if [[ "$OS" == "linux" ]]; then
        echo "  sudo apt-get install -y python3-pip"
    else
        echo "  curl https://bootstrap.pypa.io/get-pip.py -o get-pip.py"
        echo "  python3 get-pip.py"
    fi
    exit 1
fi
echo ""

# 3. 检查现有依赖
echo "3️⃣ 检查现有 Python 依赖..."
echo ""

# 检查 PyMuPDF
if python3 -c "import fitz" 2>/dev/null; then
    echo "  ✅ PyMuPDF (fitz) 已安装"
    PYMUPDF_INSTALLED=true
else
    echo "  ❌ PyMuPDF 未安装"
    PYMUPDF_INSTALLED=false
fi

# 检查 python-docx
if python3 -c "from docx import Document" 2>/dev/null; then
    echo "  ✅ python-docx 已安装"
    DOCX_INSTALLED=true
else
    echo "  ❌ python-docx 未安装"
    DOCX_INSTALLED=false
fi

# 检查 markitdown
if python3 -c "import markitdown" 2>/dev/null; then
    echo "  ✅ markitdown 已安装"
    MARKITDOWN_INSTALLED=true
else
    echo "  ❌ markitdown 未安装"
    MARKITDOWN_INSTALLED=false
fi

echo ""

# 4. 安装缺失的依赖
if [[ "$PYMUPDF_INSTALLED" == false ]] || [[ "$DOCX_INSTALLED" == false ]] || [[ "$MARKITDOWN_INSTALLED" == false ]]; then
    echo "4️⃣ 安装缺失的依赖..."
    echo ""

    # 创建 requirements.txt
    cat > /tmp/numind_requirements.txt << 'EOF'
# Sales RAG 文档解析依赖
PyMuPDF>=1.23.0      # PDF 解析 (高质量 blocks 模式)
python-docx>=0.8.11  # DOCX 解析 (段落 + 表格)
markitdown           # 通用文档解析 (PDF, XLSX, PPTX, etc.)
EOF

    echo "📦 安装依赖包..."
    echo ""

    # 使用国内镜像源加速（如果在中国）
    if [[ "$OS" == "linux" ]]; then
        pip3 install --no-cache-dir -r /tmp/numind_requirements.txt -i https://pypi.tuna.tsinghua.edu.cn/simple
    else
        pip3 install --no-cache-dir -r /tmp/numind_requirements.txt
    fi

    echo ""
    echo "✅ 依赖安装完成"

    # 清理临时文件
    rm -f /tmp/numind_requirements.txt
else
    echo "4️⃣ 所有依赖已安装，无需操作"
fi

echo ""

# 5. 验证安装
echo "5️⃣ 验证安装结果..."
echo ""

SUCCESS=true

# 验证 PyMuPDF
if python3 -c "import fitz; print(f'PyMuPDF version: {fitz.version}')" 2>/dev/null; then
    echo "  ✅ PyMuPDF 验证成功"
else
    echo "  ❌ PyMuPDF 验证失败"
    SUCCESS=false
fi

# 验证 python-docx
if python3 -c "from docx import Document; print('python-docx 验证成功')" 2>/dev/null; then
    echo "  ✅ python-docx 验证成功"
else
    echo "  ❌ python-docx 验证失败"
    SUCCESS=false
fi

# 验证 markitdown
if python3 -c "import markitdown; print('markitdown 验证成功')" 2>/dev/null; then
    echo "  ✅ markitdown 验证成功"
else
    echo "  ❌ markitdown 验证失败"
    SUCCESS=false
fi

echo ""

# 6. 检查解析脚本
echo "6️⃣ 检查文档解析脚本..."
echo ""

SCRIPT_PATH="scripts/document_parser.py"
if [[ -f "$SCRIPT_PATH" ]]; then
    echo "  ✅ 解析脚本存在: $SCRIPT_PATH"

    # 测试脚本
    echo ""
    echo "  测试解析脚本..."
    if python3 "$SCRIPT_PATH" 2>&1 | grep -q "No file path provided"; then
        echo "  ✅ 解析脚本可正常执行"
    else
        echo "  ⚠️  解析脚本可能有问题"
    fi
else
    echo "  ❌ 解析脚本不存在: $SCRIPT_PATH"
    echo ""
    echo "  脚本应该位于项目根目录下的 scripts/document_parser.py"
    SUCCESS=false
fi

echo ""
echo "=========================================="

if [[ "$SUCCESS" == true ]]; then
    echo "✅ 所有检查通过！"
    echo ""
    echo "📝 后续步骤:"
    echo "  1. 重启 numind-server 服务"
    echo "  2. 上传 DOCX 文件到知识库测试"
    echo "  3. 查看日志确认使用了 Python 增强解析:"
    echo "     tail -f numind.log | grep 'enhanced Python parser'"
else
    echo "❌ 部分检查失败，请查看上述错误信息"
    exit 1
fi

echo "=========================================="
