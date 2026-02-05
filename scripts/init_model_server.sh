#!/bin/bash
set -e

# 安装必要的 Python 工具
echo "🔧 安装 Python 环境..."
apt-get update && apt-get install -y python3-pip

# 安装 sentence-transformers (并强制使用 CPU版 PyTorch)
echo "📦 安装依赖..."
pip3 install torch --index-url https://download.pytorch.org/whl/cpu
pip3 install sentence-transformers

# 创建缓存目录
mkdir -p /opt/numind/model/model_cache

# 下载模型脚本
echo "📥 开始下载模型..."
python3 << 'EOF'
import os
from sentence_transformers import SentenceTransformer

# 设置缓存目录
os.environ['SENTENCE_TRANSFORMERS_HOME'] = '/opt/numind/model/model_cache'
os.environ['HF_ENDPOINT'] = 'https://hf-mirror.com'

print("Downloading BAAI/bge-small-zh...")
model = SentenceTransformer('BAAI/bge-small-zh')
print("✅ 模型下载完成！")
EOF

# 设置权限确保容器内用户(1001)可读
echo "🔒 设置权限..."
chown -R 1001:1001 /opt/numind/model
chmod -R 755 /opt/numind/model

echo "🎉 全部完成！模型已准备在 /opt/numind/model/model_cache"
