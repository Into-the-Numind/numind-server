#!/usr/bin/env python3
import os
import sys
import time

# 设置模型缓存目录
os.environ['SENTENCE_TRANSFORMERS_HOME'] = '/app/model_cache'
os.environ['HF_HOME'] = '/app/model_cache'

# 中国大陆镜像源（自动检测）
if os.environ.get('USE_CHINA_MIRROR', 'auto').lower() in ('1', 'true', 'yes', 'auto'):
    os.environ['HF_ENDPOINT'] = 'https://hf-mirror.com'
    print(f"🌐 使用镜像源: {os.environ['HF_ENDPOINT']}")

max_retries = 3
for attempt in range(1, max_retries + 1):
    try:
        print(f"🔄 第 {attempt} 次尝试下载模型...")
        from sentence_transformers import SentenceTransformer
        # 强制下载并缓存
        model = SentenceTransformer('BAAI/bge-small-zh', cache_folder='/app/model_cache')
        print("✅ 模型预下载成功!")
        sys.exit(0)
    except Exception as e:
        print(f"❌ 第 {attempt} 次尝试失败: {e}")
        if attempt < max_retries:
            wait_time = 5 * attempt
            print(f"⏳ 等待 {wait_time} 秒后重试...")
            time.sleep(wait_time)
        else:
            print("⚠️ 模型预下载失败，将在运行时尝试下载")
            # 不退出，让构建继续，运行时还有机会下载
            sys.exit(0)
