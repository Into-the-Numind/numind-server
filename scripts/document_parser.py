import sys
import json
import os
import re
import subprocess
from markitdown import MarkItDown

# Keep clean_text as is, it's useful for final polish
def clean_text(text):
    """通用的文本清洗函数，去除冗余的空格、空行，仅保留必要的段落结构"""
    if not text:
        return ""
    
    # 1. 规范化换行符
    text = text.replace('\r\n', '\n').replace('\r', '\n')
    
    # 2. 按行处理
    lines = text.split('\n')
    cleaned_lines = []
    
    for line in lines:
        # 清理行内多余空格
        line = re.sub(r'[ \t]+', ' ', line)
        line = line.strip()
        
        if line:
            cleaned_lines.append(line)
            
    # 3. 重新组合：使用双换行保留段落感
    text = '\n\n'.join(cleaned_lines)
    text = re.sub(r'\n{3,}', '\n\n', text)
    
    return text.strip()

# ==================== Legacy DOC Parsing (Antiword) ====================
def extract_doc_content(doc_path):
    try:
        # Try antiword first (Linux/Mac standard for binary .doc)
        result = subprocess.run(['antiword', '-w', '0', doc_path], 
                               capture_output=True, text=True, encoding='utf-8', errors='ignore')
        
        if result.returncode == 0:
            content = result.stdout.strip()
            if content:
                return {"success": True, "content": clean_text(content)}

        return {"success": False, "error": "Unable to extract text from .doc file (antiword missing or failed)"}
        
    except FileNotFoundError:
        return {"success": False, "error": "antiword command not found"}
    except Exception as e:
        return {"success": False, "error": f"DOC extraction failed: {str(e)}"}

# ==================== Unified Modern Parsing (MarkItDown) ====================
def extract_with_markitdown(file_path):
    md = MarkItDown()
    try:
        # MarkItDown handles docx, xlsx, pptx, pdf, html, txt, etc. auto-magically
        result = md.convert(file_path)
        
        # Access the converted text content
        # Note: MarkItDown returns an object with .text_content
        if result and result.text_content:
            return {"success": True, "content": clean_text(result.text_content)}
        else:
            return {"success": False, "error": "Extracted content is empty"}
            
    except Exception as e:
        # Catch-all for conversion errors
        return {"success": False, "error": f"MarkItDown conversion failed: {str(e)}"}

# ==================== Main Entry ====================
if __name__ == "__main__":
    # Ensure stdout uses UTF-8 to avoid encoding issues with Chinese characters
    sys.stdout.reconfigure(encoding='utf-8')

    if len(sys.argv) < 2:
        print(json.dumps({"success": False, "error": "No file path provided"}))
        sys.exit(1)
        
    file_path = sys.argv[1]
    
    # Simple extension check
    ext = os.path.splitext(file_path)[1].lower()
    
    result = {"success": False, "error": "Unknown file type"}
    
    # Special handling for legacy binary .doc files (MarkItDown doesn't support them)
    if ext == ".doc":
        result = extract_doc_content(file_path)
    else:
        # Everything else (PDF, DOCX, XLSX, PPTX, TXT, MD, ETC) goes to MarkItDown
        result = extract_with_markitdown(file_path)
        
    print(json.dumps(result, ensure_ascii=False))
