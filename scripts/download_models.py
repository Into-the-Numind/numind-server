import os
import sys

# 设置模型缓存目录
cache_dir = '/app/model_cache'
os.makedirs(cache_dir, exist_ok=True)
os.environ['SENTENCE_TRANSFORMERS_HOME'] = cache_dir
os.environ['HF_HOME'] = cache_dir

# 如果在构建时能访问网络，可以尝试下载
# 如果是国内环境，可以设置镜像
os.environ['HF_ENDPOINT'] = 'https://hf-mirror.com'

def download_bge():
    print("Downloading BGE small zh model...")
    try:
        from sentence_transformers import SentenceTransformer
        # 下载并缓存
        SentenceTransformer('BAAI/bge-small-zh', cache_folder=cache_dir)
        print("BGE model downloaded successfully.")
    except Exception as e:
        print(f"Failed to download BGE model: {e}")
        # 在构建时不强制退出，以便在没有网络的环境下也能完成基础构建
        # 但这会导致运行时需要尝试下载

def download_paddle_ocr():
    print("Downloading PaddleOCR models...")
    try:
        from paddleocr import PaddleOCR
        # 初始化会自动下载默认模型
        # ch_PP-OCRv4 mobile is default but we want to be explicit and match runtime config
        PaddleOCR(
            lang="ch",
            ocr_version="PP-OCRv4",
            use_doc_orientation_classify=False,
            use_doc_unwarping=False,
            use_textline_orientation=False
        )
        print("PaddleOCR models downloaded successfully.")
    except Exception as e:
        print(f"Failed to download PaddleOCR models: {e}")

if __name__ == "__main__":
    download_bge()
    download_paddle_ocr()
