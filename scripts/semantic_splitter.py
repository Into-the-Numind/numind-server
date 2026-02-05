#!/usr/bin/env python3
"""
语义切分模块 - 使用 bge-small 模型计算句子相似度
在相似度断崖处切分（主题切换点）
"""

import sys
import json
import re
import numpy as np
from typing import List, Tuple, Dict
from functools import lru_cache

# 延迟导入 sentence-transformers，避免启动时加载
_model = None

def get_model():
    """懒加载模型，避免启动时加载"""
    global _model
    if _model is None:
        try:
            from sentence_transformers import SentenceTransformer
            # 使用轻量级中文模型 bge-small-zh
            print("Loading bge-small-zh model...", file=sys.stderr)
            _model = SentenceTransformer('BAAI/bge-small-zh')
            print("Model loaded successfully", file=sys.stderr)
        except Exception as e:
            print(f"Failed to load model: {e}", file=sys.stderr)
            raise
    return _model


def split_into_sentences(text: str) -> List[str]:
    """
    将文本切分为句子，同时保护 Markdown 表格不被拆分
    """
    # 中文句子结束符
    chinese_ends = r'[。！？；]'
    # 英文句子结束符
    english_ends = r'[.!?;]'
    
    # 匹配句子结束符后跟空格或换行或结束
    pattern = f'({chinese_ends}|{english_ends})+'
    
    sentences = []
    
    # 1. 首先按行分割，识别表格块
    lines = text.split('\n')
    current_table_block = []
    table_mode = False
    
    # 将文本视为混合块列表：普通文本块 和 表格块
    blocks = []
    current_text_block = []
    
    for line in lines:
        stripped_line = line.strip()
        # 简单判定：以 | 开头且包含至少一个 | 视为表格行
        is_table_row = stripped_line.startswith('|') and stripped_line.count('|') >= 2
        
        if is_table_row:
            # 如果之前有普通文本堆积，先保存
            if current_text_block:
                blocks.append({"type": "text", "content": "\n".join(current_text_block)})
                current_text_block = []
            
            table_mode = True
            current_table_block.append(line)
        else:
            if table_mode:
                # 表格结束
                blocks.append({"type": "table", "content": "\n".join(current_table_block)})
                current_table_block = []
                table_mode = False
            
            current_text_block.append(line)
            
    # 处理最后的块
    if current_table_block:
        blocks.append({"type": "table", "content": "\n".join(current_table_block)})
    if current_text_block:
        blocks.append({"type": "text", "content": "\n".join(current_text_block)})
        
    # 2. 对普通文本块进行分句，表格块整体作为一个句子
    for block in blocks:
        if block["type"] == "table":
            # 表格作为一个整体，不进行内部切分
            sentences.append(block["content"])
        else:
            # 普通文本进行正则句读切分
            content = block["content"]
            # 这里的切分逻辑需要稍微调整以处理多行文本
            # 简单起见，我们对 content 再次进行字符级遍历或正则切分
            # 注意：之前的逻辑是基于字符流的，这里 content 包含换行
            
            current = ""
            for char in content:
                current += char
                if re.search(f'{pattern}$', current):
                    # 只要匹配到任何结束符，就切分
                    stripped = current.strip()
                    if stripped:
                        sentences.append(stripped)
                    current = ""
                elif len(current) > 400:
                    # [PRO] 预切分逻辑：如果一个连续文本块超过 400 字符还没遇到标点，
                    # 强制物理切分，作为“伪句子”进行语义分析
                    # 这样可以处理 OCR 出来的“文本墙”，并让模型对每个部分单独计算向量
                    stripped = current.strip()
                    if stripped:
                        sentences.append(stripped)
                    current = ""
            
            if current.strip():
                sentences.append(current.strip())
                
    return sentences


def compute_similarity(embedding1: np.ndarray, embedding2: np.ndarray) -> float:
    """计算两个向量的余弦相似度"""
    dot_product = np.dot(embedding1, embedding2)
    norm1 = np.linalg.norm(embedding1)
    norm2 = np.linalg.norm(embedding2)
    
    if norm1 == 0 or norm2 == 0:
        return 0.0
    
    return float(dot_product / (norm1 * norm2))


def find_semantic_boundaries(
    sentences: List[str],
    similarities: List[float],
    threshold: float = 0.6,
    min_chunk_size: int = 100,
    max_chunk_size: int = 1000
) -> List[int]:
    """
    找到语义边界（相似度断崖处）
    
    Args:
        sentences: 句子列表
        similarities: 相邻句子相似度列表
        threshold: 相似度阈值，低于此值认为是主题切换
        min_chunk_size: 最小切片大小
        max_chunk_size: 最大切片大小
    
    Returns:
        切分点索引列表（在哪些句子后切分）
    """
    boundaries = []
    current_chunk_size = 0
    last_boundary = 0
    
    for i, sentence in enumerate(sentences):
        sentence_len = len(sentence)
        
        # 如果单个句子就超过了 max_chunk_size，强制在它之后切分
        if sentence_len >= max_chunk_size:
            if current_chunk_size > 0:
                boundaries.append(i - 1)
            boundaries.append(i)
            current_chunk_size = 0
            continue
            
        # 如果加上当前句子会超过限制，则在当前句子之前切分
        if current_chunk_size + sentence_len > max_chunk_size:
            if i > 0:
                boundaries.append(i - 1)
            current_chunk_size = sentence_len
            continue
            
        current_chunk_size += sentence_len
        
        # 相似度断崖（主题切换）及局部波谷逻辑
        if i < len(similarities):
            sim = similarities[i]
            
            # 条件2：相似度断崖（主题切换）且满足最小长度
            if sim < threshold and current_chunk_size >= min_chunk_size:
                boundaries.append(i)
                current_chunk_size = 0
                continue
            
            # 条件3：相似度局部最小值（波谷）
            if 0 < i < len(similarities) - 1:
                prev_sim = similarities[i - 1]
                next_sim = similarities[i + 1]
                if sim < prev_sim and sim < next_sim and sim < threshold:
                    if current_chunk_size >= min_chunk_size:
                        boundaries.append(i)
                        current_chunk_size = 0
    
    # 确保 boundaries 中没有重复项且是有序的
    final_boundaries = sorted(list(set(boundaries)))
    return final_boundaries


def semantic_split(
    text: str,
    threshold: float = 0.6,
    min_chunk_size: int = 100,
    max_chunk_size: int = 1000,
    overlap_size: int = 100
) -> List[Dict]:
    """
    语义切分主函数
    
    Returns:
        切片列表，每个切片包含 content 和 metadata
    """
    if not text or len(text) < min_chunk_size:
        return [{"content": text, "boundary_type": "single"}]
    
    # 1. 切分为句子
    sentences = split_into_sentences(text)
    
    if len(sentences) <= 1:
        return [{"content": text, "boundary_type": "single"}]
    
    # 2. 计算句子嵌入
    model = get_model()
    embeddings = model.encode(sentences, convert_to_numpy=True, show_progress_bar=False)
    
    # 3. 计算相邻句子相似度
    similarities = []
    for i in range(len(embeddings) - 1):
        sim = compute_similarity(embeddings[i], embeddings[i + 1])
        similarities.append(sim)
    
    # 4. 找到语义边界
    boundaries = find_semantic_boundaries(
        sentences, similarities, threshold, min_chunk_size, max_chunk_size
    )
    
    # 5. 构建切片
    chunks = []
    start_idx = 0
    
    # 辅助函数：根据长度强制切分
    def force_split_content(text, max_len):
        runes = list(text)
        res = []
        for i in range(0, len(runes), max_len):
            res.append("".join(runes[i:i+max_len]))
        return res

    for boundary in boundaries:
        end_idx = boundary + 1
        chunk_sentences = sentences[start_idx:end_idx]
        chunk_text = "".join(chunk_sentences)
        
        # 如果这个语义块本身就超长，需要进一步物理切分
        if len(chunk_text) > max_chunk_size:
            sub_chunks = force_split_content(chunk_text, max_chunk_size)
            for sub in sub_chunks:
                chunks.append({
                    "content": sub,
                    "boundary_type": "physical_force",
                    "sentence_count": 0
                })
        else:
            # 计算平均相似度（用于调试）
            if start_idx < len(similarities):
                avg_sim = sum(similarities[start_idx:min(end_idx-1, len(similarities))]) / max(1, end_idx - start_idx - 1)
            else:
                avg_sim = 1.0
            
            chunks.append({
                "content": chunk_text,
                "boundary_type": "semantic",
                "similarity_before": float(similarities[boundary]) if boundary < len(similarities) else None,
                "avg_similarity": float(avg_sim),
                "sentence_count": len(chunk_sentences)
            })
        start_idx = end_idx
    
    # 处理剩余句子
    if start_idx < len(sentences):
        chunk_sentences = sentences[start_idx:]
        chunk_text = "".join(chunk_sentences)
        if len(chunk_text) > max_chunk_size:
            sub_chunks = force_split_content(chunk_text, max_chunk_size)
            for sub in sub_chunks:
                chunks.append({
                    "content": sub,
                    "boundary_type": "physical_force",
                    "sentence_count": 0
                })
        else:
            chunks.append({
                "content": chunk_text,
                "boundary_type": "end",
                "sentence_count": len(chunk_sentences)
            })
    
    # 6. 添加重叠
    if overlap_size > 0 and len(chunks) > 1:
        chunks = add_overlap(chunks, overlap_size)
    
    return chunks


def add_overlap(chunks: List[Dict], overlap_size: int) -> List[Dict]:
    """为切片添加前后重叠"""
    if len(chunks) <= 1:
        return chunks
    
    result = []
    
    for i, chunk in enumerate(chunks):
        new_chunk = chunk.copy()
        
        # 前置重叠
        if i > 0:
            prev_content = chunks[i - 1]["content"]
            overlap_text = prev_content[-overlap_size:] if len(prev_content) > overlap_size else prev_content
            new_chunk["content"] = overlap_text + "\n\n[上下文衔接]\n\n" + chunk["content"]
            new_chunk["has_prefix_overlap"] = True
        
        # 后置重叠
        if i < len(chunks) - 1:
            next_content = chunks[i + 1]["content"]
            overlap_text = next_content[:overlap_size] if len(next_content) > overlap_size else next_content
            new_chunk["content"] = new_chunk["content"] + "\n\n[上下文衔接]\n\n" + overlap_text
            new_chunk["has_suffix_overlap"] = True
        
        result.append(new_chunk)
    
    return result


def main():
    """命令行入口"""
    if hasattr(sys.stdout, 'reconfigure'):
        sys.stdout.reconfigure(encoding='utf-8')
    
    if len(sys.argv) < 2:
        print(json.dumps({
            "success": False,
            "error": "Usage: python semantic_splitter.py <text_file> [threshold] [min_size] [max_size]"
        }, ensure_ascii=False))
        sys.exit(1)
    
    text_file = sys.argv[1]
    threshold = float(sys.argv[2]) if len(sys.argv) > 2 else 0.6
    min_size = int(sys.argv[3]) if len(sys.argv) > 3 else 100
    max_size = int(sys.argv[4]) if len(sys.argv) > 4 else 1000
    
    try:
        # 读取文本
        with open(text_file, 'r', encoding='utf-8') as f:
            text = f.read()
        
        # 执行语义切分
        chunks = semantic_split(text, threshold, min_size, max_size)
        
        result = {
            "success": True,
            "chunks": chunks,
            "total_chunks": len(chunks),
            "total_chars": len(text),
            "params": {
                "threshold": threshold,
                "min_size": min_size,
                "max_size": max_size
            }
        }
        
        print(json.dumps(result, ensure_ascii=False))
        
    except Exception as e:
        print(json.dumps({
            "success": False,
            "error": str(e)
        }, ensure_ascii=False))
        sys.exit(1)


if __name__ == "__main__":
    main()
