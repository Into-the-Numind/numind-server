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
print("✅ 语义模型下载完成！")

print("📥 开始预下载 PaddleOCR 模型...")
try:
    from paddleocr import PaddleOCR
    # 初始化一次，会自动下载模型
    # 显式使用 PP-OCRv4 mobile 版本，与运行时配置一致
    ocr = PaddleOCR(
        lang="ch", 
        ocr_version="PP-OCRv4",
        use_doc_orientation_classify=False,
        use_doc_unwarping=False,
        use_textline_orientation=False,
        show_log=False
    )
    print("✅ PaddleOCR 模型下载完成！")
except Exception as e:
    print(f"⚠️ PaddleOCR 模型下载失败 (这可能需要网络): {e}")
EOF

# 设置权限确保容器内用户(1001)可读
echo "🔒 设置权限..."
chown -R 1001:1001 /opt/numind/model
chmod -R 755 /opt/numind/model

echo "🎉 全部完成！模型已准备在 /opt/numind/model/model_cache"
