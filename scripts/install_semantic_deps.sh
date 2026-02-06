#!/bin/bash
# 安装语义切分依赖（bge-small 模型）

set -e

echo "==============================================="
echo "Installing Semantic Splitter Dependencies"
echo "==============================================="

# 检查 Python3
if ! command -v python3 &> /dev/null; then
    echo "❌ Python3 is not installed"
    exit 1
fi

echo "✅ Python3 found: $(python3 --version)"

# 安装 sentence-transformers
echo ""
echo "Installing sentence-transformers..."
pip3 install sentence-transformers -q

# 检查安装
if python3 -c "from sentence_transformers import SentenceTransformer; print('OK')" 2>/dev/null; then
    echo "✅ sentence-transformers installed successfully"
else
    echo "❌ Failed to install sentence-transformers"
    exit 1
fi

# 预下载模型（可选，会在首次使用时自动下载）
echo ""
echo "Pre-downloading bge-small-zh model..."
python3 << 'EOF'
from sentence_transformers import SentenceTransformer
print("Downloading BAAI/bge-small-zh...")
model = SentenceTransformer('BAAI/bge-small-zh')
print("✅ Model downloaded successfully")
EOF

echo ""
echo "==============================================="
echo "✅ Semantic splitter dependencies installed!"
echo "==============================================="
