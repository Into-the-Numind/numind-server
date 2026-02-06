#!/usr/bin/env python3
"""
Semantic Splitter Microservice
Loads the model once and serves splitting requests via HTTP.
"""

import sys
import os

# 在导入 PaddlePaddle 之前禁用 OneDNN (MKL-DNN)
# 解决 PaddlePaddle 3.3.0+ 版本中 "ConvertPirAttribute2RuntimeAttribute not support" 错误
# 这样可以继续使用 PaddleOCR-VL-1.5 等新模型
os.environ["FLAGS_use_mkldnn"] = "0"

import uvicorn
import tempfile
from fastapi import FastAPI, HTTPException, UploadFile, File
from pydantic import BaseModel, Field
from typing import List, Optional
from semantic_splitter import semantic_split, get_model

# Initialize FastAPI app
app = FastAPI(title="Semantic & OCR Service")

# --- OCR Engine Global ---
OCR_ENGINE = None

def get_ocr_engine():
    global OCR_ENGINE
    if OCR_ENGINE is None:
        try:
            from paddleocr import PaddleOCR
            print("Initializing PaddleOCR...", file=sys.stderr)
            # 使用 PP-OCRv4 (比 v5 轻量很多)
            # 显式禁用所有预处理以减少 CPU 占用
            OCR_ENGINE = PaddleOCR(
                lang="ch",
                ocr_version="PP-OCRv4",
                use_doc_orientation_classify=False,
                use_doc_unwarping=False,
                use_textline_orientation=False
            )
            print("PaddleOCR (PP-OCRv4) initialized successfully!", file=sys.stderr)
        except Exception as e:
            print(f"Failed to initialize PaddleOCR: {e}", file=sys.stderr)
            raise e
    return OCR_ENGINE

# Request model
class SplitRequest(BaseModel):
    text: str
    threshold: float = Field(0.6, description="Similarity threshold")
    min_chunk_size: int = Field(100, description="Minimum chunk size")
    max_chunk_size: int = Field(1000, description="Maximum chunk size")
    overlap_size: int = Field(100, description="Overlap size")

# Response model
class SplitResponse(BaseModel):
    success: bool
    chunks: List[dict]
    total_chunks: int
    error: Optional[str] = None

class OCRResponse(BaseModel):
    success: bool
    text: str
    error: Optional[str] = None

@app.on_event("startup")
async def startup_event():
    """Load models on startup"""
    try:
        print("Loading semantic model on startup...", file=sys.stderr)
        get_model()
        print("Semantic model loaded successfully!", file=sys.stderr)
        
        # Load OCR engine
        get_ocr_engine()
    except Exception as e:
        print(f"Failed to load models during startup: {e}", file=sys.stderr)
        # We don't exit(1) here to allow the server to run even if one engine fails
        # but the health check will reflect the status

@app.post("/split", response_model=SplitResponse)
async def split_text(req: SplitRequest):
    try:
        chunks = semantic_split(
            req.text, 
            req.threshold, 
            req.min_chunk_size, 
            req.max_chunk_size,
            req.overlap_size
        )
        return {
            "success": True, 
            "chunks": chunks,
            "total_chunks": len(chunks)
        }
    except Exception as e:
        print(f"Split error: {e}", file=sys.stderr)
        return {
            "success": False,
            "chunks": [],
            "total_chunks": 0,
            "error": str(e)
        }

@app.post("/ocr", response_model=OCRResponse)
async def ocr_image(file: UploadFile = File(...)):
    """识别图片中的文本"""
    temp_path = None
    try:
        # 1. 保存上传的文件到临时路径
        suffix = os.path.splitext(file.filename)[1] if file.filename else ".png"
        with tempfile.NamedTemporaryFile(delete=False, suffix=suffix) as tmp:
            content = await file.read()
            tmp.write(content)
            temp_path = tmp.name

        # 2. 调用 OCR 引擎
        ocr = get_ocr_engine()
        
        # PaddleOCR 3.x 使用 predict() 方法，2.x 使用 ocr() 方法
        # 尝试兼容两个版本
        extracted_text = []
        
        if hasattr(ocr, 'predict'):
            # PaddleOCR 3.x API
            result = ocr.predict(temp_path)
            # 3.x 返回结果对象列表，每个对象有 rec_texts 属性
            if result:
                for res in result:
                    if hasattr(res, 'rec_texts') and res.rec_texts:
                        extracted_text.extend(res.rec_texts)
                    elif isinstance(res, dict) and 'rec_texts' in res:
                        extracted_text.extend(res['rec_texts'])
        else:
            # PaddleOCR 2.x API (fallback)
            result = ocr.ocr(temp_path)
            # 2.x 返回格式: [[[[box], (text, confidence)], ...]]
            if result and result[0]:
                for line in result[0]:
                    if len(line) >= 2:
                        text = line[1][0] if isinstance(line[1], (list, tuple)) else line[1]
                        extracted_text.append(text)
        
        full_text = "\n".join(extracted_text)

        return {
            "success": True,
            "text": full_text
        }
    except Exception as e:
        print(f"OCR error: {e}", file=sys.stderr)
        import traceback
        traceback.print_exc()
        return {
            "success": False,
            "text": "",
            "error": str(e)
        }
    finally:
        # 4. 清理临时文件
        if temp_path and os.path.exists(temp_path):
            os.remove(temp_path)

@app.get("/health")
async def health_check():
    return {
        "status": "ok", 
        "semantic_model_loaded": True,
        "ocr_engine_loaded": OCR_ENGINE is not None
    }

if __name__ == "__main__":
    # Run server
    uvicorn.run(app, host="0.0.0.0", port=9093)
